package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

// LoadResult is one load run, saved under the state directory so later runs compare against it.
type LoadResult struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Generator  string         `json:"generator"`
	Profile    string         `json:"profile"`
	Rate       float64        `json:"rate"`
	Started    time.Time      `json:"started"`
	Seconds    float64        `json:"seconds"`
	Sent       int64          `json:"sent"`
	Failed     int64          `json:"failed"`
	Throughput float64        `json:"throughput"`
	P50        time.Duration  `json:"p50"`
	P95        time.Duration  `json:"p95"`
	P99        time.Duration  `json:"p99"`
	Checks     []AssertResult `json:"checks,omitempty"`
}

// ErrorRate is the failed share of sent requests, in percent.
func (r *LoadResult) ErrorRate() float64 {
	if r.Sent == 0 {
		return 0
	}
	return 100 * float64(r.Failed) / float64(r.Sent)
}

// Passed is whether every assertion held.
func (r *LoadResult) Passed() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

type AssertResult struct {
	Assert string `json:"assert"`
	Got    string `json:"got"`
	OK     bool   `json:"ok"`
}

// LoadRunNamed is the load: entry name, or an ad-hoc steady run of the generator name.
func (a *App) LoadRunNamed(name string) (*spec.LoadRun, error) {
	if r, ok := a.Spec.Load[name]; ok && r != nil {
		c := *r
		if c.Generator == "" {
			c.Generator = name
		}
		return &c, nil
	}
	if _, ok := a.Spec.Components[name]; ok {
		return &spec.LoadRun{Generator: name}, nil
	}
	return nil, fmt.Errorf("no load run or generator %q", name)
}

// ProfileRate is the rate a profile asks for at elapsed into a run of total (0: open-ended).
// ramp climbs to target over the first 80% (a minute when open-ended), spike runs at a fifth with the
// middle fifth at target, steady and soak hold target.
func ProfileRate(profile string, target float64, elapsed, total time.Duration) float64 {
	switch profile {
	case "ramp":
		span := time.Minute
		if total > 0 {
			span = total * 8 / 10
		}
		return max(1, target*min(1, float64(elapsed)/float64(span)))
	case "spike":
		if total > 0 && elapsed >= total*2/5 && elapsed < total*3/5 {
			return target
		}
		return max(1, target/5)
	}
	return target
}

// RunLoad drives the run's generator until its For ends or ctx does, calling tick every second,
// then checks the assertions and saves the result. force lets the rate exceed the generator's max.
func (a *App) RunLoad(ctx context.Context, name string, r *spec.LoadRun, force bool, tick func(core.LoadStatus)) (*LoadResult, error) {
	switch r.Profile {
	case "", "steady", "soak", "ramp", "spike":
	default:
		return nil, fmt.Errorf("profile %q: want steady, ramp, spike or soak", r.Profile)
	}
	if r.Profile == "spike" && r.For == 0 {
		return nil, fmt.Errorf("a spike needs a duration (for:)")
	}
	asserts := make([]loadAssert, len(r.Assert))
	for i, s := range r.Assert {
		var err error
		if asserts[i], err = parseAssert(s); err != nil {
			return nil, err
		}
	}
	if err := a.Guard(); err != nil {
		return nil, err
	}
	g, _, err := Get[core.LoadGenerator](a, core.KindLoad, r.Generator)
	if err != nil {
		return nil, err
	}
	if l := a.LoadLimits(r.Generator); l.Max > 0 && r.Rate > l.Max && !force {
		return nil, fmt.Errorf("%s: %.0f/s: %w (max %.0f)", r.Generator, r.Rate, ErrOverMax, l.Max)
	}
	before, _ := g.Status(ctx)
	target := r.Rate
	if target == 0 {
		target = before.Rate
	}
	setRate := func(rps float64) {
		if rps > 0 {
			_ = g.SetRate(ctx, rps)
		}
	}
	setRate(ProfileRate(r.Profile, target, 0, r.For))
	if err := g.Start(ctx); err != nil {
		return nil, err
	}
	defer g.Stop(context.Background())
	started := time.Now()
	runCtx := ctx
	if r.For > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.For)
		defer cancel()
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := before
loop:
	for {
		select {
		case <-runCtx.Done():
			break loop
		case <-t.C:
		}
		setRate(ProfileRate(r.Profile, target, time.Since(started), r.For))
		if st, err := g.Status(ctx); err == nil {
			last = st
			if tick != nil {
				tick(st)
			}
		}
	}
	if st, err := g.Status(context.Background()); err == nil {
		last = st
	}
	res := &LoadResult{Name: name, Generator: r.Generator, Profile: r.Profile, Rate: target, Started: started,
		Seconds: time.Since(started).Seconds(), Sent: last.Sent - before.Sent, Failed: last.Failed - before.Failed,
		P50: last.Latency.P50, P95: last.Latency.P95, P99: last.Latency.P99}
	if res.Sent < 0 { // the generator restarted its counters on start
		res.Sent, res.Failed = last.Sent, last.Failed
	}
	res.Throughput = float64(res.Sent) / max(res.Seconds, 1)
	for _, as := range asserts {
		res.Checks = append(res.Checks, as.check(res))
	}
	res.ID = started.Format("0102-150405")
	return res, a.saveLoadResult(res)
}

