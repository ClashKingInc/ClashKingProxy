package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/joho/godotenv"
)

// --- request rate & latency tracking (per-second ring buffer for 24h) ---
const BUCKETS = 24 * 60 * 60 // 24h of seconds

var (
	secs  [BUCKETS]int64  // epoch-second each slot represents
	hits  [BUCKETS]uint32 // requests completed in that second
	latUs [BUCKETS]uint64 // total latency (microseconds) completed in that second
)

func nowSec() int64 { return time.Now().Unix() }

func recordMetrics(duration time.Duration) {
	s := nowSec()
	i := int(s % BUCKETS)
	prev := atomic.LoadInt64(&secs[i])
	if prev != s {
		// attempt to claim/reset this slot for the new second
		if atomic.CompareAndSwapInt64(&secs[i], prev, s) {
			// we won the race; reset aggregates
			atomic.StoreUint32(&hits[i], 0)
			atomic.StoreUint64(&latUs[i], 0)
		} else {
			// somebody else updated secs; leave values as-is (they were reset by winner)
		}
	}
	atomic.AddUint32(&hits[i], 1)
	// store latency in microseconds to avoid floating arithmetic with atomics
	us := uint64(duration.Microseconds())
	atomic.AddUint64(&latUs[i], us)
}

func windowSum(lastNSec int64) int64 {
	n := nowSec()
	var sum int64 = 0
	for off := range lastNSec {
		t := n - off
		i := int(t % BUCKETS)
		if atomic.LoadInt64(&secs[i]) == t {
			s := atomic.LoadUint32(&hits[i])
			sum += int64(s)
		}
	}
	return sum
}

func windowLatencyAvgMs(lastNSec int64) (float64, bool) {
	n := nowSec()
	var reqs int64 = 0
	var totalUs uint64 = 0
	for off := range lastNSec {
		t := n - off
		i := int(t % BUCKETS)
		if atomic.LoadInt64(&secs[i]) == t {
			r := atomic.LoadUint32(&hits[i])
			reqs += int64(r)
			tu := atomic.LoadUint64(&latUs[i])
			totalUs += tu
		}
	}
	if reqs == 0 {
		return 0, false
	}
	avgMs := float64(totalUs) / float64(reqs) / 1000.0
	return avgMs, true
}

var WINDOWS = map[string]int64{
	"10s": 10,
	"1m":  60,
	"5m":  5 * 60,
	"15m": 15 * 60,
	"1h":  60 * 60,
	"6h":  6 * 60 * 60,
	"12h": 12 * 60 * 60,
	"24h": 24 * 60 * 60,
}

// API key rotation
var manualKeys []string
var manualKeysAmount int
var keyIdx uint32 = 0

func nextKey() string {
	// If we get to 2^32 we'll just wrap back to 0
	idx := atomic.AddUint32(&keyIdx, 1)
	return manualKeys[(int(idx)-1)%manualKeysAmount]
}

// StatsWindow holds stats for one window
type StatsWindow struct {
	Requests     int64    `json:"requests"`
	AvgRPS       float64  `json:"avg_rps"`
	AvgLatencyMS *float64 `json:"avg_latency_ms"`
}

// StatsResponse is the JSON for /stats
type StatsResponse struct {
	Now     string                 `json:"now"`
	Windows map[string]StatsWindow `json:"windows"`
}

func main() {
	_ = godotenv.Load()

	// load keys
	raw := os.Getenv("COC_KEYS")
	if raw == "" {
		log.Println("No API keys provided. Set COC_KEYS in env.")
		os.Exit(1)
	}
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			manualKeys = append(manualKeys, k)
		}
	}
	manualKeysAmount = len(manualKeys)
	if manualKeysAmount == 0 {
		log.Println("No API keys after parsing COC_KEYS. Exiting.")
		os.Exit(1)
	}

	host := os.Getenv("HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8011"
	}

	baseURL := "https://api.clashofclans.com/v1/"

	client := &http.Client{
		Timeout: time.Second * 20, // overall safeguard; per-request ctx timeout will be 15s
		// do not follow redirects (fetch redirect: manual)
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"message":"CoC Proxy Server is running."}`)
	})

	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		windows := make(map[string]StatsWindow)
		for label, secsWin := range WINDOWS {
			reqs := windowSum(secsWin)
			avgLatency, hasLatency := windowLatencyAvgMs(secsWin)
			wStats := StatsWindow{
				Requests:     reqs,
				AvgRPS:       float64(reqs) / float64(secsWin),
				AvgLatencyMS: nil,
			}
			if hasLatency {
				wStats.AvgLatencyMS = &avgLatency
			}
			windows[label] = wStats
		}
		resp := StatsResponse{
			Now:     time.Now().UTC().Format(time.RFC3339),
			Windows: windows,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Proxy handler for /v1/*
	http.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			recordMetrics(time.Since(start))
		}()

		// build forward URL - use RequestURI to preserve encoding (e.g., %23 for #)
		// RequestURI includes path + query string
		pathAndQuery := strings.TrimPrefix(r.RequestURI, "/v1/")
		
		// For POST, we need to filter out fields param
		if r.Method == http.MethodPost && strings.Contains(pathAndQuery, "fields=") {
			// Parse and rebuild without fields
			parts := strings.SplitN(pathAndQuery, "?", 2)
			path := parts[0]
			if len(parts) == 2 {
				q := r.URL.Query()
				q.Del("fields")
				if len(q) > 0 {
					pathAndQuery = path + "?" + q.Encode()
				} else {
					pathAndQuery = path
				}
			}
		}
		
		forward := baseURL + pathAndQuery

		// create upstream request with combined context: client context + 15s timeout
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		var body io.Reader
		if r.Body != nil {
			// For POST, we forward the body as-is
			body = r.Body
		}

		req, err := http.NewRequestWithContext(ctx, r.Method, forward, body)
		if err != nil {
			http.Error(w, "failed to create upstream request", http.StatusInternalServerError)
			return
		}

		// headers
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Encoding", "identity") // request no compression
		key := nextKey()
		req.Header.Set("Authorization", "Bearer "+key)
		if r.Method == http.MethodPost {
			// ensure content-type
			if ct := r.Header.Get("Content-Type"); ct != "" {
				req.Header.Set("Content-Type", ct)
			} else {
				req.Header.Set("Content-Type", "application/json")
			}
		}

		// copy-through selected headers for downstream response
		upstreamResp, err := client.Do(req)
		if err != nil {
			// if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) { ... }
			http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer upstreamResp.Body.Close()

		// pass through select headers
		for _, h := range []string{"cache-control", "expires", "etag", "last-modified", "content-type"} {
			if v := upstreamResp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}

		// write status code
		w.WriteHeader(upstreamResp.StatusCode)
		// copy body
		io.Copy(w, upstreamResp.Body)
	})

	addr := host + ":" + port
	log.Printf("CoC proxy listening on http://%s\nKeys loaded: %d\n", addr, manualKeysAmount)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
