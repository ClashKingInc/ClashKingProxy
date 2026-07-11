package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("compress response body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return compressed.Bytes()
}

func TestNewProxyServerPrecomputesBearerHeaders(t *testing.T) {
	server := newProxyServer(nil, newStatsCollector(), []string{"key-a", "key-b"}, "")

	want := []string{"Bearer key-a", "Bearer key-b"}
	if len(server.keys.keys) != len(want) {
		t.Fatalf("precomputed keys = %d, want %d", len(server.keys.keys), len(want))
	}
	for i := range want {
		if server.keys.keys[i] != want[i] {
			t.Errorf("precomputed key %d = %q, want %q", i, server.keys.keys[i], want[i])
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/clans/%23ABC", nil)
	for i := range want {
		got, ok := server.resolveAuthorization(req, authRotateKeys)
		if !ok {
			t.Fatalf("authorization %d was not resolved", i)
		}
		if got != want[i] {
			t.Errorf("authorization %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "empty", header: "", want: false},
		{name: "gzip", header: "gzip", want: true},
		{name: "case insensitive", header: "GZip", want: true},
		{name: "gzip among unsupported encodings", header: "br, gzip, deflate", want: true},
		{name: "positive quality", header: "br, gzip; q=0.5", want: true},
		{name: "zero quality", header: "gzip;q=0, br", want: false},
		{name: "unsupported encodings", header: "br, deflate", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := acceptsGzip(test.header); got != test.want {
				t.Fatalf("acceptsGzip(%q) = %t, want %t", test.header, got, test.want)
			}
		})
	}
}

func TestProxyRequestPreservesRequestedGzip(t *testing.T) {
	body := []byte(`{"message":"compressed"}`)
	compressed := gzipBytes(t, body)
	var upstreamAcceptEncoding string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write(compressed)
	}))
	defer upstream.Close()

	client := buildHTTPClient()
	defer client.CloseIdleConnections()
	server := newProxyServer(client, newStatsCollector(), []string{"test-key"}, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()

	statusCode, proxyFailure := server.proxyRequest(rec, req, "/v1/", upstream.URL+"/", authRotateKeys)

	if statusCode != http.StatusOK || proxyFailure {
		t.Fatalf("proxy result = (%d, %t), want (%d, false)", statusCode, proxyFailure, http.StatusOK)
	}
	if upstreamAcceptEncoding != "gzip" {
		t.Errorf("upstream Accept-Encoding = %q, want %q", upstreamAcceptEncoding, "gzip")
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want %q", got, "gzip")
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(compressed)) {
		t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(len(compressed)))
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want %q", got, "Accept-Encoding")
	}
	if !bytes.Equal(rec.Body.Bytes(), compressed) {
		t.Fatal("proxy did not preserve the compressed response body")
	}
}

func TestProxyRequestDecodesGzipWhenClientDoesNotRequestIt(t *testing.T) {
	body := []byte(`{"message":"identity"}`)
	compressed := gzipBytes(t, body)
	var upstreamAcceptEncoding string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write(compressed)
	}))
	defer upstream.Close()

	client := buildHTTPClient()
	defer client.CloseIdleConnections()
	server := newProxyServer(client, newStatsCollector(), []string{"test-key"}, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	rec := httptest.NewRecorder()

	statusCode, proxyFailure := server.proxyRequest(rec, req, "/v1/", upstream.URL+"/", authRotateKeys)

	if statusCode != http.StatusOK || proxyFailure {
		t.Fatalf("proxy result = (%d, %t), want (%d, false)", statusCode, proxyFailure, http.StatusOK)
	}
	if upstreamAcceptEncoding != "gzip" {
		t.Errorf("transport Accept-Encoding = %q, want %q", upstreamAcceptEncoding, "gzip")
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want empty", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("response body = %q, want %q", rec.Body.Bytes(), body)
	}
}

func TestProxyRequestReturnsGatewayTimeoutForUpstreamTimeout(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	server := newProxyServer(client, newStatsCollector(), []string{"test-key"}, "")

	req := httptest.NewRequest(http.MethodGet, "/v1/clans/%23ABC", nil)
	rec := httptest.NewRecorder()

	statusCode, proxyFailure := server.proxyRequest(rec, req, "/v1/", prodBaseURL, authRotateKeys)

	if statusCode != http.StatusGatewayTimeout {
		t.Fatalf("statusCode = %d, want %d", statusCode, http.StatusGatewayTimeout)
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
	if !proxyFailure {
		t.Fatal("proxyFailure = false, want true")
	}
	if retryAfter := rec.Header().Get("Retry-After"); retryAfter != "5" {
		t.Fatalf("Retry-After = %q, want %q", retryAfter, "5")
	}
}

func TestProxyRequestReturnsBadGatewayForOtherUpstreamErrors(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	}
	server := newProxyServer(client, newStatsCollector(), []string{"test-key"}, "")

	req := httptest.NewRequest(http.MethodGet, "/v1/clans/%23ABC", nil)
	rec := httptest.NewRecorder()

	statusCode, proxyFailure := server.proxyRequest(rec, req, "/v1/", prodBaseURL, authRotateKeys)

	if statusCode != http.StatusBadGateway {
		t.Fatalf("statusCode = %d, want %d", statusCode, http.StatusBadGateway)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if !proxyFailure {
		t.Fatal("proxyFailure = false, want true")
	}
}
