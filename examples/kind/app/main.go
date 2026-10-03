// Command app is the demo workload for rig's examples: one binary, three roles chosen by ROLE.
//
//	api      GET /echo calls the worker, records a trace across both
//	worker   GET /work burns some CPU
//	loadgen  sends GET TARGET at the rate stored as JSON {"rate": n} in Consul key RATE_KEY
//
// Every role serves Prometheus metrics on :9090/metrics, net/http/pprof there when PPROF_ENABLED=true,
// logs JSON lines, and reports spans to ZIPKIN_URL when set. Standard library only.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/http/pprof"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	role    = env("ROLE", "api")
	logger  = slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", role)
	metrics = newRegistry()
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	go serveMetrics(":" + env("METRICS_PORT", "9090"))
	switch role {
	case "api":
		http.HandleFunc("/echo", traced("GET /echo", echo))
	case "worker":
		http.HandleFunc("/work", traced("GET /work", work))
	case "loadgen":
		go generate()
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	addr := ":" + env("PORT", "8080")
	logger.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}

func serveMetrics(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", metrics.serve)
	if os.Getenv("PPROF_ENABLED") == "true" {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	_ = http.ListenAndServe(addr, mux)
}

var client = &http.Client{Timeout: 5 * time.Second}

func echo(w http.ResponseWriter, r *http.Request, sp *span) {
	n := 2000 + mrand.IntN(8000)
	req, _ := http.NewRequestWithContext(r.Context(), "GET", env("WORKER_URL", "http://worker:8080")+"/work?n="+strconv.Itoa(n), nil)
	child := sp.child("GET /work")
	child.inject(req.Header)
	resp, err := client.Do(req)
	child.finish(err != nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		logger.Warn("worker call failed", "err", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Fprintf(w, "echo %s %s\n", r.URL.Query().Get("q"), body)
}

func work(w http.ResponseWriter, r *http.Request, _ *span) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	sum := sha256.Sum256([]byte(r.URL.RawQuery))
	for i := 0; i < n; i++ {
		sum = sha256.Sum256(sum[:])
	}
	if mrand.IntN(100) == 0 {
		http.Error(w, "unlucky", http.StatusInternalServerError)
		return
	}
	io.WriteString(w, hex.EncodeToString(sum[:4]))
}

// traced wraps a handler with request metrics, a server span and an access log line.
func traced(name string, h func(http.ResponseWriter, *http.Request, *span)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sp := extract(r.Header, name)
		rw := &status{ResponseWriter: w, code: 200}
		metrics.gauge("http_in_flight", "").Add(1)
		h(rw, r, sp)
		metrics.gauge("http_in_flight", "").Add(-1)
		sp.finish(rw.code >= 500)
		d := time.Since(start)
		metrics.counter("http_requests_total", fmt.Sprintf(`code="%d"`, rw.code)).Add(1)
		metrics.histogram("http_request_duration_seconds").observe(d.Seconds())
		if rw.code >= 500 {
			logger.Error("request failed", "path", r.URL.Path, "code", rw.code, "ms", d.Milliseconds(), "trace", sp.trace)
		} else if mrand.IntN(20) == 0 {
			logger.Info("request", "path", r.URL.Path, "code", rw.code, "ms", d.Milliseconds(), "trace", sp.trace)
		}
	}
}

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// ---- load generator ----

func generate() {
	var rate atomic.Int64
	key := env("RATE_KEY", "demo/load")
	go func() {
		for {
			resp, err := client.Get(env("CONSUL_URL", "http://consul:8500") + "/v1/kv/" + key + "?raw")
			if err == nil {
				var v struct {
					Rate float64 `json:"rate"`
				}
				if json.NewDecoder(resp.Body).Decode(&v) == nil && int64(v.Rate) != rate.Load() {
					rate.Store(int64(v.Rate))
					logger.Info("rate changed", "rate", v.Rate)
				}
				resp.Body.Close()
			}
			time.Sleep(2 * time.Second)
		}
	}()
	target := env("TARGET", "http://api:8080/echo?q=hi")
	tick := time.NewTicker(10 * time.Millisecond)
	var debt float64
	last := time.Now()
	for now := range tick.C {
		debt += now.Sub(last).Seconds() * float64(rate.Load())
		last = now
		for ; debt >= 1; debt-- {
			go func() {
				start := time.Now()
				resp, err := client.Get(target)
				metrics.counter("loadgen_sent_total", "").Add(1)
				if err != nil || resp.StatusCode >= 400 {
					metrics.counter("loadgen_failed_total", "").Add(1)
				}
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				metrics.histogram("loadgen_latency_seconds").observe(time.Since(start).Seconds())
			}()
		}
	}
}

// ---- tracing (Zipkin v2 JSON, B3 headers) ----

type span struct {
	trace, id, parent, name string
	start                   time.Time
}

func newID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func extract(h http.Header, name string) *span {
	s := &span{trace: h.Get("X-B3-TraceId"), parent: h.Get("X-B3-SpanId"), id: newID(8), name: name, start: time.Now()}
	if s.trace == "" {
		s.trace = newID(16)
	}
	return s
}

func (s *span) child(name string) *span {
	return &span{trace: s.trace, parent: s.id, id: newID(8), name: name, start: time.Now()}
}

func (s *span) inject(h http.Header) {
	h.Set("X-B3-TraceId", s.trace)
	h.Set("X-B3-SpanId", s.id)
	h.Set("X-B3-ParentSpanId", s.parent)
}

var (
	spanMu  sync.Mutex
	pending []map[string]any
	once    sync.Once
)

func (s *span) finish(failed bool) {
	url := os.Getenv("ZIPKIN_URL")
	if url == "" {
		return
	}
	z := map[string]any{"traceId": s.trace, "id": s.id, "name": s.name, "timestamp": s.start.UnixMicro(),
		"duration": max(1, time.Since(s.start).Microseconds()), "localEndpoint": map[string]string{"serviceName": role}}
	if s.parent != "" {
		z["parentId"] = s.parent
	}
	if failed {
		z["tags"] = map[string]string{"error": "true"}
	}
	spanMu.Lock()
	pending = append(pending, z)
	spanMu.Unlock()
	once.Do(func() {
		go func() {
			for range time.Tick(time.Second) {
				spanMu.Lock()
				batch := pending
				pending = nil
				spanMu.Unlock()
				if len(batch) == 0 {
					continue
				}
				raw, _ := json.Marshal(batch)
				if resp, err := client.Post(url+"/api/v2/spans", "application/json", bytes.NewReader(raw)); err == nil {
					resp.Body.Close()
				}
			}
		}()
	})
}

// ---- metrics (Prometheus text format) ----

type registry struct {
	mu    sync.Mutex
	cs    map[string]*atomic.Int64
	gs    map[string]*atomic.Int64
	hs    map[string]*histogram
	start time.Time
}

func newRegistry() *registry {
	return &registry{cs: map[string]*atomic.Int64{}, gs: map[string]*atomic.Int64{}, hs: map[string]*histogram{}, start: time.Now()}
}

func (r *registry) counter(name, labels string) *atomic.Int64 { return r.get(r.cs, name, labels) }
func (r *registry) gauge(name, labels string) *atomic.Int64   { return r.get(r.gs, name, labels) }

func (r *registry) get(m map[string]*atomic.Int64, name, labels string) *atomic.Int64 {
	k := name
	if labels != "" {
		k += "{" + labels + "}"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := m[k]
	if !ok {
		c = &atomic.Int64{}
		m[k] = c
	}
	return c
}

var buckets = []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}

type histogram struct {
	mu     sync.Mutex
	counts []int64
	sum    float64
	n      int64
}

func (r *registry) histogram(name string) *histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.hs[name]
	if !ok {
		h = &histogram{counts: make([]int64, len(buckets))}
		r.hs[name] = h
	}
	return h
}

func (h *histogram) observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, b := range buckets {
		if v <= b {
			h.counts[i]++
		}
	}
	h.sum += v
	h.n++
}

func (r *registry) serve(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	for k, c := range r.cs {
		lines = append(lines, fmt.Sprintf("%s %d", k, c.Load()))
	}
	for k, g := range r.gs {
		lines = append(lines, fmt.Sprintf("%s %d", k, g.Load()))
	}
	for name, h := range r.hs {
		h.mu.Lock()
		for i, b := range buckets {
			lines = append(lines, fmt.Sprintf(`%s_bucket{le="%g"} %d`, name, b, h.counts[i]))
		}
		lines = append(lines, fmt.Sprintf(`%s_bucket{le="+Inf"} %d`, name, h.n), fmt.Sprintf("%s_sum %g", name, h.sum), fmt.Sprintf("%s_count %d", name, h.n))
		h.mu.Unlock()
	}
	lines = append(lines, fmt.Sprintf("process_uptime_seconds %g", math.Round(time.Since(r.start).Seconds())))
	sort.Strings(lines)
	io.WriteString(w, strings.Join(lines, "\n")+"\n")
}
