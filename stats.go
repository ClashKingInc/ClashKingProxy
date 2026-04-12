package main

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	secondBuckets            = 24 * 60 * 60
	minuteBuckets            = 48 * 60
	dayEndpointMinuteBuckets = 24 * 60
	weekEndpointHourBuckets  = 7 * 24
)

var summaryWindows = map[string]int64{
	"10s": 10,
	"1m":  60,
	"5m":  5 * 60,
	"15m": 15 * 60,
	"1h":  60 * 60,
	"6h":  6 * 60 * 60,
	"12h": 12 * 60 * 60,
	"24h": 24 * 60 * 60,
}

var seriesIntervals = map[string]int64{
	"1m":  1,
	"5m":  5,
	"15m": 15,
	"30m": 30,
	"1h":  60,
}

var lookbackWindows = map[string]int64{
	"1h":  60,
	"6h":  6 * 60,
	"12h": 12 * 60,
	"24h": 24 * 60,
	"48h": 48 * 60,
}

type statusCounts struct {
	TwoXX   int64 `json:"2xx"`
	ThreeXX int64 `json:"3xx"`
	FourXX  int64 `json:"4xx"`
	FiveXX  int64 `json:"5xx"`
}

type statsWindow struct {
	Requests      int64        `json:"requests"`
	AvgRPS        float64      `json:"avg_rps"`
	AvgLatencyMS  *float64     `json:"avg_latency_ms"`
	StatusCounts  statusCounts `json:"status_counts"`
	ProxyFailures int64        `json:"proxy_failures"`
}

type seriesPoint struct {
	Start         string       `json:"start"`
	End           string       `json:"end"`
	Requests      int64        `json:"requests"`
	AvgLatencyMS  *float64     `json:"avg_latency_ms"`
	StatusCounts  statusCounts `json:"status_counts"`
	ProxyFailures int64        `json:"proxy_failures"`
}

type statsSeries struct {
	Interval string        `json:"interval"`
	Lookback string        `json:"lookback"`
	Points   []seriesPoint `json:"points"`
}

type endpointCount struct {
	Endpoint string `json:"endpoint"`
	Requests int64  `json:"requests"`
}

type endpointBreakdown struct {
	Window    string          `json:"window"`
	Limit     int             `json:"limit"`
	Endpoints []endpointCount `json:"endpoints"`
}

type statsResponse struct {
	Now               string                 `json:"now"`
	Windows           map[string]statsWindow `json:"windows"`
	SeriesData        *statsSeries           `json:"series_data,omitempty"`
	EndpointBreakdown *endpointBreakdown     `json:"endpoint_breakdown,omitempty"`
}

type aggregate struct {
	Requests      int64
	LatencyUS     uint64
	StatusCounts  statusCounts
	ProxyFailures int64
}

func (a aggregate) avgLatencyMS() *float64 {
	if a.Requests == 0 {
		return nil
	}
	return new(float64(a.LatencyUS) / float64(a.Requests) / 1000.0)
}

type endpointBucket struct {
	stamp  int64
	counts map[string]uint32
}

type statsCollector struct {
	now func() time.Time

	secStamp         [secondBuckets]int64
	secHits          [secondBuckets]uint32
	secLatencyUS     [secondBuckets]uint64
	secTwoXX         [secondBuckets]uint32
	secThreeXX       [secondBuckets]uint32
	secFourXX        [secondBuckets]uint32
	secFiveXX        [secondBuckets]uint32
	secProxyFailures [secondBuckets]uint32

	minStamp         [minuteBuckets]int64
	minHits          [minuteBuckets]uint32
	minLatencyUS     [minuteBuckets]uint64
	minTwoXX         [minuteBuckets]uint32
	minThreeXX       [minuteBuckets]uint32
	minFourXX        [minuteBuckets]uint32
	minFiveXX        [minuteBuckets]uint32
	minProxyFailures [minuteBuckets]uint32

	endpointMu      sync.Mutex
	endpointMinutes [dayEndpointMinuteBuckets]endpointBucket
	endpointHours   [weekEndpointHourBuckets]endpointBucket
}

func newStatsCollector() *statsCollector {
	return &statsCollector{now: time.Now}
}

func (s *statsCollector) Record(endpoint string, duration time.Duration, statusCode int, proxyFailure bool) {
	now := s.now()
	secStamp := now.Unix()
	minStamp := secStamp / 60
	hourStamp := secStamp / 3600

	recordMetricRing(
		s.secStamp[:],
		s.secHits[:],
		s.secLatencyUS[:],
		s.secTwoXX[:],
		s.secThreeXX[:],
		s.secFourXX[:],
		s.secFiveXX[:],
		s.secProxyFailures[:],
		secStamp,
		duration,
		statusCode,
		proxyFailure,
	)

	recordMetricRing(
		s.minStamp[:],
		s.minHits[:],
		s.minLatencyUS[:],
		s.minTwoXX[:],
		s.minThreeXX[:],
		s.minFourXX[:],
		s.minFiveXX[:],
		s.minProxyFailures[:],
		minStamp,
		duration,
		statusCode,
		proxyFailure,
	)

	if endpoint != "" {
		s.recordEndpoint(endpoint, minStamp, hourStamp)
	}
}

