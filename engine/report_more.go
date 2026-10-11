package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// ReportTests is a suite's last run as a report sees it.
type ReportTests struct {
	Suite  string   `json:"suite"`
	Run    string   `json:"run"`
	Passed int      `json:"passed"`
	Failed int      `json:"failed"`
	Skip   int      `json:"skipped"`
	Fails  []string `json:"fails,omitempty"`
}

// ReportDelta is one metric's stat now against the compared report.
type ReportDelta struct {
	Metric string  `json:"metric"`
	Series string  `json:"series,omitempty"`
	Stat   string  `json:"stat"`
	Was    float64 `json:"was"`
	Now    float64 `json:"now"`
}

func (d ReportDelta) Change() string {
	if d.Was == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f%%", 100*(d.Now-d.Was)/d.Was)
}

// enrich adds what the report asks beyond metrics: tests, load, compare, the template and the AI's
// summary, then keeps a JSON copy for later compares.
func (a *App) enrich(ctx context.Context, r *spec.Report, out *ReportResult) error {
	if r.Tests != "" {
		if run, err := a.LoadTestRun("last", r.Tests); err == nil {
			c := run.Counts()
			t := &ReportTests{Suite: r.Tests, Run: run.ID, Passed: c[TestPass], Failed: c[TestFail], Skip: c[TestSkip]}
			for _, tc := range run.Tests {
				if tc.Name != "" && tc.Status == TestFail {
					t.Fails = append(t.Fails, tc.Package+" "+tc.Name)
				}
			}
			out.Tests = t
		}
	}
	if r.Load != "" {
		out.Load, _ = a.LoadResultFor("last", r.Load, "")
	}
	if r.Compare != "" {
		if base, err := a.savedReport(out.Name, r.Compare); err == nil {
			out.Compare = compareRows(base, out)
		}
	}
	if r.Template != "" {
		text, err := a.renderTemplate(ctx, r.Template, out)
		if err != nil {
			return fmt.Errorf("report %s: template: %w", out.Name, err)
		}
		out.Text = text
	}
	if r.AI {
		if runner, err := a.AI(""); err == nil {
			var b bytes.Buffer
			out.Markdown(&b)
			out.Summary, _ = runner.Ask(ctx, "Summarise this report for the team in at most five bullet points: what changed, what is worse, what to look at. Cite metrics by name.\n\n"+a.Redactor().Redact(b.String()))
		}
	}
	return a.keepReport(out)
}

func (a *App) reportHistory(name string) string { return filepath.Join(a.StateDir(), "reports", name) }

