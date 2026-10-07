package scaffold

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// stringOrList is a compose value written either way: command: "a b" or command: [a, b].
type stringOrList []string

func (s *stringOrList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = strings.Fields(n.Value)
		return nil
	}
	var l []string
	err := n.Decode(&l)
	*s = l
	return err
}

// keysOrList is depends_on or environment: a list, or a mapping (whose keys count, or key=value).
type keysOrList struct {
	keys   []string
	values map[string]string
}

func (k *keysOrList) UnmarshalYAML(n *yaml.Node) error {
	k.values = map[string]string{}
	if n.Kind == yaml.SequenceNode {
		var l []string
		if err := n.Decode(&l); err != nil {
			return err
		}
		for _, item := range l {
			key, v, _ := strings.Cut(item, "=")
			if _, dup := k.values[key]; !dup {
				k.keys = append(k.keys, key)
			}
			k.values[key] = v // compose lets a later entry win
		}
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		k.keys = append(k.keys, key)
		if n.Content[i+1].Kind == yaml.ScalarNode {
			k.values[key] = n.Content[i+1].Value
		}
	}
	return nil
}

type composeBuild struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func (b *composeBuild) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		b.Context = n.Value
		return nil
	}
	type plain composeBuild
	return n.Decode((*plain)(b))
}

type composeService struct {
	Image       string        `yaml:"image"`
	Build       *composeBuild `yaml:"build"`
	Ports       []yaml.Node   `yaml:"ports"`
	Expose      []yaml.Node   `yaml:"expose"`
	Environment keysOrList    `yaml:"environment"`
	DependsOn   keysOrList    `yaml:"depends_on"`
	Command     stringOrList  `yaml:"command"`
	Entrypoint  stringOrList  `yaml:"entrypoint"`
	Volumes     []yaml.Node   `yaml:"volumes"`
	EnvFile     envFiles      `yaml:"env_file"`
}

// envFiles is env_file: a path, a list of paths, or a list of {path, required}.
type envFiles []string

func (e *envFiles) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*e = []string{n.Value}
		return nil
	}
	for _, item := range n.Content {
		if item.Kind == yaml.MappingNode {
			var m struct {
				Path string `yaml:"path"`
			}
			if err := item.Decode(&m); err != nil {
				return err
			}
			*e = append(*e, m.Path)
		} else {
			*e = append(*e, item.Value)
		}
	}
	return nil
}

// compose turns the first compose file found into services.
func (p *Plan) compose(dir string) error {
	for _, f := range composeFiles {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var doc struct {
			Services map[string]composeService `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return err
		}
		names := make([]string, 0, len(doc.Services))
		for n := range doc.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p.Services = append(p.Services, fromCompose(cleanName(n), doc.Services[n], f))
		}
		p.note("%s: %d services", f, len(names))
		return nil
	}
	return nil
}

func fromCompose(name string, c composeService, file string) *Service {
	s := &Service{Name: name, Image: c.Image, From: file, DockerCommand: c.Entrypoint, DockerArgs: c.Command}
	if c.Build != nil {
		s.Context = strings.TrimPrefix(c.Build.Context, "./")
		if s.Context == "" {
			s.Context = "."
		}
		if c.Build.Dockerfile != "" {
			s.Dockerfile = filepath.Join(s.Context, c.Build.Dockerfile)
		}
	}
	switch {
	case c.Build != nil:
		s.Role = "app"
	case kindOf(c.Image) != "" || tool(c.Image):
		s.Role = "infra"
	default:
		s.Role = "app"
	}
	for _, d := range c.DependsOn.keys {
		s.DependsOn = append(s.DependsOn, cleanName(d))
	}
	for _, k := range c.Environment.keys {
		s.Env = append(s.Env, [2]string{k, c.Environment.values[k]})
	}
	seen := map[int]bool{}
	for _, n := range append(c.Ports, c.Expose...) {
		if pt := containerPort(n); pt > 0 && !seen[pt] {
			seen[pt] = true
			s.Ports = append(s.Ports, Port{portName(pt, len(s.Ports)), pt})
		}
	}
	if k := kindOf(c.Image); len(s.Ports) == 0 && k != "" {
		s.Ports = presets[k].ports // what it listens on, so svc:// addresses resolve
	}
	for _, v := range c.Volumes {
		// only binds of project files carry over; named volumes are compose's own
		if v.Kind == yaml.ScalarNode && (strings.HasPrefix(v.Value, "./") || strings.HasPrefix(v.Value, "../")) {
			s.Volumes = append(s.Volumes, v.Value)
		}
	}
	return s
}

// containerPort reads "8080", "8080:80", "127.0.0.1:8080:80/tcp" or {target: 80}.
func containerPort(n yaml.Node) int {
	if n.Kind == yaml.MappingNode {
		var m struct {
			Target int `yaml:"target"`
		}
		_ = n.Decode(&m)
		return m.Target
	}
	v := strings.Split(strings.Split(n.Value, "/")[0], ":")
	last := v[len(v)-1]
	if lo, _, ok := strings.Cut(last, "-"); ok {
		last = lo
	}
	pt, _ := strconv.Atoi(last)
	return pt
}

var wellKnown = map[int]string{5432: "pg", 3306: "mysql", 1433: "mssql", 6379: "redis", 9092: "kafka", 5672: "amqp",
	15672: "mgmt", 8500: "http", 9090: "web", 16686: "ui", 4317: "otlp-grpc", 4318: "otlp", 9411: "http", 3100: "http",
	27017: "mongo", 9200: "http", 8080: "http", 8000: "http", 3000: "http", 5000: "http"}

func portName(pt, i int) string {
	if n, ok := wellKnown[pt]; ok && (i == 0 || n != "http") {
		return n
	}
	if i == 0 {
		return "http"
	}
	return "p" + strconv.Itoa(pt)
}
