package contracts

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// Caller reaches a plugin's sources and actions by their namespaced names: the plugin host.
type Caller interface {
	Query(ctx context.Context, name string, args protocol.Args) (*protocol.Frame, error)
	Run(ctx context.Context, name string, args protocol.Args, dryRun bool) (*protocol.RunResult, error)
	Stream(ctx context.Context, name string, args protocol.Args) (<-chan protocol.StreamBatch, error)
}

// optional maps each optional core interface to the capability that serves it.
var optional = []struct {
	iface reflect.Type
	cap   string
}{
	{reflect.TypeFor[core.Querier](), "query"},
	{reflect.TypeFor[core.Pinger](), "ping"},
	{reflect.TypeFor[core.Actioner](), "actions"},
	{reflect.TypeFor[core.Browser](), "browse"},
	{reflect.TypeFor[core.PathQuerier](), "path-query"},
	{reflect.TypeFor[core.RowQuerier](), "row-query"},
	{reflect.TypeFor[core.Editor](), "edit"},
	{reflect.TypeFor[core.Snapshotter](), "snapshot"},
	{reflect.TypeFor[core.Seeder](), "seed"},
	{reflect.TypeFor[core.StateStore](), "state"},
	{reflect.TypeFor[core.Registrar](), "registry"},
	{reflect.TypeFor[core.ImageLoader](), "load-image"},
	{reflect.TypeFor[core.Bridger](), "bridge"},
	{reflect.TypeFor[core.Autoscaler](), "autoscale"},
	{reflect.TypeFor[core.Resourcer](), "resources"},
	{reflect.TypeFor[core.EnvReader](), "env"},
	{reflect.TypeFor[core.Forwarder](), "forward"},
	{reflect.TypeFor[core.Hosts](), "hosts"},
	{reflect.TypeFor[core.Manifester](), "manifests"},
	{reflect.TypeFor[core.Pusher](), "push"},
	{reflect.TypeFor[core.LoadConfigured](), "configured"},
	{reflect.TypeFor[core.LoadScaler](), "scale"},
	{reflect.TypeFor[core.QueueInspector](), "inspect"},
	{reflect.TypeFor[core.KVReader](), "all"},
}

func capOf(iface string) string {
	for _, o := range optional {
		if o.iface.Name() == iface {
			return o.cap
		}
	}
	return ""
}

// Bridge is component, served by plugin through the contract for kind, as the core interface that
// kind's screens and commands use. Every call carries the component's name, so one plugin can serve
// several components. The bridge has every optional interface of the kind, and core.As admits only
// those whose capability pr provides.
func Bridge(kind core.Kind, c Caller, plugin, component string, pr *protocol.Provides) (any, error) {
	ct := ForKind(string(kind))
	if ct == nil {
		return nil, fmt.Errorf("no contract bridges the %s kind", kind)
	}
	if pr == nil {
		pr = &protocol.Provides{}
	}
	b := &base{c: c, plugin: plugin, comp: component, contract: ct, pr: pr, prefix: strings.Split(ct.ID, "/")[0]}
	switch kind {
	case core.KindRuntime:
		return &runtimeBridge{b}, nil
	case core.KindBuilder:
		return &builderBridge{b}, nil
	case core.KindMetrics:
		return &metricsBridge{b}, nil
	case core.KindTracing:
		return &tracesBridge{b}, nil
	case core.KindProfiler:
		return &profilerBridge{b}, nil
	case core.KindDebugger:
		return &debuggerBridge{b}, nil
	case core.KindLogs:
		return &logsBridge{b}, nil
	case core.KindDatabase:
		return &databaseBridge{b}, nil
	case core.KindCache:
		return &cacheBridge{b}, nil
	case core.KindMessaging:
		return &queueBridge{b}, nil
	case core.KindKV:
		return &kvBridge{b}, nil
	case core.KindLoad:
		return &loadBridge{b}, nil
	case core.KindHosts:
		return &hostsBridge{b}, nil
	case core.KindQuery:
		return &queryBridge{b}, nil
	}
	return nil, fmt.Errorf("no contract bridges the %s kind", kind)
}

type base struct {
	c                    Caller
	plugin, comp, prefix string
	contract             *Contract
	pr                   *protocol.Provides

	mu    sync.Mutex
	facts map[string]map[string]any
}

// Has says whether the plugin serves the optional interface named iface; the kind's own is always served.
func (b *base) Has(iface string) bool {
	cp := capOf(iface)
	return cp == "" || b.contract.Has(b.pr, cp)
}

