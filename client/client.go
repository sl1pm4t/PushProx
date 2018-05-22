package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/websocket"

	"bufio"

	"sync"

	"bytes"
	"encoding/base64"

	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/robustperception/pushprox/util"
)

type Client struct {
	coordinator *Coordinator
	logger      log.Logger
	proxyURL    string
	wsClient    *WSClient

	state   ClientState
	stateCh chan ClientStateChange
	doneCh  chan bool
	mu      sync.Mutex
}

type ClientState int

type ClientStateChange struct {
	old ClientState
	new ClientState
}

const (
	ErrorState ClientState = iota
	InitState
	DisconnectedState
	ConnectedState
	PendingRegisteredState
	RegisteredState
	ReadyState
	ProcessingRequestState
	RequestCompletedState
	ConnectErrorState
)

func (s ClientState) String() string {
	switch s {
	case ErrorState:
		return "ErrorState"
	case InitState:
		return "InitState"
	case DisconnectedState:
		return "DisconnectedState"
	case ConnectedState:
		return "ConnectedState"
	case PendingRegisteredState:
		return "PendingRegisteredState"
	case RegisteredState:
		return "RegisteredState"
	case ReadyState:
		return "ReadyState"
	case ProcessingRequestState:
		return "ProcessingRequestState"
	case RequestCompletedState:
		return "RequestCompletedState"
	case ConnectErrorState:
		return "ConnectErrorState"
	}
	return "Unknown"
}

var (
	readyMessage = &util.SocketMessage{
		Type: util.Ready,
	}
)

func NewClient(proxyURL string, logger log.Logger, coordinator *Coordinator) *Client {
	return &Client{
		proxyURL:    proxyURL,
		logger:      logger,
		coordinator: coordinator,
		state:       InitState,
		stateCh:     make(chan ClientStateChange),
	}
}

func (c *Client) Run() {
	level.Info(c.logger).Log("msg", "starting client")
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go c.stateLoop(wg)
	c.SetState(DisconnectedState)
	wg.Wait()
	level.Info(c.logger).Log("msg", "goodbye")
}

func (c *Client) stateLoop(wg *sync.WaitGroup) {
	defer wg.Done()
	var stateChange ClientStateChange
	for {
		// wait for state change
		select {
		case stateChange = <-c.stateCh:
		}

		// process change
		switch stateChange.new {
		case ConnectErrorState:
			fallthrough
		case DisconnectedState:
			if stateChange.old != InitState {
				// Don't pound the server. TODO: Randomised exponential backoff.
				time.Sleep(5 * time.Second)
			}
			c.connect()

		case ConnectedState:
			// listen for WS client disconnects
			go func() {
				select {
				case <-c.wsClient.doneCh:
					// pass Done along
					c.wsClient.Done()
					// update state of this Client
					c.SetState(DisconnectedState)
				}
			}()

			c.register()

		case PendingRegisteredState:
			// TODO: handle timeout
		case RegisteredState:
			c.sendReady()

		case ReadyState:
			// TODO: handle timeout?
		case RequestCompletedState:
			c.sendReady()

		case ErrorState:
			// reset
			if c.wsClient != nil {
				c.wsClient.Done()
			}
			// call to Done() should trigger transition to 'DisconnectedState'
		}
	}
}

