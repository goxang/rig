// Package scrape is a metrics backend with no server: it scrapes the services' Prometheus or
// OpenTelemetry (Prometheus exporter) endpoints itself, keeps a short in-memory history, and
// answers a PromQL subset over it. Good for local runs and for clusters without Prometheus.
package scrape

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/adapters/metrics/prometheus"
	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindMetrics, "scrape", "scrape /metrics endpoints directly and keep a short history (no Prometheus server)", New)
}

type Options struct {
	// Targets are service names (their metrics probe) or svc://... / http://... URLs; default every service with metrics.
	Targets   []string      `yaml:"targets"`
	Interval  time.Duration `yaml:"interval"`
	Retention time.Duration `yaml:"retention"`
}

type Metrics struct {
	opt  Options
	env  core.Env
	db   *store
	once sync.Once
	stop chan struct{}
}

func New(env core.Env, c *spec.Component) (any, error) {
	m := &Metrics{env: env, stop: make(chan struct{})}
	if err := c.Decode(&m.opt); err != nil {
		return nil, err
	}
	if m.opt.Interval == 0 {
		m.opt.Interval = 5 * time.Second
	}
	if m.opt.Retention == 0 {
		m.opt.Retention = 30 * time.Minute
	}
	m.db = &store{series: map[string]*series{}, stale: 3 * m.opt.Interval, retention: m.opt.Retention}
	return m, nil
}

func (m *Metrics) start() {
	m.once.Do(func() {
		// two samples a second apart, so a first query (a one-shot CLI call) already has a rate
		m.scrapeAll(context.Background())
		time.Sleep(time.Second)
		m.scrapeAll(context.Background())
		go func() {
			t := time.NewTicker(m.opt.Interval)
			defer t.Stop()
			for {
				select {
				case <-m.stop:
					return
				case <-t.C:
					m.scrapeAll(context.Background())
				}
			}
		}()
	})
}

func (m *Metrics) Close() error {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	return nil
}

type target struct {
	service string
	addr    string // svc://... or URL
}

func (m *Metrics) targets() []target {
	p := m.env.Project()
	names := m.opt.Targets
	if len(names) == 0 {
		for _, n := range p.ServiceNames() {
			if p.Services[n].Metrics != nil {
				names = append(names, n)
			}
		}
	}
	var out []target
	for _, n := range names {
		s, ok := p.Services[n]
		if !ok {
			out = append(out, target{service: n, addr: n})
			continue
		}
		path, port := "/metrics", ""
		if s.Metrics != nil {
			if s.Metrics.Path != "" {
				path = s.Metrics.Path
			}
			port = s.Metrics.Port
		}
		out = append(out, target{service: n, addr: fmt.Sprintf("svc://%s:%s%s", n, port, path)})
	}
	return out
}

func (m *Metrics) scrapeAll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, m.opt.Interval)
	defer cancel()
	now := time.Now()
	var wg sync.WaitGroup
	for _, t := range m.targets() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			up := 1.0
			if err := m.scrape(ctx, t, now); err != nil {
				up = 0
			}
			m.db.add(map[string]string{"__name__": "up", "service": t.service}, now, up)
		}()
	}
	wg.Wait()
	m.db.trim(now)
}

func (m *Metrics) scrape(ctx context.Context, t target, now time.Time) error {
	addr, err := m.env.Resolve(ctx, t.addr)
	if err != nil {
		return err
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", addr, resp.Status)
	}
	return parseExposition(resp.Body, func(l map[string]string, v float64) {
		l["service"] = t.service
		m.db.add(l, now, v)
	})
}

// parseExposition reads the Prometheus text format; # lines and timestamps are ignored.
func parseExposition(r io.Reader, emit func(map[string]string, float64)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		labels := map[string]string{}
		var name, rest string
		if i := strings.IndexByte(line, '{'); i >= 0 {
			end := strings.LastIndexByte(line, '}')
			if end < i {
				continue
			}
			name = line[:i]
			parseLabels(line[i+1:end], labels)
			rest = strings.TrimSpace(line[end+1:])
		} else {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			name, rest = f[0], strings.Join(f[1:], " ")
		}
		vs := strings.Fields(rest)
		if len(vs) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(vs[0], 64)
		if err != nil {
			continue
		}
		labels["__name__"] = name
		emit(labels, v)
	}
	return sc.Err()
}

