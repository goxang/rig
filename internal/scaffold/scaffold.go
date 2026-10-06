// Package scaffold writes a first rig.yaml from what a directory already has: compose files,
// Kubernetes manifests, and services in Go, Python, Node, Java or Rust, plus infrastructure presets.
package scaffold

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Service struct {
	Name, Role, Image string
	// Go is a main package to build; Dockerfile and Context a docker build.
	Go, Dockerfile, Context string
	Run                     []string
	RunDir                  string
	Ports                   []Port
	Env                     [][2]string
	DependsOn               []string
	// Docker is the docker runtime's section: compose's entrypoint, command and volumes.
	DockerCommand, DockerArgs, Volumes []string
	Shared                             bool
	From                               string
	// Adopt is a running container rig takes over (docker: {container: ...}) instead of making one.
	Adopt string
}

type Port struct {
	Name string
	Port int
}

// Component is a components: entry, its fields in order.
type Component struct {
	Name   string
	Fields [][2]string
}

type Suite struct {
	Name, Dir, Command string
	Packages           []string
}

type Plan struct {
	Project    string
	Services   []*Service
	Components []Component
	Suites     []Suite
	Manifests  []string
	Godev      bool
	// OTel is where services send traces, when a tracing backend that takes OTLP is there.
	OTel string
	// Notes say what was found where, for the summary.
	Notes []string
}

type Options struct {
	// With are presets to add: postgres, mysql, mssql, redis, kafka, rabbitmq, consul, prometheus,
	// jaeger, loki, otel.
	With []string
	// Scan turns the source scan off when false (compose and manifests still count).
	NoScan bool
}

