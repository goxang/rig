package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Importer turns another tool's service file into rig services; paths in them are relative to dir,
// the project's directory.
type Importer func(path, dir string) (map[string]*Service, error)

var importers = map[string]Importer{"godev": importGodev}

func RegisterImporter(name string, f Importer) { importers[name] = f }

func Importers() []string { return keys(importers) }

// applyImports adds imported services; a service defined in rig.yaml wins over an imported one.
func (p *Project) applyImports() error {
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
				if _, ok := p.Services[n]; !ok {
					p.Services[n] = s
				}
			}
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
