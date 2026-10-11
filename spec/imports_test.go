package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportKeepsMultilineStrings(t *testing.T) {
	old := PluginImport
	defer func() { PluginImport = old }()
	query := "\nsum(rate(a[5m])) by (cmd)  /sum(b) by (cmd)"
	PluginImport = func(dir, name, path string) (map[string]any, error) {
		return map[string]any{"dashboards": map[string]any{"redis": map[string]any{"panels": []any{
			map[string]any{"title": "t", "query": query}}}}}, nil
	}
	d := t.TempDir()
	f := filepath.Join(d, "rig.yaml")
	if err := os.WriteFile(f, []byte("version: 1\nproject: x\nimports:\n  - grafana: r.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, err := Load(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Dashboards["redis"].Panels[0].Query; got != query {
		t.Fatalf("query = %q", got)
	}
}
