// Package consul is the KV adapter for Consul's HTTP API.
package consul

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/httpx"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindKV, "consul", "Consul KV over HTTP", New)
}

type KV struct {
	ep  httpx.Endpoint
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	k := &KV{env: env}
	return k, c.Decode(&k.ep)
}

func path(key string) string {
	parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/v1/kv/" + strings.Join(parts, "/")
}

func (k *KV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var r []struct {
		Value string `json:"Value"`
	}
	err := k.ep.Do(ctx, k.env, "GET", path(key), nil, &r)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(r) == 0 {
		return nil, false, nil
	}
	v, err := base64.StdEncoding.DecodeString(r[0].Value)
	return v, true, err
}

func (k *KV) Put(ctx context.Context, key string, value []byte) error {
	var ok bool
	if err := k.ep.Do(ctx, k.env, "PUT", path(key), value, &ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("consul refused the write")
	}
	return nil
}

func (k *KV) Delete(ctx context.Context, key string) error {
	return k.ep.Do(ctx, k.env, "DELETE", path(key), nil, nil)
}

func (k *KV) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := k.ep.Do(ctx, k.env, "GET", path(prefix)+"?keys", nil, &keys)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil, nil
	}
	return keys, err
}

func (k *KV) Ping(ctx context.Context) error {
	return k.ep.Do(ctx, k.env, "GET", "/v1/status/leader", nil, nil)
}

func (k *KV) QueryLanguage() string { return "kv" }

// RunQuery lists keys under a prefix ending in "/", or prints one key's value.
func (k *KV) RunQuery(ctx context.Context, q string) (core.Table, error) {
	q = strings.TrimSpace(q)
	if q == "" || strings.HasSuffix(q, "/") {
		keys, err := k.List(ctx, q)
		t := core.Table{Columns: []string{"key"}}
		for _, key := range keys {
			t.Rows = append(t.Rows, []string{key})
		}
		return t, err
	}
	v, ok, err := k.Get(ctx, q)
	if err != nil {
		return core.Table{}, err
	}
	if !ok {
		return core.Table{Note: q + " not found"}, nil
	}
	t := core.Table{Columns: []string{q}}
	for _, l := range strings.Split(string(v), "\n") {
		t.Rows = append(t.Rows, []string{l})
	}
	return t, nil
}
