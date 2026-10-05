package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWithOverridesBeatFileVars(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rig.yaml")
	os.WriteFile(f, []byte(`project: p
environments:
  e:
    vars: { DB: old }
    runtime: { type: local }
queries:
  q: { source: db, query: "@${DB} SELECT 1" }
`), 0o644)
	p, _, err := LoadWith(f, "e", map[string]string{"DB": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Queries["q"].Query; got != "@new SELECT 1" {
		t.Fatalf("query = %q", got)
	}
	p, _, _ = Load(f, "e")
	if got := p.Queries["q"].Query; got != "@old SELECT 1" {
		t.Fatalf("without overrides: %q", got)
	}
}
