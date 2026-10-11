package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedundant(t *testing.T) {
	RegisterImporter("lintfake", func(string, string) (map[string]*Service, error) {
		return map[string]*Service{"api": {Image: "api:1", Ports: map[string]int{"http": 8080}}}, nil
	})
	dir := t.TempDir()
	f := filepath.Join(dir, "rig.yaml")
	os.WriteFile(f, []byte(`project: p
imports: [{lintfake: x}]
services:
  api:
    image: api:1 # same
    ports: {http: 8080}
    groups: [web]
  db: {image: postgres}
`), 0o644)
	rs, fixed, err := Redundant(f)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range rs {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, " ") != "services.api.image services.api.ports" {
		t.Fatalf("redundant %v", paths)
	}
	if s := string(fixed); strings.Contains(s, "api:1") || !strings.Contains(s, "groups: [web]") || !strings.Contains(s, "postgres") {
		t.Fatalf("fixed\n%s", s)
	}
}