func (s *statsCollector) buildWindows(now time.Time) map[string]statsWindow {
	windows := make(map[string]statsWindow, len(summaryWindows))
	currentSecond := now.Unix()
	for label, seconds := range summaryWindows {
		agg := aggregateMetricRing(
			s.secStamp[:],
			s.secHits[:],
			s.secLatencyUS[:],
			s.secTwoXX[:],
			s.secThreeXX[:],
			s.secFourXX[:],
			s.secFiveXX[:],
			s.secProxyFailures[:],
			currentSecond-seconds+1,
			currentSecond,
		)
		windows[label] = statsWindow{
			Requests:      agg.Requests,
			AvgRPS:        float64(agg.Requests) / float64(seconds),
			AvgLatencyMS:  agg.avgLatencyMS(),
			StatusCounts:  agg.StatusCounts,
			ProxyFailures: agg.ProxyFailures,
		}
	}
	return windows
}

func (s *statsCollector) buildSeries(now time.Time, intervalLabel, lookbackLabel string) (*statsSeries, bool) {
	intervalMinutes, ok := seriesIntervals[intervalLabel]
	if !ok {
		return nil, false
	}
	lookbackMinutes, ok := lookbackWindows[lookbackLabel]
	if !ok || lookbackMinutes < intervalMinutes || lookbackMinutes%intervalMinutes != 0 {
		return nil, false
	}

	pointCount := int(lookbackMinutes / intervalMinutes)
	points := make([]seriesPoint, 0, pointCount)
	currentMinute := now.Unix() / 60
	alignedMinute := currentMinute - (currentMinute % intervalMinutes)

	for i := pointCount - 1; i >= 0; i-- {
		startMinute := alignedMinute - int64(i)*intervalMinutes
		endMinute := startMinute + intervalMinutes - 1
		agg := aggregateMetricRing(
			s.minStamp[:],
			s.minHits[:],
			s.minLatencyUS[:],
			s.minTwoXX[:],
			s.minThreeXX[:],
			s.minFourXX[:],
			s.minFiveXX[:],
			s.minProxyFailures[:],
			startMinute,
			endMinute,
		)
		points = append(points, seriesPoint{
			Start:         time.Unix(startMinute*60, 0).UTC().Format(time.RFC3339),
			End:           time.Unix((endMinute+1)*60, 0).UTC().Format(time.RFC3339),
			Requests:      agg.Requests,
			AvgLatencyMS:  agg.avgLatencyMS(),
			StatusCounts:  agg.StatusCounts,
			ProxyFailures: agg.ProxyFailures,
		})
	}

	return &statsSeries{
		Interval: intervalLabel,
		Lookback: lookbackLabel,
		Points:   points,
	}, true
}

func (s *statsCollector) buildEndpointBreakdown(now time.Time, windowLabel string, limit int) (*endpointBreakdown, bool) {
	var counts map[string]int64
	switch windowLabel {
	case "24h":
		counts = s.aggregateEndpointWindow(now.Unix()/60, 24*60, s.endpointMinutes[:])
	case "7d":
		counts = s.aggregateEndpointWindow(now.Unix()/3600, 7*24, s.endpointHours[:])
	default:
		return nil, false
	}

	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	rows := make([]endpointCount, 0, len(counts))
	for endpoint, requests := range counts {
		rows = append(rows, endpointCount{Endpoint: endpoint, Requests: requests})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Requests == rows[j].Requests {
			return rows[i].Endpoint < rows[j].Endpoint
		}
		return rows[i].Requests > rows[j].Requests
	})

	if len(rows) > limit {
		rows = rows[:limit]
	}

	return &endpointBreakdown{
		Window:    windowLabel,
		Limit:     limit,
		Endpoints: rows,
	}, true
}

func (s *statsCollector) recordEndpoint(endpoint string, minuteStamp, hourStamp int64) {
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()

	updateEndpointBucket(s.endpointMinutes[:], minuteStamp, endpoint)
	updateEndpointBucket(s.endpointHours[:], hourStamp, endpoint)
}

func (s *statsCollector) aggregateEndpointWindow(currentStamp, length int64, buckets []endpointBucket) map[string]int64 {
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()

	total := make(map[string]int64)
	for offset := int64(0); offset < length; offset++ {
		stamp := currentStamp - offset
		index := int(stamp % int64(len(buckets)))
		bucket := buckets[index]
		if bucket.stamp != stamp {
			continue
		}
		for endpoint, count := range bucket.counts {
			total[endpoint] += int64(count)
		}
	}
	return total
}

func updateEndpointBucket(buckets []endpointBucket, stamp int64, endpoint string) {
	index := int(stamp % int64(len(buckets)))
	bucket := &buckets[index]
	if bucket.stamp != stamp {
		bucket.stamp = stamp
		bucket.counts = make(map[string]uint32)
	}
	bucket.counts[endpoint]++
}

