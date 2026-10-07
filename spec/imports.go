package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Importer turns another tool's service file into rig services; paths in them are relative to dir,
// the project's directory.
type Importer func(path, dir string) (map[string]*Service, error)

var importers = map[string]Importer{"godev": importGodev}

func RegisterImporter(name string, f Importer) { importers[name] = f }

func Importers() []string { return keys(importers) }

// applyImports adds imported services. A service rig.yaml also defines is the imported one with
// rig.yaml's keys merged over it, so rig.yaml only carries what the other tool's file has no word for.
func (p *Project) applyImports(root *yaml.Node) error {
	own := mappingValue(root, "services")
	claimed := map[string]string{}
	for n, s := range p.Services {
		claimed[s.workload(n)] = n
	}
	for _, imp := range p.Imports {
		for kind, path := range imp {
			f, ok := importers[kind]
			if !ok {
				return fmt.Errorf("unknown import %q; have %v", kind, Importers())
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(p.Dir, path)
			}
			svcs, err := f(path, p.Dir)
			if err != nil {
				return fmt.Errorf("import %s %s: %w", kind, path, err)
			}
			if p.Services == nil {
				p.Services = map[string]*Service{}
			}
			for n, s := range svcs {
				if by, ok := claimed[s.workload(n)]; ok && by != n {
					continue // rig.yaml names this workload's service differently
				}
				if _, ok := p.Services[n]; !ok {
					p.Services[n] = s
					continue
				}
				patch := mappingValue(own, n)
				if patch == nil {
					continue
				}
				var base yaml.Node
				if err := base.Encode(s); err != nil {
					return err
				}
				var out Service
				if err := mergeNodes(&base, patch).Decode(&out); err != nil {
					return fmt.Errorf("service %s over its import from %s: %w", n, path, err)
				}
				p.Services[n] = &out
			}
		}
	}
	return nil
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

func importGodev(path, _ string) (map[string]*Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Services map[string]struct {
			Path    string            `yaml:"path"`
			Command []string          `yaml:"command"`
			Args    []string          `yaml:"args"`
			Env     map[string]string `yaml:"env"`
			Group   []string          `yaml:"group"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	out := map[string]*Service{}
	for n, g := range f.Services {
		s := &Service{Name: n, Groups: g.Group, Env: g.Env, Role: RoleApp}
		switch {
		case len(g.Command) > 0:
			s.Run = &Run{Command: g.Command}
		case g.Path != "":
			s.Build = &Build{Go: g.Path}
			s.Run = &Run{Args: g.Args}
		}
		sort.Strings(s.Groups)
		out[n] = s
	}
	return out, nil
}