func (a *App) keepReport(r *ReportResult) error {
	dir := a.reportHistory(r.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, _ := json.Marshal(r)
	return os.WriteFile(filepath.Join(dir, r.To.Format("20060102-150405")+".json"), raw, 0o644)
}

// savedReport is report name as kept at id (its end time), or with "last" the latest one kept.
func (a *App) savedReport(name, id string) (*ReportResult, error) {
	files, _ := filepath.Glob(filepath.Join(a.reportHistory(name), "*.json"))
	sort.Strings(files)
	for i := len(files) - 1; i >= 0; i-- {
		if id == "last" || strings.HasPrefix(filepath.Base(files[i]), id) {
			raw, err := os.ReadFile(files[i])
			if err != nil {
				return nil, err
			}
			var r ReportResult
			return &r, json.Unmarshal(raw, &r)
		}
	}
	return nil, fmt.Errorf("no kept report %s %s", name, id)
}

func compareRows(base, now *ReportResult) []ReportDelta {
	was := map[string]ReportRow{}
	for _, r := range base.Rows {
		was[r.Metric+"\x00"+r.Series] = r
	}
	var out []ReportDelta
	for _, r := range now.Rows {
		b, ok := was[r.Metric+"\x00"+r.Series]
		if !ok {
			continue
		}
		for _, st := range now.Stats {
			v, ok1 := r.Values[st]
			w, ok2 := b.Values[st]
			if ok1 && ok2 {
				out = append(out, ReportDelta{Metric: r.Metric, Series: r.Series, Stat: st, Was: w, Now: v})
			}
		}
	}
	return out
}

func (a *App) renderTemplate(ctx context.Context, text string, r *ReportResult) (string, error) {
	funcs := template.FuncMap{
		// table "source" "key=value"...: a plugin source's rows as a markdown table
		"table": func(source string, kv ...string) (string, error) {
			args := protocol.Args{}
			for _, p := range kv {
				k, v, _ := strings.Cut(p, "=")
				args[k] = v
			}
			f, err := a.Host().Query(ctx, source, args)
			if err != nil {
				return "", err
			}
			return frameMarkdown(f), nil
		},
	}
	t, err := template.New(r.Name).Funcs(funcs).Parse(text)
	if err != nil {
		return "", err
	}
	parts := map[string]string{}
	for k, f := range map[string]func(io.Writer){"Rows": r.body, "Tests": r.testsMD, "Load": r.loadMD, "Compare": r.compareMD} {
		var b bytes.Buffer
		f(&b)
		parts[k] = b.String()
	}
	var b bytes.Buffer
	err = t.Execute(&b, parts)
	return b.String(), err
}

func frameMarkdown(f *protocol.Frame) string {
	var b strings.Builder
	var names []string
	for _, fl := range f.Fields {
		names = append(names, fl.Name)
	}
	b.WriteString("| " + strings.Join(names, " | ") + " |\n|" + strings.Repeat("---|", len(names)) + "\n")
	for _, row := range frames.Rows(f) {
		cells := make([]string, len(names))
		for i, n := range names {
			cells[i] = strings.ReplaceAll(fmt.Sprint(row[n]), "|", "\\|")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	return b.String()
}

// extras are the sections after the body (or template), unless the template placed them itself.
func (r *ReportResult) extras(w io.Writer) {
	if r.Text == "" {
		r.testsMD(w)
		r.loadMD(w)
		r.compareMD(w)
	}
	if r.Summary != "" {
		fmt.Fprintf(w, "\n### Summary\n\n%s\n", r.Summary)
	}
}

func (r *ReportResult) testsMD(w io.Writer) {
	if t := r.Tests; t != nil {
		fmt.Fprintf(w, "\n### Tests: %s (%s)\n\n%d passed, %d failed, %d skipped\n", t.Suite, t.Run, t.Passed, t.Failed, t.Skip)
		for _, f := range t.Fails {
			fmt.Fprintf(w, "- ✗ %s\n", f)
		}
	}
}

func (r *ReportResult) loadMD(w io.Writer) {
	if l := r.Load; l != nil {
		fmt.Fprintf(w, "\n### Load: %s (%s)\n\n%d sent, %.2f%% errors, %.1f/s, p50 %s, p95 %s, p99 %s\n", l.Name, l.ID, l.Sent, l.ErrorRate(),
			l.Throughput, l.P50.Round(time.Microsecond), l.P95.Round(time.Microsecond), l.P99.Round(time.Microsecond))
		for _, c := range l.Checks {
			mark := "✓"
			if !c.OK {
				mark = "✗"
			}
			fmt.Fprintf(w, "- %s %s (got %s)\n", mark, c.Assert, c.Got)
		}
	}
}

func (r *ReportResult) compareMD(w io.Writer) {
	if len(r.Compare) == 0 {
		return
	}
	fmt.Fprintf(w, "\n### Compared with the last report\n\n| metric | series | stat | was | now | change |\n|---|---|---|---:|---:|---:|\n")
	for _, d := range r.Compare {
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", d.Metric, d.Series, d.Stat, strconv.FormatFloat(d.Was, 'g', 4, 64), strconv.FormatFloat(d.Now, 'g', 4, 64), d.Change())
	}
}

// ponytail: the markdown is shown preformatted, not rendered; render it when someone reads these in a browser
func (r *ReportResult) html(w io.Writer) error {
	var b bytes.Buffer
	r.Markdown(&b)
	_, err := fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><style>body{font:14px ui-monospace,monospace;margin:2em}</style><pre>%s</pre>\n",
		html.EscapeString(r.Name), html.EscapeString(b.String()))
	return err
}

func (r *ReportResult) csv(w io.Writer) error {
	c := csv.NewWriter(w)
	_ = c.Write([]string{"metric", "series", "stat", "value"})
	for _, row := range r.Rows {
		for _, st := range r.Stats {
			if v, ok := row.Values[st]; ok {
				_ = c.Write([]string{row.Metric, row.Series, st, strconv.FormatFloat(v, 'f', -1, 64)})
			}
		}
	}
	c.Flush()
	return c.Error()
}

// junit makes each load assertion and failed test a test case, so CI shows what broke.
func (r *ReportResult) junit(w io.Writer) error {
	type failure struct {
		Message string `xml:"message,attr"`
	}
	type tc struct {
		Name    string   `xml:"name,attr"`
		Class   string   `xml:"classname,attr"`
		Failure *failure `xml:"failure,omitempty"`
	}
	var cases []tc
	if l := r.Load; l != nil {
		for _, c := range l.Checks {
			t := tc{Name: c.Assert, Class: "load." + l.Name}
			if !c.OK {
				t.Failure = &failure{Message: "got " + c.Got}
			}
			cases = append(cases, t)
		}
	}
	if t := r.Tests; t != nil {
		for _, f := range t.Fails {
			cases = append(cases, tc{Name: f, Class: "tests." + t.Suite, Failure: &failure{Message: "failed"}})
		}
	}
	fails := 0
	for _, c := range cases {
		if c.Failure != nil {
			fails++
		}
	}
	suite := struct {
		XMLName  xml.Name `xml:"testsuite"`
		Name     string   `xml:"name,attr"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
		Cases    []tc     `xml:"testcase"`
	}{Name: r.Name, Tests: len(cases), Failures: fails, Cases: cases}
	io.WriteString(w, xml.Header)
	e := xml.NewEncoder(w)
	e.Indent("", "  ")
	return e.Encode(suite)
}

// Failed says whether the report holds a failure: a failed test or a broken load assertion.
func (r *ReportResult) Failed() bool {
	return (r.Tests != nil && r.Tests.Failed > 0) || (r.Load != nil && !r.Load.Passed())
}

// Publish runs the report's publish: actions through policy, with ${report} and ${file} filled in.
func (l *Local) Publish(ctx context.Context, rep *ReportResult, file string) error {
	r := l.App.Spec.Reports[rep.Name]
	if r == nil || len(r.Publish) == 0 {
		return nil
	}
	var md bytes.Buffer
	rep.Markdown(&md)
	fill := strings.NewReplacer("${report}", md.String(), "${file}", file)
	for _, p := range r.Publish {
		args := protocol.Args{}
		for k, v := range p.Args {
			if s, ok := v.(string); ok {
				v = fill.Replace(s)
			}
			args[k] = v
		}
		if _, _, err := l.Run(ctx, p.Action, args, api.RunOpts{}); err != nil {
			return fmt.Errorf("report %s: publish %s: %w", rep.Name, p.Action, err)
		}
	}
	return nil
}