func (p *Plan) service(name string) *Service {
	for _, s := range p.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (p *Plan) hasComponent(name string) bool {
	for _, c := range p.Components {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (p *Plan) addComponent(c Component) {
	if !p.hasComponent(c.Name) {
		p.Components = append(p.Components, c)
	}
}

func (p *Plan) note(format string, args ...any) {
	p.Notes = append(p.Notes, fmt.Sprintf(format, args...))
}

var nameRe = regexp.MustCompile(`[^a-z0-9-]+`)

func cleanName(s string) string {
	return strings.Trim(nameRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// Detect builds the plan for dir.
func Detect(ctx context.Context, dir string, o Options) (*Plan, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p := &Plan{Project: cleanName(filepath.Base(dir))}
	if err := p.compose(dir); err != nil {
		return nil, err
	}
	if exists(filepath.Join(dir, ".godev.yaml")) {
		p.Godev = true
		p.note(".godev.yaml: its services are imported")
	}
	if !o.NoScan {
		p.scan(ctx, dir)
	}
	p.manifests(dir)
	for _, w := range o.With {
		if err := p.preset(strings.ToLower(strings.TrimSpace(w))); err != nil {
			return nil, err
		}
	}
	p.infraComponents()
	return p, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---- infrastructure ----

// infra knows the usual images: the preset that adds one, and what to call its ports.
type infra struct {
	match   []string // image name parts
	image   string
	ports   []Port
	env     [][2]string
	args    []string
	service string
}

var presets = map[string]infra{
	"postgres": {match: []string{"postgres", "postgis", "timescale"}, image: "postgres:17", service: "postgres",
		ports: []Port{{"pg", 5432}}, env: [][2]string{{"POSTGRES_PASSWORD", "${PG_PASSWORD:-dev}"}}},
	"mysql": {match: []string{"mysql", "mariadb"}, image: "mysql:8.4", service: "mysql",
		ports: []Port{{"mysql", 3306}}, env: [][2]string{{"MYSQL_ROOT_PASSWORD", "${MYSQL_PASSWORD:-dev}"}}},
	"mssql": {match: []string{"mssql"}, image: "mcr.microsoft.com/mssql/server:2022-latest", service: "mssql",
		ports: []Port{{"mssql", 1433}}, env: [][2]string{{"ACCEPT_EULA", "Y"}, {"MSSQL_SA_PASSWORD", "${MSSQL_PASSWORD:-Dev_passw0rd}"}}},
	"redis": {match: []string{"redis", "valkey", "keydb"}, image: "redis:7", service: "redis", ports: []Port{{"redis", 6379}}},
	"kafka": {match: []string{"kafka", "redpanda"}, image: "confluentinc/cp-kafka:7.7.1", service: "kafka", ports: []Port{{"kafka", 9092}},
		env: [][2]string{{"CLUSTER_ID", "MkU3OEVBNTcwNTJENDM2Qk"}, {"KAFKA_NODE_ID", "1"}, {"KAFKA_PROCESS_ROLES", "broker,controller"},
			{"KAFKA_LISTENERS", "PLAINTEXT://:9092,CONTROLLER://:9093"}, {"KAFKA_ADVERTISED_LISTENERS", "PLAINTEXT://kafka:9092"},
			{"KAFKA_CONTROLLER_QUORUM_VOTERS", "1@kafka:9093"}, {"KAFKA_CONTROLLER_LISTENER_NAMES", "CONTROLLER"},
			{"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP", "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT"}, {"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR", "1"}}},
	"rabbitmq":   {match: []string{"rabbitmq"}, image: "rabbitmq:3-management", service: "rabbitmq", ports: []Port{{"amqp", 5672}, {"mgmt", 15672}}},
	"consul":     {match: []string{"consul"}, image: "hashicorp/consul:1.19", service: "consul", ports: []Port{{"http", 8500}}},
	"prometheus": {match: []string{"prometheus"}, image: "prom/prometheus:v3.1.0", service: "prometheus", ports: []Port{{"web", 9090}}},
	"jaeger":     {match: []string{"jaeger"}, image: "jaegertracing/all-in-one:1.62.0", service: "jaeger", ports: []Port{{"ui", 16686}, {"otlp", 4318}, {"otlp-grpc", 4317}}},
	"zipkin":     {match: []string{"zipkin"}, image: "openzipkin/zipkin:3", service: "zipkin", ports: []Port{{"http", 9411}}},
	"loki":       {match: []string{"loki"}, image: "grafana/loki:3.3.2", service: "loki", ports: []Port{{"http", 3100}}},
	"tempo":      {match: []string{"grafana/tempo"}, service: "tempo", ports: []Port{{"http", 3200}, {"otlp", 4318}}},
	"collector":  {match: []string{"otel/opentelemetry-collector", "opentelemetry-collector"}, service: "otel-collector", ports: []Port{{"otlp", 4318}, {"otlp-grpc", 4317}}},
}

// Presets are the names --with takes.
func Presets() []string {
	out := []string{"otel"}
	for k, v := range presets {
		if v.image != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// tool says an image is a UI or exporter next to infrastructure (kafka-ui, redis-exporter), not the thing itself.
func tool(image string) bool {
	img := strings.ToLower(image)
	for _, t := range []string{"-ui", "exporter", "admin", "insight", "commander", "kafdrop", "akhq"} {
		if strings.Contains(img, t) {
			return true
		}
	}
	return false
}

func kindOf(image string) string {
	if tool(image) {
		return ""
	}
	img := strings.ToLower(image)
	keys := make([]string, 0, len(presets))
	for k := range presets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, m := range presets[k].match {
			if strings.Contains(img, m) {
				return k
			}
		}
	}
	return ""
}

func (p *Plan) preset(name string) error {
	if name == "otel" {
		if p.byKind("jaeger") == nil && p.byKind("tempo") == nil && p.byKind("collector") == nil {
			if err := p.preset("jaeger"); err != nil {
				return err
			}
		}
		return nil
	}
	in, ok := presets[name]
	if !ok || in.image == "" {
		return fmt.Errorf("no preset %q (have %s)", name, strings.Join(Presets(), ", "))
	}
	if p.byKind(name) != nil {
		return nil
	}
	p.Services = append(p.Services, &Service{Name: in.service, Role: "infra", Image: in.image, Ports: in.ports, Env: in.env,
		DockerArgs: in.args, From: "--with " + name})
	p.note("--with %s: %s (%s)", name, in.service, in.image)
	return nil
}

func (p *Plan) byKind(kind string) *Service {
	for _, s := range p.Services {
		if s.Role == "infra" && kindOf(s.Image) == kind {
			return s
		}
	}
	return nil
}

func env(s *Service, key, def string) string {
	for _, kv := range s.Env {
		if kv[0] == key {
			return kv[1]
		}
	}
	return def
}

func portOf(s *Service, def int) int {
	for _, pt := range s.Ports {
		if pt.Port == def {
			return def
		}
	}
	if len(s.Ports) > 0 {
		return s.Ports[0].Port
	}
	return def
}

// infraComponents adds the component that reads each piece of infrastructure, and otel: when a
// backend takes OTLP.
func (p *Plan) infraComponents() {
	addr := func(s *Service, port int) string { return "svc://" + s.Name + ":" + strconv.Itoa(port) }
	metricsSvc := 0
	for _, s := range p.Services {
		if s.Role != "infra" {
			metricsSvc++
		}
	}
	for _, s := range p.Services {
		if s.Role != "infra" {
			continue
		}
		switch kindOf(s.Image) {
		case "postgres":
			p.addComponent(Component{"db", [][2]string{{"type", "sql"}, {"driver", "postgres"}, {"addr", addr(s, portOf(s, 5432))},
				{"user", env(s, "POSTGRES_USER", "postgres")}, {"password", env(s, "POSTGRES_PASSWORD", "")}, {"database", env(s, "POSTGRES_DB", "postgres")}}})
		case "mysql":
			user, pass := "root", env(s, "MYSQL_ROOT_PASSWORD", env(s, "MARIADB_ROOT_PASSWORD", ""))
			if u := env(s, "MYSQL_USER", ""); u != "" && pass == "" {
				user, pass = u, env(s, "MYSQL_PASSWORD", "")
			}
			p.addComponent(Component{"mysql", [][2]string{{"type", "sql"}, {"driver", "mysql"}, {"addr", addr(s, portOf(s, 3306))},
				{"user", user}, {"password", pass}, {"database", env(s, "MYSQL_DATABASE", "mysql")}}})
		case "mssql":
			p.addComponent(Component{"mssql", [][2]string{{"type", "sql"}, {"driver", "mssql"}, {"addr", addr(s, portOf(s, 1433))},
				{"user", "sa"}, {"password", env(s, "MSSQL_SA_PASSWORD", env(s, "SA_PASSWORD", ""))}}})
		case "redis":
			p.addComponent(Component{"cache", [][2]string{{"type", "redis"}, {"addr", addr(s, portOf(s, 6379))}}})
		case "kafka":
			p.addComponent(Component{"bus", [][2]string{{"type", "kafka"}, {"addr", addr(s, portOf(s, 9092))}}})
		case "rabbitmq":
			p.addComponent(Component{"queue", [][2]string{{"type", "rabbitmq"}, {"addr", addr(s, portOf(s, 15672))}}})
		case "consul":
			p.addComponent(Component{"kv", [][2]string{{"type", "consul"}, {"addr", addr(s, portOf(s, 8500))}}})
		case "prometheus":
			p.addComponent(Component{"prom", [][2]string{{"type", "prometheus"}, {"addr", addr(s, portOf(s, 9090))}}})
		case "jaeger":
			p.addComponent(Component{"traces", [][2]string{{"type", "jaeger"}, {"addr", addr(s, portOf(s, 16686))}}})
			p.OTel = "svc://" + s.Name + ":4318/v1/traces"
		case "zipkin":
			p.addComponent(Component{"traces", [][2]string{{"type", "zipkin"}, {"addr", addr(s, portOf(s, 9411))}}})
		case "tempo":
			p.addComponent(Component{"traces", [][2]string{{"type", "tempo"}, {"addr", addr(s, portOf(s, 3200))}}})
		case "loki":
			p.addComponent(Component{"logs", [][2]string{{"type", "loki"}, {"addr", addr(s, portOf(s, 3100))}}})
		case "collector":
			p.OTel = "svc://" + s.Name + ":4318"
		}
	}
	if p.OTel != "" && p.byKind("collector") != nil {
		p.OTel = "svc://" + p.byKind("collector").Name + ":4318"
	}
}

// ---- output ----

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func mapping(kv ...any) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(kv); i += 2 {
		v, ok := kv[i+1].(*yaml.Node)
		if !ok {
			v = scalar(fmt.Sprint(kv[i+1]))
		}
		n.Content = append(n.Content, scalar(kv[i].(string)), v)
	}
	return n
}

func flow(n *yaml.Node) *yaml.Node { n.Style = yaml.FlowStyle; return n }

func seq(vs []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range vs {
		n.Content = append(n.Content, scalar(v))
	}
	return n
}

func (s *Service) node() *yaml.Node {
	n := mapping()
	add := func(k string, v *yaml.Node) { n.Content = append(n.Content, scalar(k), v) }
	if s.Role == "infra" {
		add("role", scalar("infra"))
	}
	if s.Shared {
		add("shared", scalar("true"))
	}
	if len(s.DependsOn) > 0 {
		add("depends_on", seq(s.DependsOn))
	}
	if s.Image != "" {
		add("image", scalar(s.Image))
	}
	switch {
	case s.Go != "":
		add("build", flow(mapping("go", s.Go)))
	case s.Dockerfile != "" || s.Context != "":
		b := mapping()
		if s.Context != "" && s.Context != "." {
			b.Content = append(b.Content, scalar("context"), scalar(s.Context))
		}
		df := s.Dockerfile
		if df == "" {
			df = filepath.Join(s.Context, "Dockerfile")
		}
		b.Content = append(b.Content, scalar("dockerfile"), scalar(df))
		add("build", flow(b))
	}
	if len(s.Run) > 0 {
		r := mapping("command", seq(s.Run))
		if s.RunDir != "" && s.RunDir != "." {
			r.Content = append(r.Content, scalar("dir"), scalar(s.RunDir))
		}
		add("run", flow(r))
	}
	if len(s.Ports) > 0 {
		ps := flow(mapping())
		for _, pt := range s.Ports {
			ps.Content = append(ps.Content, scalar(pt.Name), scalar(strconv.Itoa(pt.Port)))
		}
		add("ports", ps)
	}
	if len(s.Env) > 0 {
		e := mapping()
		for _, kv := range s.Env {
			v := scalar(kv[1])
			v.Style = yaml.DoubleQuotedStyle
			e.Content = append(e.Content, scalar(kv[0]), v)
		}
		add("env", e)
	}
	if len(s.DockerCommand)+len(s.DockerArgs)+len(s.Volumes) > 0 || s.Adopt != "" {
		d := flow(mapping())
		if s.Adopt != "" {
			d.Content = append(d.Content, scalar("container"), scalar(s.Adopt))
		}
		if len(s.DockerCommand) > 0 {
			d.Content = append(d.Content, scalar("command"), seq(s.DockerCommand))
		}
		if len(s.DockerArgs) > 0 {
			d.Content = append(d.Content, scalar("args"), seq(s.DockerArgs))
		}
		if len(s.Volumes) > 0 {
			d.Content = append(d.Content, scalar("volumes"), seq(s.Volumes))
		}
		add("docker", d)
	}
	if len(n.Content) == 0 {
		return flow(n)
	}
	return n
}

// YAML is the rig.yaml of the plan.
func (p *Plan) YAML() ([]byte, error) {
	hasInfra, runnable, built := false, false, false
	for _, s := range p.Services {
		hasInfra = hasInfra || s.Role == "infra"
		runnable = runnable || s.Role != "infra" && (len(s.Run) > 0 || s.Go != "")
		built = built || s.Go != "" || s.Dockerfile != "" || s.Context != ""
	}
	for _, s := range p.Services {
		if s.Role == "infra" && runnable {
			s.Shared = true // local processes use the docker environment's
		}
	}
	root := mapping("version", "1", "project", p.Project, "default", "docker")
	if runnable && !hasInfra {
		root.Content[5] = scalar("local")
	}
	add := func(k string, v *yaml.Node) { root.Content = append(root.Content, scalar(k), v) }
	if p.Godev {
		add("imports", &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{flow(mapping("godev", ".godev.yaml"))}})
	}
	if len(p.Manifests) > 0 {
		add("manifests", seq(p.Manifests))
	}
	if p.OTel != "" {
		add("otel", flow(mapping("traces", p.OTel)))
	}
	svcs := mapping()
	for _, s := range p.Services {
		svcs.Content = append(svcs.Content, scalar(s.Name), s.node())
	}
	if len(p.Services) == 0 {
		svcs.Style = yaml.FlowStyle
	}
	add("services", svcs)

	envs := mapping()
	addEnv := func(name string, n *yaml.Node) { envs.Content = append(envs.Content, scalar(name), n) }
	addEnv("docker", mapping("description", "containers on one docker network", "runtime", flow(mapping("type", "docker"))))
	if runnable {
		local := mapping("description", "the services as processes on this machine")
		if hasInfra {
			local.Content = append(local.Content, scalar("infra"), scalar("docker"))
		}
		local.Content = append(local.Content, scalar("runtime"), flow(mapping("type", "local")))
		addEnv("local", local)
	}
	if built || len(p.Manifests) > 0 {
		kind := mapping("type", "kind", "cluster", p.Project, "namespace", p.Project, "registry_port", "5001", "create_namespace", "true")
		if len(p.Manifests) > 0 {
			kind.Content = append(kind.Content, scalar("manifests"), seq(p.Manifests))
		}
		addEnv("kind", mapping("description", "a throwaway kind cluster: rig do runtime create, then rig up --build", "runtime", kind))
	}
	add("environments", envs)

	if len(p.Components) > 0 {
		cs := mapping()
		for _, c := range p.Components {
			m := flow(mapping())
			for _, kv := range c.Fields {
				m.Content = append(m.Content, scalar(kv[0]), scalar(kv[1]))
			}
			cs.Content = append(cs.Content, scalar(c.Name), m)
		}
		add("components", cs)
	}
	if len(p.Suites) > 0 {
		ts := mapping()
		for _, t := range p.Suites {
			m := flow(mapping())
			if t.Dir != "" && t.Dir != "." {
				m.Content = append(m.Content, scalar("dir"), scalar(t.Dir))
			}
			if t.Command != "" {
				m.Content = append(m.Content, scalar("command"), scalar(t.Command))
			}
			if len(t.Packages) > 0 {
				m.Content = append(m.Content, scalar("packages"), seq(t.Packages))
			}
			ts.Content = append(ts.Content, scalar(t.Name), m)
		}
		add("tests", ts)
	}

	var b bytes.Buffer
	b.WriteString("# rig.yaml: every environment of " + p.Project + " in one file. Reference: https://github.com/goxang/rig/blob/main/docs/config.md\n")
	b.WriteString("# Written by rig init from what the directory has; check ports, health checks and passwords.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}