func (b *base) args(a protocol.Args) protocol.Args {
	if a == nil {
		a = protocol.Args{}
	}
	a["component"] = b.comp
	return a
}

func (b *base) rows(ctx context.Context, source string, a protocol.Args) ([]map[string]any, error) {
	f, err := b.c.Query(ctx, b.plugin+"."+source, b.args(a))
	if err != nil {
		return nil, err
	}
	return frames.Rows(f), nil
}

// fact is a source's first row, asked once: what an interface answers without a context.
func (b *base) fact(source string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.facts[source]; ok {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := b.rows(ctx, source, nil)
	if err != nil || len(rows) == 0 {
		return map[string]any{}
	}
	if b.facts == nil {
		b.facts = map[string]map[string]any{}
	}
	b.facts[source] = rows[0]
	return rows[0]
}

func (b *base) run(ctx context.Context, action string, a protocol.Args) (protocol.Args, error) {
	return b.runOut(ctx, nil, action, a)
}

// runOut runs action with what the plugin prints going to out.
func (b *base) runOut(ctx context.Context, out io.Writer, action string, a protocol.Args) (protocol.Args, error) {
	res, err := b.c.Run(jobs.Writer(ctx, out), b.plugin+"."+action, b.args(a), false)
	if err != nil || res == nil {
		return nil, err
	}
	return res.Outputs, nil
}

func (b *base) do(ctx context.Context, action string, a protocol.Args) error {
	_, err := b.run(ctx, action, a)
	return err
}

func (b *base) table(ctx context.Context, source string, a protocol.Args) (core.Table, error) {
	f, err := b.c.Query(ctx, b.plugin+"."+source, b.args(a))
	if err != nil {
		return core.Table{}, err
	}
	return tableOf(f), nil
}

func tableOf(f *protocol.Frame) core.Table {
	var t core.Table
	for _, fl := range f.Fields {
		t.Columns = append(t.Columns, fl.Name)
	}
	for i := range frames.Len(f) {
		row := make([]string, len(f.Fields))
		for j, fl := range f.Fields {
			row[j] = frames.Format(fl, f.Columns[j][i])
		}
		t.Rows = append(t.Rows, row)
	}
	if f.Meta != nil {
		t.Note = f.Meta.Notice
	}
	return t
}

// command is the argv an action answers (outputs.argv, with env and dir), to run on the caller's terminal.
func command(ctx context.Context, out protocol.Args) (*exec.Cmd, error) {
	argv := strs(out["argv"])
	if len(argv) == 0 {
		return nil, fmt.Errorf("the plugin answered no argv to run")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = strs(out["env"]), str(out["dir"])
	return cmd, nil
}

// ---- shared optional interfaces ----

func (b *base) QueryLanguage() string { return str(b.fact(b.prefix + ".query-language")["language"]) }
func (b *base) RunQuery(ctx context.Context, q string) (core.Table, error) {
	return b.table(ctx, b.prefix+".run-query", protocol.Args{"query": q})
}

func (b *base) Ping(ctx context.Context) error {
	_, err := b.rows(ctx, b.prefix+".ping", nil)
	return err
}

func (b *base) Actions() []core.Action {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, _ := b.rows(ctx, b.prefix+".actions", nil)
	var out []core.Action
	for _, r := range rows {
		name := str(r["name"])
		out = append(out, core.Action{Name: name, Help: str(r["help"]), Mutate: r["mutate"] == true,
			Run: func(ctx context.Context, args []string, w io.Writer) error {
				_, err := b.runOut(ctx, w, b.prefix+".run-action", protocol.Args{"name": name, "args": args})
				return err
			}})
	}
	return out
}

func (b *base) Browse(ctx context.Context, path []string) (core.Table, bool, error) {
	f, err := b.c.Query(ctx, b.plugin+"."+b.prefix+".browse", b.args(protocol.Args{"path": path}))
	if err != nil {
		return core.Table{}, false, err
	}
	// A frame named "leaf" holds rows, not further levels.
	return tableOf(f), f.Name == "leaf", nil
}

func (b *base) QueryAt(ctx context.Context, path []string, q string) (core.Table, error) {
	return b.table(ctx, b.prefix+".query-at", protocol.Args{"path": path, "query": q})
}

func (b *base) SuggestQuery(ctx context.Context, path []string) string {
	rows, _ := b.rows(ctx, b.prefix+".suggest", protocol.Args{"path": path})
	if len(rows) == 0 {
		return ""
	}
	return str(rows[0]["query"])
}

func (b *base) SuggestRowQuery(ctx context.Context, path []string, t core.Table, row int) string {
	if row < 0 || row >= len(t.Rows) {
		return ""
	}
	rows, _ := b.rows(ctx, b.prefix+".suggest-row", protocol.Args{"path": path, "columns": t.Columns, "row": t.Rows[row]})
	if len(rows) == 0 {
		return ""
	}
	return str(rows[0]["query"])
}

// Delete sends the chosen rows themselves; the plugin sees a table of just those.
func (b *base) Delete(ctx context.Context, path []string, t core.Table, rows []int) error {
	var sel [][]string
	for _, i := range rows {
		if i >= 0 && i < len(t.Rows) {
			sel = append(sel, t.Rows[i])
		}
	}
	return b.do(ctx, b.prefix+".delete-rows", protocol.Args{"path": path, "columns": t.Columns, "rows": sel})
}

func (b *base) Set(ctx context.Context, path []string, t core.Table, row, col int, value string) error {
	if row < 0 || row >= len(t.Rows) {
		return fmt.Errorf("no row %d", row)
	}
	return b.do(ctx, b.prefix+".set-cell", protocol.Args{"path": path, "columns": t.Columns, "row": t.Rows[row], "col": col, "value": value})
}

func (b *base) Snapshot(ctx context.Context, file string, log io.Writer) error {
	_, err := b.runOut(ctx, log, b.prefix+".snapshot", protocol.Args{"file": file})
	return err
}

func (b *base) Restore(ctx context.Context, file string, log io.Writer) error {
	_, err := b.runOut(ctx, log, b.prefix+".restore", protocol.Args{"file": file})
	return err
}

func (b *base) Seed(ctx context.Context, log io.Writer) error {
	_, err := b.runOut(ctx, log, b.prefix+".seed", nil)
	return err
}

func (b *base) Fresh(ctx context.Context) (bool, error) {
	rows, err := b.rows(ctx, b.prefix+".fresh", nil)
	return len(rows) > 0 && rows[0]["fresh"] == true, err
}

func (b *base) Host() string { return str(b.fact(b.prefix + ".seed-host")["service"]) }

// stream reads log lines from a streaming source: frame rows, or bare lines.
func (b *base) stream(ctx context.Context, source string, a protocol.Args) (<-chan core.LogLine, error) {
	in, err := b.c.Stream(ctx, b.plugin+"."+source, b.args(a))
	if err != nil {
		return nil, err
	}
	out := make(chan core.LogLine, 64)
	go func() {
		defer close(out)
		for batch := range in {
			var lines []core.LogLine
			if batch.Frame != nil {
				for _, row := range frames.Rows(batch.Frame) {
					lines = append(lines, core.LogLine{Time: when(row["time"]), Service: str(row["service"]), Instance: str(row["instance"]), Stream: str(row["stream"]), Text: str(row["text"])})
				}
			}
			for _, l := range batch.Lines {
				lines = append(lines, core.LogLine{Time: time.Now(), Text: l})
			}
			for _, l := range lines {
				select {
				case out <- l:
				case <-ctx.Done():
					return
				}
			}
			if batch.Done || batch.Error != nil {
				return
			}
		}
	}()
	return out, nil
}

// ---- runtime ----

type runtimeBridge struct{ *base }

func (r *runtimeBridge) Discover(ctx context.Context) ([]core.Workload, error) {
	rows, err := r.rows(ctx, "runtime.discover", nil)
	var out []core.Workload
	for _, w := range rows {
		out = append(out, core.Workload{Name: str(w["name"]), Kind: str(w["kind"]), Service: str(w["service"]), Status: core.Status{State: core.State(str(w["state"]))}})
	}
	return out, err
}

func (r *runtimeBridge) svc(ctx context.Context, action string, s *spec.Service, a protocol.Args) error {
	if a == nil {
		a = protocol.Args{}
	}
	a["service"] = s.Name
	return r.do(ctx, action, a)
}

func (r *runtimeBridge) Start(ctx context.Context, s *spec.Service) error {
	return r.svc(ctx, "runtime.start", s, nil)
}
func (r *runtimeBridge) Stop(ctx context.Context, s *spec.Service) error {
	return r.svc(ctx, "runtime.stop", s, nil)
}
func (r *runtimeBridge) Restart(ctx context.Context, s *spec.Service) error {
	return r.svc(ctx, "runtime.restart", s, nil)
}
func (r *runtimeBridge) Scale(ctx context.Context, s *spec.Service, n int) error {
	return r.svc(ctx, "runtime.scale", s, protocol.Args{"replicas": n})
}

func (r *runtimeBridge) Deploy(ctx context.Context, s *spec.Service, rel core.Release) error {
	a := protocol.Args{"image": rel.Image, "replicas": rel.Replicas}
	if rel.Env != nil {
		a["env"] = rel.Env
	}
	return r.svc(ctx, "runtime.deploy", s, a)
}

func (r *runtimeBridge) Status(ctx context.Context, s *spec.Service) (core.Status, error) {
	st, err := r.StatusAll(ctx, []*spec.Service{s})
	if err != nil {
		return core.Status{Service: s.Name, State: core.StateUnknown, Message: err.Error()}, err
	}
	return st[0], nil
}

// StatusAll is one runtime.status call; a service the plugin does not answer for is absent.
func (r *runtimeBridge) StatusAll(ctx context.Context, services []*spec.Service) ([]core.Status, error) {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.Name
	}
	rows, err := r.rows(ctx, "runtime.status", protocol.Args{"services": names})
	if err != nil {
		return nil, err
	}
	by := map[string]core.Status{}
	for _, row := range rows {
		by[str(row["service"])] = statusOf(row)
	}
	out := make([]core.Status, len(services))
	for i, n := range names {
		st, ok := by[n]
		if !ok {
			st = core.Status{Service: n, State: core.StateAbsent}
		}
		out[i] = st
	}
	return out, nil
}

func statusOf(row map[string]any) core.Status {
	st := core.Status{Service: str(row["service"]), State: core.State(str(row["state"])), Ready: integer(row["ready"]),
		Desired: integer(row["desired"]), Image: str(row["image"]), Message: str(row["message"])}
	decode(row["instances"], &st.Instances)
	decode(row["autoscale"], &st.Autoscale)
	return st
}

func (r *runtimeBridge) Logs(ctx context.Context, s *spec.Service, o core.LogOptions) (<-chan core.LogLine, error) {
	return r.stream(ctx, "runtime.logs", protocol.Args{"service": s.Name, "instance": o.Instance, "since": o.Since.Seconds(), "tail": o.Tail, "follow": o.Follow})
}

func (r *runtimeBridge) Exec(ctx context.Context, s *spec.Service, o core.ExecOptions) error {
	out, err := r.run(ctx, "runtime.exec", protocol.Args{"service": s.Name, "instance": o.Instance, "command": o.Command, "tty": o.TTY})
	if err != nil {
		return err
	}
	cmd, err := command(ctx, out)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
	return cmd.Run()
}

func (r *runtimeBridge) LoadState(ctx context.Context) (map[string]string, error) {
	rows, err := r.rows(ctx, "runtime.state", nil)
	out := map[string]string{}
	for _, row := range rows {
		out[str(row["key"])] = str(row["value"])
	}
	return out, err
}

func (r *runtimeBridge) SaveState(ctx context.Context, state map[string]string) error {
	return r.do(ctx, "runtime.save-state", protocol.Args{"state": state})
}

func (r *runtimeBridge) Registry() string { return str(r.fact("runtime.registry")["registry"]) }

func (r *runtimeBridge) LoadImage(ctx context.Context, image string) error {
	return r.do(ctx, "runtime.load-image", protocol.Args{"image": image})
}

func (r *runtimeBridge) Bridge(ctx context.Context, s *spec.Service, ports map[int]string) error {
	p := map[string]string{}
	for k, v := range ports {
		p[strconv.Itoa(k)] = v
	}
	return r.svc(ctx, "runtime.bridge", s, protocol.Args{"ports": p})
}

func (r *runtimeBridge) SetAutoscale(ctx context.Context, s *spec.Service, b core.Bounds) error {
	return r.svc(ctx, "runtime.autoscale", s, protocol.Args{"min": b.Min, "max": b.Max, "cpu": b.CPU, "memory": b.Memory})
}

func (r *runtimeBridge) Resources(ctx context.Context, s *spec.Service) ([]core.Resources, error) {
	rows, err := r.rows(ctx, "runtime.resources", protocol.Args{"service": s.Name})
	var out []core.Resources
	for _, row := range rows {
		out = append(out, core.Resources{Container: str(row["container"]), CPURequest: str(row["cpu_request"]), CPULimit: str(row["cpu_limit"]),
			MemRequest: str(row["mem_request"]), MemLimit: str(row["mem_limit"])})
	}
	return out, err
}

func (r *runtimeBridge) SetResources(ctx context.Context, s *spec.Service, res core.Resources) error {
	return r.svc(ctx, "runtime.set-resources", s, protocol.Args{"container": res.Container, "cpu_request": res.CPURequest,
		"cpu_limit": res.CPULimit, "mem_request": res.MemRequest, "mem_limit": res.MemLimit})
}

func (r *runtimeBridge) LiveEnv(ctx context.Context, s *spec.Service) (map[string]string, error) {
	rows, err := r.rows(ctx, "runtime.env", protocol.Args{"service": s.Name})
	out := map[string]string{}
	for _, row := range rows {
		out[str(row["name"])] = str(row["value"])
	}
	return out, err
}

func (r *runtimeBridge) Forward(ctx context.Context, t core.Target) (string, error) {
	out, err := r.run(ctx, "runtime.forward", protocol.Args{"service": t.Service, "instance": t.Instance, "port": t.Port})
	return str(out["addr"]), err
}

func (r *runtimeBridge) Hosts(ctx context.Context) ([]core.Host, error) {
	rows, err := r.rows(ctx, "runtime.hosts", nil)
	return hostsOf(rows), err
}

func (r *runtimeBridge) Shell(ctx context.Context, host string, cmd []string) (*exec.Cmd, error) {
	out, err := r.run(ctx, "runtime.shell", protocol.Args{"host": host, "command": cmd})
	if err != nil {
		return nil, err
	}
	return command(ctx, out)
}

func (r *runtimeBridge) Objects(s *spec.Service) ([]*manifest.Object, *manifest.Object, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := r.rows(ctx, "runtime.objects", protocol.Args{"service": s.Name})
	if err != nil || len(rows) == 0 {
		return nil, nil, err
	}
	var files []string
	for _, row := range rows {
		if f := str(row["file"]); !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	set, err := manifest.Scan(files...)
	if err != nil {
		return nil, nil, err
	}
	var objs []*manifest.Object
	var w *manifest.Object
	for _, row := range rows {
		for _, o := range set.Objects {
			if o.File == str(row["file"]) && o.Doc == integer(row["doc"]) {
				objs = append(objs, o)
				if row["workload"] == true {
					w = o
				}
			}
		}
	}
	return objs, w, nil
}

func (r *runtimeBridge) LiveObject(ctx context.Context, kind, name string) ([]byte, error) {
	rows, err := r.rows(ctx, "runtime.live-object", protocol.Args{"kind": kind, "name": name})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return []byte(str(rows[0]["json"])), nil
}

func (r *runtimeBridge) LiveObjects(ctx context.Context) ([]byte, error) {
	rows, err := r.rows(ctx, "runtime.live-objects", nil)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return []byte(str(rows[0]["yaml"])), nil
}

func (r *runtimeBridge) InjectedEnv(s *spec.Service) map[string]bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, _ := r.rows(ctx, "runtime.injected-env", protocol.Args{"service": s.Name})
	out := map[string]bool{}
	for _, row := range rows {
		out[str(row["name"])] = true
	}
	return out
}

func (r *runtimeBridge) KubectlArgs() []string { return strs(r.fact("runtime.kubectl-args")["args"]) }

func (r *runtimeBridge) ApplyManifests(ctx context.Context, objs []*manifest.Object) error {
	var docs []map[string]any
	for _, o := range objs {
		y, err := manifest.Encode([]*manifest.Object{o})
		if err != nil {
			return err
		}
		docs = append(docs, map[string]any{"file": o.File, "doc": o.Doc, "yaml": string(y)})
	}
	return r.do(ctx, "runtime.apply", protocol.Args{"objects": docs})
}

// ---- builder ----

type builderBridge struct{ *base }

func (b *builderBridge) Build(ctx context.Context, s *spec.Service, o core.BuildOptions) (string, error) {
	out, err := b.runOut(ctx, o.Out, "builder.build", protocol.Args{"service": s.Name, "tag": o.Tag, "registry": o.Registry, "push": o.Push, "dir": o.Dir})
	return str(out["image"]), err
}

func (b *builderBridge) Push(ctx context.Context, image string, w io.Writer) error {
	_, err := b.runOut(ctx, w, "builder.push", protocol.Args{"image": image})
	return err
}

// ---- observability ----

type metricsBridge struct{ *base }

func (m *metricsBridge) Instant(ctx context.Context, q string) ([]core.Sample, error) {
	rows, err := m.rows(ctx, "metrics.instant", protocol.Args{"query": q})
	var out []core.Sample
	for _, r := range rows {
		out = append(out, core.Sample{Labels: labels(r), Value: number(r["value"])})
	}
	return out, err
}

func (m *metricsBridge) Range(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]core.Series, error) {
	rows, err := m.rows(ctx, "metrics.range", protocol.Args{"query": q, "start": seconds(start), "end": seconds(end), "step": step.Seconds()})
	var out []core.Series
	at := map[string]int{}
	for _, r := range rows {
		k := str(r["series"])
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, core.Series{Labels: labels(r)})
		}
		out[i].Points = append(out[i].Points, core.Point{T: when(r["time"]), V: number(r["value"])})
	}
	return out, err
}

