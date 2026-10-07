// Package spec is the rig.yaml schema: services, environments and the components that serve them.
package spec

import (
	"fmt"
	"sort"
	"strings"
	"time"

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
	Dashboards   map[string]*Dashboard   `yaml:"dashboards"`
	// Flow is the Flow screen: requests travelling through the system as packets between nodes.
	Flow *Flow `yaml:"flow"`
	// Tests are named test suites: `rig test <name>` and the Tests screen.
	Tests     map[string]*TestSuite `yaml:"tests"`
	Manifests []string              `yaml:"manifests"`
	// Tasks are named lists of shell steps: `rig task <name>`.
	Tasks map[string]Task `yaml:"tasks"`
	// Queries are saved queries: run on demand, or on a schedule while the UI is open.
	Queries map[string]*Query `yaml:"queries"`
	// Secrets are names ${NAME} may use whose values live outside the repo: the environment, then
	// `rig secret set`, then the default written here.
	Secrets map[string]Secret `yaml:"secrets"`
	// Alerts are watched while the UI is open and shown in its header; `rig alerts` checks them once.
	Alerts []Alert `yaml:"alerts"`
	// Reports name the metrics a run is measured by: `rig report <name>`, a test suite's report:,
	// the Load screen's W.
	Reports map[string]*Report `yaml:"reports"`
	// Sections split the Services screen by business area; services in none fall under "other".
	Sections map[string]*Section `yaml:"sections"`
	// OTel points every app and load service at OpenTelemetry backends.
	OTel *OTel `yaml:"otel"`
	// UI picks the screens of `rig` and their order.
	UI *UI `yaml:"ui"`
	// AI tells the assistant (`rig ai`, @ in the UI) about the project: paths it must not read, notes.
	AI *AI `yaml:"ai"`
	// Help is shown by `rig -h` under the project name: this project's own quick-start for its main
	// CLI workflows (e.g. build/deploy/ship with a target database and image tag). Plain text, a few
	// example command lines; left out, `rig -h` shows only its own generic help.
	Help string `yaml:"help"`

	// DashboardOrder, TestOrder and SectionOrder are the names as rig.yaml lists them.
	DashboardOrder []string `yaml:"-"`
	TestOrder      []string `yaml:"-"`
	SectionOrder   []string `yaml:"-"`

	// Unset are the ${NAME}s the file uses (outside other environments) that nothing sets and that
	// have no :-default: they stay as written.
	Unset []string `yaml:"-"`
	// FromEnv are the ${NAME}s the file took from the process environment, with their values.
	FromEnv map[string]string `yaml:"-"`

	// Dir is where the project file lives; relative paths in it resolve from here.
	Dir  string `yaml:"-"`
	File string `yaml:"-"`
}

// OTel becomes the standard OTEL_* variables of every app and load service; a variable the service
// sets itself wins. Addresses take svc://service:port[/path]: inside the runtime's network for
// containers and pods, forwarded for local processes.
type OTel struct {
	// Endpoint takes every signal (an OpenTelemetry Collector: svc://otel-collector:4318); Traces,
	// Metrics and Logs send one signal elsewhere, with its full path (svc://jaeger:4318/v1/traces).
	// A signal with no address is off.
	Endpoint string `yaml:"endpoint"`
	Traces   string `yaml:"traces"`
	Metrics  string `yaml:"metrics"`
	Logs     string `yaml:"logs"`
	// Protocol is http/protobuf (default), http/json or grpc.
	Protocol string `yaml:"protocol"`
	// Sample is the fraction of traces kept (parent based); left out, the SDK's default (all).
	Sample *float64 `yaml:"sample"`
	// Attributes are added to every service's resource, after service.namespace (the project) and
	// deployment.environment (the environment).
	Attributes map[string]string `yaml:"attributes"`
}

// Screens are the UI's screens in their default order.
var Screens = []string{"services", "logs", "metrics", "traces", "queries", "kv", "data", "load", "flow", "manifests", "hosts", "tests"}

type UI struct {
	// Tabs are the screens shown, in order (number keys follow it); left out, every screen the
	// project configures something for shows, in the order of Screens.
	Tabs []string `yaml:"tabs"`
	// Mode is simple (fewer columns, keys and panels, for newcomers) or detailed (the default); V
	// switches it in the UI.
	Mode string  `yaml:"mode"`
	Logs *LogsUI `yaml:"logs"`
}