func parseLabels(s string, out map[string]string) {
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			return
		}
		k := strings.TrimSpace(strings.TrimLeft(s[:eq], ", "))
		var b strings.Builder
		i := eq + 2
		for ; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			if s[i] == '"' {
				break
			}
			b.WriteByte(s[i])
		}
		out[k] = b.String()
		if i+1 >= len(s) {
			return
		}
		s = s[i+1:]
	}
}

func (m *Metrics) Instant(ctx context.Context, q string) ([]core.Sample, error) {
	n, err := parse(q)
	if err != nil {
		return nil, err
	}
	m.start()
	m.db.mu.RLock()
	defer m.db.mu.RUnlock()
	var out []core.Sample
	for _, s := range n.eval(m.db, time.Now()) {
		if !math.IsNaN(s.v) {
			out = append(out, core.Sample{Labels: s.labels, Value: s.v})
		}
	}
	return out, nil
}

func (m *Metrics) Range(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]core.Series, error) {
	n, err := parse(q)
	if err != nil {
		return nil, err
	}
	m.start()
	// the first call seeds the history after the caller picked end; stretch end over those samples
	if now := time.Now(); end.Before(now) && now.Sub(end) < 3*m.opt.Interval {
		end = now
	}
	if step <= 0 {
		step = m.opt.Interval
	}
	m.db.mu.RLock()
	defer m.db.mu.RUnlock()
	by := map[string]*core.Series{}
	var order []string
	var times []time.Time
	for t := end; !t.Before(start); t = t.Add(-step) {
		times = append(times, t)
	}
	slices.Reverse(times)
	for _, t := range times {
		for _, s := range n.eval(m.db, t) {
			if math.IsNaN(s.v) {
				continue
			}
			k := key(s.labels)
			sr, ok := by[k]
			if !ok {
				sr = &core.Series{Labels: s.labels}
				by[k] = sr
				order = append(order, k)
			}
			sr.Points = append(sr.Points, core.Point{T: t, V: s.v})
		}
	}
	sort.Strings(order)
	out := make([]core.Series, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out, nil
}

func (m *Metrics) QueryLanguage() string { return "promql" }

func (m *Metrics) RunQuery(ctx context.Context, q string) (core.Table, error) {
	ss, err := m.Instant(ctx, q)
	if err != nil {
		return core.Table{}, err
	}
	return prometheus.SamplesTable(ss), nil
}

// ---- store ----

type series struct {
	labels map[string]string
	points []core.Point
}

func (s *series) at(t time.Time, stale time.Duration) (core.Point, bool) {
	i := sort.Search(len(s.points), func(i int) bool { return s.points[i].T.After(t) })
	if i == 0 {
		return core.Point{}, false
	}
	p := s.points[i-1]
	return p, t.Sub(p.T) <= stale
}

func (s *series) between(from, to time.Time) []core.Point {
	lo := sort.Search(len(s.points), func(i int) bool { return s.points[i].T.After(from) })
	hi := sort.Search(len(s.points), func(i int) bool { return s.points[i].T.After(to) })
	return s.points[lo:hi]
}

type store struct {
	mu        sync.RWMutex
	series    map[string]*series
	stale     time.Duration
	retention time.Duration
}

func (db *store) add(l map[string]string, t time.Time, v float64) {
	k := key(l)
	db.mu.Lock()
	defer db.mu.Unlock()
	s, ok := db.series[k]
	if !ok {
		s = &series{labels: l}
		db.series[k] = s
	}
	s.points = append(s.points, core.Point{T: t, V: v})
}

func (db *store) trim(now time.Time) {
	db.mu.Lock()
	defer db.mu.Unlock()
	cut := now.Add(-db.retention)
	for k, s := range db.series {
		i := sort.Search(len(s.points), func(i int) bool { return s.points[i].T.After(cut) })
		s.points = s.points[i:]
		if len(s.points) == 0 {
			delete(db.series, k)
		}
	}
}

func (db *store) match(ms []matcher) []*series {
	var out []*series
	for _, s := range db.series {
		ok := true
		for _, m := range ms {
			if !m.match(s.labels[m.name]) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}
