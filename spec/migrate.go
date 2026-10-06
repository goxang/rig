package spec

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the rig.yaml schema version this build understands. Bump it and add a
// Migration below whenever a change needs an old rig.yaml rewritten to keep loading.
const CurrentVersion = 1

// Migration rewrites a project file's root mapping from one version to the next; From is the
// version it expects going in, and it must leave "version" one higher.
type Migration struct {
	From  int
	Apply func(root *yaml.Node) error
}

// migrations runs in order; none exist yet because the schema hasn't changed since version 1.
var migrations []Migration

// Migrate brings a rig.yaml up to CurrentVersion in place, keeping comments and formatting.
// It reports the version found and whether the file was rewritten.
func Migrate(file string) (from int, changed bool, err error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return 0, false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return 0, false, fmt.Errorf("%s: %w", file, err)
	}
	if len(doc.Content) == 0 {
		return 0, false, fmt.Errorf("%s: empty file", file)
	}
	root := doc.Content[0]
	from = versionOf(root)
	version := from
	for version < CurrentVersion {
		m := migrationFrom(version)
		if m == nil {
			return from, false, fmt.Errorf("%s: no migration from version %d to %d", file, version, version+1)
		}
		if err := m.Apply(root); err != nil {
			return from, false, fmt.Errorf("%s: migrating version %d to %d: %w", file, version, version+1, err)
		}
		setVersion(root, version+1)
		version++
	}
	if version == from {
		return from, false, nil
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return from, false, err
	}
	if err := enc.Close(); err != nil {
		return from, false, err
	}
	if err := os.WriteFile(file, out.Bytes(), 0o644); err != nil {
		return from, false, err
	}
	return from, true, nil
}

func migrationFrom(v int) *Migration {
	for i := range migrations {
		if migrations[i].From == v {
			return &migrations[i]
		}
	}
	return nil
}

func versionOf(root *yaml.Node) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "version" {
			var v int
			_ = root.Content[i+1].Decode(&v)
			return v
		}
	}
	return 0
}

func setVersion(root *yaml.Node, v int) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "version" {
			root.Content[i+1].Value = fmt.Sprint(v)
			root.Content[i+1].Tag = "!!int"
			return
		}
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Value: "version"}
	val := &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(v), Tag: "!!int"}
	root.Content = append([]*yaml.Node{key, val}, root.Content...)
}
