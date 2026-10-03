// Package http is a query component for HTTP APIs: "GET /path", "POST /path {json}".
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/internal/httpx"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindQuery, "http", "ad hoc HTTP requests against a service or URL", New)
}

type Query struct {
	ep  httpx.Endpoint
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	q := &Query{env: env}
	return q, c.Decode(&q.ep)
}

func (q *Query) QueryLanguage() string { return "http" }

func (q *Query) Ping(ctx context.Context) error {
	_, err := q.RunQuery(ctx, "GET /")
	return err
}

// RunQuery sends "METHOD /path [body]"; a bare path means GET.
func (q *Query) RunQuery(ctx context.Context, s string) (core.Table, error) {
	s = strings.TrimSpace(s)
	method, rest, _ := strings.Cut(s, " ")
	if strings.HasPrefix(method, "/") {
		method, rest = "GET", s
	}
	path, body, _ := strings.Cut(strings.TrimSpace(rest), " ")
	base, err := q.ep.Base(ctx, q.env)
	if err != nil {
		return core.Table{}, err
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), base+path, strings.NewReader(body))
	if err != nil {
		return core.Table{}, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range q.ep.Headers {
		req.Header.Set(k, v)
	}
	if q.ep.User != "" {
		req.SetBasicAuth(q.ep.User, q.ep.Password)
	}
	start := time.Now()
	resp, err := httpx.Default.Do(req)
	if err != nil {
		return core.Table{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		raw = pretty.Bytes()
	}
	t := core.Table{Columns: []string{"body"}, Note: fmt.Sprintf("%s in %s", resp.Status, time.Since(start).Round(time.Millisecond))}
	for _, l := range strings.Split(string(raw), "\n") {
		t.Rows = append(t.Rows, []string{l})
	}
	return t, nil
}
