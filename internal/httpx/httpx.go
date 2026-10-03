// Package httpx is the HTTP plumbing adapters share: base URLs from svc:// addresses, JSON calls with auth.
package httpx

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
)

var Default = &http.Client{Timeout: 30 * time.Second}

// Endpoint is an HTTP API an adapter talks to, given as a URL or svc://service:port.
type Endpoint struct {
	Addr     string            `yaml:"addr"`
	User     string            `yaml:"user"`
	Password string            `yaml:"password"`
	Token    string            `yaml:"token"`
	Headers  map[string]string `yaml:"headers"`
}

// Base resolves the address through env and returns it with a scheme and without a trailing slash.
func (e Endpoint) Base(ctx context.Context, env core.Env) (string, error) {
	if e.Addr == "" {
		return "", fmt.Errorf("no addr")
	}
	scheme := "http://"
	addr := e.Addr
	if rest, ok := strings.CutPrefix(addr, "svc+https://"); ok {
		scheme, addr = "https://", "svc://"+rest
	}
	a, err := env.Resolve(ctx, addr)
	if err != nil {
		return "", err
	}
	if !strings.Contains(a, "://") {
		a = scheme + a
	}
	return strings.TrimRight(a, "/"), nil
}

// Do sends a request to base+path and decodes a JSON answer into out (when out is not nil).
func (e Endpoint) Do(ctx context.Context, env core.Env, method, path string, body any, out any) error {
	base, err := e.Base(ctx, env)
	if err != nil {
		return err
	}
	return e.DoURL(ctx, method, base+path, body, out)
}

func (e Endpoint) DoURL(ctx context.Context, method, url string, body any, out any) error {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.User != "" {
		req.SetBasicAuth(e.User, e.Password)
	}
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	for k, v := range e.Headers {
		req.Header.Set(k, v)
	}
	resp, err := Default.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, msg)
	}
	switch o := out.(type) {
	case nil:
	case *[]byte:
		*o = raw
	default:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: %w", method, url, err)
		}
	}
	return nil
}
