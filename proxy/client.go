package main

import (
	"io"

	"os"

	"bufio"
	"bytes"
	"net/http"
	"strings"

	"encoding/base64"

	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/robustperception/pushprox/util"
	"golang.org/x/net/websocket"
)

const channelBufSize = 1

// Proxy client.
type Client struct {
	fqdn        string
	ws          *websocket.Conn
	coordinator *Coordinator
	ch          chan *util.SocketMessage
	doneCh      chan bool
}

// Create new proxy client.
func NewClient(fqdn string, ws *websocket.Conn, coordinator *Coordinator) *Client {
	if fqdn == "" {
		level.Error(log.NewLogfmtLogger(os.Stdout)).Log("msg", "fqdn cannot be empty")
	}

	if ws == nil {
		level.Error(log.NewLogfmtLogger(os.Stdout)).Log("msg", "ws cannot be nil")
	}

	if coordinator == nil {
		level.Error(log.NewLogfmtLogger(os.Stdout)).Log("msg", "coordinator cannot be nil")
	}

	ch := make(chan *util.SocketMessage, channelBufSize)
	doneCh := make(chan bool)

	return &Client{fqdn, ws, coordinator, ch, doneCh}
}

func (c *Client) Conn() *websocket.Conn {
	return c.ws
}

func (c *Client) Write(msg *util.SocketMessage) {
	select {
	case c.ch <- msg:
	default:
		level.Warn(c.coordinator.logger).Log("msg", "could not send msg, client is disconnected", "fqdn", c.fqdn)
		c.coordinator.Del(c)
	}
}

func (c *Client) Done() {
	level.Debug(c.coordinator.logger).Log("msg", "---DONE---", "fqdn", c.fqdn)
	select {
	case c.doneCh <- true:
	case <-time.After(time.Second):
		// The done signal can originate on several different goroutines, which all re-fire
		// the signal onto the channel to make sure it's received by all others.
		// The last one to signal would block and leak a goroutine.
		// To avoid a leak, timeout after 1s.
		level.Debug(c.coordinator.logger).Log("msg", "timed out waiting to signal Done", "fqdn", c.fqdn)
	}
}

// Listen Write and Read request via chanel
func (c *Client) Listen() {
	go c.listenWrite()
	c.listenRead()
}

// Listen write request via channel
func (c *Client) listenWrite() {
	level.Info(c.coordinator.logger).Log("msg", "starting write loop for client", "fqdn", c.fqdn)
	for {
		select {
		// send websocket ping every 3s
		case <-time.After(3 * time.Second):
			level.Debug(c.coordinator.logger).Log("msg", "ping", "fqdn", c.fqdn)
			err := util.PINGER.Send(c.ws, "ping")
			if err != nil {
				level.Error(c.coordinator.logger).Log("msg", "ping err", "fqdn", c.fqdn, "err", err.Error())
				c.Done()
			}

		// send message to the client
		case msg := <-c.ch:
			level.Info(c.coordinator.logger).Log("msg", "sending JSON msg to client", "fqdn", c.fqdn, "type", msg.Type)
			err := websocket.JSON.Send(c.ws, msg)
			if err == io.EOF {
				c.coordinator.logger.Log("msg", "websocket got EOF", "fqdn", c.fqdn)

				c.Done()
			} else if err != nil {
				level.Error(c.coordinator.logger).Log("msg", "error sending JSON msg to client", "fqdn", c.fqdn, "err", err.Error())
				c.Done()
			} else {
				level.Info(c.coordinator.logger).Log("msg", "successfully sent JSON msg to client", "fqdn", c.fqdn, "type", msg.Type)
			}

		// receive done request
		case <-c.doneCh:
			level.Info(c.coordinator.logger).Log("msg", "closing listenWrite loop", "fqdn", c.fqdn)
			c.coordinator.Del(c)
			defer c.Done() // for listenRead method
			return
		}
	}
}

// Listen read request via chanel
func (c *Client) listenRead() {
	level.Info(c.coordinator.logger).Log("msg", "starting read for client", "fqdn", c.fqdn)
	for {
		select {

		// receive done request
		case <-c.doneCh:
			level.Debug(c.coordinator.logger).Log("msg", "closing listenRead loop", "fqdn", c.fqdn)
			c.coordinator.Del(c)
			defer c.Done() // for listenWrite method
			return

		// read data from websocket connection
		default:
			var msg *util.SocketMessage
			err := websocket.JSON.Receive(c.ws, &msg)
			if err != nil {
				if err == io.EOF {
					level.Warn(c.coordinator.logger).Log("msg", "websocket got EOF", "fqdn", c.fqdn)
					defer c.Done()
					return
				}
				level.Error(c.coordinator.logger).Log("err", err.Error())

			} else {
				c.processMessage(msg)
			}
		}
	}
}

func (c *Client) processMessage(msg *util.SocketMessage) {
	switch msg.Type {
	case util.Ready:
		level.Info(c.coordinator.logger).Log("msg", "client ready", "fqdn", c.fqdn)
		go coordinator.WaitForScrapeInstruction(c)

	case util.Response:
		level.Info(c.coordinator.logger).Log("msg", "client response", "fqdn", c.fqdn)

		buf := &bytes.Buffer{}
		io.Copy(buf, strings.NewReader(msg.Payload["response"]))
		decoded, err := base64.StdEncoding.DecodeString(buf.String())
		if err != nil {
			level.Error(c.coordinator.logger).Log("msg", "could not decode response payload", "err", err)
		}
		scrapeResult, _ := http.ReadResponse(bufio.NewReader(bytes.NewReader(decoded)), nil)
		level.Info(c.coordinator.logger).Log("msg", "got response", "scrape_id", scrapeResult.Header.Get("Id"))
		err = coordinator.ScrapeResult(scrapeResult)
		if err != nil {
			level.Error(c.coordinator.logger).Log("msg", "error processing response:", "err", err, "scrape_id", scrapeResult.Header.Get("Id"))
			c.Write(&util.SocketMessage{Type: util.Error, Payload: map[string]string{"error": err.Error()}})
		}
	default:
		level.Error(c.coordinator.logger).Log("msg", "unknown SocketMessage received", "fqdn", c.fqdn, "type", msg.Type)
		c.doneCh <- true
	}
}
