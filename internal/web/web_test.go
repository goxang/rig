package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func TestRoutes(t *testing.T) {
	srv := httptest.NewServer((&Handler{Token: "secret-token"}).Routes())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/")
	if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("index: %v %v", err, res)
	}
	for _, auth := range []string{"", "Bearer nope"} {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/engine/catalog", nil)
		req.Header.Set("Authorization", auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("auth %q: %v %v", auth, err, res)
		}
	}
}

// stubRuntime satisfies the runtime kind; the secret check never reaches its methods.
type stubRuntime struct{ core.Runtime }

func init() {
	plugin.Register(core.KindRuntime, "webtest", "test runtime", func(core.Env, *spec.Component) (any, error) { return stubRuntime{}, nil })
}

func TestSecretOnlyForAskedNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rig.yaml")
	os.WriteFile(file, []byte("project: w\nenvironments:\n  dev: { runtime: webtest }\nservices:\n  api: { run: { command: [sleep, \"60\"] } }\n"), 0o644)
	h := &Handler{File: file, Token: "t", Open: engine.Open}
	defer h.Close()
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/secret", strings.NewReader(`{"name":"HOME_TOKEN","value":"x"}`))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a secret no view asks for was written: %v %v", err, res)
	}
}

func TestUsedPlugins(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rig.yaml")
	os.WriteFile(file, []byte(`project: u
plugins: { jira: {}, slack: { off: true } }
environments:
  dev: { runtime: local }
components:
  cache: { type: redis }
services:
  api: { build: { go: ./cmd/api }, run: { command: [api] } }
`), 0o644)
	p, _, err := spec.Load(file, "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(used(p), " ")
	if got != "delve go goland jira local redis" {
		t.Fatalf("used: %s", got)
	}
}
