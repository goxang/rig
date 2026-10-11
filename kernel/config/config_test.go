package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, d *Doc, path string) string {
	t.Helper()
	n := d.Get(path)
	if n == nil {
		return "<nil>"
	}
	return n.Value
}

func TestComposeLayers(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "conf/a.yaml"), "services:\n  api:\n    port: 1\n    cmd: a\n")
	write(t, filepath.Join(dir, "conf/b.yaml"), "services:\n  api:\n    port: 2\n")
	write(t, filepath.Join(dir, ".rig/local.yaml"), "services:\n  api:\n    cmd: mine\n")
	root := `include: [conf/*.yaml]
imports:
  - compose: docker-compose.yml
templates:
  go:
    lang: go
    dir: ./cmd/${name}
  svc:
    extends: go
    health: /healthz
defaults:
  services:
    owner: team
services:
  api:
    extends: svc
    dir: ./api
  worker:
    image: null
`
	file := filepath.Join(dir, "rig.yaml")
	imp := func(kind, path string, _ *yaml.Node) (*Doc, error) {
		return Parse([]byte("services:\n  worker:\n    image: w:1\n    port: 9\n  db:\n    image: pg\n"), path, "import "+kind)
	}
	d, err := Compose([]byte(root), file, Options{Import: imp})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"services.api.port":     "2",
		"services.api.cmd":      "mine",
		"services.api.lang":     "go",
		"services.api.dir":      "./api",
		"services.api.health":   "/healthz",
		"services.api.owner":    "team",
		"services.worker.port":  "9",
		"services.worker.dir":   "<nil>",
		"services.db.image":     "pg",
		"services.db.owner":     "team",
		"services.worker.image": "<nil>",
		"templates":             "<nil>",
		"services.api.extends":  "<nil>",
	} {
		if got := scalar(t, d, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if o, _ := d.Origin("services.api.cmd"); o.Layer != "local" {
		t.Errorf("cmd from %v, want local", o)
	}
	trail := d.Explain("services.api.port")
	if len(trail) != 2 || !strings.HasSuffix(trail[0].File, "a.yaml") || !strings.HasSuffix(trail[1].File, "b.yaml") {
		t.Errorf("port trail %v, want a.yaml then b.yaml", trail)
	}
}

func TestTemplateName(t *testing.T) {
	d, err := Compose([]byte("templates:\n  t:\n    dir: ./cmd/${name}\nservices:\n  api:\n    extends: t\n"), "/x/rig.yaml", Options{NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, d, "services.api.dir"); got != "./cmd/api" {
		t.Errorf("dir = %q", got)
	}
}

func TestTemplateLoop(t *testing.T) {
	_, err := Compose([]byte("templates:\n  a: {extends: b}\n  b: {extends: a}\nservices:\n  x: {extends: a}\n"), "/x/rig.yaml", Options{NoLocal: true})
	if err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("want a loop error, got %v", err)
	}
}

func TestDefaultsAreLowest(t *testing.T) {
	plugin, _ := Parse([]byte("ui:\n  theme: dark\n  refresh: 5s\n"), "plugin:x", "plugin")
	d, err := Compose([]byte("ui:\n  theme: light\n"), "/x/rig.yaml", Options{Defaults: []*Doc{plugin}, NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if scalar(t, d, "ui.theme") != "light" || scalar(t, d, "ui.refresh") != "5s" {
		t.Errorf("got %s %s", scalar(t, d, "ui.theme"), scalar(t, d, "ui.refresh"))
	}
}

func TestCache(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "src/a"), "1")
	k1, err := Key("compose", "1", filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: filepath.Join(dir, "cache")}
	if _, ok := c.Get(k1); ok {
		t.Fatal("cold cache hit")
	}
	frag, _ := Parse([]byte("services:\n  a: {port: 1}\n"), "x", "import")
	if err := c.Put(k1, frag.Root); err != nil {
		t.Fatal(err)
	}
	if n, ok := c.Get(k1); !ok || Value(Value(Value(n, "services"), "a"), "port").Value != "1" {
		t.Fatal("warm cache miss")
	}
	write(t, filepath.Join(dir, "src/a"), "2")
	if k2, _ := Key("compose", "1", filepath.Join(dir, "src")); k2 == k1 {
		t.Fatal("key ignores file content")
	}
}

func TestLoneNullStays(t *testing.T) {
	d, err := Compose([]byte("services:\n  redis:\n"), "/x/rig.yaml", Options{NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := d.Get("services.redis"); n == nil || !isNull(n) {
		t.Fatalf("redis = %v, want a null entry", n)
	}
}