func (c *Client) GetState() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) SetState(newState ClientState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldState := c.state

	switch {
	// validate state transitions
	case newState == ErrorState:
	case newState == DisconnectedState:
	case oldState == DisconnectedState && newState == ConnectErrorState:
	case oldState == DisconnectedState && newState == ConnectedState:
	case oldState == ConnectErrorState && newState == ConnectErrorState:
	case oldState == ConnectErrorState && newState == ConnectedState:
	case oldState == ConnectedState && newState == PendingRegisteredState:
	case oldState == PendingRegisteredState && newState == RegisteredState:
	case oldState == RegisteredState && newState == ReadyState:
	case oldState == ReadyState && newState == ProcessingRequestState:
	case oldState == ProcessingRequestState && newState == RequestCompletedState:
	case oldState == RequestCompletedState && newState == ReadyState:

	default:
		level.Error(c.logger).Log("msg", "invalid state transition", "oldState", oldState, "newState", newState)
		//newState = ErrorState
		panic("invalid state transition")
	}

	level.Debug(c.logger).Log("msg", "state change", "oldState", oldState, "newState", newState)

	c.state = newState
	go func() {
		c.stateCh <- ClientStateChange{oldState, newState}
	}()
}

func (c *Client) connect() {
	level.Debug(c.logger).Log("msg", "connect")
	base, err := url.Parse(c.proxyURL)
	if err != nil {
		level.Error(c.logger).Log("msg", "error parsing url", "err", err)
		return
	}
	u, err := url.Parse("/socket")
	if err != nil {
		level.Error(c.logger).Log("msg", "error parsing url", "err", err)
		return
	}
	url := base.ResolveReference(u)

	if url.Scheme == "http" {
		url.Scheme = "ws"
	} else if url.Scheme == "https" {
		url.Scheme = "wss"
	}

	origin := fmt.Sprintf("http://%s/", *myFqdn)

	level.Info(c.logger).Log("msg", "dialing proxy:", "url", url.String())
	ws, err := websocket.Dial(url.String(), "", origin)
	if err != nil {
		level.Error(c.logger).Log("err", err.Error())
		c.SetState(ConnectErrorState)
		return
	}

	c.wsClient = NewWSClient(c.proxyURL, ws, c.coordinator, c.processMessage)
	c.wsClient.Listen()
	c.SetState(ConnectedState)
}

func (c *Client) register() {
	level.Debug(c.logger).Log("msg", "register")
	register := &util.SocketMessage{
		Type: util.Register,
		Payload: map[string]string{
			"fqdn": *myFqdn,
		},
	}
	c.wsClient.Write(register)

	level.Info(c.logger).Log("msg", "sent register to proxy")
	c.SetState(PendingRegisteredState)
}

func (c *Client) sendReady() {
	level.Debug(c.logger).Log("msg", "sendReady")
	err := c.wsClient.Write(readyMessage)
	if err != nil {
		c.SetState(ErrorState)
		return
	}

	level.Info(c.logger).Log("msg", "sent ready to proxy")
	c.SetState(ReadyState)
}

func (c *Client) processMessage(msg *util.SocketMessage) {
	level.Debug(c.logger).Log("msg", "processMessage")

	switch msg.Type {
	case util.RegisterAck:
		c.SetState(RegisteredState)

	case util.Request:
		reader := bufio.NewReader(strings.NewReader(msg.Payload["request"]))
		request, err := http.ReadRequest(reader)
		if err != nil {
			level.Error(c.coordinator.logger).Log(err)
		}
		level.Info(c.coordinator.logger).Log("msg", "received scrape request")

		c.SetState(ProcessingRequestState)

		// have to remove RequestURI otherwise http lib will complain
		request.RequestURI = ""
		resp := c.coordinator.doScrape(request)

		c.sendResponse(resp)

	default:
		level.Error(c.coordinator.logger).Log("msg", "unknown SocketMessage received", "fqdn", c.proxyURL, "type", msg.Type)
		c.SetState(ErrorState)

	}
}

func (c *Client) sendResponse(resp *http.Response) {
	level.Debug(c.logger).Log("msg", "sendResponse")

	// encode HTTP response as base64
	buf := &bytes.Buffer{}
	resp.Write(buf)
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	// package in WebSocketMessage object
	msg := &util.SocketMessage{
		Type: util.Response,
		Payload: map[string]string{
			"response": encoded,
		},
	}
	// fire away
	c.wsClient.Write(msg)
	c.SetState(RequestCompletedState)
}
