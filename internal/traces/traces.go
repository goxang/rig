// Package traces holds what every tracing adapter shares: summaries and the "traces" query language.
package traces

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
)

// Summarize builds a trace's summary from its spans; shared by every tracing adapter.
func Summarize(spans []core.Span) core.TraceSummary {
	s := core.TraceSummary{Spans: len(spans)}
	if len(spans) == 0 {
		return s
	}
	svcs := map[string]bool{}
	var root *core.Span
	end := time.Time{}
	for i := range spans {
		sp := &spans[i]
		s.ID = sp.TraceID
		svcs[sp.Service] = true
		if sp.Error {
			s.Error = true
		}
		// the parentless span, else the earliest one (a trace whose root was not collected)
		if root == nil || sp.Parent == "" && root.Parent != "" || (sp.Parent == "") == (root.Parent == "") && sp.Start.Before(root.Start) {
			root = sp
		}
		if e := sp.Start.Add(sp.Duration); e.After(end) {
			end = e
		}
		if s.Start.IsZero() || sp.Start.Before(s.Start) {
			s.Start = sp.Start
		}
	}
	s.Root = root.Service + ": " + root.Name
	s.Duration = end.Sub(s.Start)
	for n := range svcs {
		s.Services = append(s.Services, n)
	}
	sort.Strings(s.Services)
	return s
}

// QueryTable answers the "traces" query language for any core.Tracing.
func QueryTable(ctx context.Context, t core.Tracing, q string) (core.Table, error) {
	f := strings.Fields(q)
	if len(f) == 1 && !strings.Contains(f[0], "=") {
		spans, err := t.Trace(ctx, f[0])
		if err != nil {
			return core.Table{}, err
		}
		tb := core.Table{Columns: []string{"service", "span", "start", "duration", "error"}}
		var t0 time.Time
		if len(spans) > 0 {
			t0 = spans[0].Start
		}
		for _, s := range spans {
			tb.Rows = append(tb.Rows, []string{s.Service, s.Name, "+" + s.Start.Sub(t0).String(), s.Duration.String(), fmt.Sprint(s.Error)})
		}
		return tb, nil
	}
	var tq core.TraceQuery
	for _, kv := range f {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "service":
			tq.Service = v
		case "op", "operation", "span":
			tq.Operation = v
		case "min", "minDuration":
			tq.MinDuration, _ = time.ParseDuration(v)
		case "lookback":
			tq.Lookback, _ = time.ParseDuration(v)
		case "limit":
			tq.Limit, _ = strconv.Atoi(v)
		default:
			return core.Table{}, fmt.Errorf("unknown %q: use service= op= min= lookback= limit=, or a trace id", k)
		}
	}
	ts, err := t.Search(ctx, tq)
	if err != nil {
		return core.Table{}, err
	}
	tb := core.Table{Columns: []string{"trace", "root", "start", "duration", "spans", "services", "error"}}
	for _, s := range ts {
		tb.Rows = append(tb.Rows, []string{s.ID, s.Root, s.Start.Format("15:04:05.000"), s.Duration.String(), strconv.Itoa(s.Spans), strings.Join(s.Services, ","), fmt.Sprint(s.Error)})
	}
	return tb, nil
}
