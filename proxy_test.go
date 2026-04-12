package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKeyRotatorNextCycles(t *testing.T) {
	rotator := keyRotator{keys: []string{"key-1", "key-2"}}

	got := []string{
		rotator.Next(),
		rotator.Next(),
		rotator.Next(),
	}
	want := []string{"key-1", "key-2", "key-1"}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Next() at index %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveAuthorizationRotateKeys(t *testing.T) {
	server := newProxyServer(nil, nil, []string{"first", "second"}, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/players/%23TAG", nil)

	first, ok := server.resolveAuthorization(req, authRotateKeys)
	if !ok {
		t.Fatal("resolveAuthorization() returned ok=false for rotated keys")
	}
	if first != "Bearer first" {
		t.Fatalf("first rotated header = %q, want %q", first, "Bearer first")
	}

	second, ok := server.resolveAuthorization(req, authRotateKeys)
	if !ok {
		t.Fatal("resolveAuthorization() returned ok=false for rotated keys on second call")
	}
	if second != "Bearer second" {
		t.Fatalf("second rotated header = %q, want %q", second, "Bearer second")
	}
}

func TestResolveAuthorizationForwardBearer(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
		ok     bool
	}{
		{
			name:   "accepts case insensitive bearer",
			header: "bearer token-123",
			want:   "Bearer token-123",
			ok:     true,
		},
		{
			name:   "trims spaces around token",
			header: "  Bearer   token-123   ",
			want:   "Bearer token-123",
			ok:     true,
		},
		{
			name:   "rejects missing token",
			header: "Bearer   ",
			ok:     false,
		},
		{
			name:   "rejects wrong scheme",
			header: "Basic token-123",
			ok:     false,
		},
	}

	server := newProxyServer(nil, nil, []string{"unused"}, "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/dev/players/%23TAG", nil)
			req.Header.Set("Authorization", tt.header)

			got, ok := server.resolveAuthorization(req, authForwardBearer)
			if ok != tt.ok {
				t.Fatalf("resolveAuthorization() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("resolveAuthorization() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildForwardPathAndQuery(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		requestURI  string
		routePrefix string
		want        string
	}{
		{
			name:        "removes fields from post queries",
			method:      http.MethodPost,
			requestURI:  testProdPlayerPathWithFields,
			routePrefix: "/v1/",
			want:        "players/%23TAG?limit=10",
		},
		{
			name:        "drops query entirely when fields is the only post parameter",
			method:      http.MethodPost,
			requestURI:  testProdPlayerPath + "?fields=name",
			routePrefix: "/v1/",
			want:        "players/%23TAG",
		},
		{
			name:        "keeps fields for get requests",
			method:      http.MethodGet,
			requestURI:  testProdPlayerPathWithFields,
			routePrefix: "/v1/",
			want:        "players/%23TAG?fields=name&limit=10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.requestURI, strings.NewReader("{}"))
			req.RequestURI = tt.requestURI

			got := buildForwardPathAndQuery(req, tt.routePrefix)
			if got != tt.want {
				t.Fatalf("buildForwardPathAndQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: ""},
		{raw: strings.TrimRight(testExampleBaseURL, "/"), want: testExampleBaseURL},
		{raw: testExampleBaseURL, want: testExampleBaseURL},
		{raw: testExampleBaseURL + "//", want: testExampleBaseURL},
	}

	for _, tt := range tests {
		if got := normalizeBaseURL(tt.raw); got != tt.want {
			t.Fatalf("normalizeBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: "/"},
		{path: "/v1/players/%23ABC123", want: testProdPlayerEndpoint},
		{path: "/v1/clans/%23ABC123/currentwar", want: "/clans/{clanTag}/currentwar"},
		{path: "/dev/locations/32000007/rankings/players", want: "/locations/{locationId}/rankings/players"},
		{path: "/v1/labels/players", want: "/labels/players"},
		{path: "/v1/labels/12345", want: "/labels/{labelId}"},
		{path: "/v1/goldpass/seasons/2024-01", want: "/goldpass/seasons/{seasonId}"},
		{path: "/v1/whatever/123/%23ABC123", want: "/whatever/{id}/{tag}"},
	}

	for _, tt := range tests {
		if got := normalizeEndpoint(tt.path); got != tt.want {
			t.Fatalf("normalizeEndpoint(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestNormalizeUnknownSegment(t *testing.T) {
	tests := []struct {
		segment string
		want    string
	}{
		{segment: "", want: ""},
		{segment: "%23ABC123", want: "{tag}"},
		{segment: "%2523ABC123", want: "{tag}"},
		{segment: "#ABC123", want: "{tag}"},
		{segment: "12345", want: "{id}"},
		{segment: "warlog", want: "warlog"},
	}

	for _, tt := range tests {
		if got := normalizeUnknownSegment(tt.segment); got != tt.want {
			t.Fatalf("normalizeUnknownSegment(%q) = %q, want %q", tt.segment, got, tt.want)
		}
	}
}

func TestIsStaticLabelSegment(t *testing.T) {
	tests := []struct {
		segment string
		want    bool
	}{
		{segment: "players", want: true},
		{segment: "capital", want: true},
		{segment: "random", want: false},
	}

	for _, tt := range tests {
		if got := isStaticLabelSegment(tt.segment); got != tt.want {
			t.Fatalf("isStaticLabelSegment(%q) = %v, want %v", tt.segment, got, tt.want)
		}
	}
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "12345", want: true},
		{value: "12a45", want: false},
		{value: "-123", want: false},
	}

	for _, tt := range tests {
		if got := isNumeric(tt.value); got != tt.want {
			t.Fatalf("isNumeric(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestProxyRequestForwardsRequestAndCopiesResponse(t *testing.T) {
	var got struct {
		Method        string
		PathAndQuery  string
		Authorization string
		Accept        string
		ContentType   string
		Body          string
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		got.Method = r.Method
		got.PathAndQuery = r.URL.RequestURI()
		got.Authorization = r.Header.Get("Authorization")
		got.Accept = r.Header.Get("Accept")
		got.ContentType = r.Header.Get(headerContentType)
		got.Body = string(body)

		w.Header().Set(headerContentType, testJSONContentType)
		w.Header().Set("ETag", testProxyETag)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	server := newProxyServer(upstream.Client(), nil, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodPost, testProdPlayerPathWithFields, strings.NewReader(`{"hello":"world"}`))
	req.RequestURI = testProdPlayerPathWithFields
	rec := httptest.NewRecorder()

	status, proxyFailure := server.proxyRequest(rec, req, "/v1/", normalizeBaseURL(upstream.URL), authRotateKeys)
	if status != http.StatusCreated {
		t.Fatalf("proxyRequest() status = %d, want %d", status, http.StatusCreated)
	}
	if proxyFailure {
		t.Fatal("proxyRequest() proxyFailure = true, want false")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("response code = %d, want %d", rec.Code, http.StatusCreated)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("response body = %q, want %q", rec.Body.String(), `{"ok":true}`)
	}
	if rec.Header().Get("ETag") != testProxyETag {
		t.Fatalf("ETag header = %q, want %q", rec.Header().Get("ETag"), testProxyETag)
	}

	if got.Method != http.MethodPost {
		t.Fatalf("upstream method = %q, want %q", got.Method, http.MethodPost)
	}
	if got.PathAndQuery != testProdPlayerForwardedPath {
		t.Fatalf("upstream path and query = %q, want %q", got.PathAndQuery, testProdPlayerForwardedPath)
	}
	if got.Authorization != "Bearer "+testRotatedKey {
		t.Fatalf("upstream authorization = %q, want %q", got.Authorization, "Bearer "+testRotatedKey)
	}
	if got.Accept != testJSONContentType {
		t.Fatalf("upstream accept = %q, want %q", got.Accept, testJSONContentType)
	}
	if got.ContentType != testJSONContentType {
		t.Fatalf("upstream content type = %q, want %q", got.ContentType, testJSONContentType)
	}
	if got.Body != `{"hello":"world"}` {
		t.Fatalf("upstream body = %q, want %q", got.Body, `{"hello":"world"}`)
	}
}

func TestProxyRequestForwardsBearerForDevRoutes(t *testing.T) {
	var authHeader string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	server := newProxyServer(upstream.Client(), nil, []string{"unused"}, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, testDevPlayerPath, nil)
	req.RequestURI = testDevPlayerPath
	req.Header.Set("Authorization", testForwardedBearer)
	rec := httptest.NewRecorder()

	status, proxyFailure := server.proxyRequest(rec, req, "/dev/", normalizeBaseURL(upstream.URL), authForwardBearer)
	if status != http.StatusOK {
		t.Fatalf("proxyRequest() status = %d, want %d", status, http.StatusOK)
	}
	if proxyFailure {
		t.Fatal("proxyRequest() proxyFailure = true, want false")
	}
	if authHeader != testForwardedBearer {
		t.Fatalf("forwarded authorization = %q, want %q", authHeader, testForwardedBearer)
	}
}

func TestProxyRequestRequiresBearerForDevRoutes(t *testing.T) {
	server := newProxyServer(nil, nil, []string{testRotatedKey}, "https://dev.example.com")
	req := httptest.NewRequest(http.MethodGet, testDevPlayerPath, nil)
	req.RequestURI = testDevPlayerPath
	rec := httptest.NewRecorder()

	status, proxyFailure := server.proxyRequest(rec, req, "/dev/", "https://dev.example.com/", authForwardBearer)
	if status != http.StatusUnauthorized {
		t.Fatalf("proxyRequest() status = %d, want %d", status, http.StatusUnauthorized)
	}
	if !proxyFailure {
		t.Fatal("proxyRequest() proxyFailure = false, want true")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("response code = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestProxyRequestReturnsServiceUnavailableWithoutBaseURL(t *testing.T) {
	server := newProxyServer(nil, nil, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodGet, testProdPlayerPath, nil)
	req.RequestURI = testProdPlayerPath
	rec := httptest.NewRecorder()

	status, proxyFailure := server.proxyRequest(rec, req, "/v1/", "", authRotateKeys)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("proxyRequest() status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	if !proxyFailure {
		t.Fatal("proxyRequest() proxyFailure = false, want true")
	}
	if !strings.Contains(rec.Body.String(), "upstream is not configured") {
		t.Fatalf("response body = %q, want to contain %q", rec.Body.String(), "upstream is not configured")
	}
}

func TestProxyRequestReturnsBadGatewayOnTransportError(t *testing.T) {
	server := newProxyServer(nil, nil, []string{testRotatedKey}, "")
	server.client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failure")
		}),
	}

	req := httptest.NewRequest(http.MethodGet, testProdPlayerPath, nil)
	req.RequestURI = testProdPlayerPath
	rec := httptest.NewRecorder()

	status, proxyFailure := server.proxyRequest(rec, req, "/v1/", testExampleBaseURL, authRotateKeys)
	if status != http.StatusBadGateway {
		t.Fatalf("proxyRequest() status = %d, want %d", status, http.StatusBadGateway)
	}
	if !proxyFailure {
		t.Fatal("proxyRequest() proxyFailure = false, want true")
	}
	if !strings.Contains(rec.Body.String(), "upstream request failed") {
		t.Fatalf("response body = %q, want to contain %q", rec.Body.String(), "upstream request failed")
	}
}

func TestHandleRootReturnsJSON(t *testing.T) {
	server := newProxyServer(nil, nil, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.handleRoot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleRoot() status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get(headerContentType) != testJSONContentType {
		t.Fatalf("content type = %q, want %q", rec.Header().Get(headerContentType), testJSONContentType)
	}
	if rec.Body.String() != `{"message":"CoC Proxy Server is running."}` {
		t.Fatalf("response body = %q", rec.Body.String())
	}
}

func TestHandleStatsReturnsJSONPayload(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 30, 0, time.UTC)
	stats, setNow := newTestStatsCollector(base)

	setNow(base.Add(-1 * time.Minute))
	stats.Record(testClanEndpoint, 40*time.Millisecond, http.StatusOK, false)

	setNow(base)
	stats.Record(testProdPlayerEndpoint, 20*time.Millisecond, http.StatusNotFound, true)
	stats.Record(testProdPlayerEndpoint, 30*time.Millisecond, http.StatusOK, false)

	server := newProxyServer(nil, stats, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodGet, "/stats?series=1m&lookback=1h&endpoints=24h&limit=1", nil)
	rec := httptest.NewRecorder()

	server.handleStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleStats() status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Header().Get(headerContentType), testJSONContentType) {
		t.Fatalf("content type = %q, want JSON", rec.Header().Get(headerContentType))
	}

	var response statsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if response.SeriesData == nil {
		t.Fatal("SeriesData = nil")
	}
	if response.EndpointBreakdown == nil {
		t.Fatal("EndpointBreakdown = nil")
	}
	if response.Windows["1m"].Requests != 2 {
		t.Fatalf("1m requests = %d, want %d", response.Windows["1m"].Requests, 2)
	}
	if response.EndpointBreakdown.Window != "24h" {
		t.Fatalf("endpoint breakdown window = %q, want %q", response.EndpointBreakdown.Window, "24h")
	}
	if len(response.EndpointBreakdown.Endpoints) != 1 {
		t.Fatalf("endpoint breakdown rows = %d, want %d", len(response.EndpointBreakdown.Endpoints), 1)
	}
	if response.EndpointBreakdown.Endpoints[0].Endpoint != testProdPlayerEndpoint {
		t.Fatalf("top endpoint = %q, want %q", response.EndpointBreakdown.Endpoints[0].Endpoint, testProdPlayerEndpoint)
	}
}

func TestHandleStatsRejectsInvalidLimit(t *testing.T) {
	stats := newStatsCollector()
	stats.now = func() time.Time {
		return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	}

	server := newProxyServer(nil, stats, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodGet, "/stats?endpoints=24h&limit=bad", nil)
	rec := httptest.NewRecorder()

	server.handleStats(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleStats() status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid limit") {
		t.Fatalf("handleStats() body = %q, want to contain %q", rec.Body.String(), "invalid limit")
	}
}

func TestHandleStatsRejectsInvalidSeries(t *testing.T) {
	stats := newStatsCollector()
	stats.now = func() time.Time {
		return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	}

	server := newProxyServer(nil, stats, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodGet, "/stats?series=2m", nil)
	rec := httptest.NewRecorder()

	server.handleStats(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleStats() status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid series or lookback") {
		t.Fatalf("handleStats() body = %q, want to contain %q", rec.Body.String(), "invalid series or lookback")
	}
}

func TestHandleStatsRejectsInvalidEndpointWindow(t *testing.T) {
	stats := newStatsCollector()
	stats.now = func() time.Time {
		return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	}

	server := newProxyServer(nil, stats, []string{testRotatedKey}, "")
	req := httptest.NewRequest(http.MethodGet, "/stats?endpoints=30d", nil)
	rec := httptest.NewRecorder()

	server.handleStats(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleStats() status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid endpoints window") {
		t.Fatalf("handleStats() body = %q, want to contain %q", rec.Body.String(), "invalid endpoints window")
	}
}

func TestRoutesProxyRequestUpdatesStats(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	stats, _ := newTestStatsCollector(base)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	server := newProxyServer(upstream.Client(), stats, []string{testRotatedKey}, "")
	server.prodBaseURL = normalizeBaseURL(upstream.URL)

	req := httptest.NewRequest(http.MethodGet, testProdPlayerPath, nil)
	rec := httptest.NewRecorder()
	server.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeHTTP() status = %d, want %d", rec.Code, http.StatusOK)
	}

	windows := stats.buildWindows(base)
	if windows["10s"].Requests != 1 {
		t.Fatalf("10s requests = %d, want %d", windows["10s"].Requests, 1)
	}

	breakdown, ok := stats.buildEndpointBreakdown(base, "24h", 10)
	if !ok {
		t.Fatal("buildEndpointBreakdown() returned ok=false")
	}
	if len(breakdown.Endpoints) != 1 {
		t.Fatalf("endpoint rows = %d, want %d", len(breakdown.Endpoints), 1)
	}
	if breakdown.Endpoints[0].Endpoint != testProdPlayerEndpoint {
		t.Fatalf("recorded endpoint = %q, want %q", breakdown.Endpoints[0].Endpoint, testProdPlayerEndpoint)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
