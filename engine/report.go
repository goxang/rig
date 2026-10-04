package engine

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/spec"
)

var defaultStats = []string{"avg", "max", "last"}

// ReportRow is one series of one metric, summarised.
type ReportRow struct {
	Metric string             `json:"metric"`
	Series string             `json:"series,omitempty"`
	Unit   string             `json:"unit,omitempty"`
	Values map[string]float64 `json:"values"`
	Err    string             `json:"err,omitempty"`
}

// ReportResult is a reports: entry measured over one window.
type ReportResult struct {
	Name  string      `json:"name"`
	Env   string      `json:"env"`
	From  time.Time   `json:"from"`
	To    time.Time   `json:"to"`
	Stats []string    `json:"stats"`
	Rows  []ReportRow `json:"rows"`
}

func (a *App) ReportNames() []string { return SortedKeys(a.Spec.Reports) }

// MeasureReport runs a report's queries over [from, to] against source (default the report's own)
// and summarises every series they return.
func (a *App) MeasureReport(ctx context.Context, name, source string, from, to time.Time) (*ReportResult, error) {
	r, ok := a.Spec.Reports[name]
	if !ok {
		return nil, fmt.Errorf("no report %q in %s (have %v)", name, a.Spec.File, a.ReportNames())
	}
	if source == "" {
		source = r.Source
	}
	src, _, err := Get[core.Metrics](a, core.KindMetrics, source)
	if err != nil {
		return nil, err
	}
	rng := to.Sub(from)
	step := max(rng/200, 5*time.Second)
	out := &ReportResult{Name: name, From: from, To: to}
	if a.Env != nil {
		out.Env = a.Env.Name
	}
	seen := map[string]bool{}
	for _, m := range r.Metrics {
		stats := m.Stats
		if len(stats) == 0 {
			stats = defaultStats
		}
		for _, st := range stats {
			if !seen[st] {
				seen[st] = true
				out.Stats = append(out.Stats, st)
			}
		}
		title := m.Title
		if title == "" {
			title = m.Query
		}
		ss, err := src.Range(ctx, ExpandQuery(m.Query, nil, rng, step), from, to, step)
		if err != nil {
			out.Rows = append(out.Rows, ReportRow{Metric: title, Unit: m.Unit, Err: err.Error()})
			continue
		}
		if len(ss) == 0 {
			out.Rows = append(out.Rows, ReportRow{Metric: title, Unit: m.Unit, Err: "no data"})
		}
		for _, s := range ss {
			row := ReportRow{Metric: title, Unit: m.Unit, Values: map[string]float64{}}
			if len(ss) > 1 || m.Legend != "" {
				row.Series = viz.LegendOf(m.Legend, s.Labels)
			}
			for _, st := range stats {
				if v, ok := summarise(s.Points, st); ok {
					row.Values[st] = v
				}
			}
			out.Rows = append(out.Rows, row)
		}
	}
	return out, nil
}

func summarise(ps []core.Point, stat string) (float64, bool) {
	var vs []float64
	for _, p := range ps {
		if !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
			vs = append(vs, p.V)
		}
	}
	if len(vs) == 0 {
		return 0, false
	}
	switch stat {
	case "last":
		return vs[len(vs)-1], true
	case "avg":
		sum := 0.0
		for _, v := range vs {
			sum += v
		}
		return sum / float64(len(vs)), true
	}
	sort.Float64s(vs)
	switch stat {
	case "min":
		return vs[0], true
	case "max":
		return vs[len(vs)-1], true
	}
	if q, err := strconv.ParseFloat(strings.TrimPrefix(stat, "p"), 64); err == nil && strings.HasPrefix(stat, "p") && q > 0 && q <= 100 {
		return vs[int(math.Ceil(q/100*float64(len(vs))))-1], true
	}
	return 0, false
}

// Markdown writes the report as a table, one row per series.
func (r *ReportResult) Markdown(w io.Writer) {
	fmt.Fprintf(w, "## %s · %s · %s → %s (%s)\n\n", r.Name, r.Env, r.From.Format("2006-01-02 15:04:05"), r.To.Format("15:04:05"), r.To.Sub(r.From).Round(time.Second))
	fmt.Fprintf(w, "| metric | series | %s |\n|---|---|%s\n", strings.Join(r.Stats, " | "), strings.Repeat("---:|", len(r.Stats)))
	for _, row := range r.Rows {
		cells := make([]string, len(r.Stats))
		for i, st := range r.Stats {
			if v, ok := row.Values[st]; ok {
				cells[i] = viz.Human(v, row.Unit)
			}
		}
		series := row.Series
		if row.Err != "" {
			series = "⚠ " + row.Err
		}
		fmt.Fprintf(w, "| %s | %s | %s |\n", row.Metric, series, strings.Join(cells, " | "))
	}
}

// ReportFile is where a measured report is saved unless the caller names a file: the project's
// data directory, outside the repository.
func (a *App) ReportFile(name string, at time.Time) string {
	dir, err := spec.DataDir(a.Spec.Dir)
	if err != nil {
		dir = filepath.Join(a.Spec.Dir, ".rig")
	}
	return filepath.Join(dir, "reports", name+"-"+at.Format("20060102-150405")+".md")
}

func WriteReportFile(file string, write func(io.Writer)) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	write(f)
	return f.Close()
}
