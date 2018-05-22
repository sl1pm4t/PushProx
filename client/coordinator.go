package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/robustperception/pushprox/util"
	"golang.org/x/net/websocket"
)

type Coordinator struct {
	logger log.Logger
}

func (c *Coordinator) Add(client *WSClient) {
	// NoOp
}

func (c *Coordinator) Del(client *WSClient) {
	// NoOp
}

func (c *Coordinator) doScrape(request *http.Request) *http.Response {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

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
		return c.wrapResponse(resp, request)
		//err = c.doPush(resp, request, ws)
		//if err != nil {
		//	level.Warn(logger).Log("msg", "Failed to push failed scrape response:", "err", err)
		//	return
		//}
		//level.Info(logger).Log("msg", "Pushed failed scrape response")
		//return
	}
	level.Info(logger).Log("msg", "Retrieved scrape response", "status_code", scrapeResp.StatusCode)
	return c.wrapResponse(scrapeResp, request)
	//err = c.doPush(scrapeResp, request, ws)
	//if err != nil {
	//	level.Warn(logger).Log("msg", "Failed to push scrape response:", "err", err)
	//	return
	//}
	//level.Info(logger).Log("msg", "Pushed scrape result")
}

func (c *Coordinator) wrapResponse(resp *http.Response, origRequest *http.Request) *http.Response {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))
	return resp
}

// Report the result of the scrape back up to the proxy.
func (c *Coordinator) doPush(resp *http.Response, origRequest *http.Request, ws *websocket.Conn) error {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))

	buf := &bytes.Buffer{}
	resp.Write(buf)
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	msg := util.SocketMessage{
		Type: util.Response,
		Payload: map[string]string{
			"response": encoded,
		},
	}

	err := websocket.JSON.Send(ws, msg)
	if err != nil {
		return err
	}
	return nil
}
