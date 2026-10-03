// Package prometheus reads metrics from anything that serves the Prometheus HTTP API:
// Prometheus, Thanos, Mimir, VictoriaMetrics, or an OpenTelemetry pipeline that ends in one of them.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/httpx"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindMetrics, "prometheus", "Prometheus HTTP API (Prometheus, Thanos, Mimir, VictoriaMetrics)", New)
}

type Metrics struct {
	ep  httpx.Endpoint
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	m := &Metrics{env: env}
	if err := c.Decode(&m.ep); err != nil {
		return nil, err
	}
	return m, nil
}

type apiResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Raw        json.RawMessage `json:"result"`
		Result     []result        `json:"-"`
	} `json:"data"`
}

type result struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
	Values [][]any           `json:"values"`
}

func (m *Metrics) get(ctx context.Context, path string, q url.Values) (apiResponse, error) {
	var r apiResponse
	err := m.ep.Do(ctx, m.env, "GET", path+"?"+q.Encode(), nil, &r)
	if err == nil && r.Status != "success" {
		err = fmt.Errorf("%s: %s", r.ErrorType, r.Error)
	}
	if err != nil {
		return r, err
	}
	switch r.Data.ResultType {
	case "scalar", "string":
		var v []any
		err = json.Unmarshal(r.Data.Raw, &v)
		r.Data.Result = []result{{Value: v}}
	default:
		err = json.Unmarshal(r.Data.Raw, &r.Data.Result)
	}
	return r, err
}

func (m *Metrics) Instant(ctx context.Context, query string) ([]core.Sample, error) {
	r, err := m.get(ctx, "/api/v1/query", url.Values{"query": {query}})
	if err != nil {
		return nil, err
	}
	var out []core.Sample
	for _, x := range r.Data.Result {
		if len(x.Value) == 2 {
			out = append(out, core.Sample{Labels: x.Metric, Value: num(x.Value[1])})
		}
	}
	return out, nil
}

func (m *Metrics) Range(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]core.Series, error) {
	r, err := m.get(ctx, "/api/v1/query_range", url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	})
	if err != nil {
		return nil, err
	}
	out := make([]core.Series, 0, len(r.Data.Result))
	for _, x := range r.Data.Result {
		s := core.Series{Labels: x.Metric}
		for _, v := range x.Values {
			if len(v) == 2 {
				s.Points = append(s.Points, core.Point{T: time.Unix(int64(num(v[0])), 0), V: num(v[1])})
			}
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *Metrics) Ping(ctx context.Context) error {
	return m.ep.Do(ctx, m.env, "GET", "/-/ready", nil, nil)
}

func (m *Metrics) QueryLanguage() string { return "promql" }

func (m *Metrics) RunQuery(ctx context.Context, q string) (core.Table, error) {
	ss, err := m.Instant(ctx, q)
	if err != nil {
		return core.Table{}, err
	}
	return SamplesTable(ss), nil
}

// SamplesTable shows samples as rows: one column per label, then the value.
func SamplesTable(ss []core.Sample) core.Table {
	seen := map[string]bool{}
	var cols []string
	for _, s := range ss {
		for k := range s.Labels {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	t := core.Table{Columns: append(append([]string{}, cols...), "value")}
	for _, s := range ss {
		row := make([]string, 0, len(cols)+1)
		for _, c := range cols {
			row = append(row, s.Labels[c])
		}
		t.Rows = append(t.Rows, append(row, strconv.FormatFloat(s.Value, 'g', 6, 64)))
	}
	return t
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}
