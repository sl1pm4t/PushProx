package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	kitlog "github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"

	"time"

	"github.com/robustperception/pushprox/util"
	"github.com/rs/cors"
	"golang.org/x/net/websocket"
)

var (
	listenAddress = kingpin.Flag("web.listen-address", "Address to listen on for proxy and client requests.").Default(":8080").String()

	clients = make(map[*websocket.Conn]bool) // connected clients

	allowedLevel = promlog.AllowedLevel{}
	logger       kitlog.Logger
	coordinator  *Coordinator

	regAck = &util.SocketMessage{
		Type: util.RegisterAck,
	}
)

func copyHTTPResponse(resp *http.Response, w http.ResponseWriter) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

type targetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

func main() {
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger = promlog.New(allowedLevel)
	coordinator = NewCoordinator(logger)

	mux := http.NewServeMux()

	mux.Handle("/socket", websocket.Handler(handleSocketConnection))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Proxy request
		if r.URL.Host != "" {
			ctx, _ := context.WithTimeout(r.Context(), util.GetScrapeTimeout(r.Header))
			request := r.WithContext(ctx)
			request.RequestURI = ""

			resp, err := coordinator.DoScrape(ctx, request)
			if err != nil {
				level.Error(logger).Log("msg", "Error scraping:", "err", err, "url", request.URL.String())
				http.Error(w, fmt.Sprintf("Error scraping %q: %s", request.URL.String(), err.Error()), 500)
				return
			}
			defer resp.Body.Close()
			copyHTTPResponse(resp, w)
			return
		}

		// Scrape response from client.
		if r.URL.Path == "/push" {
			buf := &bytes.Buffer{}
			io.Copy(buf, r.Body)
			scrapeResult, _ := http.ReadResponse(bufio.NewReader(buf), nil)
			level.Info(logger).Log("msg", "Got /push", "scrape_id", scrapeResult.Header.Get("Id"))
			err := coordinator.ScrapeResult(scrapeResult)
			if err != nil {
				level.Error(logger).Log("msg", "Error pushing:", "err", err, "scrape_id", scrapeResult.Header.Get("Id"))
				http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), 500)
			}
			return
		}

		if r.URL.Path == "/clients" {
			known := coordinator.KnownClients()
			targets := make([]*targetGroup, 0, len(known))
			for _, k := range known {
				targets = append(targets, &targetGroup{Targets: []string{k}})
			}
			json.NewEncoder(w).Encode(targets)
			level.Info(logger).Log("msg", "Responded to /clients", "client_count", len(known))
			return
		}

		http.Error(w, "404: Unknown path", 404)
	})

	// cors.Default() setup the middleware with default options being
	// all origins accepted with simple methods (GET, POST). See
	// documentation below for more options.
	handler := cors.Default().Handler(mux)

	level.Info(logger).Log("msg", "listening", "address", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, handler))
}

func handleSocketConnection(ws *websocket.Conn) {
	level.Info(logger).Log("msg", "processing new WS connection", "remote_addr", ws.RemoteAddr)
	// Register our new client

	// wait for first register message
	var msg util.SocketMessage
	// Read in a new message as JSON and map it to a Socket Message object
	level.Info(logger).Log("msg", "waiting for register message")

	err := websocket.JSON.Receive(ws, &msg)
	if err != nil {
		level.Error(logger).Log("error: %v", err)
		ws.Close()
		return
	}
	level.Info(logger).Log("msg", "got message")

	if msg.Type == util.Register {
		fqdn := msg.Payload["fqdn"]
		client := NewClient(fqdn, ws, coordinator)
		coordinator.Add(client)
		go func() {
			time.Sleep(time.Second)
			client.Write(regAck)
		}()
		client.Listen()
	}
}
