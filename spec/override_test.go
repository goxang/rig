package spec

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestExpandedPluginYAML(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rig.yaml")
	os.WriteFile(f, []byte("project: p\nvars: { DB: main }\nenvironments:\n  e: { runtime: { type: local } }\n"), 0o644)
	p, _, err := Load(f, "e")
	if err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	_ = yaml.Unmarshal([]byte(`{query: "@${DB} SELECT 1"}`), &n)
	var v struct{ Query string }
	if err := p.Expanded(&n).Decode(&v); err != nil || v.Query != "@main SELECT 1" {
		t.Fatalf("query = %q, %v", v.Query, err)
	}
	if n.Content[0].Content[1].Value != "@${DB} SELECT 1" {
		t.Fatal("the original changed")
	}
}
