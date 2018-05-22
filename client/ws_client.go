package main

import (
	"io"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/robustperception/pushprox/util"
	"golang.org/x/net/websocket"
)

const channelBufSize = 1

type ProcessWebSocketMessageFunc func(message *util.SocketMessage)

// WebSocket client.
type WSClient struct {
	fqdn           string
	ws             *websocket.Conn
	coordinator    *Coordinator
	ch             chan *util.SocketMessage
	doneCh         chan bool
	processMessage ProcessWebSocketMessageFunc
}

// Create new WebSocket client.
func NewWSClient(fqdn string, ws *websocket.Conn, coordinator *Coordinator, processMessageFunc ProcessWebSocketMessageFunc) *WSClient {
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

	return &WSClient{
		fqdn,
		ws,
		coordinator,
		ch, doneCh,
		processMessageFunc,
	}
}

func (c *WSClient) Conn() *websocket.Conn {
	return c.ws
}

func (c *WSClient) Write(msg *util.SocketMessage) {
	select {
	case c.ch <- msg:
	default:
		level.Warn(c.coordinator.logger).Log("msg", "could not send msg, client is disconnected", "fqdn", c.fqdn)
	}
}

func (c *WSClient) Done() {
	level.Debug(c.coordinator.logger).Log("msg", "---DONE---", "fqdn", c.fqdn)
	c.doneCh <- true
}

// Listen Write and Read request via chanel
func (c *WSClient) Listen() {
	readyCh := make(chan bool, 2)
	go c.listenWrite(readyCh)
	go c.listenRead(readyCh)
	readyCount := 0
	for range readyCh {
		readyCount += 1
		if readyCount >= 2 {
			return
		}
	}
}

// Listen write request via channel
func (c *WSClient) listenWrite(readyCh chan bool) {
	level.Info(c.coordinator.logger).Log("msg", "starting write loop for client", "fqdn", c.fqdn)
	readyCh <- true
	for {
		select {
		case <-time.After(3 * time.Second):
			// send websocket ping every 3s
			level.Debug(c.coordinator.logger).Log("msg", "ping", "fqdn", c.fqdn)
			err := util.PINGER.Send(c.ws, "ping")
			if err != nil {
				level.Error(c.coordinator.logger).Log("msg", "ping err", "fqdn", c.fqdn, "err", err.Error())
				defer c.Done()
				return
			}

		case msg := <-c.ch:
			// send message to the server
			level.Info(c.coordinator.logger).Log("msg", "sending JSON msg to client", "fqdn", c.fqdn, "type", msg.Type)
			err := websocket.JSON.Send(c.ws, msg)
			if err == io.EOF {
				c.coordinator.logger.Log("msg", "websocket got EOF", "fqdn", c.fqdn)

				defer c.Done()
				return
			} else if err != nil {
				level.Error(c.coordinator.logger).Log("msg", "error sending JSON msg to client", "fqdn", c.fqdn, "err", err.Error())
			} else {
				level.Info(c.coordinator.logger).Log("msg", "successfully sent JSON msg to client", "fqdn", c.fqdn, "type", msg.Type)
			}

		case <-c.doneCh:
			// receive done request
			level.Info(c.coordinator.logger).Log("msg", "closing listenWrite loop", "fqdn", c.fqdn)
			c.coordinator.Del(c)
			defer c.Done() // for listenRead method
			return
		}
	}
}

// Listen read request via chanel
func (c *WSClient) listenRead(readyCh chan bool) {
	level.Info(c.coordinator.logger).Log("msg", "starting read for client", "fqdn", c.fqdn)
	readyCh <- true
	for {
		select {
		case <-c.doneCh:
			// receive done request
			level.Debug(c.coordinator.logger).Log("msg", "closing listenRead loop", "fqdn", c.fqdn)
			c.coordinator.Del(c)
			defer c.Done() // for listenWrite method
			return

		default:
			// read data from websocket connection
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

//func (c *WSClient) processMessage(msg *util.SocketMessage) {
//	switch msg.Type {
//	case util.Request:
//		reader := bufio.NewReader(strings.NewReader(msg.Payload["request"]))
//		request, err := http.ReadRequest(reader)
//		if err != nil {
//			level.Error(c.coordinator.logger).Log(err)
//		}
//		level.Info(c.coordinator.logger).Log("msg", "received scrape request")
//
//		// have to remove RequestURI otherwise http lib will complain
//		request.RequestURI = ""
//		c.coordinator.doScrape(request, c.ws)
//	default:
//		level.Error(c.coordinator.logger).Log("msg", "unknown SocketMessage received", "fqdn", c.fqdn, "type", msg.Type)
//		c.Done()
//	}
//}
