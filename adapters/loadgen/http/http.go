// Package http is a built-in open-model load generator: requests go out at a fixed rate whatever
// the latency (no coordinated omission), from this process, for as long as rig runs.
package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindLoad, "http", "built-in constant-rate HTTP generator (runs inside rig)", New)
}

type Options struct {
	Target   string            `yaml:"target"`
	Method   string            `yaml:"method"`
	Body     string            `yaml:"body"`
	Headers  map[string]string `yaml:"headers"`
	Rate     float64           `yaml:"rate"`
	MaxInFly int               `yaml:"max_in_flight"`
	Timeout  time.Duration     `yaml:"timeout"`
}

type Gen struct {
	opt    Options
	env    core.Env
	client *http.Client

	mu      sync.Mutex
	rate    float64
	cancel  context.CancelFunc
	running bool

	sent, failed atomic.Int64
	inflight     atomic.Int64
	lat          *reservoir
}

func New(env core.Env, c *spec.Component) (any, error) {
	g := &Gen{env: env, lat: newReservoir(4096)}
	if err := c.Decode(&g.opt); err != nil {
		return nil, err
	}
	if g.opt.Target == "" {
		return nil, fmt.Errorf("http load generator needs target")
	}
	if g.opt.Method == "" {
		g.opt.Method = "GET"
	}
	if g.opt.MaxInFly == 0 {
		g.opt.MaxInFly = 512
	}
	if g.opt.Timeout == 0 {
		g.opt.Timeout = 10 * time.Second
	}
	g.rate = g.opt.Rate
	g.client = &http.Client{Timeout: g.opt.Timeout, Transport: &http.Transport{MaxIdleConnsPerHost: g.opt.MaxInFly, Proxy: nil}}
	return g, nil
}

func (g *Gen) url(ctx context.Context) (string, error) {
	u, err := g.env.Resolve(ctx, g.opt.Target)
	if err != nil {
		return "", err
	}
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	return u, nil
}

func (g *Gen) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return nil
	}
	url, err := g.url(ctx)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	g.cancel, g.running = cancel, true
	go g.loop(runCtx, url)
	return nil
}

func (g *Gen) Stop(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
	g.running = false
	return nil
}

func (g *Gen) Close() error { return g.Stop(context.Background()) }

func (g *Gen) SetRate(_ context.Context, rps float64) error {
	g.mu.Lock()
	g.rate = rps
	g.mu.Unlock()
	return nil
}

func (g *Gen) currentRate() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rate
}

// loop releases requests on a schedule computed from the rate, catching up when a tick runs late.
func (g *Gen) loop(ctx context.Context, url string) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	last := time.Now()
	var debt float64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			debt += now.Sub(last).Seconds() * g.currentRate()
			last = now
			for ; debt >= 1; debt-- {
				if g.inflight.Load() >= int64(g.opt.MaxInFly) {
					g.failed.Add(1) // the target cannot keep up; counting it keeps the rate honest
					g.sent.Add(1)
					continue
				}
				g.inflight.Add(1)
				go g.fire(ctx, url)
			}
		}
	}
}

func (g *Gen) fire(ctx context.Context, url string) {
	defer g.inflight.Add(-1)
	var body io.Reader
	if g.opt.Body != "" {
		body = bytes.NewBufferString(g.opt.Body)
	}
	req, err := http.NewRequestWithContext(ctx, g.opt.Method, url, body)
	if err != nil {
		g.failed.Add(1)
		return
	}
	for k, v := range g.opt.Headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := g.client.Do(req)
	g.sent.Add(1)
	if err != nil {
		if ctx.Err() == nil {
			g.failed.Add(1)
		}
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	g.lat.add(time.Since(start))
	if resp.StatusCode >= 400 {
		g.failed.Add(1)
	}
}

func (g *Gen) Status(context.Context) (core.LoadStatus, error) {
	g.mu.Lock()
	st := core.LoadStatus{Running: g.running, Rate: g.rate}
	g.mu.Unlock()
	st.Sent, st.Failed = g.sent.Load(), g.failed.Load()
	st.Latency = g.lat.percentiles()
	st.Extra = map[string]string{"in_flight": fmt.Sprint(g.inflight.Load()), "target": g.opt.Target}
	return st, nil
}

// reservoir keeps the most recent latencies for percentiles.
type reservoir struct {
	mu   sync.Mutex
	buf  []time.Duration
	next int
	full bool
}

func newReservoir(n int) *reservoir { return &reservoir{buf: make([]time.Duration, n)} }

func (r *reservoir) add(d time.Duration) {
	r.mu.Lock()
	r.buf[r.next] = d
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *reservoir) percentiles() core.Latency {
	r.mu.Lock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	s := append([]time.Duration(nil), r.buf[:n]...)
	r.mu.Unlock()
	if len(s) == 0 {
		return core.Latency{}
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	at := func(p float64) time.Duration { return s[min(len(s)-1, int(p*float64(len(s))))] }
	return core.Latency{P50: at(.50), P95: at(.95), P99: at(.99)}
}
