// Package core defines what every component kind must do. Adapters in adapters/<kind>/<type>
// implement one of these interfaces; anything an adapter can do beyond it is offered through
// the optional interfaces at the bottom (Forwarder, Actioner, Querier, ...), found by type assertion.
package core

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/goxang/rig/spec"
)

type Kind string

const (
	KindRuntime   Kind = "runtime"
	KindBuilder   Kind = "builder"
	KindMetrics   Kind = "metrics"
	KindTracing   Kind = "tracing"
	KindProfiler  Kind = "profiler"
	KindDebugger  Kind = "debugger"
	KindLogs      Kind = "logs"
	KindDatabase  Kind = "database"
	KindCache     Kind = "cache"
	KindMessaging Kind = "messaging"
	KindKV        Kind = "kv"
	KindLoad      Kind = "loadgen"
	KindHosts     Kind = "hosts"
	KindQuery     Kind = "query"
)

var Kinds = []Kind{KindRuntime, KindBuilder, KindMetrics, KindTracing, KindProfiler, KindDebugger, KindLogs,
	KindDatabase, KindCache, KindMessaging, KindKV, KindLoad, KindHosts, KindQuery}

var ErrUnsupported = errors.New("not supported by this adapter")

// Env is what an adapter can reach: the project, the runtime of the active environment,
// address resolution through that runtime, and the other components.
type Env interface {
	Project() *spec.Project
	Environment() *spec.Environment
	Runtime() Runtime
	Resolve(ctx context.Context, addr string) (string, error)
	Component(name string) (any, error)
	StateDir() string
	// DefaultImage is what to run for s when no release names one: the image last built for it, else its image field.
	DefaultImage(ctx context.Context, s *spec.Service) string
	// Owner is the runtime a service runs on (another environment's, for shared infrastructure).
	Owner(service string) (Runtime, *spec.Service, error)
}

// ImageRef names the image a builder produces for s: registry/repo:tag, repo being s.Image or s.Name.
func ImageRef(s *spec.Service, registry, tag string) string {
	repo := s.Image
	if repo == "" || strings.ContainsAny(repo, ":@") {
		repo = s.Name
	}
	ref := repo + ":" + tag
	if registry != "" {
		ref = registry + "/" + ref
	}
	return ref
}

// ---- service management ----

type State string

const (
	StateRunning  State = "running"
	StateStarting State = "starting"
	StateDegraded State = "degraded"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
	StateAbsent   State = "absent"
	StateUnknown  State = "unknown"
)

type Instance struct {
	ID       string
	Host     string
	IP       string
	State    State
	Ready    bool
	Restarts int
	Started  time.Time
	CPU      float64 // cores
	Memory   int64   // bytes
}

type Status struct {
	Service   string
	State     State
	Ready     int
	Desired   int
	Image     string
	Message   string
	Instances []Instance
	// Autoscale is the range an autoscaler keeps the replica count in, nil when none does.
	Autoscale *Bounds
}

// Bounds is an autoscaler's replica range; CPU and Memory are the average utilization (percent of
// requests) it scales on, used when SetAutoscale creates one (0: not a target).
type Bounds struct{ Min, Max, CPU, Memory int }

// Autoscaler is a runtime whose services can have an autoscaler (a Kubernetes HPA) bounding their scale.
// SetAutoscale moves an existing autoscaler's range, or creates one with b's targets.
type Autoscaler interface {
	SetAutoscale(ctx context.Context, s *spec.Service, b Bounds) error
}

// Resources are one container's requests and limits, as Kubernetes quantities ("" leaves one unset).
type Resources struct {
	Container                                  string
	CPURequest, CPULimit, MemRequest, MemLimit string
}

// Resourcer is a runtime whose services' containers have requests and limits to read and change.
type Resourcer interface {
	Resources(ctx context.Context, s *spec.Service) ([]Resources, error)
	SetResources(ctx context.Context, s *spec.Service, r Resources) error
}

// Workload is something running in the environment, managed by rig or not.
type Workload struct {
	Name    string
	Kind    string
	Service string // the rig service it belongs to, "" when unmanaged
	Status  Status
}

type LogLine struct {
	Time     time.Time
	Service  string
	Instance string
	Stream   string
	Text     string
}

type LogOptions struct {
	Follow   bool
	Since    time.Duration
	Tail     int
	Instance string
}

type ExecOptions struct {
	Command  []string
	Instance string
	TTY      bool
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
}

// Release is what Deploy rolls out; empty fields keep what the service already runs.
type Release struct {
	Image    string
	Env      map[string]string
	Replicas int
}

type Runtime interface {
	Discover(ctx context.Context) ([]Workload, error)
	Start(ctx context.Context, s *spec.Service) error
	Stop(ctx context.Context, s *spec.Service) error
	Restart(ctx context.Context, s *spec.Service) error
	Status(ctx context.Context, s *spec.Service) (Status, error)
	Scale(ctx context.Context, s *spec.Service, replicas int) error
	Logs(ctx context.Context, s *spec.Service, o LogOptions) (<-chan LogLine, error)
	Exec(ctx context.Context, s *spec.Service, o ExecOptions) error
	Deploy(ctx context.Context, s *spec.Service, r Release) error
}

