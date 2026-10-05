// Package tempo reads traces through Grafana Tempo's own API: TraceQL search and OTLP traces.
package tempo

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/httpx"
	"github.com/goxang/rig/internal/traces"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindTracing, "tempo", "Grafana Tempo: TraceQL search, OpenTelemetry traces", New)
}

type Tracing struct {
	ep  httpx.Endpoint
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	t := &Tracing{env: env}
	if err := c.Decode(&t.ep); err != nil {
		return nil, err
	}
	if t.ep.Headers == nil {
		t.ep.Headers = map[string]string{}
	}
	// without it Tempo answers /api/traces in protobuf
	t.ep.Headers["Accept"] = "application/json"
	return t, nil
}

func (t *Tracing) Services(ctx context.Context) ([]string, error) {
	var r struct {
		TagValues []string `json:"tagValues"`
	}
	err := t.ep.Do(ctx, t.env, "GET", "/api/search/tag/service.name/values", nil, &r)
	sort.Strings(r.TagValues)
	return r.TagValues, err
}

// TraceQL is the query a TraceQuery stands for.
func TraceQL(q core.TraceQuery) string {
	var conds []string
	if q.Service != "" {
		conds = append(conds, "resource.service.name = "+strconv.Quote(q.Service))
	}
	if q.Operation != "" {
		conds = append(conds, "name = "+strconv.Quote(q.Operation))
	}
	if q.MinDuration > 0 {
		conds = append(conds, "duration >= "+q.MinDuration.String())
	}
	return "{ " + strings.Join(conds, " && ") + " }"
}

func (t *Tracing) Search(ctx context.Context, q core.TraceQuery) ([]core.TraceSummary, error) {
	return t.search(ctx, TraceQL(q), q.Lookback, q.Limit)
}

func (t *Tracing) search(ctx context.Context, traceql string, lookback time.Duration, limit int) ([]core.TraceSummary, error) {
	if lookback == 0 {
		lookback = time.Hour
	}
	if limit == 0 {
		limit = 50
	}
	now := time.Now()
	v := url.Values{"q": {traceql}, "limit": {strconv.Itoa(limit)},
		"start": {strconv.FormatInt(now.Add(-lookback).Unix(), 10)}, "end": {strconv.FormatInt(now.Unix(), 10)}}
	var r struct {
		Traces []struct {
			TraceID           string `json:"traceID"`
			RootServiceName   string `json:"rootServiceName"`
			RootTraceName     string `json:"rootTraceName"`
			StartTimeUnixNano string `json:"startTimeUnixNano"`
			DurationMs        int64  `json:"durationMs"`
			SpanSets          []struct {
				Matched int `json:"matched"`
			} `json:"spanSets"`
		} `json:"traces"`
	}
	if err := t.ep.Do(ctx, t.env, "GET", "/api/search?"+v.Encode(), nil, &r); err != nil {
		return nil, err
	}
	out := make([]core.TraceSummary, 0, len(r.Traces))
	for _, tr := range r.Traces {
		ns, _ := strconv.ParseInt(tr.StartTimeUnixNano, 10, 64)
		s := core.TraceSummary{ID: tr.TraceID, Root: tr.RootServiceName + ": " + tr.RootTraceName, Start: time.Unix(0, ns),
			Duration: time.Duration(tr.DurationMs) * time.Millisecond, Services: []string{tr.RootServiceName}}
		for _, ss := range tr.SpanSets {
			s.Spans += ss.Matched
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

type attr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue"`
		IntValue    *string  `json:"intValue"`
		DoubleValue *float64 `json:"doubleValue"`
		BoolValue   *bool    `json:"boolValue"`
	} `json:"value"`
}

func (a attr) String() string {
	v := a.Value
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return *v.IntValue
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	}
	return ""
}

type scope struct {
	Spans []struct {
		TraceID           string `json:"traceId"`
		SpanID            string `json:"spanId"`
		ParentSpanID      string `json:"parentSpanId"`
		Name              string `json:"name"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
		EndTimeUnixNano   string `json:"endTimeUnixNano"`
		Attributes        []attr `json:"attributes"`
		Status            struct {
			Code any `json:"code"`
		} `json:"status"`
	} `json:"spans"`
}

type resourceSpans struct {
	Resource struct {
		Attributes []attr `json:"attributes"`
	} `json:"resource"`
	ScopeSpans                  []scope `json:"scopeSpans"`
	InstrumentationLibrarySpans []scope `json:"instrumentationLibrarySpans"`
}

// otlpID turns an OTLP JSON id (base64 from Tempo, hex from others) into hex.
func otlpID(s string) string {
	if _, err := hex.DecodeString(s); err == nil && (len(s) == 16 || len(s) == 32) {
		return s
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return hex.EncodeToString(b)
	}
	return s
}

func spansOf(batches []resourceSpans) []core.Span {
	var out []core.Span
	for _, b := range batches {
		service := ""
		for _, a := range b.Resource.Attributes {
			if a.Key == "service.name" {
				service = a.String()
			}
		}
		for _, sc := range append(b.ScopeSpans, b.InstrumentationLibrarySpans...) {
			for _, s := range sc.Spans {
				start, _ := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
				end, _ := strconv.ParseInt(s.EndTimeUnixNano, 10, 64)
				sp := core.Span{TraceID: otlpID(s.TraceID), ID: otlpID(s.SpanID), Service: service, Name: s.Name,
					Start: time.Unix(0, start), Duration: time.Duration(end - start), Tags: map[string]string{}}
				if s.ParentSpanID != "" {
					sp.Parent = otlpID(s.ParentSpanID)
				}
				for _, a := range s.Attributes {
					sp.Tags[a.Key] = a.String()
				}
				// the code is 2 or "STATUS_CODE_ERROR" depending on the encoder
				if c := fmt.Sprint(s.Status.Code); c == "2" || c == "STATUS_CODE_ERROR" {
					sp.Error = true
				}
				out = append(out, sp)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func (t *Tracing) Trace(ctx context.Context, id string) ([]core.Span, error) {
	var r struct {
		Batches       []resourceSpans `json:"batches"`
		ResourceSpans []resourceSpans `json:"resourceSpans"`
	}
	if err := t.ep.Do(ctx, t.env, "GET", "/api/traces/"+url.PathEscape(id), nil, &r); err != nil {
		return nil, err
	}
	spans := spansOf(append(r.Batches, r.ResourceSpans...))
	if len(spans) == 0 {
		return nil, fmt.Errorf("trace %s not found", id)
	}
	return spans, nil
}

func (t *Tracing) QueryLanguage() string { return "traceql" }

// RunQuery takes TraceQL ({ status = error }), or what every tracing adapter takes: a trace id, or
// service=, op=, min=, lookback=, limit=.
func (t *Tracing) RunQuery(ctx context.Context, q string) (core.Table, error) {
	if !strings.HasPrefix(strings.TrimSpace(q), "{") {
		return traces.QueryTable(ctx, t, q)
	}
	sums, err := t.search(ctx, q, 0, 0)
	if err != nil {
		return core.Table{}, err
	}
	tb := core.Table{Columns: []string{"trace", "root", "start", "duration", "matched"}}
	for _, s := range sums {
		tb.Rows = append(tb.Rows, []string{s.ID, s.Root, s.Start.Local().Format("15:04:05.000"), s.Duration.String(), strconv.Itoa(s.Spans)})
	}
	return tb, nil
}