func labels(r map[string]any) map[string]string {
	out := map[string]string{}
	decode(r["labels"], &out)
	if len(out) == 0 && str(r["series"]) != "" {
		out["series"] = str(r["series"])
	}
	return out
}

type tracesBridge struct{ *base }

func (t *tracesBridge) Services(ctx context.Context) ([]string, error) {
	rows, err := t.rows(ctx, "traces.services", nil)
	var out []string
	for _, r := range rows {
		out = append(out, str(r["service"]))
	}
	return out, err
}

func (t *tracesBridge) Search(ctx context.Context, q core.TraceQuery) ([]core.TraceSummary, error) {
	rows, err := t.rows(ctx, "traces.search", protocol.Args{"service": q.Service, "operation": q.Operation,
		"min_duration": q.MinDuration.Seconds(), "lookback": q.Lookback.Seconds(), "limit": q.Limit})
	var out []core.TraceSummary
	for _, r := range rows {
		out = append(out, core.TraceSummary{ID: str(r["id"]), Root: str(r["root"]), Start: when(r["start"]), Duration: duration(r["duration"]),
			Spans: integer(r["spans"]), Services: strs(r["services"]), Error: r["error"] == true})
	}
	return out, err
}

func (t *tracesBridge) Trace(ctx context.Context, id string) ([]core.Span, error) {
	rows, err := t.rows(ctx, "traces.get", protocol.Args{"id": id})
	var out []core.Span
	for _, r := range rows {
		var tags map[string]string
		decode(r["tags"], &tags)
		out = append(out, core.Span{TraceID: cmp.Or(str(r["trace"]), id), ID: str(r["id"]), Parent: str(r["parent"]), Service: str(r["service"]),
			Name: str(r["name"]), Start: when(r["start"]), Duration: duration(r["duration"]), Error: r["error"] == true, Tags: tags})
	}
	return out, err
}

