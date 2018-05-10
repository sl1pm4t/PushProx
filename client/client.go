package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"bufio"

	"io"

	"github.com/ShowMax/go-fqdn"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/robustperception/pushprox/util"
)

var (
	myFqdn   = kingpin.Flag("fqdn", "FQDN to register with").Default(fqdn.Get()).String()
	proxyURL = kingpin.Flag("proxy-url", "Push proxy to talk to.").Required().String()
)

type Coordinator struct {
	logger log.Logger
}

func (c *Coordinator) doScrape(request *http.Request, client *http.Client, ws *websocket.Conn) {
	logger := log.With(c.logger, "scrape_id", request.Header.Get("id"))
	ctx, _ := context.WithTimeout(request.Context(), util.GetScrapeTimeout(request.Header))
	request = request.WithContext(ctx)
	// We cannot handle http requests at the proxy, as we would only
	// see a CONNECT, so use a URL parameter to trigger it.
	params := request.URL.Query()
	if params.Get("_scheme") == "https" {
		request.URL.Scheme = "https"
		params.Del("_scheme")
		request.URL.RawQuery = params.Encode()
	}

	scrapeResp, err := client.Do(request)
	if err != nil {
		msg := fmt.Sprintf("Failed to scrape %s: %s", request.URL.String(), err)
		level.Warn(logger).Log("msg", "Failed to scrape", "Request URL", request.URL.String(), "err", err)
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       ioutil.NopCloser(strings.NewReader(msg)),
		}
		err = c.doPush(resp, request, ws)
		if err != nil {
			level.Warn(logger).Log("msg", "Failed to push failed scrape response:", "err", err)
			return
		}
		level.Info(logger).Log("msg", "Pushed failed scrape response")
		return
	}
	level.Info(logger).Log("msg", "Retrieved scrape response")
	err = c.doPush(scrapeResp, request, ws)
	if err != nil {
		level.Warn(logger).Log("msg", "Failed to push scrape response:", "err", err)
		return
	}
	level.Info(logger).Log("msg", "Pushed scrape result")
}

// Report the result of the scrape back up to the proxy.
func (c *Coordinator) doPush(resp *http.Response, origRequest *http.Request, ws *websocket.Conn) error {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))

	buf := &bytes.Buffer{}
	resp.Write(buf)
	msg := util.SocketMessage{
		Type: util.Response,
		Payload: map[string]string{
			"response": buf.String(),
		},
	}

	err := websocket.JSON.Send(ws, msg)
	if err != nil {
		return err
	}
	return nil
}

func loop(c Coordinator) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	base, err := url.Parse(*proxyURL)
	if err != nil {
		level.Error(c.logger).Log("msg", "Error parsing url:", "err", err)
		return
	}
	u, err := url.Parse("/socket")
	if err != nil {
		level.Error(c.logger).Log("msg", "Error parsing url:", "err", err)
		return
	}
	url := base.ResolveReference(u)

	if url.Scheme == "http" {
		url.Scheme = "ws"
	} else if url.Scheme == "https" {
		url.Scheme = "wss"
	}

	var origin = fmt.Sprintf("http://%s/", *myFqdn)

	level.Info(c.logger).Log("msg", "dialing proxy:", "url", url.String())
	ws, err := websocket.Dial(url.String(), "", origin)
	if err != nil {
		level.Error(c.logger).Log("err", err.Error())
		time.Sleep(time.Second) // Don't pound the server. TODO: Randomised exponential backoff.
		return
	}

	err = websocket.JSON.Send(ws, &util.SocketMessage{
		Type: util.Register,
		Payload: map[string]string{
			"fqdn": *myFqdn,
		},
	})
	if err != nil {
		level.Error(c.logger).Log(err)
	}
	level.Info(c.logger).Log("msg", "client registered with proxy")

	ready := &util.SocketMessage{
		Type: util.Ready,
	}

	for {
		err = websocket.JSON.Send(ws, ready)
		if err != nil {
			level.Error(c.logger).Log(err)
		}

		var msg = &util.SocketMessage{}
		err = websocket.JSON.Receive(ws, msg)
		if err != nil {
			if err == io.EOF {
				level.Info(c.logger).Log("msg", "websocket got EOF")
				return
			}
			level.Error(c.logger).Log(err)
			return
		}
		level.Info(c.logger).Log("msg", "received JSON msg")

		reader := bufio.NewReader(strings.NewReader(msg.Payload["request"]))
		request, err := http.ReadRequest(reader)
		if err != nil {
			level.Error(c.logger).Log(err)
		}
		fmt.Printf("Scrape Request: %s\n", request)
		request.RequestURI = ""
		c.doScrape(request, client, ws)
		//time.Sleep(time.Second)
	}
}

func main() {
	allowedLevel := promlog.AllowedLevel{}
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(allowedLevel)
	coordinator := Coordinator{logger: logger}
	if *proxyURL == "" {
		level.Error(coordinator.logger).Log("msg", "-proxy-url flag must be specified.")
		os.Exit(1)
	}
	level.Info(coordinator.logger).Log("msg", "URL and FQDN info", "proxy_url", *proxyURL, "Using FQDN of", *myFqdn)
	for {
		loop(coordinator)
	}
}
