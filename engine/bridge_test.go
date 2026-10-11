package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/core"
)

func TestPluginServesAKind(t *testing.T) {
	t.Setenv("RIG_TRUST", "project")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pm"), 0o755)
	os.WriteFile(filepath.Join(dir, "pm/rig-plugin.yaml"), []byte(`name: pm
version: 0.1.0
implements: [metrics/1]
sources:
  metrics.instant: { run: "printf '[{\"series\":\"up\",\"labels\":{\"job\":\"api\"},\"value\":1}]'" }
  metrics.range: { run: "printf '[]'" }
`), 0o644)
	p := filepath.Join(dir, "rig.yaml")
	os.WriteFile(p, []byte(`project: t
environments:
  test: { runtime: { type: fake } }
plugins:
  pm: { path: ./pm }
components:
  m: { type: pm }
`), 0o644)
	a, err := Open(p, "test")
	if err != nil {
		t.Fatal(err)
	}
	if k, typ, err := a.Kind("m"); k != core.KindMetrics || typ != "pm" || err != nil {
		t.Fatalf("kind %q %q %v", k, typ, err)
	}
	if n := a.Names(core.KindMetrics); len(n) != 1 || n[0] != "m" {
		t.Fatalf("metrics components %v", n)
	}
	c, err := a.Component("m")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := c.(core.Metrics)
	if !ok {
		t.Fatalf("%T is not metrics", c)
	}
	s, err := m.Instant(context.Background(), "up")
	if err != nil || len(s) != 1 || s[0].Value != 1 || s[0].Labels["job"] != "api" {
		t.Fatalf("instant = %+v, %v", s, err)
	}
}
