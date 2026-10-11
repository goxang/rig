package spec

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goxang/rig/kernel/config"

	"gopkg.in/yaml.v3"
)

// Importer turns another tool's service file into rig services; paths in them are relative to dir,
// the project's directory.
type Importer func(path, dir string) (map[string]*Service, error)

var importers = map[string]Importer{}

func RegisterImporter(name string, f Importer) { importers[name] = f }

func Importers() []string { return keys(importers) }

// ErrNoImporter is an imports: kind no installed plugin reads: the import is skipped, not fatal,
// so a checkout opens before its plugins are installed.
var ErrNoImporter = errors.New("no installed plugin imports it")

// PluginImport runs the installed plugin importer of that name on path, returning its rig.yaml
// fragment; the plugin host sets it.
var PluginImport func(dir, name, path string) (map[string]any, error)

// importFragment runs one `imports:` entry as a configuration layer below rig.yaml, so a service
// rig.yaml also defines is the imported one with rig.yaml's keys over it. A workload a rig.yaml
// service already claims under another name is left out.
func importFragment(kind, path, dir string, root *yaml.Node) (*config.Doc, error) {
	path = os.ExpandEnv(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	frag, svcs, err := runImporter(kind, path, dir)
	if err != nil {
		return nil, fmt.Errorf("import %s %s: %w", kind, path, err)
	}
	var own map[string]*Service
	if n := config.Value(root, "services"); n != nil {
		_ = n.Decode(&own)
	}
	claimed := map[string]string{}
	for n, s := range own {
		if s != nil {
			claimed[s.workload(n)] = n
		}
	}
	out := map[string]*Service{}
	for n, s := range svcs {
		if by, ok := claimed[s.workload(n)]; ok && by != n {
			continue
		}
		out[n] = s
	}
	frag["services"] = out
	node, err := toNode(frag)
	if err != nil {
		return nil, err
	}
	prune(node)
	return config.FromNode(node, path, "import "+kind), nil
}

// toNode builds the node of a JSON-like fragment itself: yaml.v3's Node.Encode re-parses what it
// emits and fails on a string starting with a newline inside a list (a multi-line Grafana query).
func toNode(v any) (*yaml.Node, error) {
	switch v := v.(type) {
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			c, err := toNode(v[k])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, c)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range v {
			c, err := toNode(e)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, c)
		}
		return n, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	}
	var n yaml.Node
	return &n, n.Encode(v)
}

// runImporter splits what an importer returns into its services and the rest of its fragment.
func runImporter(kind, path, dir string) (map[string]any, map[string]*Service, error) {
	if f, ok := importers[kind]; ok {
		svcs, err := f(path, dir)
		return map[string]any{}, svcs, err
	}
	if PluginImport == nil {
		return nil, nil, fmt.Errorf("no importer %q", kind)
	}
	frag, err := PluginImport(dir, kind, path)
	if err != nil {
		return nil, nil, err
	}
	var svcs map[string]*Service
	if raw, ok := frag["services"]; ok {
		b, err := yaml.Marshal(raw)
		if err != nil {
			return nil, nil, err
		}
		if err := yaml.Unmarshal(b, &svcs); err != nil {
			return nil, nil, fmt.Errorf("the fragment's services: %w", err)
		}
	}
	if frag == nil {
		frag = map[string]any{}
	}
	return frag, svcs, nil
}

// prune drops the zero values encoding a Service writes, so they cannot hide a template's keys.
func prune(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.MappingNode:
		out := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			if !prune(n.Content[i+1]) {
				out = append(out, n.Content[i], n.Content[i+1])
			}
		}
		n.Content = out
		return len(out) == 0
	case yaml.SequenceNode:
		for _, c := range n.Content {
			prune(c)
		}
		return len(n.Content) == 0
	case yaml.ScalarNode:
		return n.Tag == "!!null" || n.Value == "" && n.Style == 0 || n.Tag == "!!bool" && n.Value == "false" || n.Tag == "!!int" && n.Value == "0"
	}
	return false
}

// workload is the Kubernetes workload service name stands for, kind/name in lower case: k8s.workload,
// else the Deployment of that name.
func (s *Service) workload(name string) string {
	var sec struct {
		Workload string `yaml:"workload"`
	}
	_, _ = s.Section("k8s", &sec)
	w := strings.ToLower(sec.Workload)
	if w == "" {
		w = name
	}
	if !strings.Contains(w, "/") {
		w = "deployment/" + w
	}
	return w
}

// ImportPaths are the absolute paths of the project's imports of these kinds, in file order.
func (p *Project) ImportPaths(kinds ...string) []string {
	var out []string
	for _, imp := range p.Imports {
		for _, k := range kinds {
			if path, ok := imp[k]; ok {
				if !filepath.IsAbs(path) {
					path = filepath.Join(p.Dir, path)
				}
				out = append(out, path)
			}
		}
	}
	return out
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