// LogsUI says how the Logs screen reads structured (JSON) lines; every key has a default.
type LogsUI struct {
	Wrap bool `yaml:"wrap"`
	// Raw shows lines as written instead of "LEVEL message key=value"; s switches.
	Raw bool `yaml:"raw"`
	// Time, Level and Message are the JSON keys of those parts, first match wins.
	Time    []string `yaml:"time"`
	Level   []string `yaml:"level"`
	Message []string `yaml:"message"`
	// Fields, when set, are the only other keys shown, in this order; Hide are keys left out (stack,
	// caller, ...) until h shows them.
	Fields []string `yaml:"fields"`
	Hide   []string `yaml:"hide"`
}

type AI struct {
	// Deny are project paths (globs) the assistant never reads, e.g. configs/** holding credentials.
	Deny         []string `yaml:"deny"`
	Instructions string   `yaml:"instructions"`
	// Ideas are the questions tab offers in an empty chat, by screen (services, logs, ...) or "all";
	// they come before rig's own.
	Ideas map[string][]string `yaml:"ideas"`
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
	// EnvFile are KEY=VALUE files (from the project directory) read under env:, later files winning.
	EnvFile []string `yaml:"env_file"`
	Image   string   `yaml:"image"`
	// Replicas left out keeps what the environment has (the manifest's count, or a running workload's);
	// 0 keeps a service deployed but stopped.
	Replicas *int   `yaml:"replicas"`
	Build    *Build `yaml:"build"`
	Run      *Run   `yaml:"run"`
	Health   *Probe `yaml:"health"`
	Metrics  *Probe `yaml:"metrics"`
	Pprof    *Probe `yaml:"pprof"`
	// Shared marks infrastructure that one environment runs for others (see Environment.Infra).
	Shared bool `yaml:"shared"`
	// Delay is how long up waits after the service's dependencies are ready before starting it.
	Delay time.Duration `yaml:"delay"`
	// Manual services start only when named (or through their group or section), never with "all"
	// or their role (rig infra up).
	Manual bool `yaml:"manual"`
	// Watch is what rig watch (ctrl+w in the UI) rebuilds and restarts the service on.
	Watch *Watch `yaml:"watch"`

	// Sections owned by adapters (local:, docker:, k8s:, ...), decoded by the adapter that reads them.
	Sections map[string]yaml.Node `yaml:",inline"`
}

// Watch is a service's sources for live rebuilds; .gitignore'd files never count.
type Watch struct {
	// Paths are directories or files (from the project directory); left out, the packages a build.go
	// main imports from the project, else build.context, else run.dir.
	Paths []string `yaml:"paths"`
	// Ignore are globs matched against a changed file's path and name (*_test.go, docs/**).
	Ignore []string `yaml:"ignore"`
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
	Name        string     `yaml:"-"`
	Description string     `yaml:"description"`
	Runtime     *Component `yaml:"runtime"`
	Protected   bool       `yaml:"protected"`
	// ReadOnly refuses every change (start, stop, deploy, exec, data and KV writes, load), even with
	// --yes; reads, logs, metrics, traces and read-only queries still work.
	ReadOnly   bool                  `yaml:"readonly"`
	Vars       map[string]string     `yaml:"vars"`
	Only       []string              `yaml:"only"`
	Components map[string]*Component `yaml:"components"`
	Services   map[string]yaml.Node  `yaml:"services"`
	// Tasks replace the project's tasks of the same name in this environment.
	Tasks map[string]Task `yaml:"tasks"`
	// Queries replace the project's queries of the same name in this environment.
	Queries map[string]*Query `yaml:"queries"`
	// Alerts add to the project's alerts in this environment.
	Alerts []Alert `yaml:"alerts"`
	// OTel replaces the fields it sets of the project's otel:.
	OTel *OTel `yaml:"otel"`
	// Infra names the environment that runs this one's shared services, so heavy infrastructure
	// (a database, a broker) runs once for local, docker and kind alike.
	Infra string `yaml:"infra"`
}

