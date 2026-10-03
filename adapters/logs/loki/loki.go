// Package loki reads logs from Grafana Loki: following polls query_range, ad hoc queries take LogQL.
package loki

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/internal/httpx"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindLogs, "loki", "Grafana Loki (LogQL)", New)
}

type Options struct {
	httpx.Endpoint `yaml:",inline"`
	// Selector picks a service's streams; {service} is replaced by its name.
	Selector string `yaml:"selector"`
	Label    string `yaml:"label"`
}

type Loki struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	l := &Loki{env: env}
	if err := c.Decode(&l.opt); err != nil {
		return nil, err
	}
	if l.opt.Label == "" {
		l.opt.Label = "app"
	}
	if l.opt.Selector == "" {
		l.opt.Selector = `{` + l.opt.Label + `="{service}"}`
	}
	return l, nil
}

type entry struct {
	t       time.Time
	stream  map[string]string
	text    string
	service string
}

func (l *Loki) queryRange(ctx context.Context, q string, start, end time.Time, limit int) ([]entry, error) {
	v := url.Values{"query": {q}, "start": {strconv.FormatInt(start.UnixNano(), 10)}, "end": {strconv.FormatInt(end.UnixNano(), 10)},
		"limit": {strconv.Itoa(limit)}, "direction": {"forward"}}
	var r struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := l.opt.Do(ctx, l.env, "GET", "/loki/api/v1/query_range?"+v.Encode(), nil, &r); err != nil {
		return nil, err
	}
	var out []entry
	for _, s := range r.Data.Result {
		for _, v := range s.Values {
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			out = append(out, entry{t: time.Unix(0, ns), stream: s.Stream, text: v[1], service: s.Stream[l.opt.Label]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].t.Before(out[j].t) })
	return out, nil
}

func (l *Loki) selector(services []string) string {
	if len(services) == 1 {
		return strings.ReplaceAll(l.opt.Selector, "{service}", services[0])
	}
	re := ".+"
	if len(services) > 1 {
		re = strings.Join(services, "|")
	}
	return `{` + l.opt.Label + `=~"` + re + `"}`
}

func (l *Loki) Logs(ctx context.Context, q core.LogQuery) (<-chan core.LogLine, error) {
	sel := l.selector(q.Services)
	if q.Match != "" {
		sel += ` |= "` + strings.ReplaceAll(q.Match, `"`, `\"`) + `"`
	}
	since := q.Since
	if since == 0 {
		since = 15 * time.Minute
	}
	limit := q.Tail
	if limit == 0 {
		limit = 200
	}
	first, err := l.queryRange(ctx, sel, time.Now().Add(-since), time.Now(), limit)
	if err != nil {
		return nil, err
	}
	out := make(chan core.LogLine, 512)
	go func() {
		defer close(out)
		last := time.Now().Add(-since)
		send := func(es []entry) bool {
			for _, e := range es {
				if !e.t.After(last) {
					continue
				}
				last = e.t
				select {
				case out <- core.LogLine{Time: e.t, Service: e.service, Instance: e.stream["pod"], Text: e.text}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		if !send(first) || !q.Follow {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			es, err := l.queryRange(ctx, sel, last, time.Now(), 1000)
			if err != nil || !send(es) {
				if ctx.Err() != nil {
					return
				}
			}
		}
	}()
	return out, nil
}

func (l *Loki) QueryLanguage() string { return "logql" }

func (l *Loki) RunQuery(ctx context.Context, q string) (core.Table, error) {
	es, err := l.queryRange(ctx, q, time.Now().Add(-time.Hour), time.Now(), 500)
	if err != nil {
		return core.Table{}, err
	}
	t := core.Table{Columns: []string{"time", "stream", "line"}}
	for _, e := range es {
		t.Rows = append(t.Rows, []string{e.t.Format("15:04:05.000"), e.service, e.text})
	}
	return t, nil
}