type BuildOptions struct {
	Tag      string
	Registry string
	Push     bool
	Out      io.Writer
	// Dir is the source tree to build from; empty means the project directory.
	Dir string
}

// ErrTagExists stops a push that would replace an image tag already in the registry, unless confirmed.
var ErrTagExists = errors.New("tag already exists in the registry: pass --yes to overwrite it")

type Builder interface {
	Build(ctx context.Context, s *spec.Service, o BuildOptions) (image string, err error)
}

// ---- observability ----

type Point struct {
	T time.Time
	V float64
}

type Series struct {
	Labels map[string]string
	Points []Point
}

type Sample struct {
	Labels map[string]string
	Value  float64
}

type Metrics interface {
	Instant(ctx context.Context, query string) ([]Sample, error)
	Range(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error)
}

type Span struct {
	TraceID  string
	ID       string
	Parent   string
	Service  string
	Name     string
	Start    time.Time
	Duration time.Duration
	Error    bool
	Tags     map[string]string
}

type TraceSummary struct {
	ID       string
	Root     string
	Start    time.Time
	Duration time.Duration
	Spans    int
	Services []string
	Error    bool
}

type TraceQuery struct {
	Service     string
	Operation   string
	MinDuration time.Duration
	Lookback    time.Duration
	Limit       int
}

type Tracing interface {
	Services(ctx context.Context) ([]string, error)
	Search(ctx context.Context, q TraceQuery) ([]TraceSummary, error)
	Trace(ctx context.Context, id string) ([]Span, error)
}

type ProfileRequest struct {
	Service  *spec.Service
	Instance string
	Kind     string
	Duration time.Duration
	OutDir   string
}

type Profile struct {
	Kind    string
	Format  string
	File    string
	Summary string
}

type Profiler interface {
	Kinds() []string
	Capture(ctx context.Context, r ProfileRequest) (Profile, error)
}

// DebugPort is the local port a service's debugger listens on: the same every time, so an IDE's
// remote-debug configuration written once keeps working.
func DebugPort(service string) int {
	h := fnv.New32a()
	h.Write([]byte(service))
	return 40000 + int(h.Sum32()%5000)
}

type DebugSession struct {
	Addr  string
	Hint  string
	Close func() error
}

type Debugger interface {
	Attach(ctx context.Context, s *spec.Service, instance string) (DebugSession, error)
}

type LogQuery struct {
	Services []string
	// Instance narrows a single-service query to one instance (pod, container).
	Instance string
	Since    time.Duration
	Tail     int
	Follow   bool
	Match    string
	// Regex makes Match an RE2 regular expression ((?i) for any case) instead of plain text.
	Regex bool
}

// Matcher is q's line filter; an invalid regular expression is an error.
func (q LogQuery) Matcher() (func(string) bool, error) {
	switch {
	case q.Match == "":
		return func(string) bool { return true }, nil
	case q.Regex:
		re, err := regexp.Compile(q.Match)
		if err != nil {
			return nil, err
		}
		return re.MatchString, nil
	}
	return func(s string) bool { return strings.Contains(s, q.Match) }, nil
}

type LogSource interface {
	Logs(ctx context.Context, q LogQuery) (<-chan LogLine, error)
}

// ---- data ----

type Table struct {
	Columns []string
	Rows    [][]string
	Note    string
}

type Database interface {
	Query(ctx context.Context, db, query string) (Table, error)
	Exec(ctx context.Context, db, query string) (int64, error)
	Databases(ctx context.Context) ([]string, error)
	CreateDatabase(ctx context.Context, name string) error
	DropDatabase(ctx context.Context, name string) error
}

type Cache interface {
	Do(ctx context.Context, args ...string) (string, error)
	Info(ctx context.Context) (map[string]string, error)
}

type Queue struct {
	Name      string
	Messages  int
	Ready     int
	Unacked   int
	Consumers int
	InRate    float64
	OutRate   float64
}

type Messaging interface {
	Queues(ctx context.Context) ([]Queue, error)
	Purge(ctx context.Context, queue string) error
	Publish(ctx context.Context, target string, body []byte) error
}

// QueueInspector shows one queue in depth on the Data screen: its settings, bindings and consumers
// as field/value rows, a peek at its messages (they stay queued), and deleting it.
type QueueInspector interface {
	QueueInfo(ctx context.Context, queue string) (Table, error)
	Peek(ctx context.Context, queue string, n int) (Table, error)
	DeleteQueue(ctx context.Context, queue string) error
}

type KV interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// ---- load ----

type Latency struct {
	P50, P95, P99 time.Duration
}

type LoadStatus struct {
	Running bool
	Rate    float64 // configured requests per second
	Sent    int64
	Failed  int64
	Latency Latency
	Extra   map[string]string
	// PerInstance is each generator instance's sent counter, when the generator reports one.
	PerInstance map[string]int64
}

type LoadGenerator interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	SetRate(ctx context.Context, rps float64) error
	Status(ctx context.Context) (LoadStatus, error)
}

// ---- infrastructure ----