type profilerBridge struct{ *base }

func (p *profilerBridge) Kinds() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, _ := p.rows(ctx, "profiles.kinds", nil)
	var out []string
	for _, r := range rows {
		out = append(out, str(r["kind"]))
	}
	return out
}

func (p *profilerBridge) Capture(ctx context.Context, r core.ProfileRequest) (core.Profile, error) {
	out, err := p.run(ctx, "profiles.capture", protocol.Args{"service": r.Service.Name, "instance": r.Instance, "kind": r.Kind, "duration": r.Duration.Seconds(), "dir": r.OutDir})
	return core.Profile{Kind: cmp.Or(str(out["kind"]), r.Kind), Format: str(out["format"]), File: str(out["file"]), Summary: str(out["summary"])}, err
}

type debuggerBridge struct{ *base }

func (d *debuggerBridge) Attach(ctx context.Context, s *spec.Service, instance string) (core.DebugSession, error) {
	out, err := d.run(ctx, "debugger.attach", protocol.Args{"service": s.Name, "instance": instance})
	if err != nil {
		return core.DebugSession{}, err
	}
	return core.DebugSession{Addr: str(out["addr"]), Hint: str(out["hint"]), Close: func() error {
		return d.do(context.Background(), "debugger.detach", protocol.Args{"service": s.Name})
	}}, nil
}

