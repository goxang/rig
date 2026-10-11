package spec

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/goxang/rig/kernel/config"

	"gopkg.in/yaml.v3"
)

// Redundancy is a rig.yaml value that its imports, includes or plugins already give.
type Redundancy struct {
	Path string
	Line int
	From config.Origin
}

// layering are the top-level keys that pull in the layers below rig.yaml.
var layering = []string{"version", "project", "imports", "include", "plugins"}

// Redundant lists rig.yaml's values equal to what the layers below it give, and rig.yaml without them.
func Redundant(file string) ([]Redundancy, []byte, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, raw, nil
	}
	root := doc.Content[0]
	base := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range layering {
		if v := config.Value(root, k); v != nil {
			base.Content = append(base.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, v)
		}
	}
	braw, err := yaml.Marshal(base)
	if err != nil {
		return nil, nil, err
	}
	dir := filepath.Dir(file)
	opts := config.Options{NoLocal: true, Import: func(kind, path string, _ *yaml.Node) (*config.Doc, error) {
		return importFragment(kind, path, dir, root)
	}}
	if PluginDefaults != nil {
		var pl struct {
			Plugins map[string]*Plugin `yaml:"plugins"`
		}
		_ = root.Decode(&pl)
		opts.Defaults = PluginDefaults(dir, pl.Plugins)
	}
	below, err := config.Compose(braw, file, opts)
	if err != nil {
		return nil, nil, err
	}
	var out []Redundancy
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if path == "" && slices.Contains(layering, k.Value) {
				continue
			}
			p := k.Value
			if path != "" {
				p = path + "." + k.Value
			}
			b := below.Get(p)
			if b == nil {
				continue
			}
			if same(v, b) {
				o, _ := below.Origin(p)
				out = append(out, Redundancy{Path: p, Line: k.Line, From: o})
				n.Content = slices.Delete(n.Content, i, i+2)
				i -= 2
				continue
			}
			if v.Kind == yaml.MappingNode && b.Kind == yaml.MappingNode {
				walk(v, p)
			}
		}
	}
	walk(root, "")
	if len(out) == 0 {
		return nil, raw, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, err
	}
	return out, buf.Bytes(), enc.Close()
}

func same(a, b *yaml.Node) bool {
	var x, y any
	return a.Decode(&x) == nil && b.Decode(&y) == nil && reflect.DeepEqual(x, y)
}
