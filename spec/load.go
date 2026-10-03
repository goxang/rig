package spec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var FileNames = []string{"rig.yaml", "rig.yml", ".rig.yaml", ".rig.yml"}

var ErrNotFound = errors.New("no rig.yaml found in this directory or any parent (run `rig init`)")

// Find walks up from dir to the first project file; $RIG_FILE wins when set.
func Find(dir string) (string, error) {
	if f := os.Getenv("RIG_FILE"); f != "" {
		return filepath.Abs(f)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		for _, n := range FileNames {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotFound
		}
		dir = parent
	}
}

// Load reads the project and resolves it for one environment: variables expanded,
// the environment's service patches merged, its components layered over the shared ones.
// env "" picks $RIG_ENV, then the project's default, then the only environment.
func Load(file, env string) (*Project, *Environment, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	if len(root.Content) == 0 {
		return nil, nil, fmt.Errorf("%s: empty", file)
	}

	var head struct {
		Name         string            `yaml:"project"`
		Default      string            `yaml:"default"`
		Vars         map[string]string `yaml:"vars"`
		Secrets      map[string]Secret `yaml:"secrets"`
		Environments map[string]struct {
			Vars map[string]string `yaml:"vars"`
		} `yaml:"environments"`
	}
	if err := root.Decode(&head); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	env = pickEnv(env, head.Default, keys(head.Environments))
	if env != "" {
		if _, ok := head.Environments[env]; !ok {
			return nil, nil, fmt.Errorf("environment %q is not defined; have %s", env, strings.Join(keys(head.Environments), ", "))
		}
	}

	builtin := map[string]string{"env": env, "project": head.Name}
	stored, _ := LoadSecrets(head.Name)
	lookup := func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		if v, ok := stored[name]; ok {
			return v, true
		}
		if s, ok := head.Secrets[name]; ok && s.Default != "" {
			return s.Default, true
		}
		if v, ok := builtin[name]; ok {
			return v, true
		}
		if v, ok := head.Environments[env].Vars[name]; ok {
			return Expand(v, osLookup), true
		}
		if v, ok := head.Vars[name]; ok {
			return Expand(v, osLookup), true
		}
		return "", false
	}
	expandNode(&root, lookup)

	p := &Project{File: file, Dir: filepath.Dir(file)}
	if err := root.Decode(p); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := p.applyImports(); err != nil {
		return nil, nil, err
	}
	if p.Services == nil {
		p.Services = map[string]*Service{}
	}
	if p.Components == nil {
		p.Components = map[string]*Component{}
	}
	for n, c := range p.Components {
		c.Name = n
	}

	for n, s := range p.Services {
		s.Name = n
		if s.Role == "" {
			s.Role = RoleApp
		}
	}
	var e *Environment
	if env != "" {
		e = p.Environments[env]
		e.Name = env
		if err := p.applyEnvironment(e); err != nil {
			return nil, nil, err
		}
	}
	for n, s := range p.Services {
		s.Name = n
		if s.Role == "" {
			s.Role = RoleApp
		}
	}
	for n, e := range p.Environments {
		e.Name = n
	}
	for n, q := range p.Queries {
		q.Name = n
	}
	if e != nil {
		for n, q := range e.Queries {
			q.Name = n
		}
	}
	return p, e, p.validate()
}

func (p *Project) applyEnvironment(e *Environment) error {
	for name, patch := range e.Services {
		s, ok := p.Services[name]
		if !ok {
			return fmt.Errorf("environment %s patches unknown service %q", e.Name, name)
		}
		var base yaml.Node
		if err := base.Encode(s); err != nil {
			return err
		}
		merged := mergeNodes(&base, &patch)
		var out Service
		if err := merged.Decode(&out); err != nil {
			return fmt.Errorf("environment %s, service %s: %w", e.Name, name, err)
		}
		p.Services[name] = &out
	}
	for n, c := range e.Components {
		c.Name = n
		if c.Type == "" && c.Node.Kind == yaml.MappingNode && p.Components[n] != nil {
			// a patch without type only adjusts options of the shared component
			base := p.Components[n]
			merged := mergeNodes(&base.Node, &c.Node)
			c.Kind, c.Type, c.Node = base.Kind, base.Type, *merged
		}
		p.Components[n] = c
	}
	if len(e.Only) > 0 {
		keep := map[string]bool{}
		for _, t := range e.Only {
			for _, n := range p.Select([]string{t}) {
				keep[n] = true
			}
		}
		for n := range p.Services {
			if !keep[n] {
				delete(p.Services, n)
			}
		}
		// what the environment leaves out is provided some other way there, or not needed
		for _, s := range p.Services {
			var deps []string
			for _, d := range s.DependsOn {
				if keep[d] {
					deps = append(deps, d)
				}
			}
			s.DependsOn = deps
		}
	}
	return nil
}

func (p *Project) validate() error {
	var errs []string
	for _, n := range p.ServiceNames() {
		s := p.Services[n]
		for _, d := range s.DependsOn {
			if _, ok := p.Services[d]; !ok {
				errs = append(errs, fmt.Sprintf("service %s depends on unknown %q", n, d))
			}
		}
	}
	if _, err := p.Order(p.ServiceNames()); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

func pickEnv(env, def string, have []string) string {
	switch {
	case env != "":
		return env
	case os.Getenv("RIG_ENV") != "":
		return os.Getenv("RIG_ENV")
	case def != "":
		return def
	case len(have) == 1:
		return have[0]
	}
	return ""
}

var varRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_.]*)(:-([^}]*))?\}`)

func osLookup(n string) (string, bool) { return os.LookupEnv(n) }

// Expand replaces ${NAME} and ${NAME:-default}; $$ is a literal $. Unknown names stay as written.
func Expand(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "$") {
		return s
	}
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$$" {
			return "$"
		}
		g := varRe.FindStringSubmatch(m)
		if v, ok := lookup(g[1]); ok && v != "" {
			return v
		}
		if g[2] != "" {
			return g[3]
		}
		if v, ok := lookup(g[1]); ok {
			return v
		}
		return m
	})
}

func expandNode(n *yaml.Node, lookup func(string) (string, bool)) {
	if n.Kind == yaml.ScalarNode {
		n.Value = Expand(n.Value, lookup)
		return
	}
	for _, c := range n.Content {
		expandNode(c, lookup)
	}
}

// mergeNodes deep-merges mapping patch onto base; anything else in patch replaces.
func mergeNodes(base, patch *yaml.Node) *yaml.Node {
	if base.Kind == yaml.DocumentNode && len(base.Content) > 0 {
		base = base.Content[0]
	}
	if patch.Kind == yaml.DocumentNode && len(patch.Content) > 0 {
		patch = patch.Content[0]
	}
	if base.Kind != yaml.MappingNode || patch.Kind != yaml.MappingNode {
		return patch
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: base.Tag}
	idx := map[string]int{}
	for i := 0; i+1 < len(base.Content); i += 2 {
		idx[base.Content[i].Value] = len(out.Content)
		out.Content = append(out.Content, base.Content[i], base.Content[i+1])
	}
	for i := 0; i+1 < len(patch.Content); i += 2 {
		k, v := patch.Content[i], patch.Content[i+1]
		if j, ok := idx[k.Value]; ok {
			out.Content[j+1] = mergeNodes(out.Content[j+1], v)
			continue
		}
		out.Content = append(out.Content, k, v)
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
