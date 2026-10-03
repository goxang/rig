// Package rabbitmq reads and manages RabbitMQ through its management HTTP API (the management plugin).
package rabbitmq

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/httpx"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindMessaging, "rabbitmq", "RabbitMQ management API: queues, rates, purge, publish", New)
}

type Options struct {
	httpx.Endpoint `yaml:",inline"`
	VHost          string `yaml:"vhost"`
}

type Rabbit struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	r := &Rabbit{env: env}
	if err := c.Decode(&r.opt); err != nil {
		return nil, err
	}
	if r.opt.User == "" {
		r.opt.User, r.opt.Password = "guest", "guest"
	}
	if r.opt.VHost == "" {
		r.opt.VHost = "/"
	}
	return r, nil
}

func (r *Rabbit) vhost() string { return url.PathEscape(r.opt.VHost) }

func (r *Rabbit) Queues(ctx context.Context) ([]core.Queue, error) {
	var raw []struct {
		Name                   string `json:"name"`
		Messages               int    `json:"messages"`
		MessagesReady          int    `json:"messages_ready"`
		MessagesUnacknowledged int    `json:"messages_unacknowledged"`
		Consumers              int    `json:"consumers"`
		MessageStats           struct {
			PublishDetails struct {
				Rate float64 `json:"rate"`
			} `json:"publish_details"`
			DeliverGetDetails struct {
				Rate float64 `json:"rate"`
			} `json:"deliver_get_details"`
		} `json:"message_stats"`
	}
	if err := r.opt.Do(ctx, r.env, "GET", "/api/queues/"+r.vhost(), nil, &raw); err != nil {
		return nil, err
	}
	out := make([]core.Queue, 0, len(raw))
	for _, q := range raw {
		out = append(out, core.Queue{Name: q.Name, Messages: q.Messages, Ready: q.MessagesReady, Unacked: q.MessagesUnacknowledged,
			Consumers: q.Consumers, InRate: q.MessageStats.PublishDetails.Rate, OutRate: q.MessageStats.DeliverGetDetails.Rate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Rabbit) Purge(ctx context.Context, queue string) error {
	return r.opt.Do(ctx, r.env, "DELETE", "/api/queues/"+r.vhost()+"/"+url.PathEscape(queue)+"/contents", nil, nil)
}

// Publish sends body to target: "exchange/routing-key", or a queue name through the default exchange.
func (r *Rabbit) Publish(ctx context.Context, target string, body []byte) error {
	exchange, key, ok := strings.Cut(target, "/")
	if !ok {
		exchange, key = "amq.default", target
	}
	req := map[string]any{"properties": map[string]any{}, "routing_key": key, "payload": base64.StdEncoding.EncodeToString(body), "payload_encoding": "base64"}
	var resp struct {
		Routed bool `json:"routed"`
	}
	if err := r.opt.Do(ctx, r.env, "POST", "/api/exchanges/"+r.vhost()+"/"+url.PathEscape(exchange)+"/publish", req, &resp); err != nil {
		return err
	}
	if !resp.Routed {
		return fmt.Errorf("published to %s but no queue was bound to it", target)
	}
	return nil
}

func (r *Rabbit) Ping(ctx context.Context) error {
	return r.opt.Do(ctx, r.env, "GET", "/api/overview", nil, nil)
}

func (r *Rabbit) QueryLanguage() string { return "rabbitmq" }

// RunQuery GETs a management API path, e.g. "queues", "exchanges", "connections"; JSON lists become tables.
func (r *Rabbit) RunQuery(ctx context.Context, q string) (core.Table, error) {
	path := "/api/" + strings.TrimPrefix(strings.TrimSpace(q), "/api/")
	path = strings.TrimSuffix(path, "/")
	var raw []map[string]any
	if err := r.opt.Do(ctx, r.env, "GET", path, nil, &raw); err != nil {
		return core.Table{}, err
	}
	cols := []string{"name", "vhost", "type", "state", "messages", "consumers", "durable"}
	present := map[string]bool{}
	for _, m := range raw {
		for _, c := range cols {
			if _, ok := m[c]; ok {
				present[c] = true
			}
		}
	}
	t := core.Table{}
	for _, c := range cols {
		if present[c] {
			t.Columns = append(t.Columns, c)
		}
	}
	for _, m := range raw {
		row := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			row[i] = fmt.Sprint(m[c])
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

func (r *Rabbit) Actions() []core.Action {
	return []core.Action{{Name: "purge-all", Mutate: true, Help: "purge every queue that has messages: purge-all [prefix]", Run: func(ctx context.Context, args []string, out io.Writer) error {
		qs, err := r.Queues(ctx)
		if err != nil {
			return err
		}
		for _, q := range qs {
			if q.Messages == 0 || len(args) > 0 && !strings.HasPrefix(q.Name, args[0]) {
				continue
			}
			if err := r.Purge(ctx, q.Name); err != nil {
				return err
			}
			fmt.Fprintf(out, "purged %-50s %s messages\n", q.Name, strconv.Itoa(q.Messages))
		}
		return nil
	}}}
}