func recordMetricRing(
	stamps []int64,
	hits []uint32,
	latencyUS []uint64,
	twoXX []uint32,
	threeXX []uint32,
	fourXX []uint32,
	fiveXX []uint32,
	proxyFailures []uint32,
	stamp int64,
	duration time.Duration,
	statusCode int,
	proxyFailure bool,
) {
	index := int(stamp % int64(len(stamps)))
	prev := atomic.LoadInt64(&stamps[index])
	if prev != stamp {
		if atomic.CompareAndSwapInt64(&stamps[index], prev, stamp) {
			atomic.StoreUint32(&hits[index], 0)
			atomic.StoreUint64(&latencyUS[index], 0)
			atomic.StoreUint32(&twoXX[index], 0)
			atomic.StoreUint32(&threeXX[index], 0)
			atomic.StoreUint32(&fourXX[index], 0)
			atomic.StoreUint32(&fiveXX[index], 0)
			atomic.StoreUint32(&proxyFailures[index], 0)
		}
	}

	atomic.AddUint32(&hits[index], 1)
	atomic.AddUint64(&latencyUS[index], uint64(duration.Microseconds()))
	switch statusCode / 100 {
	case 2:
		atomic.AddUint32(&twoXX[index], 1)
	case 3:
		atomic.AddUint32(&threeXX[index], 1)
	case 4:
		atomic.AddUint32(&fourXX[index], 1)
	case 5:
		atomic.AddUint32(&fiveXX[index], 1)
	}
	if proxyFailure {
		atomic.AddUint32(&proxyFailures[index], 1)
	}
}

func aggregateMetricRing(
	stamps []int64,
	hits []uint32,
	latencyUS []uint64,
	twoXX []uint32,
	threeXX []uint32,
	fourXX []uint32,
	fiveXX []uint32,
	proxyFailures []uint32,
	startStamp, endStamp int64,
) aggregate {
	if startStamp > endStamp {
		return aggregate{}
	}

	var result aggregate
	for stamp := startStamp; stamp <= endStamp; stamp++ {
		index := int(stamp % int64(len(stamps)))
		if atomic.LoadInt64(&stamps[index]) != stamp {
			continue
		}
		result.Requests += int64(atomic.LoadUint32(&hits[index]))
		result.LatencyUS += atomic.LoadUint64(&latencyUS[index])
		result.StatusCounts.TwoXX += int64(atomic.LoadUint32(&twoXX[index]))
		result.StatusCounts.ThreeXX += int64(atomic.LoadUint32(&threeXX[index]))
		result.StatusCounts.FourXX += int64(atomic.LoadUint32(&fourXX[index]))
		result.StatusCounts.FiveXX += int64(atomic.LoadUint32(&fiveXX[index]))
		result.ProxyFailures += int64(atomic.LoadUint32(&proxyFailures[index]))
	}
	return result
}

func normalizeEndpoint(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if strings.HasPrefix(trimmed, "v1/") {
		trimmed = strings.TrimPrefix(trimmed, "v1/")
	} else if strings.HasPrefix(trimmed, "dev/") {
		trimmed = strings.TrimPrefix(trimmed, "dev/")
	}
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "/"
	}

	parts := strings.Split(trimmed, "/")
	switch parts[0] {
	case "players":
		if len(parts) >= 2 {
			parts[1] = "{playerTag}"
		}
	case "clans":
		if len(parts) >= 2 {
			parts[1] = "{clanTag}"
		}
	case "locations":
		if len(parts) >= 2 && parts[1] != "global" {
			parts[1] = "{locationId}"
		}
	case "labels":
		if len(parts) >= 2 && !isStaticLabelSegment(parts[1]) {
			parts[1] = "{labelId}"
		}
	case "leagues", "warleagues", "capitalleagues", "builderbaseleagues":
		if len(parts) >= 2 {
			parts[1] = "{leagueId}"
		}
	case "goldpass":
		if len(parts) >= 3 && parts[1] == "seasons" && parts[2] != "current" {
			parts[2] = "{seasonId}"
		}
	default:
		for i := 1; i < len(parts); i++ {
			parts[i] = normalizeUnknownSegment(parts[i])
		}
	}

	for i := 2; i < len(parts); i++ {
		parts[i] = normalizeUnknownSegment(parts[i])
	}

	return "/" + strings.Join(parts, "/")
}

func isStaticLabelSegment(segment string) bool {
	switch segment {
	case "players", "clans", "players-builder-base", "clans-builder-base", "capital":
		return true
	default:
		return false
	}
}

func normalizeUnknownSegment(segment string) string {
	switch {
	case segment == "":
		return segment
	case strings.HasPrefix(segment, "%23"), strings.HasPrefix(segment, "%2523"), strings.HasPrefix(segment, "#"):
		return "{tag}"
	case isNumeric(segment):
		return "{id}"
	default:
		return segment
	}
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