// Query is a saved query against a component. {{name}} placeholders are filled from Params
// (the defaults) or from the caller.
type Query struct {
	Name   string            `yaml:"-"`
	Source string            `yaml:"source"`
	Query  string            `yaml:"query"`
	Help   string            `yaml:"help"`
	Group  string            `yaml:"group"`
	Params map[string]string `yaml:"params"`
	// Every runs the query on a schedule while the UI is open, when Active (or activated there).
	Every  time.Duration `yaml:"every"`
	Active bool          `yaml:"active"`
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

// Section is a part of the Services screen; Services are names, groups or roles.
type Section struct {
	Name     string   `yaml:"-"`
	Help     string   `yaml:"help"`
	Services []string `yaml:"services"`
}

// SectionMap maps each service to the first section listing it; services in none are left out.
func (p *Project) SectionMap() map[string]string {
	out := map[string]string{}
	for _, n := range p.SectionOrder {
		if len(p.Sections[n].Services) == 0 {
			continue
		}
		for _, svc := range p.Select(p.Sections[n].Services) {
			if _, ok := out[svc]; !ok {
				out[svc] = n
			}
		}
	}
	return out
}

// Dashboard is a list of panels, or {vars:, panels:} when its queries use $variables.
// Task is a list of shell steps, or {help, steps} to say what it is for.
type Task struct {
	Help string `yaml:"help"`
	// Group is the tab the TUI's task picker lists it under; without one, the name's first word
	// (nexus-prune: nexus) when other tasks share it.
	Group string    `yaml:"group"`
	Args  []TaskArg `yaml:"args"`
	// Confirm asks before the first step, on any environment; --yes answers it.
	Confirm bool `yaml:"confirm"`
	// ReadOnly marks a task that changes nothing, so it runs in a read-only environment too.
	ReadOnly bool     `yaml:"readonly"`
	Steps    []string `yaml:"steps"`
	// StepHelp says what each step does: the # comment above it in rig.yaml, or its own first # lines.
	StepHelp []string `yaml:"-"`
}

// TaskArg is an input a task asks for before it runs (rig task <name> on a terminal, T in the UI).
// An UPPER_CASE name reaches the steps as that env var; a lower-case one as positional words ($1...,
// $RIG_ARGS). Choices, or From (services: every service and group; branches: the git branches;
// hosts), offer a pick list.
type TaskArg struct {
	Name    string   `yaml:"name"`
	Help    string   `yaml:"help"`
	Default string   `yaml:"default"`
	Choices []string `yaml:"choices"`
	From    string   `yaml:"from"`
	Multi   bool     `yaml:"multi"`
}

// Env is whether the arg is passed as NAME=value rather than as positional words.
func (a TaskArg) Env() bool { return a.Name != "" && strings.ToUpper(a.Name) == a.Name }

func (t *Task) UnmarshalYAML(n *yaml.Node) error {
	steps := n
	if n.Kind == yaml.SequenceNode {
		if err := n.Decode(&t.Steps); err != nil {
			return err
		}
	} else {
		type plain Task
		if err := n.Decode((*plain)(t)); err != nil {
			return err
		}
		steps = mappingValue(n, "steps")
	}
	if steps == nil || len(steps.Content) != len(t.Steps) {
		return nil
	}
	t.StepHelp = make([]string, len(t.Steps))
	for i, s := range steps.Content {
		t.StepHelp[i] = stepHelp(s.HeadComment, t.Steps[i])
	}
	return nil
}

func stepHelp(comment, step string) string {
	lines := strings.Split(comment, "\n")
	if comment == "" {
		lines = nil
		for _, l := range strings.Split(strings.TrimSpace(step), "\n") {
			if l = strings.TrimSpace(l); !strings.HasPrefix(l, "#") || strings.HasPrefix(l, "#!") {
				break
			}
			lines = append(lines, l)
		}
	}
	var words []string
	for _, l := range lines {
		words = append(words, strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")))
	}
	return strings.TrimSpace(strings.Join(words, " "))
}

// Flow draws how requests travel: nodes (load generators, services, queues, databases, third
// parties) in columns, links between them, packets moving at each link's measured rate.
type Flow struct {
	Help string `yaml:"help"`
	// Source is the metrics component the queries ask (default: the first).
	Source string               `yaml:"source"`
	Nodes  map[string]*FlowNode `yaml:"nodes"`
	Links  []FlowLink           `yaml:"links"`
	// NodeOrder is Nodes as rig.yaml lists them: their order down a column.
	NodeOrder []string `yaml:"-"`
}

func (f *Flow) UnmarshalYAML(n *yaml.Node) error {
	type plain Flow
	if err := n.Decode((*plain)(f)); err != nil {
		return err
	}
	f.NodeOrder = mappingKeys(n, "nodes")
	return nil
}

type FlowNode struct {
	Label string `yaml:"label"`
	Help  string `yaml:"help"`
	// Kind picks the icon and colour: load, gateway, service, queue, database, cache, external.
	Kind string `yaml:"kind"`
	// Column places the node (0 is the left); left out, it follows the links (one right of its inputs).
	Column *int `yaml:"column"`
	// Open is what a click opens: a service, a component (Data, Load, KV...), dashboard:<name>, or a
	// screen.
	Open string `yaml:"open"`
	// Rate, Errors, Latency (ms) and Backlog are instant metrics queries.
	Rate    string `yaml:"rate"`
	Errors  string `yaml:"errors"`
	Latency string `yaml:"latency"`
	Backlog string `yaml:"backlog"`
	// Load names load generators: their target rates make the node's offered load.
	Load []string `yaml:"load"`
	// Max is what the node handles per second: past 70% it runs hot, past 90% it is overloaded.
	Max float64 `yaml:"max"`
	// Slow is a latency (ms) past which the node counts as overloaded.
	Slow float64 `yaml:"slow"`
}

type FlowLink struct {
	From  string `yaml:"from"`
	To    string `yaml:"to"`
	Label string `yaml:"label"`
	// Rate and Errors are instant queries; Rate left out, the link carries its target's rate shared
	// over the target's inputs.
	Rate   string `yaml:"rate"`
	Errors string `yaml:"errors"`
}

type Dashboard struct {
	Help string `yaml:"help"`
	// Vars are the dashboard's $variables, picked in the UI; queries use them as $name.
	Vars   map[string]*DashVar `yaml:"vars"`
	Panels []Panel             `yaml:"panels"`
	// VarOrder is Vars as rig.yaml lists them.
	VarOrder []string `yaml:"-"`
}

func (d *Dashboard) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		return n.Decode(&d.Panels)
	}
	type plain Dashboard
	if err := n.Decode((*plain)(d)); err != nil {
		return err
	}
	d.VarOrder = mappingKeys(n, "vars")
	return nil
}

