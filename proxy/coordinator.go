package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/robustperception/pushprox/util"
)

var (
	registrationTimeout = kingpin.Flag("registration.timeout", "After how long a registration expires.").Default("5m").Duration()
)

type Coordinator struct {
	mu sync.Mutex

	// Clients waiting for a scrape.
	waiting map[string]chan *http.Request
	// Responses from clients.
	responses map[string]chan *http.Response
	// Clients we know about.
	clients map[string]*Client

	logger log.Logger
}

func NewCoordinator(logger log.Logger) *Coordinator {
	c := &Coordinator{
		waiting:   map[string]chan *http.Request{},
		responses: map[string]chan *http.Response{},
		clients:   map[string]*Client{},
		logger:    logger,
	}
	return c
}

var idCounter int64

// Generate a unique ID
func genId() string {
	id := atomic.AddInt64(&idCounter, 1)
	// TODO: Add MAC address.
	// TODO: Sign these to prevent spoofing.
	return fmt.Sprintf("%d-%d-%d", time.Now().Unix(), id, os.Getpid())
}

func (c *Coordinator) getRequestChannel(fqdn string) chan *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.waiting[fqdn]
	if !ok {
		ch = make(chan *http.Request)
		c.waiting[fqdn] = ch
	}
	return ch
}

// Remove a request channel. Idempotent.
func (c *Coordinator) removeRequestChannel(fqdn string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.waiting, fqdn)
}

func (c *Coordinator) getResponseChannel(id string) chan *http.Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.responses[id]
	if !ok {
		ch = make(chan *http.Response)
		c.responses[id] = ch
	}
	return ch
}

// Remove a response channel. Idempotent.
func (c *Coordinator) removeResponseChannel(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.responses, id)
}

// Request a scrape.
func (c *Coordinator) DoScrape(ctx context.Context, r *http.Request) (*http.Response, error) {
	id := genId()
	level.Info(c.logger).Log("msg", "DoScrape", "scrape_id", id, "url", r.URL.Host)
	r.Header.Add("Id", id)
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("Matching client not found for %q: %s", r.URL.Host, ctx.Err())
	case c.getRequestChannel(r.URL.Hostname()) <- r:
	}

	respCh := c.getResponseChannel(id)
	defer c.removeResponseChannel(id)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-respCh:
		return resp, nil
	}
}

// Client registering to accept a scrape request. Blocking.
func (c *Coordinator) WaitForScrapeInstruction(client *Client) {
	level.Info(c.logger).Log("msg", "WaitForScrapeInstruction", "fqdn", client.fqdn)

	ch := c.getRequestChannel(client.fqdn)
	defer c.removeRequestChannel(client.fqdn)
	select {
	case request := <-ch:
		if request != nil {
			buf := &bytes.Buffer{}
			request.WriteProxy(buf)
			level.Info(logger).Log("msg", "got scrape request for client", "fqdn", client.fqdn)

			scrapeRequest := &util.SocketMessage{
				Type: util.Request,
				Payload: map[string]string{
					"request": buf.String(),
				},
			}

			client.Write(scrapeRequest)
		}
	case <-client.doneCh:
		level.Info(logger).Log("msg", "WaitForScrapeInstruction got doneCh before scrape request", "fqdn", client.fqdn)
		client.Done()
		return
	}
}

// Client sending a scrape result in.
func (c *Coordinator) ScrapeResult(r *http.Response) error {
	id := r.Header.Get("Id")
	level.Info(c.logger).Log("msg", "ScrapeResult", "scrape_id", id)
	ctx, _ := context.WithTimeout(context.Background(), util.GetScrapeTimeout(r.Header))
	// Don't expose internal headers.
	r.Header.Del("Id")
	r.Header.Del("X-Prometheus-Scrape-Timeout-Seconds")
	select {
	case c.getResponseChannel(id) <- r:
		return nil
	case <-ctx.Done():
		c.removeResponseChannel(id)
		return ctx.Err()
	}
}

// What clients are alive.
func (c *Coordinator) KnownClients() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	known := make([]string, 0, len(c.clients))
	for k, client := range c.clients {
		if !client.deletedTime.IsZero() && client.deletedTime.Before(time.Now().Add(-*registrationTimeout)) {
			level.Info(c.logger).Log("msg", "deleting client", "fqdn", client.fqdn, "deleted time", client.deletedTime)
			delete(c.clients, k)
		} else {
			known = append(known, k)
		}
	}
	return known
}

// Add client to known list
func (c *Coordinator) Add(client *Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clients[client.fqdn] = client
	level.Info(c.logger).Log("msg", "Added client", "fqdn", client.fqdn)
}

// Del client from known list
func (c *Coordinator) Del(client *Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := client.ws.Close()
	if err != nil {
		level.Debug(c.logger).Log("msg", "could not close client ws connection", "fqdn", client.fqdn, "err", err.Error())
	}
	//delete(c.clients, client.fqdn)
	client.deletedTime = time.Now()
	level.Info(c.logger).Log("msg", "marked client for deletion", "fqdn", client.fqdn)
}
