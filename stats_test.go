package main

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func newTestStatsCollector(now time.Time) (*statsCollector, func(time.Time)) {
	current := now
	collector := newStatsCollector()
	collector.now = func() time.Time {
		return current
	}
	return collector, func(next time.Time) {
		current = next
	}
}

func TestStatsCollectorBuildWindows(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	collector, setNow := newTestStatsCollector(base)

	setNow(base.Add(-9 * time.Second))
	collector.Record(testProdPlayerEndpoint, 50*time.Millisecond, http.StatusOK, false)

	setNow(base)
	collector.Record(testClanEndpoint, 100*time.Millisecond, http.StatusBadGateway, true)

	windows := collector.buildWindows(base)
	window := windows["10s"]

	if window.Requests != 2 {
		t.Fatalf("10s window requests = %d, want %d", window.Requests, 2)
	}
	if window.ProxyFailures != 1 {
		t.Fatalf("10s window proxy failures = %d, want %d", window.ProxyFailures, 1)
	}
	if window.StatusCounts.TwoXX != 1 || window.StatusCounts.FiveXX != 1 {
		t.Fatalf("10s window status counts = %+v, want 2xx=1 and 5xx=1", window.StatusCounts)
	}
	if math.Abs(window.AvgRPS-0.2) > 0.0001 {
		t.Fatalf("10s window avg RPS = %f, want %f", window.AvgRPS, 0.2)
	}
	requireFloat64Ptr(t, window.AvgLatencyMS, 75.0)

	if windows["1m"].Requests != 2 {
		t.Fatalf("1m window requests = %d, want %d", windows["1m"].Requests, 2)
	}
}

func TestStatsCollectorBuildSeries(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 30, 0, time.UTC)
	collector, setNow := newTestStatsCollector(base)

	setNow(base.Truncate(time.Minute).Add(-1 * time.Minute))
	collector.Record(testProdPlayerEndpoint, 40*time.Millisecond, http.StatusOK, false)

	setNow(base.Truncate(time.Minute))
	collector.Record(testProdPlayerEndpoint, 20*time.Millisecond, http.StatusNotFound, true)

	series, ok := collector.buildSeries(base, "1m", "1h")
	if !ok {
		t.Fatal("buildSeries() returned ok=false")
	}
	if len(series.Points) != 60 {
		t.Fatalf("series point count = %d, want %d", len(series.Points), 60)
	}

	previous := series.Points[len(series.Points)-2]
	if previous.Requests != 1 {
		t.Fatalf("previous point requests = %d, want %d", previous.Requests, 1)
	}
	if previous.StatusCounts.TwoXX != 1 {
		t.Fatalf("previous point status counts = %+v, want 2xx=1", previous.StatusCounts)
	}
	requireFloat64Ptr(t, previous.AvgLatencyMS, 40.0)

	last := series.Points[len(series.Points)-1]
	if last.Requests != 1 {
		t.Fatalf("last point requests = %d, want %d", last.Requests, 1)
	}
	if last.ProxyFailures != 1 {
		t.Fatalf("last point proxy failures = %d, want %d", last.ProxyFailures, 1)
	}
	if last.StatusCounts.FourXX != 1 {
		t.Fatalf("last point status counts = %+v, want 4xx=1", last.StatusCounts)
	}
	requireFloat64Ptr(t, last.AvgLatencyMS, 20.0)
}

func TestStatsCollectorBuildSeriesRejectsInvalidLookback(t *testing.T) {
	collector := newStatsCollector()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if _, ok := collector.buildSeries(now, "5m", "1h30m"); ok {
		t.Fatal("buildSeries() ok = true for invalid lookback")
	}
}

func TestStatsCollectorBuildSeriesRejectsInvalidInterval(t *testing.T) {
	collector := newStatsCollector()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if _, ok := collector.buildSeries(now, "2m", "1h"); ok {
		t.Fatal("buildSeries() ok = true for invalid interval")
	}
}

func TestStatsCollectorBuildEndpointBreakdown(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 0, 0, time.UTC)
	collector, setNow := newTestStatsCollector(base)

	for range 3 {
		setNow(base)
		collector.Record(testProdPlayerEndpoint, 25*time.Millisecond, http.StatusOK, false)
	}
	for range 2 {
		setNow(base.Add(-1 * time.Minute))
		collector.Record(testClanEndpoint, 25*time.Millisecond, http.StatusOK, false)
	}
	setNow(base.Add(-25 * time.Hour))
	collector.Record("/ignored", 25*time.Millisecond, http.StatusOK, false)

	breakdown, ok := collector.buildEndpointBreakdown(base, "24h", 1)
	if !ok {
		t.Fatal("buildEndpointBreakdown() returned ok=false")
	}
	if breakdown.Limit != 1 {
		t.Fatalf("breakdown limit = %d, want %d", breakdown.Limit, 1)
	}
	if len(breakdown.Endpoints) != 1 {
		t.Fatalf("endpoint row count = %d, want %d", len(breakdown.Endpoints), 1)
	}
	if breakdown.Endpoints[0].Endpoint != testProdPlayerEndpoint {
		t.Fatalf("top endpoint = %q, want %q", breakdown.Endpoints[0].Endpoint, testProdPlayerEndpoint)
	}
	if breakdown.Endpoints[0].Requests != 3 {
		t.Fatalf("top endpoint requests = %d, want %d", breakdown.Endpoints[0].Requests, 3)
	}
}

