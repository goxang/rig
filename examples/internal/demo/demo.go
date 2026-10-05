// Package demo is what the example services share: JSON logs, Prometheus metrics, Zipkin spans and
// pprof, with the standard library only, so each example's main.go is about its own work.
package demo

import (
	"bytes"
	"crypto/rand"
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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Role is the service's name in logs, metrics and spans.
var Role = Env("ROLE", "app")

var Log = slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", Role)

func Env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ServeMetrics serves /metrics, and net/http/pprof when PPROF_ENABLED=true, on METRICS_PORT (9090).
func ServeMetrics() {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", Metrics.serve)
	if os.Getenv("PPROF_ENABLED") == "true" {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	go func() { _ = http.ListenAndServe(":"+Env("METRICS_PORT", "9090"), mux) }()
}

var Client = &http.Client{Timeout: 5 * time.Second}

// Traced wraps a handler with request metrics, a server span and a log line per failed request.
func Traced(name string, h func(http.ResponseWriter, *http.Request, *Span)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sp := extract(r.Header, name)
		rw := &status{ResponseWriter: w, code: 200}
		Metrics.Gauge("http_in_flight", "").Add(1)
		h(rw, r, sp)
		Metrics.Gauge("http_in_flight", "").Add(-1)
		sp.Tag("http.method", r.Method)
		sp.Tag("http.path", r.URL.RequestURI())
		sp.Tag("http.status_code", fmt.Sprint(rw.code))
		sp.Finish(rw.code >= 500)
		d := time.Since(start)
		Metrics.Counter("http_requests_total", fmt.Sprintf(`code="%d"`, rw.code)).Add(1)
		Metrics.Histogram("http_request_duration_seconds").Observe(d.Seconds())
		switch {
		case rw.code >= 500:
			Log.Error("request failed", "path", r.URL.Path, "code", rw.code, "ms", d.Milliseconds(), "trace", sp.Trace)
		case mrand.IntN(20) == 0:
			Log.Info("request", "path", r.URL.Path, "code", rw.code, "ms", d.Milliseconds(), "trace", sp.Trace)
		}
	}
}

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// ---- tracing (Zipkin v2 JSON, B3 headers) ----

type Span struct {
	Trace, id, parent, name string
	start                   time.Time
	tags                    map[string]string
}

// Tag adds a key and value the Traces screen shows on the span.
func (s *Span) Tag(k, v string) {
	if s.tags == nil {
		s.tags = map[string]string{}
	}
	s.tags[k] = v
}

func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func extract(h http.Header, name string) *Span {
	s := &Span{Trace: h.Get("X-B3-TraceId"), parent: h.Get("X-B3-SpanId"), id: newID(8), name: name, start: time.Now()}
	if s.Trace == "" {
		s.Trace = newID(16)
	}
	return s
}

// Continue goes on with a trace another service started, from the trace and span ids it passed
// along (in a message, say); empty ids start a new trace.
func Continue(trace, parent, name string) *Span {
	h := http.Header{}
	h.Set("X-B3-TraceId", trace)
	h.Set("X-B3-SpanId", parent)
	return extract(h, name)
}

// ID is the span's own id, for the next service's Continue.
func (s *Span) ID() string { return s.id }

func (s *Span) Child(name string) *Span {
	return &Span{Trace: s.Trace, parent: s.id, id: newID(8), name: name, start: time.Now()}
}

func (s *Span) Inject(h http.Header) {
	h.Set("X-B3-TraceId", s.Trace)
	h.Set("X-B3-SpanId", s.id)
	h.Set("X-B3-ParentSpanId", s.parent)
}

var (
	spanMu  sync.Mutex
	pending []map[string]any
	once    sync.Once
)

// Finish reports the span to ZIPKIN_URL (batched each second); without one it does nothing.
func (s *Span) Finish(failed bool) {
	url := os.Getenv("ZIPKIN_URL")
	if url == "" {
		return
	}
	z := map[string]any{"traceId": s.Trace, "id": s.id, "name": s.name, "timestamp": s.start.UnixMicro(),
		"duration": max(1, time.Since(s.start).Microseconds()), "localEndpoint": map[string]string{"serviceName": Role}}
	if s.parent != "" {
		z["parentId"] = s.parent
	}
	if failed {
		s.Tag("error", "true")
	}
	if len(s.tags) > 0 {
		z["tags"] = s.tags
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
				if resp, err := Client.Post(url+"/api/v2/spans", "application/json", bytes.NewReader(raw)); err == nil {
					resp.Body.Close()
				}
			}
		}()
	})
}

// ---- metrics (Prometheus text format) ----

var Metrics = &registry{cs: map[string]*atomic.Int64{}, gs: map[string]*atomic.Int64{}, hs: map[string]*histogram{}, start: time.Now()}

type registry struct {
	mu    sync.Mutex
	cs    map[string]*atomic.Int64
	gs    map[string]*atomic.Int64
	hs    map[string]*histogram
	start time.Time
}

func (r *registry) Counter(name, labels string) *atomic.Int64 { return r.get(r.cs, name, labels) }
func (r *registry) Gauge(name, labels string) *atomic.Int64   { return r.get(r.gs, name, labels) }

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

func (r *registry) Histogram(name string) *histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.hs[name]
	if !ok {
		h = &histogram{counts: make([]int64, len(buckets))}
		r.hs[name] = h
	}
	return h
}

func (h *histogram) Observe(v float64) {
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
	_, _ = io.WriteString(w, strings.Join(lines, "\n")+"\n")
}