// DashVar is a dashboard variable: fixed Values, or the values of Label over the series Query
// returns (Grafana's label_values). Multi lets several be picked; All adds an "all" choice.
type DashVar struct {
	Values  []string `yaml:"values"`
	Source  string   `yaml:"source"`
	Query   string   `yaml:"query"`
	Label   string   `yaml:"label"`
	Default string   `yaml:"default"`
	Multi   bool     `yaml:"multi"`
	All     bool     `yaml:"all"`
}

type Panel struct {
	// Row starts a new titled row of panels; a panel with only row: is the row's header.
	Row   string `yaml:"row"`
	Title string `yaml:"title"`
	Query string `yaml:"query"`
	// Queries are several queries drawn together (p50, p95 and p99), each with its own legend.
	Queries []PanelQuery `yaml:"queries"`
	Unit    string       `yaml:"unit"`
	Source  string       `yaml:"source"`
	Legend  string       `yaml:"legend"`
	Help    string       `yaml:"help"`
	// Kind: line (default), stat, gauge, bar (one bar per series, its last value), table.
	Kind string `yaml:"kind"`
	// Width is out of 24 columns, like Grafana (default 12, stat and gauge 6); Height in lines.
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
	// Min and Max bound a gauge (default 0..100 for %, else 0..the largest value).
	Min *float64 `yaml:"min"`
	Max *float64 `yaml:"max"`
	// Warn and Crit colour stat, gauge and bar values amber and red past them.
	Warn *float64 `yaml:"warn"`
	Crit *float64 `yaml:"crit"`
	// Stack draws line series stacked.
	Stack bool `yaml:"stack"`
}

type PanelQuery struct {
	Query  string `yaml:"query"`
	Legend string `yaml:"legend"`
}

// Targets are the panel's queries: Queries, else Query with the panel's legend.
func (p Panel) Targets() []PanelQuery {
	if len(p.Queries) > 0 {
		return p.Queries
	}
	if p.Query == "" {
		return nil
	}
	return []PanelQuery{{Query: p.Query, Legend: p.Legend}}
}

