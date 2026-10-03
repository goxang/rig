// Package spec is the rig.yaml schema: services, environments and the components that serve them.
package spec

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	RoleApp   = "app"
	RoleInfra = "infra"
	RoleLoad  = "load"
)

type Project struct {
	Version      int                     `yaml:"version"`
	Name         string                  `yaml:"project"`
	Default      string                  `yaml:"default"`
	Imports      []Import                `yaml:"imports"`
	Vars         map[string]string       `yaml:"vars"`
	Services     map[string]*Service     `yaml:"services"`
	Environments map[string]*Environment `yaml:"environments"`
	Components   map[string]*Component   `yaml:"components"`
	Dashboards   map[string][]Panel      `yaml:"dashboards"`
	Manifests    []string                `yaml:"manifests"`
	// Tasks are named lists of shell steps: `rig task <name>`.
	Tasks map[string][]string `yaml:"tasks"`

	// Dir is where the project file lives; relative paths in it resolve from here.
	Dir  string `yaml:"-"`
	File string `yaml:"-"`
}

// Import pulls services from another tool's file, e.g. {godev: .godev.yaml}.
type Import map[string]string

type Service struct {
	Name      string            `yaml:"-"`
	Role      string            `yaml:"role"`
	Groups    []string          `yaml:"groups"`
	DependsOn []string          `yaml:"depends_on"`
	Ports     map[string]int    `yaml:"ports"`
	Env       map[string]string `yaml:"env"`
	Image     string            `yaml:"image"`
	Replicas  int               `yaml:"replicas"`
	Build     *Build            `yaml:"build"`
	Run       *Run              `yaml:"run"`
	Health    *Probe            `yaml:"health"`
	Metrics   *Probe            `yaml:"metrics"`
	Pprof     *Probe            `yaml:"pprof"`

	// Sections owned by adapters (local:, docker:, k8s:, ...), decoded by the adapter that reads them.
	Sections map[string]yaml.Node `yaml:",inline"`
}

type Build struct {
	Go         string            `yaml:"go"`
	Dockerfile string            `yaml:"dockerfile"`
	Context    string            `yaml:"context"`
	Command    []string          `yaml:"command"`
	Args       map[string]string `yaml:"args"`
}

type Run struct {
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
	Dir     string   `yaml:"dir"`
}

// Probe points at an HTTP path on a named or numbered port.
type Probe struct {
	Port string `yaml:"port"`
	Path string `yaml:"path"`
}

type Environment struct {
	Name        string                `yaml:"-"`
	Description string                `yaml:"description"`
	Runtime     *Component            `yaml:"runtime"`
	Protected   bool                  `yaml:"protected"`
	Vars        map[string]string     `yaml:"vars"`
	Only        []string              `yaml:"only"`
	Components  map[string]*Component `yaml:"components"`
	Services    map[string]yaml.Node  `yaml:"services"`
	// Tasks replace the project's tasks of the same name in this environment.
	Tasks map[string][]string `yaml:"tasks"`
}

// Component is one adapter instance: Kind picks the interface, Type the implementation,
// and the rest of the mapping is the adapter's own options.
type Component struct {
	Name string `yaml:"-"`
	Kind string `yaml:"kind"`
	Type string `yaml:"type"`
	Node yaml.Node
}

func (c *Component) UnmarshalYAML(n *yaml.Node) error {
	var head struct {
		Kind string `yaml:"kind"`
		Type string `yaml:"type"`
	}
	if n.Kind == yaml.ScalarNode {
		head.Type = n.Value
	} else if err := n.Decode(&head); err != nil {
		return err
	}
	c.Kind, c.Type, c.Node = head.Kind, head.Type, *n
	return nil
}

// Decode fills an adapter's options struct from the component mapping.
func (c *Component) Decode(v any) error {
	if c == nil || c.Node.Kind != yaml.MappingNode {
		return nil
	}
	if err := c.Node.Decode(v); err != nil {
		return fmt.Errorf("%s (%s): %w", c.Name, c.Type, err)
	}
	return nil
}

type Panel struct {
	Title  string `yaml:"title"`
	Query  string `yaml:"query"`
	Unit   string `yaml:"unit"`
	Source string `yaml:"source"`
	Legend string `yaml:"legend"`
	Kind   string `yaml:"kind"` // line (default), stat, bar
}

// Section decodes an adapter-owned section of the service, false when absent.
func (s *Service) Section(name string, v any) (bool, error) {
	n, ok := s.Sections[name]
	if !ok {
		return false, nil
	}
	if err := n.Decode(v); err != nil {
		return true, fmt.Errorf("service %s, %s: %w", s.Name, name, err)
	}
	return true, nil
}

func (s *Service) InGroup(g string) bool {
	for _, x := range s.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// PortNumber resolves a port by name or number; 0 when unknown.
func (s *Service) PortNumber(p string) int {
	if n, ok := s.Ports[p]; ok {
		return n
	}
	var n int
	fmt.Sscanf(p, "%d", &n)
	return n
}

func (s *Service) FirstPort() int {
	names := make([]string, 0, len(s.Ports))
	for k := range s.Ports {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, pref := range []string{"http", "grpc"} {
		if p, ok := s.Ports[pref]; ok {
			return p
		}
	}
	if len(names) == 0 {
		return 0
	}
	return s.Ports[names[0]]
}

func (p *Project) ServiceNames() []string {
	names := make([]string, 0, len(p.Services))
	for n := range p.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (p *Project) EnvironmentNames() []string {
	names := make([]string, 0, len(p.Environments))
	for n := range p.Environments {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
