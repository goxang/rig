// Package zipkin reads traces through the Zipkin v2 API (Zipkin itself, or anything compatible).
package zipkin

import (
	"context"
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
	plugin.Register(core.KindTracing, "zipkin", "Zipkin v2 API", New)
}

type Tracing struct {
	ep  httpx.Endpoint
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	t := &Tracing{env: env}
	return t, c.Decode(&t.ep)
}

type span struct {
	TraceID       string                       `json:"traceId"`
	ID            string                       `json:"id"`
	ParentID      string                       `json:"parentId"`
	Name          string                       `json:"name"`
	Timestamp     int64                        `json:"timestamp"`
	Duration      int64                        `json:"duration"`
	LocalEndpoint struct{ ServiceName string } `json:"localEndpoint"`
	Tags          map[string]string            `json:"tags"`
}

func (s span) core() core.Span {
	_, isErr := s.Tags["error"]
	return core.Span{TraceID: s.TraceID, ID: s.ID, Parent: s.ParentID, Service: s.LocalEndpoint.ServiceName, Name: s.Name,
		Start: time.UnixMicro(s.Timestamp), Duration: time.Duration(s.Duration) * time.Microsecond, Error: isErr, Tags: s.Tags}
}

func (t *Tracing) Services(ctx context.Context) ([]string, error) {
	var out []string
	err := t.ep.Do(ctx, t.env, "GET", "/api/v2/services", nil, &out)
	sort.Strings(out)
	return out, err
}

func (t *Tracing) Search(ctx context.Context, q core.TraceQuery) ([]core.TraceSummary, error) {
	v := url.Values{}
	if q.Service != "" {
		v.Set("serviceName", q.Service)
	}
	if q.Operation != "" {
		v.Set("spanName", q.Operation)
	}
	if q.MinDuration > 0 {
		v.Set("minDuration", strconv.FormatInt(q.MinDuration.Microseconds(), 10))
	}
	if q.Lookback == 0 {
		q.Lookback = time.Hour
	}
	v.Set("lookback", strconv.FormatInt(q.Lookback.Milliseconds(), 10))
	if q.Limit == 0 {
		q.Limit = 50
	}
	v.Set("limit", strconv.Itoa(q.Limit))
	var found [][]span
	if err := t.ep.Do(ctx, t.env, "GET", "/api/v2/traces?"+v.Encode(), nil, &found); err != nil {
		return nil, err
	}
	out := make([]core.TraceSummary, 0, len(found))
	for _, tr := range found {
		spans := make([]core.Span, len(tr))
		for i, s := range tr {
			spans[i] = s.core()
		}
		out = append(out, traces.Summarize(spans))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

func (t *Tracing) Trace(ctx context.Context, id string) ([]core.Span, error) {
	var tr []span
	if err := t.ep.Do(ctx, t.env, "GET", "/api/v2/trace/"+url.PathEscape(id), nil, &tr); err != nil {
		return nil, err
	}
	out := make([]core.Span, len(tr))
	for i, s := range tr {
		out[i] = s.core()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func (t *Tracing) QueryLanguage() string { return "traces" }

// RunQuery takes "service=x op=y min=100ms lookback=1h limit=20", or a trace id.
func (t *Tracing) RunQuery(ctx context.Context, q string) (core.Table, error) {
	return traces.QueryTable(ctx, t, q)
}
