// Package jaeger reads traces through the Jaeger query API (Jaeger, or Grafana Tempo's Jaeger-compatible query).
package jaeger

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/httpx"
	"github.com/goxang/rig/internal/traces"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindTracing, "jaeger", "Jaeger query API", New)
}

type Tracing struct {
	ep  httpx.Endpoint
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	t := &Tracing{env: env}
	return t, c.Decode(&t.ep)
}

type trace struct {
	TraceID string `json:"traceID"`
	Spans   []struct {
		SpanID        string `json:"spanID"`
		OperationName string `json:"operationName"`
		References    []struct {
			RefType string `json:"refType"`
			SpanID  string `json:"spanID"`
		} `json:"references"`
		StartTime int64  `json:"startTime"`
		Duration  int64  `json:"duration"`
		ProcessID string `json:"processID"`
		Tags      []struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		} `json:"tags"`
	} `json:"spans"`
	Processes map[string]struct {
		ServiceName string `json:"serviceName"`
	} `json:"processes"`
}

func (tr trace) spans() []core.Span {
	out := make([]core.Span, 0, len(tr.Spans))
	for _, s := range tr.Spans {
		sp := core.Span{TraceID: tr.TraceID, ID: s.SpanID, Name: s.OperationName, Service: tr.Processes[s.ProcessID].ServiceName,
			Start: time.UnixMicro(s.StartTime), Duration: time.Duration(s.Duration) * time.Microsecond, Tags: map[string]string{}}
		for _, r := range s.References {
			if r.RefType == "CHILD_OF" {
				sp.Parent = r.SpanID
			}
		}
		for _, t := range s.Tags {
			sp.Tags[t.Key] = fmt.Sprint(t.Value)
			if t.Key == "error" && fmt.Sprint(t.Value) == "true" || t.Key == "otel.status_code" && fmt.Sprint(t.Value) == "ERROR" {
				sp.Error = true
			}
		}
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func (t *Tracing) Services(ctx context.Context) ([]string, error) {
	var r struct {
		Data []string `json:"data"`
	}
	err := t.ep.Do(ctx, t.env, "GET", "/api/services", nil, &r)
	sort.Strings(r.Data)
	return r.Data, err
}

func (t *Tracing) Search(ctx context.Context, q core.TraceQuery) ([]core.TraceSummary, error) {
	if q.Service == "" {
		svcs, err := t.Services(ctx)
		if err != nil || len(svcs) == 0 {
			return nil, err
		}
		q.Service = svcs[0] // jaeger requires a service; the TUI and CLI pass one when they can
	}
	v := url.Values{"service": {q.Service}}
	if q.Operation != "" {
		v.Set("operation", q.Operation)
	}
	if q.MinDuration > 0 {
		v.Set("minDuration", q.MinDuration.String())
	}
	if q.Lookback == 0 {
		q.Lookback = time.Hour
	}
	v.Set("start", strconv.FormatInt(time.Now().Add(-q.Lookback).UnixMicro(), 10))
	if q.Limit == 0 {
		q.Limit = 50
	}
	v.Set("limit", strconv.Itoa(q.Limit))
	var r struct {
		Data []trace `json:"data"`
	}
	if err := t.ep.Do(ctx, t.env, "GET", "/api/traces?"+v.Encode(), nil, &r); err != nil {
		return nil, err
	}
	out := make([]core.TraceSummary, 0, len(r.Data))
	for _, tr := range r.Data {
		out = append(out, traces.Summarize(tr.spans()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

func (t *Tracing) Trace(ctx context.Context, id string) ([]core.Span, error) {
	var r struct {
		Data []trace `json:"data"`
	}
	if err := t.ep.Do(ctx, t.env, "GET", "/api/traces/"+url.PathEscape(id), nil, &r); err != nil {
		return nil, err
	}
	if len(r.Data) == 0 {
		return nil, fmt.Errorf("trace %s not found", id)
	}
	return r.Data[0].spans(), nil
}

func (t *Tracing) QueryLanguage() string { return "traces" }

func (t *Tracing) RunQuery(ctx context.Context, q string) (core.Table, error) {
	return traces.QueryTable(ctx, t, q)
}
