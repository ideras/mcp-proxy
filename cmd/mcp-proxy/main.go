package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/tbxark/mcp-proxy/internal/config"
	"github.com/tbxark/mcp-proxy/internal/proxy"
)

var BuildVersion = "dev"

func main() {
	conf := flag.String("config", "config.json", "path to config file or a http(s) url")
	insecure := flag.Bool("insecure", false, "allow insecure HTTPS connections by skipping TLS certificate verification")
	expandEnv := flag.Bool("expand-env", true, "expand environment variables in config file")
	httpHeaders := flag.String("http-headers", "", "optional HTTP headers for config URL, format: 'Key1:Value1;Key2:Value2'")
	httpTimeout := flag.Int("http-timeout", 10, "HTTP timeout in seconds when fetching config from URL")

	version := flag.Bool("version", false, "print version and exit")
	help := flag.Bool("help", false, "print help and exit")
	flag.Parse()
	if *help {
		flag.Usage()
		return
	}
	if *version {
		fmt.Println(BuildVersion)
		return
	}
	cfg, err := config.Load(*conf, *insecure, *expandEnv, *httpHeaders, *httpTimeout)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	err = proxy.Start(cfg)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
