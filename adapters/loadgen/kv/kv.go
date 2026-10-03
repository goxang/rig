// Package kv drives load generators that run as services and read their rate from a key-value store:
// the rate is written to a key (or one JSON field of it), start and stop scale the generator services,
// and counters come from metrics queries when configured.
package kv

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindLoad, "kv", "generator services whose rate lives in a KV key (e.g. Consul); start/stop scales them", New)
}

type Options struct {
	Store    string   `yaml:"store"` // the kv component
	Key      string   `yaml:"key"`
	Field    string   `yaml:"field"` // JSON field holding the rate, dotted for nested (a.b); empty: the whole value is the number
	Services []string `yaml:"services"`
	Replicas int      `yaml:"replicas"`
	Metrics  struct {
		Source  string `yaml:"source"`
		Sent    string `yaml:"sent"`
		Failed  string `yaml:"failed"`
		Latency string `yaml:"latency_p99"` // seconds
	} `yaml:"metrics"`
}

type Gen struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	g := &Gen{env: env}
	if err := c.Decode(&g.opt); err != nil {
		return nil, err
	}
	if g.opt.Store == "" || g.opt.Key == "" {
		return nil, fmt.Errorf("kv load generator needs store and key")
	}
	if g.opt.Replicas == 0 {
		g.opt.Replicas = 1
	}
	return g, nil
}

func (g *Gen) store() (core.KV, error) {
	v, err := g.env.Component(g.opt.Store)
	if err != nil {
		return nil, err
	}
	kv, ok := v.(core.KV)
	if !ok {
		return nil, fmt.Errorf("%s is not a kv component", g.opt.Store)
	}
	return kv, nil
}

func (g *Gen) scale(ctx context.Context, n int) error {
	for _, name := range g.opt.Services {
		s, ok := g.env.Project().Services[name]
		if !ok {
			return fmt.Errorf("no service %q", name)
		}
		rt := g.env.Runtime()
		if n > 0 {
			// Start deploys a generator that was never deployed; Scale then sets the count
			if err := rt.Start(ctx, s); err != nil {
				return err
			}
		}
		if err := rt.Scale(ctx, s, n); err != nil {
			return err
		}
	}
	return nil
}

func (g *Gen) Start(ctx context.Context) error { return g.scale(ctx, g.opt.Replicas) }
func (g *Gen) Stop(ctx context.Context) error  { return g.scale(ctx, 0) }

func (g *Gen) read(ctx context.Context) (map[string]any, float64, error) {
	kv, err := g.store()
	if err != nil {
		return nil, 0, err
	}
	raw, ok, err := kv.Get(ctx, g.opt.Key)
	if err != nil || !ok {
		return map[string]any{}, 0, err
	}
	if g.opt.Field == "" {
		f, _ := strconv.ParseFloat(string(raw), 64)
		return nil, f, nil
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("%s is not JSON: %w", g.opt.Key, err)
	}
	parent, leaf := fieldParent(doc, g.opt.Field, false)
	f, _ := parent[leaf].(float64)
	return doc, f, nil
}

// SetRate rewrites only the rate field, keeping the rest of the document.
func (g *Gen) SetRate(ctx context.Context, rps float64) error {
	kv, err := g.store()
	if err != nil {
		return err
	}
	if g.opt.Field == "" {
		return kv.Put(ctx, g.opt.Key, []byte(strconv.FormatFloat(rps, 'f', -1, 64)))
	}
	doc, _, err := g.read(ctx)
	if err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	parent, leaf := fieldParent(doc, g.opt.Field, true)
	parent[leaf] = rps
	raw, _ := json.MarshalIndent(doc, "", "  ")
	return kv.Put(ctx, g.opt.Key, raw)
}

func (g *Gen) Status(ctx context.Context) (core.LoadStatus, error) {
	_, rate, err := g.read(ctx)
	if err != nil {
		return core.LoadStatus{}, err
	}
	st := core.LoadStatus{Rate: rate, Extra: map[string]string{"key": g.opt.Key}}
	for _, name := range g.opt.Services {
		if s, ok := g.env.Project().Services[name]; ok {
			if ss, err := g.env.Runtime().Status(ctx, s); err == nil && ss.Ready > 0 {
				st.Running = true
			}
		}
	}
	g.counters(ctx, &st)
	return st, nil
}

func (g *Gen) counters(ctx context.Context, st *core.LoadStatus) {
	m := g.opt.Metrics
	if m.Source == "" {
		return
	}
	v, err := g.env.Component(m.Source)
	if err != nil {
		return
	}
	met, ok := v.(core.Metrics)
	if !ok {
		return
	}
	one := func(q string) float64 {
		if q == "" {
			return 0
		}
		ss, err := met.Instant(ctx, q)
		if err != nil || len(ss) == 0 {
			return 0
		}
		return ss[0].Value
	}
	st.Sent = int64(one(m.Sent))
	st.Failed = int64(one(m.Failed))
	st.Latency.P99 = time.Duration(one(m.Latency) * float64(time.Second))
}

// fieldParent walks a dotted path to the object holding its last part, creating objects when create is set.
func fieldParent(doc map[string]any, path string, create bool) (map[string]any, string) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := doc[p].(map[string]any)
		if !ok {
			if !create {
				return map[string]any{}, ""
			}
			next = map[string]any{}
			doc[p] = next
		}
		doc = next
	}
	return doc, parts[len(parts)-1]
}