type logsBridge struct{ *base }

func (l *logsBridge) Logs(ctx context.Context, q core.LogQuery) (<-chan core.LogLine, error) {
	return l.stream(ctx, "logs.query", protocol.Args{"services": q.Services, "instance": q.Instance, "since": q.Since.Seconds(),
		"tail": q.Tail, "follow": q.Follow, "match": q.Match, "regex": q.Regex})
}

// ---- data ----

type databaseBridge struct{ *base }

// Has: a database always answers queries, through database.query.
func (d *databaseBridge) Has(iface string) bool {
	return iface == "Querier" || d.base.Has(iface)
}

func (d *databaseBridge) Query(ctx context.Context, db, q string) (core.Table, error) {
	return d.table(ctx, "database.query", protocol.Args{"database": db, "query": q})
}

func (d *databaseBridge) Exec(ctx context.Context, db, q string) (int64, error) {
	out, err := d.run(ctx, "database.exec", protocol.Args{"database": db, "query": q})
	return int64(number(out["rows"])), err
}

func (d *databaseBridge) Databases(ctx context.Context) ([]string, error) {
	rows, err := d.rows(ctx, "database.list", nil)
	var out []string
	for _, r := range rows {
		out = append(out, str(r["database"]))
	}
	return out, err
}

func (d *databaseBridge) CreateDatabase(ctx context.Context, name string) error {
	return d.do(ctx, "database.create", protocol.Args{"name": name})
}