func (a *App) loadDir() string { return filepath.Join(a.StateDir(), "load") }

func (a *App) saveLoadResult(r *LoadResult) error {
	if err := os.MkdirAll(a.loadDir(), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(filepath.Join(a.loadDir(), r.ID+".json"), raw, 0o644)
}

// LoadResultFor is the saved run id, or with id "last" the latest run of name before the run skip.
func (a *App) LoadResultFor(id, name, skip string) (*LoadResult, error) {
	if id != "last" {
		raw, err := os.ReadFile(filepath.Join(a.loadDir(), id+".json"))
		if err != nil {
			return nil, fmt.Errorf("no saved load run %s", id)
		}
		var r LoadResult
		return &r, json.Unmarshal(raw, &r)
	}
	files, _ := filepath.Glob(filepath.Join(a.loadDir(), "*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		var r LoadResult
		if err == nil && json.Unmarshal(raw, &r) == nil && r.Name == name && r.ID != skip {
			return &r, nil
		}
	}
	return nil, fmt.Errorf("no earlier load run of %s to compare with", name)
}

type loadAssert struct {
	text, metric, op string
	want             float64
}

// parseAssert reads "metric op value": p50, p95 and p99 take durations, errors a percent, throughput
// a rate (1500/s), sent and failed counts.
func parseAssert(s string) (loadAssert, error) {
	f := strings.Fields(s)
	if len(f) != 3 {
		return loadAssert{}, fmt.Errorf("assert %q: want metric op value, like p95 < 200ms", s)
	}
	as := loadAssert{text: s, metric: f[0], op: f[1]}
	switch as.op {
	case "<", "<=", ">", ">=":
	default:
		return as, fmt.Errorf("assert %q: op must be <, <=, > or >=", s)
	}
	var err error
	switch as.metric {
	case "p50", "p95", "p99":
		var d time.Duration
		d, err = time.ParseDuration(f[2])
		as.want = float64(d)
	case "errors":
		as.want, err = strconv.ParseFloat(strings.TrimSuffix(f[2], "%"), 64)
	case "throughput":
		as.want, err = strconv.ParseFloat(strings.TrimSuffix(f[2], "/s"), 64)
	case "sent", "failed":
		as.want, err = strconv.ParseFloat(f[2], 64)
	default:
		return as, fmt.Errorf("assert %q: metric must be p50, p95, p99, errors, throughput, sent or failed", s)
	}
	if err != nil {
		return as, fmt.Errorf("assert %q: %w", s, err)
	}
	return as, nil
}

func (as loadAssert) check(r *LoadResult) AssertResult {
	var got float64
	var text string
	switch as.metric {
	case "p50", "p95", "p99":
		d := map[string]time.Duration{"p50": r.P50, "p95": r.P95, "p99": r.P99}[as.metric]
		got, text = float64(d), d.Round(time.Microsecond).String()
	case "errors":
		got = r.ErrorRate()
		text = strconv.FormatFloat(got, 'f', 2, 64) + "%"
	case "throughput":
		got = r.Throughput
		text = strconv.FormatFloat(got, 'f', 1, 64) + "/s"
	case "sent":
		got, text = float64(r.Sent), strconv.FormatInt(r.Sent, 10)
	case "failed":
		got, text = float64(r.Failed), strconv.FormatInt(r.Failed, 10)
	}
	ok := map[string]bool{"<": got < as.want, "<=": got <= as.want, ">": got > as.want, ">=": got >= as.want}[as.op]
	return AssertResult{Assert: as.text, Got: text, OK: ok}
}
