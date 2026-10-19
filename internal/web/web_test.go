package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goxang/rig/engine"
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

func TestSecretOnlyForAskedNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rig.yaml")
	os.WriteFile(file, []byte("project: w\nenvironments:\n  dev: { runtime: local }\nservices:\n  api: { run: { command: [sleep, \"60\"] } }\n"), 0o644)
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
