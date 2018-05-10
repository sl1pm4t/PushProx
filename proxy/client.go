package main

import (
	"io"

	"os"

	"bytes"
	"fmt"

	"bufio"
	"net/http"
	"strings"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/robustperception/pushprox/util"
	"golang.org/x/net/websocket"
)

const channelBufSize = 100

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
		c.coordinator.Del(c)
		c.coordinator.logger.Log("msg", "client is disconnected", "fqdn", c.fqdn)
	}
}

func (c *Client) Done() {
	c.doneCh <- true
}

// Listen Write and Read request via chanel
func (c *Client) Listen() {
	go c.listenWrite()
	c.listenRead()
}

// Listen write request via chanel
func (c *Client) listenWrite() {
	level.Info(c.coordinator.logger).Log("msg", "Listening write to client")
	for {
		select {

		// send message to the client
		case msg := <-c.ch:
			level.Info(c.coordinator.logger).Log("sent", msg.Type)
			websocket.JSON.Send(c.ws, msg)

			// receive done request
		case <-c.doneCh:
			c.coordinator.Del(c)
			c.doneCh <- true // for listenRead method
			return
		}
	}
}

// Listen read request via chanel
func (c *Client) listenRead() {
	level.Info(c.coordinator.logger).Log("msg", "Read from client")
	for {
		select {

		// receive done request
		case <-c.doneCh:
			c.coordinator.Del(c)
			c.doneCh <- true // for listenWrite method
			return

		// read data from websocket connection
		default:
			var msg *util.SocketMessage
			err := websocket.JSON.Receive(c.ws, &msg)
			if err == io.EOF {
				c.doneCh <- true
			} else if err != nil {
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
		level.Info(logger).Log("msg", "client ready", "fqdn", c.fqdn)

		go func() {
			request, _ := coordinator.WaitForScrapeInstruction(c)

			buf := &bytes.Buffer{}
			request.WriteProxy(buf)
			fmt.Printf("\nrequest: %s\n", buf.String())

			scrapeRequest := &util.SocketMessage{
				Type: util.Request,
				Payload: map[string]string{
					"request": buf.String(),
				},
			}

			c.Write(scrapeRequest)
		}()
	case util.Response:
		level.Info(logger).Log("msg", "client response", "fqdn", c.fqdn)

		buf := &bytes.Buffer{}
		io.Copy(buf, strings.NewReader(msg.Payload["response"]))
		scrapeResult, _ := http.ReadResponse(bufio.NewReader(buf), nil)
		level.Info(logger).Log("msg", "got response", "scrape_id", scrapeResult.Header.Get("Id"))
		err := coordinator.ScrapeResult(scrapeResult)
		if err != nil {
			level.Error(logger).Log("msg", "Error processing response:", "err", err, "scrape_id", scrapeResult.Header.Get("Id"))
			c.Write(&util.SocketMessage{Type: util.Error, Payload: map[string]string{"error": err.Error()}})
		}
	default:
		level.Error(logger).Log("msg", "unknown SocketMessage received", "kind", msg.Type)
		c.doneCh <- true
	}
}