type Host struct {
	Name  string
	Addr  string
	Roles []string
	Ready bool
	// Reason says why Ready is false, when the adapter knows (e.g. a probe error); empty otherwise.
	Reason   string
	OS       string
	Kernel   string
	CPUs     int
	CPUUsed  float64 // 0..1
	MemTotal int64
	MemUsed  int64
	// DiskTotal and DiskUsed are the root (or kubelet) filesystem's, when the adapter knows them.
	DiskTotal int64
	DiskUsed  int64
	Load1     float64
	Labels    map[string]string
	// Pods are what runs on the host, when the adapter knows (Kubernetes nodes).
	Pods []HostPod
}

type HostPod struct {
	Namespace string
	Name      string
	CPU       float64 // cores
	Mem       int64
	Started   time.Time
}

type Hosts interface {
	Hosts(ctx context.Context) ([]Host, error)
	// Shell returns a command that opens a shell (or runs command) on the host, ready to attach to a terminal.
	Shell(ctx context.Context, host string, command []string) (*exec.Cmd, error)
}

// ---- optional capabilities ----

type Target struct {
	Service  string
	Instance string
	Port     int
}

// Forwarder makes a port of a service reachable from this machine and returns host:port.
// Forwards live until the runtime is closed.
type Forwarder interface {
	Forward(ctx context.Context, t Target) (string, error)
}

// Querier answers ad hoc queries in its own language: SQL, PromQL, a redis command, ...
// Browser lets a data component be walked as a tree: the rows under a path, whose first column
// names each child, until a leaf (a table's rows, a key's value) ends it.
type Browser interface {
	Browse(ctx context.Context, path []string) (t Table, leaf bool, err error)
}

// PathQuerier runs a query where a Browser walk stands (the database of path[0], say) and suggests one
// for a path: the Data screen's "query this" (a procedure's call with its parameters to fill, say).
type PathQuerier interface {
	QueryAt(ctx context.Context, path []string, q string) (Table, error)
	SuggestQuery(ctx context.Context, path []string) string
}

// RowQuerier suggests a query that reads one row of what a Browse of path returned (Q on a row of the
// Data screen), e.g. a SELECT by its primary key.
type RowQuerier interface {
	SuggestRowQuery(ctx context.Context, path []string, t Table, row int) string
}

// Editor changes what a Browse of path returned in t: Delete removes rows (indexes into t.Rows), Set
// writes one cell. Each refuses a level it cannot change safely.
type Editor interface {
	Delete(ctx context.Context, path []string, t Table, rows []int) error
	Set(ctx context.Context, path []string, t Table, row, col int, value string) error
}

type Querier interface {
	QueryLanguage() string
	RunQuery(ctx context.Context, q string) (Table, error)
}

// Action is an adapter-specific operation, exposed as `rig do <component> <action>`.
type Action struct {
	Name   string
	Help   string
	Mutate bool
	Run    func(ctx context.Context, args []string, out io.Writer) error
}

type Actioner interface {
	Actions() []Action
}

// StateStore keeps small run state (current tag, rates, databases) where the whole team sees it.
type StateStore interface {
	LoadState(ctx context.Context) (map[string]string, error)
	SaveState(ctx context.Context, state map[string]string) error
}

// ImageLoader puts a locally built image where the runtime can run it without a registry.
type ImageLoader interface {
	LoadImage(ctx context.Context, image string) error
}

// Registrar tells builders where this environment pulls images from.
type Registrar interface {
	Registry() string
}

// ProcessLocator gives the host pid of a service, for runtimes whose services are local processes.
type ProcessLocator interface {
	PID(service string) (int, bool)
}

type confirmedKey struct{}

// WithConfirmed marks ctx as carrying the user's go-ahead for destructive actions (--yes, a TUI confirm).
func WithConfirmed(ctx context.Context) context.Context {
	return context.WithValue(ctx, confirmedKey{}, true)
}

func Confirmed(ctx context.Context) bool {
	v, _ := ctx.Value(confirmedKey{}).(bool)
	return v
}

// Relauncher restarts a local service with its command wrapped, e.g. under a debugger.
type Relauncher interface {
	Relaunch(ctx context.Context, s *spec.Service, wrap func(argv []string) []string) error
}

// StatusLister answers the status of many services in one round trip (one kubectl or docker call).
type StatusLister interface {
	StatusAll(ctx context.Context, services []*spec.Service) ([]Status, error)
}

// Bridger makes a service that runs elsewhere (on this machine, in docker) reachable inside the
// runtime under its own name, at the given host:port per service port.
type Bridger interface {
	Bridge(ctx context.Context, s *spec.Service, ports map[int]string) error
}

// LoadConfigured is a load generator whose settings live in a KV key and that runs as services:
// the Load screen edits that key and those services' env.
type LoadConfigured interface {
	ConfigKey() (store, key string)
	Services() []string
}

// LoadScaler is a load generator that runs as replicas: more replicas, more load.
type LoadScaler interface {
	Replicas(ctx context.Context) (int, error)
	SetReplicas(ctx context.Context, n int) error
}

// Pinger reports whether a component is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}
