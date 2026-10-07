package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
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
	Name     string           `json:"name"`
	Env      string           `json:"env"`
	From     time.Time        `json:"from"`
	To       time.Time        `json:"to"`
	Stats    []string         `json:"stats"`
	Rows     []ReportRow      `json:"rows"`
	Buckets  []time.Time      `json:"buckets,omitempty"`
	Timeline []ReportTimeline `json:"timeline,omitempty"`
	Traces   []ReportTraceRow `json:"traces,omitempty"`
	Tables   []ReportTableRow `json:"tables,omitempty"`
	Format   string           `json:"-"`
}

// ReportTimeline is one series' average per interval of the window (the report's every:).
type ReportTimeline struct {
	Metric string `json:"metric"`
	Series string `json:"series,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Values []Gap  `json:"values"`
}

// Gap is a value that may be missing (NaN): JSON says null for it, which it cannot say NaN.
type Gap float64

func (g Gap) MarshalJSON() ([]byte, error) {
	if math.IsNaN(float64(g)) || math.IsInf(float64(g), 0) {
		return []byte("null"), nil
	}
	return json.Marshal(float64(g))
}

// ReportTraceRow sums up the traces one traces: entry matched in the window.
type ReportTraceRow struct {
	Title   string              `json:"title"`
	Count   int                 `json:"count"`
	Errors  int                 `json:"errors"`
	P50     time.Duration       `json:"p50_ns"`
	P95     time.Duration       `json:"p95_ns"`
	Max     time.Duration       `json:"max_ns"`
	Slowest []core.TraceSummary `json:"slowest,omitempty"`
	Err     string              `json:"err,omitempty"`
}

// ReportTableRow is what one queries: entry returned at the end of the window.
type ReportTableRow struct {
	Title   string     `json:"title"`
	Query   string     `json:"query"`
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Total   int        `json:"total"`
	Err     string     `json:"err,omitempty"`
}

// reportLimits is what a verbosity keeps: series per metric, rows per query, slowest traces.
func reportLimits(verbosity string) (series, rows, slowest int) {
	switch verbosity {
	case "brief":
		return 5, 0, 0
	case "full":
		return math.MaxInt, math.MaxInt, 20
	}
	return 20, 20, 5
}

func (a *App) ReportNames() []string { return SortedKeys(a.Spec.Reports) }

// MeasureReport runs a report's queries over [from, to] against source (default the report's own)
// and summarises every series they return.
func (a *App) MeasureReport(ctx context.Context, name, source string, from, to time.Time) (*ReportResult, error) {
	r, ok := a.Spec.Reports[name]
	if !ok {
		return nil, fmt.Errorf("no report %q in %s (have %v)", name, a.Spec.File, a.ReportNames())
	}
	out := &ReportResult{Name: name, From: from, To: to, Format: r.Format}
	if a.Env != nil {
		out.Env = a.Env.Name
	}
	maxSeries, maxRows, slowest := reportLimits(r.Verbosity)
	if len(r.Metrics) > 0 {
		if err := a.reportMetrics(ctx, r, cmp.Or(source, r.Source), out, maxSeries); err != nil {
			return nil, err
		}
	}
	for _, t := range r.Traces {
		out.Traces = append(out.Traces, a.reportTraces(ctx, t, from, to, slowest))
	}
	for _, q := range r.Queries {
		out.Tables = append(out.Tables, a.reportQuery(ctx, q, from, to, maxRows))
	}
	return out, nil
}

func (a *App) reportMetrics(ctx context.Context, r *spec.Report, source string, out *ReportResult, maxSeries int) error {
	src, _, err := Get[core.Metrics](a, core.KindMetrics, source)
	if err != nil {
		return err
	}
	from, to := out.From, out.To
	rng := to.Sub(from)
	step := max(rng/200, 5*time.Second)
	if r.Every > 0 {
		step = min(step, r.Every)
		for t := from; t.Before(to); t = t.Add(r.Every) {
			out.Buckets = append(out.Buckets, t)
		}
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
		title := cmp.Or(m.Title, m.Query)
		ss, err := src.Range(ctx, ExpandQuery(m.Query, nil, rng, step), from, to, step)
		if err != nil {
			out.Rows = append(out.Rows, ReportRow{Metric: title, Unit: m.Unit, Err: err.Error()})
			continue
		}
		if len(ss) == 0 {
			out.Rows = append(out.Rows, ReportRow{Metric: title, Unit: m.Unit, Err: "no data"})
		}
		for i, s := range ss {
			if i == maxSeries {
				out.Rows = append(out.Rows, ReportRow{Metric: title, Unit: m.Unit, Err: fmt.Sprintf("%d more series (verbosity: full keeps them)", len(ss)-i)})
				break
			}
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
			if len(out.Buckets) > 0 {
				out.Timeline = append(out.Timeline, ReportTimeline{Metric: title, Series: row.Series, Unit: m.Unit, Values: bucketed(s.Points, out.Buckets, r.Every)})
			}
		}
	}
	return nil
}

// bucketed averages points per bucket; a bucket without points is NaN.
func bucketed(ps []core.Point, buckets []time.Time, every time.Duration) []Gap {
	out := make([]Gap, len(buckets))
	for i, b := range buckets {
		var in []core.Point
		for _, p := range ps {
			if !p.T.Before(b) && p.T.Before(b.Add(every)) {
				in = append(in, p)
			}
		}
		v, ok := summarise(in, "avg")
		if !ok {
			v = math.NaN()
		}
		out[i] = Gap(v)
	}
	return out
}

func (a *App) reportTraces(ctx context.Context, t spec.ReportTrace, from, to time.Time, slowest int) ReportTraceRow {
	row := ReportTraceRow{Title: cmp.Or(t.Title, strings.TrimSpace(t.Service+" "+t.Operation))}
	tr, _, err := Get[core.Tracing](a, core.KindTracing, t.Source)
	if err != nil {
		row.Err = err.Error()
		return row
	}
	ts, err := tr.Search(ctx, core.TraceQuery{Service: t.Service, Operation: t.Operation, MinDuration: t.Min, Lookback: time.Since(from), Limit: 1000})
	if err != nil {
		row.Err = err.Error()
		return row
	}
	var ds []time.Duration
	var kept []core.TraceSummary
	for _, s := range ts {
		if s.Start.Before(from) || s.Start.After(to) {
			continue
		}
		kept = append(kept, s)
		ds = append(ds, s.Duration)
		if s.Error {
			row.Errors++
		}
	}
	row.Count = len(kept)
	if len(ds) > 0 {
		slices.Sort(ds)
		row.P50, row.P95, row.Max = ds[(len(ds)-1)/2], ds[int(math.Ceil(0.95*float64(len(ds))))-1], ds[len(ds)-1]
	}
	if len(kept) == 1000 {
		row.Err = "the tracing backend returned its 1000-trace limit: counts are a floor"
	}
	slices.SortFunc(kept, func(x, y core.TraceSummary) int { return cmp.Compare(y.Duration, x.Duration) })
	row.Slowest = kept[:min(slowest, len(kept))]
	return row
}

func (a *App) reportQuery(ctx context.Context, q spec.ReportQuery, from, to time.Time, maxRows int) ReportTableRow {
	text := strings.NewReplacer("${from}", from.Format(time.RFC3339), "${to}", to.Format(time.RFC3339)).Replace(q.Query)
	row := ReportTableRow{Title: cmp.Or(q.Title, q.Query), Query: text}
	source := q.Source
	if saved, ok := a.Queries()[q.Query]; ok && source == "" {
		source, text = saved.Source, saved.Query
		row.Query = text
	}
	var t core.Table
	err := func() error {
		v, err := a.Component(source)
		if err != nil {
			return err
		}
		qr, ok := v.(core.Querier)
		if !ok {
			return fmt.Errorf("%s does not answer queries", source)
		}
		if writes(qr.QueryLanguage(), text) {
			return errors.New("a report only reads: this query would change something")
		}
		t, err = a.RunQuery(ctx, source, text)
		return err
	}()
	if err != nil {
		row.Err = err.Error()
		return row
	}
	row.Columns, row.Total = t.Columns, len(t.Rows)
	row.Rows = t.Rows[:min(maxRows, len(t.Rows))]
	return row
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

// Markdown writes the report as tables: metrics, timeline, traces, queries.
func (r *ReportResult) Markdown(w io.Writer) {
	fmt.Fprintf(w, "## %s · %s · %s → %s (%s)\n\n", r.Name, r.Env, r.From.Format("2006-01-02 15:04:05"), r.To.Format("15:04:05"), r.To.Sub(r.From).Round(time.Second))
	if len(r.Rows) > 0 {
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
	if len(r.Timeline) > 0 {
		heads := make([]string, len(r.Buckets))
		for i, b := range r.Buckets {
			heads[i] = b.Format("15:04:05")
		}
		fmt.Fprintf(w, "\n### timeline\n\n| metric | series | %s |\n|---|---|%s\n", strings.Join(heads, " | "), strings.Repeat("---:|", len(heads)))
		for _, t := range r.Timeline {
			cells := make([]string, len(t.Values))
			for i, v := range t.Values {
				if !math.IsNaN(float64(v)) {
					cells[i] = viz.Human(float64(v), t.Unit)
				}
			}
			fmt.Fprintf(w, "| %s | %s | %s |\n", t.Metric, t.Series, strings.Join(cells, " | "))
		}
	}
	if len(r.Traces) > 0 {
		fmt.Fprintf(w, "\n### traces\n\n| traces | count | errors | p50 | p95 | max |\n|---|---:|---:|---:|---:|---:|\n")
		for _, t := range r.Traces {
			if t.Err != "" && t.Count == 0 {
				fmt.Fprintf(w, "| %s | ⚠ %s | | | | |\n", t.Title, t.Err)
				continue
			}
			fmt.Fprintf(w, "| %s | %d | %d | %s | %s | %s |\n", t.Title, t.Count, t.Errors, ms(t.P50), ms(t.P95), ms(t.Max))
		}
		for _, t := range r.Traces {
			if t.Err != "" && t.Count > 0 {
				fmt.Fprintf(w, "\n⚠ %s: %s\n", t.Title, t.Err)
			}
			if len(t.Slowest) == 0 {
				continue
			}
			fmt.Fprintf(w, "\nslowest %s:\n\n", t.Title)
			for _, s := range t.Slowest {
				mark := ""
				if s.Error {
					mark = " ⚠ error"
				}
				fmt.Fprintf(w, "- %s %s %s%s\n", ms(s.Duration), s.Root, s.ID, mark)
			}
		}
	}
	for _, t := range r.Tables {
		fmt.Fprintf(w, "\n### %s\n\n", t.Title)
		if t.Err != "" {
			fmt.Fprintf(w, "⚠ %s\n", t.Err)
			continue
		}
		fmt.Fprintf(w, "%d rows", t.Total)
		if len(t.Rows) < t.Total {
			fmt.Fprintf(w, ", the first %d shown", len(t.Rows))
		}
		fmt.Fprintln(w)
		if len(t.Rows) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n| %s |\n|%s\n", strings.Join(t.Columns, " | "), strings.Repeat("---|", len(t.Columns)))
		for _, row := range t.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = strings.ReplaceAll(strings.ReplaceAll(c, "|", "\\|"), "\n", " ")
			}
			fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | "))
		}
	}
}

func ms(d time.Duration) string { return viz.Human(float64(d)/float64(time.Millisecond), "ms") }

// Write writes the report as md, json or xml.
func (r *ReportResult) Write(w io.Writer, format string) error {
	switch format {
	case "json":
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		return e.Encode(r)
	case "xml":
		return r.xml(w)
	case "", "md", "markdown":
		r.Markdown(w)
		return nil
	}
	return fmt.Errorf("format %q: want md, json or xml", format)
}

// ReportFormat is the format a file name asks for, else the report's own.
func ReportFormat(file, fallback string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".md", ".markdown":
		return "md"
	}
	return cmp.Or(fallback, "md")
}

// xml spells the report out with elements; encoding/xml does not take maps.
func (r *ReportResult) xml(w io.Writer) error {
	type stat struct {
		Name  string  `xml:"name,attr"`
		Value float64 `xml:",chardata"`
	}
	type metric struct {
		Name   string `xml:"name,attr"`
		Series string `xml:"series,attr,omitempty"`
		Unit   string `xml:"unit,attr,omitempty"`
		Err    string `xml:"error,attr,omitempty"`
		Stats  []stat `xml:"stat"`
	}
	type point struct {
		At    time.Time `xml:"at,attr"`
		Value string    `xml:",chardata"`
	}
	type line struct {
		Metric string  `xml:"metric,attr"`
		Series string  `xml:"series,attr,omitempty"`
		Points []point `xml:"point"`
	}
	type cellRow struct {
		Cells []string `xml:"cell"`
	}
	type table struct {
		Title   string    `xml:"title,attr"`
		Total   int       `xml:"total,attr"`
		Err     string    `xml:"error,attr,omitempty"`
		Query   string    `xml:"query"`
		Columns []string  `xml:"columns>column"`
		Rows    []cellRow `xml:"row"`
	}
	type trace struct {
		Title  string `xml:"title,attr"`
		Count  int    `xml:"count,attr"`
		Errors int    `xml:"errors,attr"`
		P50ms  int64  `xml:"p50ms,attr"`
		P95ms  int64  `xml:"p95ms,attr"`
		MaxMs  int64  `xml:"maxms,attr"`
		Err    string `xml:"error,attr,omitempty"`
	}
	type doc struct {
		XMLName  xml.Name  `xml:"report"`
		Name     string    `xml:"name,attr"`
		Env      string    `xml:"env,attr"`
		From     time.Time `xml:"from,attr"`
		To       time.Time `xml:"to,attr"`
		Metrics  []metric  `xml:"metrics>metric"`
		Timeline []line    `xml:"timeline>line"`
		Traces   []trace   `xml:"traces>trace"`
		Tables   []table   `xml:"queries>query"`
	}
	d := doc{Name: r.Name, Env: r.Env, From: r.From, To: r.To}
	for _, row := range r.Rows {
		m := metric{Name: row.Metric, Series: row.Series, Unit: row.Unit, Err: row.Err}
		for _, st := range r.Stats {
			if v, ok := row.Values[st]; ok {
				m.Stats = append(m.Stats, stat{st, v})
			}
		}
		d.Metrics = append(d.Metrics, m)
	}
	for _, t := range r.Timeline {
		l := line{Metric: t.Metric, Series: t.Series}
		for i, v := range t.Values {
			if !math.IsNaN(float64(v)) {
				l.Points = append(l.Points, point{r.Buckets[i], strconv.FormatFloat(float64(v), 'g', -1, 64)})
			}
		}
		d.Timeline = append(d.Timeline, l)
	}
	for _, t := range r.Traces {
		d.Traces = append(d.Traces, trace{t.Title, t.Count, t.Errors, t.P50.Milliseconds(), t.P95.Milliseconds(), t.Max.Milliseconds(), t.Err})
	}
	for _, t := range r.Tables {
		tb := table{Title: t.Title, Total: t.Total, Err: t.Err, Query: t.Query, Columns: t.Columns}
		for _, row := range t.Rows {
			tb.Rows = append(tb.Rows, cellRow{row})
		}
		d.Tables = append(d.Tables, tb)
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	e := xml.NewEncoder(w)
	e.Indent("", "  ")
	if err := e.Encode(d); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// ReportFile is where a measured report is saved unless the caller names a file: the project's
// data directory, outside the repository.
func (a *App) ReportFile(name string, at time.Time) string {
	return filepath.Join(a.reportDir(), name+"-"+at.Format("20060102-150405")+".md")
}

func (a *App) reportDir() string {
	dir, err := spec.DataDir(a.Spec.Dir)
	if err != nil {
		dir = filepath.Join(a.Spec.Dir, ".rig")
	}
	return filepath.Join(dir, "reports")
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

// SaveReport writes rep to file (default under the data directory) in the format the file's
// extension or the report asks for, and returns the file.
func (a *App) SaveReport(rep *ReportResult, file string) (string, error) {
	format := ReportFormat(file, rep.Format)
	if file == "" {
		file = strings.TrimSuffix(a.ReportFile(rep.Name, rep.To), ".md") + "." + format
	}
	var werr error
	err := WriteReportFile(file, func(w io.Writer) { werr = rep.Write(w, format) })
	return file, cmp.Or(werr, err)
}

// reporterFile holds when a started reporter began: a local file, so starting one never writes to
// the environment.
func (a *App) reporterFile(name string) string {
	env := "default"
	if a.Env != nil {
		env = a.Env.Name
	}
	return filepath.Join(a.reportDir(), "."+env+"-"+name+".started")
}

// StartReporter notes now as the start of report name's window; StopReporter measures it.
func (a *App) StartReporter(name string) (time.Time, error) {
	if _, ok := a.Spec.Reports[name]; !ok {
		return time.Time{}, fmt.Errorf("no report %q in %s (have %v)", name, a.Spec.File, a.ReportNames())
	}
	if at, ok := a.ReporterStarted(name); ok {
		return at, fmt.Errorf("%s has been running since %s: rig report stop %s ends it", name, at.Format("15:04:05"), name)
	}
	now := time.Now()
	f := a.reporterFile(name)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return now, err
	}
	return now, os.WriteFile(f, []byte(now.Format(time.RFC3339Nano)), 0o600)
}

// ReporterStarted says whether report name's reporter runs, and since when.
func (a *App) ReporterStarted(name string) (time.Time, bool) {
	raw, err := os.ReadFile(a.reporterFile(name))
	if err != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	return at, err == nil
}

// StopReporter measures report name from its start until now and forgets the start.
func (a *App) StopReporter(ctx context.Context, name, source string) (*ReportResult, error) {
	from, ok := a.ReporterStarted(name)
	if !ok {
		return nil, fmt.Errorf("%s is not started: rig report start %s", name, name)
	}
	rep, err := a.MeasureReport(ctx, name, source, from, time.Now())
	if err != nil {
		return nil, err
	}
	_ = os.Remove(a.reporterFile(name))
	return rep, nil
}
