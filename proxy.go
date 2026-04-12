package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const prodBaseURL = "https://api.clashofclans.com/v1/"

type authMode int

const (
	authRotateKeys authMode = iota
	authForwardBearer
)

type keyRotator struct {
	keys []string
	idx  uint32
}

func (r *keyRotator) Next() string {
	next := atomic.AddUint32(&r.idx, 1)
	return r.keys[(int(next)-1)%len(r.keys)]
}

type proxyServer struct {
	client      *http.Client
	stats       *statsCollector
	keys        *keyRotator
	prodBaseURL string
	devBaseURL  string
}

func newProxyServer(client *http.Client, stats *statsCollector, keys []string, devBaseURL string) *proxyServer {
	if client == nil {
		client = buildHTTPClient()
	}
	if stats == nil {
		stats = newStatsCollector()
	}
	return &proxyServer{
		client:      client,
		stats:       stats,
		keys:        &keyRotator{keys: append([]string(nil), keys...)},
		prodBaseURL: prodBaseURL,
		devBaseURL:  normalizeBaseURL(devBaseURL),
	}
}

func (s *proxyServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/v1/", s.handleProxy("/v1/", s.prodBaseURL, authRotateKeys))
	mux.HandleFunc("/dev/", s.handleProxy("/dev/", s.devBaseURL, authForwardBearer))
	return mux
}

func (s *proxyServer) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"message":"CoC Proxy Server is running."}`)
}

func (s *proxyServer) handleStats(w http.ResponseWriter, r *http.Request) {
	now := s.stats.now()
	response := statsResponse{
		Now:     now.UTC().Format(time.RFC3339),
		Windows: s.stats.buildWindows(now),
	}

	query := r.URL.Query()
	if seriesLabel := query.Get("series"); seriesLabel != "" {
		lookbackLabel := query.Get("lookback")
		if lookbackLabel == "" {
			lookbackLabel = "48h"
		}
		series, ok := s.stats.buildSeries(now, seriesLabel, lookbackLabel)
		if !ok {
			http.Error(w, "invalid series or lookback", http.StatusBadRequest)
			return
		}
		response.SeriesData = series
	}

	if endpointWindow := query.Get("endpoints"); endpointWindow != "" {
		limit := 25
		if rawLimit := query.Get("limit"); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		breakdown, ok := s.stats.buildEndpointBreakdown(now, endpointWindow, limit)
		if !ok {
			http.Error(w, "invalid endpoints window", http.StatusBadRequest)
			return
		}
		response.EndpointBreakdown = breakdown
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *proxyServer) handleProxy(routePrefix, baseURL string, mode authMode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := s.stats.now()
		endpoint := normalizeEndpoint(r.URL.EscapedPath())

		statusCode, proxyFailure := s.proxyRequest(w, r, routePrefix, baseURL, mode)
		s.stats.Record(endpoint, s.stats.now().Sub(start), statusCode, proxyFailure)
	}
}

func (s *proxyServer) proxyRequest(w http.ResponseWriter, r *http.Request, routePrefix, baseURL string, mode authMode) (int, bool) {
	if baseURL == "" {
		http.Error(w, "upstream is not configured", http.StatusServiceUnavailable)
		return http.StatusServiceUnavailable, true
	}

	authHeader, ok := s.resolveAuthorization(r, mode)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return http.StatusUnauthorized, true
	}

	pathAndQuery := buildForwardPathAndQuery(r, routePrefix)
	forwardURL := baseURL + pathAndQuery

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, r.Method, forwardURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create upstream request", http.StatusInternalServerError)
		return http.StatusInternalServerError, true
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)
	if r.Method == http.MethodPost {
		if contentType := r.Header.Get("Content-Type"); contentType != "" {
			req.Header.Set("Content-Type", contentType)
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	upstreamResp, err := s.client.Do(req)
	if err != nil {
		http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return http.StatusBadGateway, true
	}
	defer func() {
		if err := upstreamResp.Body.Close(); err != nil {
			log.Printf("failed to close upstream response body: %v", err)
		}
	}()

	for _, headerName := range []string{"cache-control", "expires", "etag", "last-modified", "content-type"} {
		if value := upstreamResp.Header.Get(headerName); value != "" {
			w.Header().Set(headerName, value)
		}
	}

	w.WriteHeader(upstreamResp.StatusCode)
	_, _ = io.Copy(w, upstreamResp.Body)
	return upstreamResp.StatusCode, false
}

func (s *proxyServer) resolveAuthorization(r *http.Request, mode authMode) (string, bool) {
	switch mode {
	case authRotateKeys:
		return "Bearer " + s.keys.Next(), true
	case authForwardBearer:
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			return "", false
		}
		token := strings.TrimSpace(authHeader[len("Bearer "):])
		if token == "" {
			return "", false
		}
		return "Bearer " + token, true
	default:
		return "", false
	}
}

func buildForwardPathAndQuery(r *http.Request, routePrefix string) string {
	pathAndQuery := strings.TrimPrefix(r.RequestURI, routePrefix)
	if r.Method == http.MethodPost && strings.Contains(pathAndQuery, "fields=") {
		parts := strings.SplitN(pathAndQuery, "?", 2)
		pathOnly := parts[0]
		if len(parts) == 2 {
			query := r.URL.Query()
			query.Del("fields")
			if encoded := query.Encode(); encoded != "" {
				pathAndQuery = pathOnly + "?" + encoded
			} else {
				pathAndQuery = pathOnly
			}
		}
	}
	return pathAndQuery
}

func normalizeBaseURL(raw string) string {
	if raw == "" {
		return ""
	}
	return strings.TrimRight(raw, "/") + "/"
}