// TestSuite is a set of Go packages tested together (go test -json), with the flags it always needs,
// or a Command of any stack.
type TestSuite struct {
	Name  string `yaml:"-"`
	Help  string `yaml:"help"`
	Group string `yaml:"group"`
	// Command runs the suite through sh instead of go test: pytest, jest, mvn test, cargo test, ...
	// Writing JUnit XML to $RIG_JUNIT gives the screen one row per test; without it the suite passes
	// or fails as a whole by its exit status.
	Command  string   `yaml:"command"`
	Packages []string `yaml:"packages"`
	// Exclude drops packages from Packages (go list patterns: ./pkg/tests/...), so a suite can be
	// "everything but the integration tests".
	Exclude []string `yaml:"exclude"`
	// Dir is where go test runs (default the project directory).
	Dir  string `yaml:"dir"`
	Run  string `yaml:"run"`
	Skip string `yaml:"skip"`
	Tags string `yaml:"tags"`
	// Env reaches the test binaries; svc:// addresses resolve in the active environment.
	Env      map[string]string `yaml:"env"`
	Timeout  time.Duration     `yaml:"timeout"`
	Race     bool              `yaml:"race"`
	Cover    bool              `yaml:"cover"`
	Short    bool              `yaml:"short"`
	Count    int               `yaml:"count"`
	Parallel int               `yaml:"parallel"`
	// Bench runs benchmarks matching it (go test -bench); Benchtime and Benchmem tune them.
	Bench     string `yaml:"bench"`
	Benchtime string `yaml:"benchtime"`
	Benchmem  bool   `yaml:"benchmem"`
	// Flags are more go test flags; Args go to the test binary after -args.
	Flags []string `yaml:"flags"`
	Args  []string `yaml:"args"`
	// Needs are services (or groups) the suite expects up; the UI warns when they are not.
	Needs []string `yaml:"needs"`
	// Report is a reports: entry measured over each run and saved with it.
	Report string `yaml:"report"`
}

type Report struct {
	Help string `yaml:"help"`
	// Source is the metrics component (default the first).
	Source  string         `yaml:"source"`
	Metrics []ReportMetric `yaml:"metrics"`
	Traces  []ReportTrace  `yaml:"traces"`
	Queries []ReportQuery  `yaml:"queries"`
	// Verbosity is brief, normal (default) or full: how many series, rows and slow traces it keeps.
	Verbosity string `yaml:"verbosity"`
	// Every adds a timeline: each metric's value per interval of the window.
	Every time.Duration `yaml:"every"`
	// Format is md (default), json or xml; a file name's extension wins.
	Format string `yaml:"format"`
}

// ReportTrace counts the traces of a service (and operation, and slower than Min) in the window
// with their error count and latency percentiles.
type ReportTrace struct {
	Title     string        `yaml:"title"`
	Source    string        `yaml:"source"`
	Service   string        `yaml:"service"`
	Operation string        `yaml:"operation"`
	Min       time.Duration `yaml:"min"`
}

// ReportQuery runs a reading query (or a saved one by name) once at the end of the window; ${from}
// and ${to} in it become the window's bounds (RFC 3339).
type ReportQuery struct {
	Title  string `yaml:"title"`
	Source string `yaml:"source"`
	Query  string `yaml:"query"`
}

// ReportMetric is one query, summarised per series by Stats over the run's window: avg, min, max,
// last, p50, p90, p95, p99 (default avg, max, last).
type ReportMetric struct {
	Title  string   `yaml:"title"`
	Query  string   `yaml:"query"`
	Unit   string   `yaml:"unit"`
	Legend string   `yaml:"legend"`
	Stats  []string `yaml:"stats"`
}

// mappingKeys lists the keys of the mapping under key in n, in file order.
func mappingKeys(n *yaml.Node, key string) []string {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != key || n.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		var out []string
		m := n.Content[i+1]
		for j := 0; j+1 < len(m.Content); j += 2 {
			out = append(out, m.Content[j].Value)
		}
		return out
	}
	return nil
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

// Count is the replica count rig.yaml asks for, and whether it asks at all.
func (s *Service) Count() (int, bool) {
	if s.Replicas == nil {
		return 0, false
	}
	return *s.Replicas, true
}

// CountOr is the replica count rig.yaml asks for, else def.
func (s *Service) CountOr(def int) int {
	if n, ok := s.Count(); ok {
		return n
	}
	return def
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

// Alert fires when a number crosses Warn or Crit. Source "hosts" watches every node's Metric (cpu,
// memory or disk, in percent); any other source is a component whose Query returns rows, each
// row's first number checked and its text cells naming it.
type Alert struct {
	Name   string  `yaml:"name"`
	Source string  `yaml:"source"`
	Metric string  `yaml:"metric"`
	Query  string  `yaml:"query"`
	Warn   float64 `yaml:"warn"`
	Crit   float64 `yaml:"crit"`
	// Below fires when the number drops under the thresholds instead (consumers, ready replicas).
	Below bool   `yaml:"below"`
	Unit  string `yaml:"unit"`
}

type Secret struct {
	Help    string `yaml:"help"`
	Default string `yaml:"default"`
}
