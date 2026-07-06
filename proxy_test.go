package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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