func TestStatsCollectorBuildEndpointBreakdownNormalizesLimits(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 0, 0, time.UTC)
	collector, setNow := newTestStatsCollector(base)

	setNow(base)
	collector.Record(testProdPlayerEndpoint, 25*time.Millisecond, http.StatusOK, false)

	breakdown, ok := collector.buildEndpointBreakdown(base, "24h", 0)
	if !ok {
		t.Fatal("buildEndpointBreakdown() returned ok=false for default limit")
	}
	if breakdown.Limit != 25 {
		t.Fatalf("default limit = %d, want %d", breakdown.Limit, 25)
	}

	breakdown, ok = collector.buildEndpointBreakdown(base, "24h", 101)
	if !ok {
		t.Fatal("buildEndpointBreakdown() returned ok=false for capped limit")
	}
	if breakdown.Limit != 100 {
		t.Fatalf("capped limit = %d, want %d", breakdown.Limit, 100)
	}
}

func TestStatsCollectorBuildEndpointBreakdownForSevenDays(t *testing.T) {
	base := time.Date(2026, time.January, 8, 12, 0, 0, 0, time.UTC)
	collector, setNow := newTestStatsCollector(base)

	setNow(base)
	collector.Record(testProdPlayerEndpoint, 25*time.Millisecond, http.StatusOK, false)
	setNow(base.Add(-6 * time.Hour))
	collector.Record(testClanEndpoint, 25*time.Millisecond, http.StatusOK, false)
	setNow(base.Add(-8 * 24 * time.Hour))
	collector.Record("/ignored", 25*time.Millisecond, http.StatusOK, false)

	breakdown, ok := collector.buildEndpointBreakdown(base, "7d", 10)
	if !ok {
		t.Fatal("buildEndpointBreakdown() returned ok=false")
	}
	if len(breakdown.Endpoints) != 2 {
		t.Fatalf("7d endpoint rows = %d, want %d", len(breakdown.Endpoints), 2)
	}
}

func TestStatsCollectorBuildEndpointBreakdownRejectsInvalidWindow(t *testing.T) {
	collector := newStatsCollector()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if _, ok := collector.buildEndpointBreakdown(now, "30d", 10); ok {
		t.Fatal("buildEndpointBreakdown() ok = true for invalid window")
	}
}

func TestAggregateAvgLatencyMSNilWhenEmpty(t *testing.T) {
	var agg aggregate

	if got := agg.avgLatencyMS(); got != nil {
		t.Fatalf("avgLatencyMS() = %v, want nil", *got)
	}
}

func TestRecordMetricRingResetsOverwrittenBucket(t *testing.T) {
	stamps := make([]int64, 2)
	hits := make([]uint32, 2)
	latencyUS := make([]uint64, 2)
	twoXX := make([]uint32, 2)
	threeXX := make([]uint32, 2)
	fourXX := make([]uint32, 2)
	fiveXX := make([]uint32, 2)
	proxyFailures := make([]uint32, 2)

	recordMetricRing(stamps, hits, latencyUS, twoXX, threeXX, fourXX, fiveXX, proxyFailures, 1, 50*time.Millisecond, http.StatusOK, false)
	recordMetricRing(stamps, hits, latencyUS, twoXX, threeXX, fourXX, fiveXX, proxyFailures, 3, 20*time.Millisecond, http.StatusBadGateway, true)

	oldAgg := aggregateMetricRing(stamps, hits, latencyUS, twoXX, threeXX, fourXX, fiveXX, proxyFailures, 1, 1)
	if oldAgg.Requests != 0 {
		t.Fatalf("overwritten stamp requests = %d, want %d", oldAgg.Requests, 0)
	}

	newAgg := aggregateMetricRing(stamps, hits, latencyUS, twoXX, threeXX, fourXX, fiveXX, proxyFailures, 3, 3)
	if newAgg.Requests != 1 {
		t.Fatalf("new stamp requests = %d, want %d", newAgg.Requests, 1)
	}
	if newAgg.StatusCounts.FiveXX != 1 {
		t.Fatalf("new stamp 5xx count = %d, want %d", newAgg.StatusCounts.FiveXX, 1)
	}
	if newAgg.ProxyFailures != 1 {
		t.Fatalf("new stamp proxy failures = %d, want %d", newAgg.ProxyFailures, 1)
	}
	requireFloat64Ptr(t, newAgg.avgLatencyMS(), 20.0)
}

func requireFloat64Ptr(t *testing.T, got *float64, want float64) {
	t.Helper()

	if got == nil {
		t.Fatalf("float pointer = nil, want %f", want)
	}
	if math.Abs(*got-want) > 0.0001 {
		t.Fatalf("float pointer = %f, want %f", *got, want)
	}
}
