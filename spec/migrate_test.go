package spec

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rig.yaml")
	if err := os.WriteFile(file, []byte("version: 0\nproject: p\nname_was: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	saved := migrations
	defer func() { migrations = saved }()
	migrations = []Migration{{From: 0, Apply: func(root *yaml.Node) error {
		for i := range root.Content {
			if root.Content[i].Value == "name_was" {
				root.Content[i].Value = "renamed"
			}
		}
		return nil
	}}}

	from, changed, err := Migrate(file)
	if err != nil {
		t.Fatal(err)
	}
	if from != 0 || !changed {
		t.Fatalf("from=%d changed=%v, want 0 true", from, changed)
	}
	if v := versionOf(load(t, file)); v != CurrentVersion {
		t.Fatalf("version = %d, want %d", v, CurrentVersion)
	}
	var p Project
	raw, _ := os.ReadFile(file)
	if err := yaml.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Version != CurrentVersion {
		t.Fatalf("decoded version = %d, want %d", p.Version, CurrentVersion)
	}

	from, changed, err = Migrate(file)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("second Migrate should be a no-op, from=%d", from)
	}
}

func load(t *testing.T, file string) *yaml.Node {
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}
