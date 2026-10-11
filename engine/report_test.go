package engine

import (
	"bytes"
	"context"
	"encoding/xml"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

func TestReportFormats(t *testing.T) {
	at := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	rep := &ReportResult{Name: "load", Env: "dev", From: at, To: at.Add(2 * time.Minute), Stats: []string{"avg"},
		Rows:     []ReportRow{{Metric: "rps", Unit: "/s", Values: map[string]float64{"avg": 12}}},
		Buckets:  []time.Time{at, at.Add(time.Minute)},
		Timeline: []ReportTimeline{{Metric: "rps", Values: []Gap{10, Gap(math.NaN())}}},
		Traces:   []ReportTraceRow{{Title: "api", Count: 3, Errors: 1, P95: 250 * time.Millisecond}},
		Tables:   []ReportTableRow{{Title: "orders", Columns: []string{"id", "note"}, Rows: [][]string{{"1", "a|b"}}, Total: 7}},
	}
	var md bytes.Buffer
	if err := rep.Write(&md, "md"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### timeline", "| 14:00:00 | 14:01:00 |", "| api | 3 | 1 |", "7 rows, the first 1 shown", `a\|b`} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, md.String())
		}
	}
	var js bytes.Buffer
	if err := rep.Write(&js, "json"); err != nil || !strings.Contains(js.String(), "null") {
		t.Fatalf("json with a gap: %v\n%s", err, js.String())
	}
	var x bytes.Buffer
	if err := rep.Write(&x, "xml"); err != nil {
		t.Fatal(err)
	}
	var back struct {
		Name    string `xml:"name,attr"`
		Metrics []struct {
			Stats []float64 `xml:"stat"`
		} `xml:"metrics>metric"`
	}
	if err := xml.Unmarshal(x.Bytes(), &back); err != nil || back.Name != "load" || back.Metrics[0].Stats[0] != 12 {
		t.Fatalf("xml %v %+v\n%s", err, back, x.String())
	}
	if ReportFormat("a/b.XML", "md") != "xml" || ReportFormat("", "json") != "json" || ReportFormat("x.txt", "") != "md" {
		t.Fatal("ReportFormat")
	}
}

func TestBucketed(t *testing.T) {
	at := time.Unix(0, 0)
	ps := []core.Point{{T: at, V: 1}, {T: at.Add(30 * time.Second), V: 3}, {T: at.Add(2 * time.Minute), V: 9}}
	got := bucketed(ps, []time.Time{at, at.Add(time.Minute), at.Add(2 * time.Minute)}, time.Minute)
	if got[0] != 2 || !math.IsNaN(float64(got[1])) || got[2] != 9 {
		t.Fatalf("%v", got)
	}
}

func TestReporterStartStop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	a := &App{Spec: &spec.Project{Dir: t.TempDir(), Reports: map[string]*spec.Report{"load": {}}}}
	if _, err := a.StartReporter("nope"); err == nil {
		t.Fatal("started an unknown report")
	}
	at, err := a.StartReporter("load")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := a.ReporterStarted("load"); !ok || !got.Equal(at) {
		t.Fatalf("started %v %v", got, ok)
	}
	if _, err := a.StartReporter("load"); err == nil {
		t.Fatal("started twice")
	}
	rep, err := a.StopReporter(context.Background(), "load", "")
	if err != nil || !rep.From.Equal(at) {
		t.Fatalf("%v %+v", err, rep)
	}
	if _, ok := a.ReporterStarted("load"); ok {
		t.Fatal("still started after stop")
	}
}
