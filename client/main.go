package main

import (
	"os"

	"github.com/ShowMax/go-fqdn"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

var (
	myFqdn   = kingpin.Flag("fqdn", "FQDN to register with").Default(fqdn.Get()).String()
	proxyURL = kingpin.Flag("proxy-url", "Push proxy to talk to.").Required().String()
)

func main() {
	// parse flags & setup logging
	allowedLevel := promlog.AllowedLevel{}
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(allowedLevel)

	// setup client coordinator
	coordinator := &Coordinator{logger: logger}
	if *proxyURL == "" {
		level.Error(coordinator.logger).Log("msg", "-proxy-url flag must be specified.")
		os.Exit(1)
	}
	level.Info(coordinator.logger).Log("msg", "URL and FQDN info", "proxy_url", *proxyURL, "Using FQDN of", *myFqdn)

	// start client
	client := NewClient(*proxyURL, logger, coordinator)
	client.Run()
}