func (d *databaseBridge) DropDatabase(ctx context.Context, name string) error {
	return d.do(ctx, "database.drop", protocol.Args{"name": name})
}

func (d *databaseBridge) QueryLanguage() string {
	if d.base.Has("Querier") {
		return d.base.QueryLanguage()
	}
	return "sql"
}

// RunQuery runs q on the default database; "@name <sql>" picks another.
func (d *databaseBridge) RunQuery(ctx context.Context, q string) (core.Table, error) {
	db := ""
	if rest, ok := strings.CutPrefix(strings.TrimSpace(q), "@"); ok {
		db, q, _ = strings.Cut(rest, " ")
	}
	return d.Query(ctx, db, q)
}

type cacheBridge struct{ *base }

func (c *cacheBridge) Do(ctx context.Context, args ...string) (string, error) {
	out, err := c.run(ctx, "cache.do", protocol.Args{"args": args})
	return str(out["reply"]), err
}

func (c *cacheBridge) Info(ctx context.Context) (map[string]string, error) {
	rows, err := c.rows(ctx, "cache.info", nil)
	out := map[string]string{}
	for _, r := range rows {
		out[str(r["key"])] = str(r["value"])
	}
	return out, err
}

type queueBridge struct{ *base }

func (q *queueBridge) Queues(ctx context.Context) ([]core.Queue, error) {
	rows, err := q.rows(ctx, "queue.list", nil)
	var out []core.Queue
	for _, r := range rows {
		out = append(out, core.Queue{Name: str(r["name"]), Messages: integer(r["messages"]), Ready: integer(r["ready"]), Unacked: integer(r["unacked"]),
			Consumers: integer(r["consumers"]), InRate: number(r["in_rate"]), OutRate: number(r["out_rate"])})
	}
	return out, err
}

