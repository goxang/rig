package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAffected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rig.yaml")
	body := `
project: a
environments: { dev: { runtime: { type: fake } } }
services:
  api: { build: { dockerfile: api/Dockerfile }, watch: { paths: [api, lib] } }
  web: { build: { context: web } }
  load: { role: load, run: { command: [k6] } }
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Open(p, "dev")
	if err != nil {
		t.Fatal(err)
	}
	got := a.Affected(context.Background(), []string{"lib/x.go", "docs/readme.md"})
	if !slices.Equal(got, []string{"api"}) {
		t.Fatalf("affected %v", got)
	}
	if got := a.Affected(context.Background(), []string{"web/index.html", "webby/x"}); !slices.Equal(got, []string{"web"}) {
		t.Fatalf("affected %v", got)
	}
}
