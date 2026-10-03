// Package redis is the cache adapter for Redis and protocol-compatible servers (Valkey, KeyDB, Dragonfly).
package redis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindCache, "redis", "Redis protocol (Redis, Valkey, KeyDB, Dragonfly)", New)
}

type Options struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	// Unsafe allows commands that wipe or reconfigure the server (FLUSHALL, CONFIG SET, ...) from queries.
	Unsafe bool `yaml:"unsafe"`
}

type Cache struct {
	opt Options
	env core.Env

	mu     sync.Mutex
	client *goredis.Client
}

func New(env core.Env, c *spec.Component) (any, error) {
	r := &Cache{env: env}
	return r, c.Decode(&r.opt)
}

func (r *Cache) conn(ctx context.Context) (*goredis.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client, nil
	}
	addr, err := r.env.Resolve(ctx, r.opt.Addr)
	if err != nil {
		return nil, err
	}
	r.client = goredis.NewClient(&goredis.Options{Addr: strings.TrimPrefix(addr, "redis://"), Password: r.opt.Password, DB: r.opt.DB})
	return r.client, nil
}

func (r *Cache) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

func (r *Cache) Ping(ctx context.Context) error {
	c, err := r.conn(ctx)
	if err != nil {
		return err
	}
	return c.Ping(ctx).Err()
}

var unsafe = map[string]bool{"FLUSHALL": true, "FLUSHDB": true, "CONFIG": true, "SHUTDOWN": true, "DEBUG": true, "MIGRATE": true, "REPLICAOF": true, "SLAVEOF": true}

func (r *Cache) Do(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if unsafe[strings.ToUpper(args[0])] && !r.opt.Unsafe {
		return "", fmt.Errorf("%s is blocked; set unsafe: true on the component to allow it", strings.ToUpper(args[0]))
	}
	c, err := r.conn(ctx)
	if err != nil {
		return "", err
	}
	in := make([]any, len(args))
	for i, a := range args {
		in[i] = a
	}
	v, err := c.Do(ctx, in...).Result()
	if err == goredis.Nil {
		return "(nil)", nil
	}
	if err != nil {
		return "", err
	}
	return render(v, ""), nil
}

func render(v any, indent string) string {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return "(empty)"
		}
		var b strings.Builder
		for i, e := range x {
			fmt.Fprintf(&b, "%s%d) %s\n", indent, i+1, render(e, indent+"   "))
		}
		return strings.TrimRight(b.String(), "\n")
	case map[any]any:
		var b strings.Builder
		for k, e := range x {
			fmt.Fprintf(&b, "%s%v: %s\n", indent, k, render(e, indent+"   "))
		}
		return strings.TrimRight(b.String(), "\n")
	case nil:
		return "(nil)"
	}
	return fmt.Sprint(v)
}

func (r *Cache) Info(ctx context.Context) (map[string]string, error) {
	c, err := r.conn(ctx)
	if err != nil {
		return nil, err
	}
	s, err := c.Info(ctx).Result()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r *Cache) QueryLanguage() string { return "redis" }

func (r *Cache) RunQuery(ctx context.Context, q string) (core.Table, error) {
	args, err := splitArgs(q)
	if err != nil {
		return core.Table{}, err
	}
	if len(args) == 1 && strings.EqualFold(args[0], "INFO") {
		info, err := r.Info(ctx)
		if err != nil {
			return core.Table{}, err
		}
		t := core.Table{Columns: []string{"key", "value"}}
		keys := make([]string, 0, len(info))
		for k := range info {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Rows = append(t.Rows, []string{k, info[k]})
		}
		return t, nil
	}
	out, err := r.Do(ctx, args...)
	if err != nil {
		return core.Table{}, err
	}
	t := core.Table{Columns: []string{"result"}}
	for _, l := range strings.Split(out, "\n") {
		t.Rows = append(t.Rows, []string{l})
	}
	return t, nil
}

// splitArgs splits like a shell: spaces separate, quotes group.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, c := range s {
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(c)
		case c == '"' || c == '\'':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(c)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}