func (q *queueBridge) Purge(ctx context.Context, queue string) error {
	return q.do(ctx, "queue.purge", protocol.Args{"queue": queue})
}

func (q *queueBridge) Publish(ctx context.Context, target string, body []byte) error {
	return q.do(ctx, "queue.publish", protocol.Args{"target": target, "body": string(body)})
}

func (q *queueBridge) QueueInfo(ctx context.Context, queue string) (core.Table, error) {
	return q.table(ctx, "queue.info", protocol.Args{"queue": queue})
}

func (q *queueBridge) Peek(ctx context.Context, queue string, n int) (core.Table, error) {
	return q.table(ctx, "queue.peek", protocol.Args{"queue": queue, "n": n})
}

func (q *queueBridge) DeleteQueue(ctx context.Context, queue string) error {
	return q.do(ctx, "queue.delete", protocol.Args{"queue": queue})
}

type kvBridge struct{ *base }

func (k *kvBridge) Get(ctx context.Context, key string) ([]byte, bool, error) {
	rows, err := k.rows(ctx, "kv.get", protocol.Args{"key": key})
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	return []byte(str(rows[0]["value"])), rows[0]["found"] != false, nil
}

func (k *kvBridge) Put(ctx context.Context, key string, value []byte) error {
	return k.do(ctx, "kv.put", protocol.Args{"key": key, "value": string(value)})
}

func (k *kvBridge) Delete(ctx context.Context, key string) error {
	return k.do(ctx, "kv.delete", protocol.Args{"key": key})
}

func (k *kvBridge) List(ctx context.Context, prefix string) ([]string, error) {
	rows, err := k.rows(ctx, "kv.list", protocol.Args{"prefix": prefix})
	var out []string
	for _, r := range rows {
		out = append(out, str(r["key"]))
	}
	return out, err
}

