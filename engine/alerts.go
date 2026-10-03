package engine

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelCrit
)

// Firing is one alert over a threshold, on one subject (a node, a queue, a row of its query).
type Firing struct {
	Alert   string
	Subject string
	Value   float64
	Unit    string
	Level   Level
}

func (f Firing) String() string {
	s := f.Alert
	if f.Subject != "" {
		s = f.Subject + " " + s
	}
	return fmt.Sprintf("%s %s%s", s, strconv.FormatFloat(f.Value, 'f', -1, 64), f.Unit)
}

// DefaultAlerts watch the nodes when rig.yaml names no alerts: memory, CPU and disk at 90% and 98%.
var DefaultAlerts = []spec.Alert{
	{Name: "memory", Source: "hosts", Metric: "memory", Warn: 90, Crit: 98, Unit: "%"},
	{Name: "cpu", Source: "hosts", Metric: "cpu", Warn: 90, Crit: 98, Unit: "%"},
	{Name: "disk", Source: "hosts", Metric: "disk", Warn: 90, Crit: 98, Unit: "%"},
}

func (a *App) AlertRules() []spec.Alert {
	rules := append([]spec.Alert{}, a.Spec.Alerts...)
	if a.Env != nil {
		rules = append(rules, a.Env.Alerts...)
	}
	if len(rules) == 0 {
		return DefaultAlerts
	}
	return rules
}

// CheckAlerts evaluates every rule once; the most severe firings come first. A rule that cannot be
// evaluated is reported as an error, not as a firing.
func (a *App) CheckAlerts(ctx context.Context) ([]Firing, []error) {
	var out []Firing
	var errs []error
	var hosts []core.Host
	hostsRead := false
	for _, r := range a.AlertRules() {
		var vals map[string]float64
		var err error
		if r.Source == "hosts" {
			if !hostsRead {
				hostsRead = true
				if src, _, e := Get[core.Hosts](a, core.KindHosts, ""); e != nil {
					err = e
				} else {
					hosts, err = src.Hosts(ctx)
				}
			}
			vals = hostValues(hosts, r.Metric)
		} else {
			vals, err = a.queryValues(ctx, r)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("alert %s: %w", r.Name, err))
			continue
		}
		for subject, v := range vals {
			if l := level(r, v); l > LevelOK {
				out = append(out, Firing{Alert: r.Name, Subject: subject, Value: round1(v), Unit: r.Unit, Level: l})
			}
		}
	}
	sortFirings(out)
	return out, errs
}

func level(r spec.Alert, v float64) Level {
	over := func(t float64) bool {
		if r.Below {
			return v < t
		}
		return v >= t
	}
	switch {
	case r.Crit != 0 && over(r.Crit):
		return LevelCrit
	case r.Warn != 0 && over(r.Warn):
		return LevelWarn
	}
	return LevelOK
}

func hostValues(hosts []core.Host, metric string) map[string]float64 {
	out := map[string]float64{}
	for _, h := range hosts {
		switch metric {
		case "cpu":
			if h.CPUs > 0 {
				out[h.Name] = h.CPUUsed * 100
			}
		case "memory":
			if h.MemTotal > 0 {
				out[h.Name] = float64(h.MemUsed) * 100 / float64(h.MemTotal)
			}
		case "disk":
			if h.DiskTotal > 0 {
				out[h.Name] = float64(h.DiskUsed) * 100 / float64(h.DiskTotal)
			}
		}
	}
	return out
}

// queryValues runs the rule's query; each row's first number is a value, its other cells its name.
func (a *App) queryValues(ctx context.Context, r spec.Alert) (map[string]float64, error) {
	t, err := a.RunQuery(ctx, r.Source, r.Query)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, row := range t.Rows {
		var name []string
		found := false
		var v float64
		for _, c := range row {
			if f, err := strconv.ParseFloat(strings.TrimSpace(c), 64); err == nil && !found {
				v, found = f, true
				continue
			}
			if c != "" {
				name = append(name, c)
			}
		}
		if found {
			out[strings.Join(name, " ")] = v
		}
	}
	return out, nil
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func sortFirings(fs []Firing) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Level != fs[j].Level {
			return fs[i].Level > fs[j].Level
		}
		return fs[i].Value > fs[j].Value
	})
}
