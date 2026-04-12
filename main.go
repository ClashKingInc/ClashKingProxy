package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func loadKeysFromEnv() []string {
	raw := os.Getenv("COC_KEYS")
	if raw == "" {
		log.Println("No API keys provided. Set COC_KEYS in env.")
		os.Exit(1)
	}

	keys := make([]string, 0)
	for _, key := range strings.Split(raw, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		log.Println("No API keys after parsing COC_KEYS. Exiting.")
		os.Exit(1)
	}
	return keys
}

func buildHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 128
	transport.IdleConnTimeout = 90 * time.Second
	transport.ForceAttemptHTTP2 = true

	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func main() {
	_ = godotenv.Load()

	keys := loadKeysFromEnv()

	host := os.Getenv("HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8011"
	}

	server := newProxyServer(buildHTTPClient(), newStatsCollector(), keys, os.Getenv("DEV_COC_URL"))

	addr := host + ":" + port
	log.Printf("CoC proxy listening on http://%s\nKeys loaded: %d\n", addr, len(keys))
	if err := http.ListenAndServe(addr, server.routes()); err != nil {
		log.Fatal(err)
	}
}