func (k *kvBridge) All(ctx context.Context, prefix string) (map[string][]byte, error) {
	rows, err := k.rows(ctx, "kv.all", protocol.Args{"prefix": prefix})
	out := map[string][]byte{}
	for _, r := range rows {
		out[str(r["key"])] = []byte(str(r["value"]))
	}
	return out, err
}

type loadBridge struct{ *base }

func (l *loadBridge) Start(ctx context.Context) error { return l.do(ctx, "load.start", nil) }
func (l *loadBridge) Stop(ctx context.Context) error  { return l.do(ctx, "load.stop", nil) }
func (l *loadBridge) SetRate(ctx context.Context, rps float64) error {
	return l.do(ctx, "load.rate", protocol.Args{"rps": rps})
}

func (l *loadBridge) Status(ctx context.Context) (core.LoadStatus, error) {
	rows, err := l.rows(ctx, "load.status", nil)
	if err != nil || len(rows) == 0 {
		return core.LoadStatus{}, err
	}
	r := rows[0]
	st := core.LoadStatus{Running: r["running"] == true, Rate: number(r["rate"]), Sent: int64(number(r["sent"])), Failed: int64(number(r["failed"])),
		Latency: core.Latency{P50: duration(r["p50"]), P95: duration(r["p95"]), P99: duration(r["p99"])}}
	decode(r["extra"], &st.Extra)
	decode(r["per_instance"], &st.PerInstance)
	return st, nil
}

func (l *loadBridge) ConfigKey() (string, string) {
	f := l.fact("load.config")
	return str(f["store"]), str(f["key"])
}

func (l *loadBridge) Services() []string { return strs(l.fact("load.config")["services"]) }

func (l *loadBridge) Replicas(ctx context.Context) (int, error) {
	rows, err := l.rows(ctx, "load.replicas", nil)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return integer(rows[0]["replicas"]), nil
}

func (l *loadBridge) SetReplicas(ctx context.Context, n int) error {
	return l.do(ctx, "load.set-replicas", protocol.Args{"replicas": n})
}

// ---- machines ----

type hostsBridge struct{ *base }

func (h *hostsBridge) Hosts(ctx context.Context) ([]core.Host, error) {
	rows, err := h.rows(ctx, "hosts.list", nil)
	return hostsOf(rows), err
}

func hostsOf(rows []map[string]any) []core.Host {
	var out []core.Host
	for _, r := range rows {
		h := core.Host{Name: str(r["name"]), Addr: str(r["addr"]), Roles: strs(r["roles"]), Ready: r["ready"] == true, Reason: str(r["reason"]),
			OS: str(r["os"]), Kernel: str(r["kernel"]), CPUs: integer(r["cpus"]), CPUUsed: number(r["cpu_used"]), MemTotal: int64(number(r["mem_total"])),
			MemUsed: int64(number(r["mem_used"])), DiskTotal: int64(number(r["disk_total"])), DiskUsed: int64(number(r["disk_used"])), Load1: number(r["load1"])}
		decode(r["labels"], &h.Labels)
		decode(r["pods"], &h.Pods)
		out = append(out, h)
	}
	return out
}

func (h *hostsBridge) Shell(ctx context.Context, host string, cmd []string) (*exec.Cmd, error) {
	out, err := h.run(ctx, "hosts.shell", protocol.Args{"host": host, "command": cmd})
	if err != nil {
		return nil, err
	}
	return command(ctx, out)
}

type queryBridge struct{ *base }

// Has: answering queries is the query kind itself.
func (q *queryBridge) Has(iface string) bool { return iface == "Querier" || q.base.Has(iface) }

// ---- values off the wire: JSON numbers, times as Unix seconds (or RFC 3339), durations as seconds (or Go's form) ----

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	return fmt.Sprint(v)
}

func number(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func integer(v any) int { return int(number(v)) }

func seconds(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixNano()) / 1e9
}

func when(v any) time.Time {
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	if n := number(v); n != 0 {
		return time.Unix(0, int64(n*1e9))
	}
	return time.Time{}
}

func duration(v any) time.Duration {
	if s, ok := v.(string); ok {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	return time.Duration(number(v) * float64(time.Second))
}

func strs(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, str(x))
		}
		return out
	}
	return nil
}

// decode fills out from a JSON value, matching keys to Go field names without regard to case.
func decode(v, out any) {
	if v == nil {
		return
	}
	b, err := json.Marshal(v)
	if err == nil {
		_ = json.Unmarshal(b, out)
	}
}
