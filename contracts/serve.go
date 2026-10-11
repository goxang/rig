package contracts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

type (
	SourceFunc func(ctx context.Context, a protocol.Args) (*protocol.Frame, error)
	StreamFunc func(ctx context.Context, a protocol.Args, emit func(*protocol.Frame) error) error
	ActionFunc func(ctx context.Context, a protocol.Args, out io.Writer) (protocol.Args, error)
)

// Served is a component kind's side of its contract, the reverse of Bridge: what a plugin registers
// (names without the plugin prefix) to stand in for components it builds in-process.
type Served struct {
	Contract *Contract
	Sources  map[string]SourceFunc
	Streams  map[string]StreamFunc
	Actions  map[string]ActionFunc
	Effects  map[string]string
}

// Lookup finds what a call names: its component (args.component) and services.
type Lookup struct {
	Component func(ctx context.Context, name string) (any, error)
	Service   func(name string) (*spec.Service, error)
}

// Captured is the command an attach would have run on a terminal, caught instead of run.
type Captured struct {
	Argv, Env []string
	Dir       string
}

type captureKey struct{}

// WithCapture asks an adapter to fill the returned Captured and return instead of running an
// interactive command (exec, shell): the plugin serving it has no terminal, the caller does.
func WithCapture(ctx context.Context) (context.Context, *Captured) {
	c := &Captured{}
	return context.WithValue(ctx, captureKey{}, c), c
}

// CaptureOf is the Captured to fill, or nil when ctx asks to run the command.
func CaptureOf(ctx context.Context) *Captured {
	c, _ := ctx.Value(captureKey{}).(*Captured)
	return c
}

// Serve is the contract for kind served by components that look like probe (a nil pointer of the
// adapter's type will do): only the optional interfaces probe's type has are served.
func Serve(kind core.Kind, probe any, l Lookup) (*Served, error) {
	ct := ForKind(string(kind))
	if ct == nil {
		return nil, fmt.Errorf("no contract serves the %s kind", kind)
	}
	s := &server{Served: Served{Contract: ct, Sources: map[string]SourceFunc{}, Streams: map[string]StreamFunc{}, Actions: map[string]ActionFunc{}, Effects: map[string]string{}},
		ct: ct, l: l, p: strings.Split(ct.ID, "/")[0]}
	s.shared(probe)
	switch kind {
	case core.KindRuntime:
		s.runtime(probe)
	case core.KindBuilder:
		s.builder(probe)
	case core.KindMetrics:
		s.metrics()
	case core.KindTracing:
		s.traces()
	case core.KindProfiler:
		s.profiler()
	case core.KindDebugger:
		s.debugger()
	case core.KindLogs:
		s.logs()
	case core.KindDatabase:
		s.database()
	case core.KindCache:
		s.cache()
	case core.KindMessaging:
		s.queue(probe)
	case core.KindKV:
		s.kv(probe)
	case core.KindLoad:
		s.load(probe)
	case core.KindHosts:
		s.hosts()
	}
	return &s.Served, nil
}

type server struct {
	Served
	ct *Contract
	l  Lookup
	p  string
}

func is[T any](probe any) bool { _, ok := probe.(T); return ok }

func component[T any](ctx context.Context, l Lookup, a protocol.Args) (T, error) {
	var zero T
	v, err := l.Component(ctx, str(a["component"]))
	if err != nil {
		return zero, err
	}
	c, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("component %q (%T) does not serve this", str(a["component"]), v)
	}
	return c, nil
}

func src[T any](s *server, name string, fn func(ctx context.Context, c T, a protocol.Args) (*protocol.Frame, error)) {
	s.Sources[s.p+"."+name] = func(ctx context.Context, a protocol.Args) (*protocol.Frame, error) {
		c, err := component[T](ctx, s.l, a)
		if err != nil {
			return nil, err
		}
		return fn(ctx, c, a)
	}
}

func stream[T any](s *server, name string, fn func(ctx context.Context, c T, a protocol.Args) (<-chan core.LogLine, error)) {
	s.Streams[s.p+"."+name] = func(ctx context.Context, a protocol.Args, emit func(*protocol.Frame) error) error {
		c, err := component[T](ctx, s.l, a)
		if err != nil {
			return err
		}
		ch, err := fn(ctx, c, a)
		if err != nil {
			return err
		}
		for l := range ch {
			if err := emit(frame("time:time service:string instance:string stream:string text:string", []any{seconds(l.Time), l.Service, l.Instance, l.Stream, l.Text})); err != nil {
				return err
			}
		}
		return nil
	}
}

func act[T any](s *server, name string, fn func(ctx context.Context, c T, a protocol.Args, out io.Writer) (protocol.Args, error)) {
	full := s.p + "." + name
	s.Effects[full] = s.ct.Actions[full].Effect
	s.Actions[full] = func(ctx context.Context, a protocol.Args, out io.Writer) (protocol.Args, error) {
		c, err := component[T](ctx, s.l, a)
		if err != nil {
			return nil, err
		}
		return fn(ctx, c, a, out)
	}
}

func (s *server) service(a protocol.Args) (*spec.Service, error) {
	return s.l.Service(str(a["service"]))
}

// frame builds a frame from "name:type ..." and rows of values in that order.
func frame(fields string, rows ...[]any) *protocol.Frame {
	var fs []protocol.Field
	for _, f := range strings.Fields(fields) {
		n, t, _ := strings.Cut(f, ":")
		fs = append(fs, protocol.Field{Name: n, Type: t})
	}
	f := &protocol.Frame{Fields: fs, Columns: make([][]any, len(fs))}
	for j := range fs {
		f.Columns[j] = make([]any, len(rows))
		for i, r := range rows {
			f.Columns[j][i] = r[j]
		}
	}
	return f
}

func tableFrame(t core.Table) *protocol.Frame {
	f := &protocol.Frame{Columns: make([][]any, len(t.Columns))}
	for j, c := range t.Columns {
		f.Fields = append(f.Fields, protocol.Field{Name: c, Type: protocol.TypeString})
		f.Columns[j] = make([]any, len(t.Rows))
		for i, r := range t.Rows {
			if j < len(r) {
				f.Columns[j][i] = r[j]
			}
		}
	}
	if t.Note != "" {
		f.Meta = &protocol.FrameMeta{Notice: t.Note}
	}
	return f
}

func tableArg(a protocol.Args) core.Table {
	t := core.Table{Columns: strs(a["columns"])}
	decode(a["rows"], &t.Rows)
	return t
}

// rowTable is a one-row table holding args.row: what row-wise calls send for the row they name.
func rowTable(a protocol.Args) core.Table {
	return core.Table{Columns: strs(a["columns"]), Rows: [][]string{strs(a["row"])}}
}

func kvFrame(m map[string]string, k, v string) *protocol.Frame {
	f := frame(k + ":string " + v + ":string")
	for _, key := range sortedKeys(m) {
		f.Columns[0] = append(f.Columns[0], key)
		f.Columns[1] = append(f.Columns[1], m[key])
	}
	return f
}

func none(err error) (protocol.Args, error) { return nil, err }

func cmdOut(argv, env []string, dir string) protocol.Args {
	return protocol.Args{"argv": argv, "env": env, "dir": dir}
}

func (s *server) shared(probe any) {
	if is[core.Querier](probe) {
		src(s, "query-language", func(_ context.Context, c core.Querier, _ protocol.Args) (*protocol.Frame, error) {
			return frame("language:string", []any{c.QueryLanguage()}), nil
		})
		src(s, "run-query", func(ctx context.Context, c core.Querier, a protocol.Args) (*protocol.Frame, error) {
			t, err := c.RunQuery(ctx, str(a["query"]))
			return tableFrame(t), err
		})
	}
	if is[core.Pinger](probe) {
		src(s, "ping", func(ctx context.Context, c core.Pinger, _ protocol.Args) (*protocol.Frame, error) {
			return frame(""), c.Ping(ctx)
		})
	}
	if is[core.Actioner](probe) {
		src(s, "actions", func(_ context.Context, c core.Actioner, _ protocol.Args) (*protocol.Frame, error) {
			f := frame("name:string help:string mutate:bool")
			for _, x := range c.Actions() {
				f.Columns[0], f.Columns[1], f.Columns[2] = append(f.Columns[0], x.Name), append(f.Columns[1], x.Help), append(f.Columns[2], x.Mutate)
			}
			return f, nil
		})
		act(s, "run-action", func(ctx context.Context, c core.Actioner, a protocol.Args, out io.Writer) (protocol.Args, error) {
			for _, x := range c.Actions() {
				if x.Name == str(a["name"]) {
					return none(x.Run(ctx, strs(a["args"]), out))
				}
			}
			return nil, fmt.Errorf("no action %q", str(a["name"]))
		})
	}
	if is[core.Browser](probe) {
		src(s, "browse", func(ctx context.Context, c core.Browser, a protocol.Args) (*protocol.Frame, error) {
			t, leaf, err := c.Browse(ctx, strs(a["path"]))
			f := tableFrame(t)
			if leaf {
				f.Name = "leaf"
			}
			return f, err
		})
	}
	if is[core.PathQuerier](probe) {
		src(s, "query-at", func(ctx context.Context, c core.PathQuerier, a protocol.Args) (*protocol.Frame, error) {
			t, err := c.QueryAt(ctx, strs(a["path"]), str(a["query"]))
			return tableFrame(t), err
		})
		src(s, "suggest", func(ctx context.Context, c core.PathQuerier, a protocol.Args) (*protocol.Frame, error) {
			return frame("query:string", []any{c.SuggestQuery(ctx, strs(a["path"]))}), nil
		})
	}
	if is[core.RowQuerier](probe) {
		src(s, "suggest-row", func(ctx context.Context, c core.RowQuerier, a protocol.Args) (*protocol.Frame, error) {
			return frame("query:string", []any{c.SuggestRowQuery(ctx, strs(a["path"]), rowTable(a), 0)}), nil
		})
	}
	if is[core.Editor](probe) {
		act(s, "delete-rows", func(ctx context.Context, c core.Editor, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			t := tableArg(a)
			rows := make([]int, len(t.Rows))
			for i := range rows {
				rows[i] = i
			}
			return none(c.Delete(ctx, strs(a["path"]), t, rows))
		})
		act(s, "set-cell", func(ctx context.Context, c core.Editor, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(c.Set(ctx, strs(a["path"]), rowTable(a), 0, integer(a["col"]), str(a["value"])))
		})
	}
	if is[core.Snapshotter](probe) {
		act(s, "snapshot", func(ctx context.Context, c core.Snapshotter, a protocol.Args, out io.Writer) (protocol.Args, error) {
			return none(c.Snapshot(ctx, str(a["file"]), out))
		})
		act(s, "restore", func(ctx context.Context, c core.Snapshotter, a protocol.Args, out io.Writer) (protocol.Args, error) {
			return none(c.Restore(ctx, str(a["file"]), out))
		})
	}
	if is[core.Seeder](probe) {
		act(s, "seed", func(ctx context.Context, c core.Seeder, _ protocol.Args, out io.Writer) (protocol.Args, error) {
			return none(c.Seed(ctx, out))
		})
		src(s, "fresh", func(ctx context.Context, c core.Seeder, _ protocol.Args) (*protocol.Frame, error) {
			ok, err := c.Fresh(ctx)
			return frame("fresh:bool", []any{ok}), err
		})
		src(s, "seed-host", func(_ context.Context, c core.Seeder, _ protocol.Args) (*protocol.Frame, error) {
			return frame("service:string", []any{c.Host()}), nil
		})
	}
}

func statusRow(st core.Status) []any {
	return []any{st.Service, string(st.State), st.Ready, st.Desired, st.Image, st.Message, st.Instances, st.Autoscale}
}

const statusFields = "service:string state:string ready:int desired:int image:string message:string instances:json autoscale:json"

func hostsFrame(hs []core.Host) *protocol.Frame {
	f := frame("name:string addr:string roles:json ready:bool reason:string os:string kernel:string cpus:int cpu_used:number mem_total:int mem_used:int disk_total:int disk_used:int load1:number labels:json pods:json")
	for _, h := range hs {
		for j, v := range []any{h.Name, h.Addr, h.Roles, h.Ready, h.Reason, h.OS, h.Kernel, h.CPUs, h.CPUUsed, h.MemTotal, h.MemUsed, h.DiskTotal, h.DiskUsed, h.Load1, h.Labels, h.Pods} {
			f.Columns[j] = append(f.Columns[j], v)
		}
	}
	return f
}

func svcAct[T any](s *server, name string, fn func(ctx context.Context, c T, sv *spec.Service, a protocol.Args, out io.Writer) (protocol.Args, error)) {
	act(s, name, func(ctx context.Context, c T, a protocol.Args, out io.Writer) (protocol.Args, error) {
		sv, err := s.service(a)
		if err != nil {
			return nil, err
		}
		return fn(ctx, c, sv, a, out)
	})
}

func svcSrc[T any](s *server, name string, fn func(ctx context.Context, c T, sv *spec.Service, a protocol.Args) (*protocol.Frame, error)) {
	src(s, name, func(ctx context.Context, c T, a protocol.Args) (*protocol.Frame, error) {
		sv, err := s.service(a)
		if err != nil {
			return nil, err
		}
		return fn(ctx, c, sv, a)
	})
}

func (s *server) runtime(probe any) {
	src(s, "status", func(ctx context.Context, c core.Runtime, a protocol.Args) (*protocol.Frame, error) {
		var svcs []*spec.Service
		for _, n := range strs(a["services"]) {
			sv, err := s.l.Service(n)
			if err != nil {
				return nil, err
			}
			svcs = append(svcs, sv)
		}
		var sts []core.Status
		if l, ok := c.(core.StatusLister); ok {
			var err error
			if sts, err = l.StatusAll(ctx, svcs); err != nil {
				return nil, err
			}
		} else {
			for _, sv := range svcs {
				st, err := c.Status(ctx, sv)
				if err != nil {
					st = core.Status{Service: sv.Name, State: core.StateUnknown, Message: err.Error()}
				}
				sts = append(sts, st)
			}
		}
		f := frame(statusFields)
		for i, st := range sts {
			if st.Service == "" && i < len(svcs) {
				st.Service = svcs[i].Name
			}
			for j, v := range statusRow(st) {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, nil
	})
	src(s, "discover", func(ctx context.Context, c core.Runtime, _ protocol.Args) (*protocol.Frame, error) {
		ws, err := c.Discover(ctx)
		f := frame("name:string kind:string service:string state:string")
		for _, w := range ws {
			for j, v := range []any{w.Name, w.Kind, w.Service, string(w.Status.State)} {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, err
	})
	stream(s, "logs", func(ctx context.Context, c core.Runtime, a protocol.Args) (<-chan core.LogLine, error) {
		sv, err := s.service(a)
		if err != nil {
			return nil, err
		}
		return c.Logs(ctx, sv, core.LogOptions{Instance: str(a["instance"]), Since: duration(a["since"]), Tail: integer(a["tail"]), Follow: a["follow"] == true})
	})
	for name, fn := range map[string]func(core.Runtime, context.Context, *spec.Service) error{
		"start": core.Runtime.Start, "stop": core.Runtime.Stop, "restart": core.Runtime.Restart,
	} {
		svcAct(s, name, func(ctx context.Context, c core.Runtime, sv *spec.Service, _ protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(fn(c, ctx, sv))
		})
	}
	svcAct(s, "scale", func(ctx context.Context, c core.Runtime, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.Scale(ctx, sv, integer(a["replicas"])))
	})
	svcAct(s, "deploy", func(ctx context.Context, c core.Runtime, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		r := core.Release{Image: str(a["image"]), Replicas: integer(a["replicas"])}
		decode(a["env"], &r.Env)
		return none(c.Deploy(ctx, sv, r))
	})
	svcAct(s, "exec", func(ctx context.Context, c core.Runtime, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		ctx, cp := WithCapture(ctx)
		err := c.Exec(ctx, sv, core.ExecOptions{Command: strs(a["command"]), Instance: str(a["instance"]), TTY: a["tty"] == true})
		if err != nil {
			return nil, err
		}
		if len(cp.Argv) == 0 {
			return nil, errors.New("exec ran in the plugin instead of answering a command for the terminal")
		}
		return cmdOut(cp.Argv, cp.Env, cp.Dir), nil
	})
	if is[core.StateStore](probe) {
		src(s, "state", func(ctx context.Context, c core.StateStore, _ protocol.Args) (*protocol.Frame, error) {
			m, err := c.LoadState(ctx)
			return kvFrame(m, "key", "value"), err
		})
		act(s, "save-state", func(ctx context.Context, c core.StateStore, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			var m map[string]string
			decode(a["state"], &m)
			return none(c.SaveState(ctx, m))
		})
	}
	if is[core.Registrar](probe) {
		src(s, "registry", func(_ context.Context, c core.Registrar, _ protocol.Args) (*protocol.Frame, error) {
			return frame("registry:string", []any{c.Registry()}), nil
		})
	}
	if is[core.ImageLoader](probe) {
		act(s, "load-image", func(ctx context.Context, c core.ImageLoader, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(c.LoadImage(ctx, str(a["image"])))
		})
	}
	if is[core.Bridger](probe) {
		svcAct(s, "bridge", func(ctx context.Context, c core.Bridger, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			var raw map[string]string
			decode(a["ports"], &raw)
			ports := map[int]string{}
			for k, v := range raw {
				n, err := strconv.Atoi(k)
				if err != nil {
					return nil, fmt.Errorf("bridge port %q: %w", k, err)
				}
				ports[n] = v
			}
			return none(c.Bridge(ctx, sv, ports))
		})
	}
	if is[core.Autoscaler](probe) {
		svcAct(s, "autoscale", func(ctx context.Context, c core.Autoscaler, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(c.SetAutoscale(ctx, sv, core.Bounds{Min: integer(a["min"]), Max: integer(a["max"]), CPU: integer(a["cpu"]), Memory: integer(a["memory"])}))
		})
	}
	if is[core.Resourcer](probe) {
		svcSrc(s, "resources", func(ctx context.Context, c core.Resourcer, sv *spec.Service, _ protocol.Args) (*protocol.Frame, error) {
			rs, err := c.Resources(ctx, sv)
			f := frame("container:string cpu_request:string cpu_limit:string mem_request:string mem_limit:string")
			for _, r := range rs {
				for j, v := range []any{r.Container, r.CPURequest, r.CPULimit, r.MemRequest, r.MemLimit} {
					f.Columns[j] = append(f.Columns[j], v)
				}
			}
			return f, err
		})
		svcAct(s, "set-resources", func(ctx context.Context, c core.Resourcer, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(c.SetResources(ctx, sv, core.Resources{Container: str(a["container"]), CPURequest: str(a["cpu_request"]),
				CPULimit: str(a["cpu_limit"]), MemRequest: str(a["mem_request"]), MemLimit: str(a["mem_limit"])}))
		})
	}
	if is[core.EnvReader](probe) {
		svcSrc(s, "env", func(ctx context.Context, c core.EnvReader, sv *spec.Service, _ protocol.Args) (*protocol.Frame, error) {
			m, err := c.LiveEnv(ctx, sv)
			return kvFrame(m, "name", "value"), err
		})
	}
	if is[core.Forwarder](probe) {
		act(s, "forward", func(ctx context.Context, c core.Forwarder, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			// ponytail: the forward lives as long as the serving plugin process; an idle stop ends it.
			addr, err := c.Forward(context.WithoutCancel(ctx), core.Target{Service: str(a["service"]), Instance: str(a["instance"]), Port: integer(a["port"])})
			return protocol.Args{"addr": addr}, err
		})
	}
	if is[core.Hosts](probe) {
		s.hostsOf("hosts", "shell")
	}
	if is[core.Manifester](probe) {
		s.manifests()
	}
}

func (s *server) manifests() {
	svcSrc(s, "objects", func(_ context.Context, c core.Manifester, sv *spec.Service, _ protocol.Args) (*protocol.Frame, error) {
		objs, w, err := c.Objects(sv)
		f := frame("file:string doc:int workload:bool")
		for _, o := range objs {
			for j, v := range []any{o.File, o.Doc, o == w} {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, err
	})
	src(s, "live-object", func(ctx context.Context, c core.Manifester, a protocol.Args) (*protocol.Frame, error) {
		b, err := c.LiveObject(ctx, str(a["kind"]), str(a["name"]))
		return frame("json:string", []any{string(b)}), err
	})
	src(s, "live-objects", func(ctx context.Context, c core.Manifester, _ protocol.Args) (*protocol.Frame, error) {
		b, err := c.LiveObjects(ctx)
		return frame("yaml:string", []any{string(b)}), err
	})
	svcSrc(s, "injected-env", func(_ context.Context, c core.Manifester, sv *spec.Service, _ protocol.Args) (*protocol.Frame, error) {
		f := frame("name:string")
		for _, n := range sortedKeys(c.InjectedEnv(sv)) {
			f.Columns[0] = append(f.Columns[0], n)
		}
		return f, nil
	})
	src(s, "kubectl-args", func(_ context.Context, c core.Manifester, _ protocol.Args) (*protocol.Frame, error) {
		return frame("args:json", []any{c.KubectlArgs()}), nil
	})
	act(s, "apply", func(ctx context.Context, c core.Manifester, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		var docs []struct {
			File, YAML string
			Doc        int
		}
		decode(a["objects"], &docs)
		var objs []*manifest.Object
		for _, d := range docs {
			set := manifest.Parse(d.File, []byte(d.YAML))
			if len(set.Objects) != 1 {
				return nil, fmt.Errorf("%s#%d: want one object, have %d", d.File, d.Doc, len(set.Objects))
			}
			set.Objects[0].Doc = d.Doc
			objs = append(objs, set.Objects[0])
		}
		return none(c.ApplyManifests(ctx, objs))
	})
}

func (s *server) hostsOf(list, shell string) {
	src(s, list, func(ctx context.Context, c core.Hosts, _ protocol.Args) (*protocol.Frame, error) {
		hs, err := c.Hosts(ctx)
		return hostsFrame(hs), err
	})
	act(s, shell, func(ctx context.Context, c core.Hosts, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		cmd, err := c.Shell(ctx, str(a["host"]), strs(a["command"]))
		if err != nil {
			return nil, err
		}
		return cmdOut(append([]string{cmd.Path}, cmd.Args[1:]...), cmd.Env, cmd.Dir), nil
	})
}

func (s *server) builder(probe any) {
	svcAct(s, "build", func(ctx context.Context, c core.Builder, sv *spec.Service, a protocol.Args, out io.Writer) (protocol.Args, error) {
		img, err := c.Build(ctx, sv, core.BuildOptions{Tag: str(a["tag"]), Registry: str(a["registry"]), Push: a["push"] == true, Dir: str(a["dir"]), Out: out})
		return protocol.Args{"image": img}, err
	})
	if is[core.Pusher](probe) {
		act(s, "push", func(ctx context.Context, c core.Pusher, a protocol.Args, out io.Writer) (protocol.Args, error) {
			return none(c.Push(ctx, str(a["image"]), out))
		})
	}
}

func (s *server) metrics() {
	src(s, "instant", func(ctx context.Context, c core.Metrics, a protocol.Args) (*protocol.Frame, error) {
		ss, err := c.Instant(ctx, str(a["query"]))
		f := frame("series:string labels:json value:number")
		for _, x := range ss {
			for j, v := range []any{seriesName(x.Labels), x.Labels, x.Value} {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, err
	})
	src(s, "range", func(ctx context.Context, c core.Metrics, a protocol.Args) (*protocol.Frame, error) {
		ss, err := c.Range(ctx, str(a["query"]), when(a["start"]), when(a["end"]), duration(a["step"]))
		f := frame("series:string labels:json time:time value:number")
		for _, x := range ss {
			for _, p := range x.Points {
				for j, v := range []any{seriesName(x.Labels), x.Labels, seconds(p.T), p.V} {
					f.Columns[j] = append(f.Columns[j], v)
				}
			}
		}
		return f, err
	})
}

func seriesName(l map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(l) {
		parts = append(parts, k+"="+l[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (s *server) traces() {
	src(s, "services", func(ctx context.Context, c core.Tracing, _ protocol.Args) (*protocol.Frame, error) {
		ss, err := c.Services(ctx)
		f := frame("service:string")
		for _, x := range ss {
			f.Columns[0] = append(f.Columns[0], x)
		}
		return f, err
	})
	src(s, "search", func(ctx context.Context, c core.Tracing, a protocol.Args) (*protocol.Frame, error) {
		ts, err := c.Search(ctx, core.TraceQuery{Service: str(a["service"]), Operation: str(a["operation"]), MinDuration: duration(a["min_duration"]),
			Lookback: duration(a["lookback"]), Limit: integer(a["limit"])})
		f := frame("id:string root:string start:time duration:duration spans:int services:json error:bool")
		for _, t := range ts {
			for j, v := range []any{t.ID, t.Root, seconds(t.Start), t.Duration.Seconds(), t.Spans, t.Services, t.Error} {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, err
	})
	src(s, "get", func(ctx context.Context, c core.Tracing, a protocol.Args) (*protocol.Frame, error) {
		ss, err := c.Trace(ctx, str(a["id"]))
		f := frame("trace:string id:string parent:string service:string name:string start:time duration:duration error:bool tags:json")
		for _, x := range ss {
			for j, v := range []any{x.TraceID, x.ID, x.Parent, x.Service, x.Name, seconds(x.Start), x.Duration.Seconds(), x.Error, x.Tags} {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, err
	})
}

func (s *server) profiler() {
	src(s, "kinds", func(_ context.Context, c core.Profiler, _ protocol.Args) (*protocol.Frame, error) {
		f := frame("kind:string")
		for _, k := range c.Kinds() {
			f.Columns[0] = append(f.Columns[0], k)
		}
		return f, nil
	})
	svcAct(s, "capture", func(ctx context.Context, c core.Profiler, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		p, err := c.Capture(ctx, core.ProfileRequest{Service: sv, Instance: str(a["instance"]), Kind: str(a["kind"]), Duration: duration(a["duration"]), OutDir: str(a["dir"])})
		return protocol.Args{"kind": p.Kind, "format": p.Format, "file": p.File, "summary": p.Summary}, err
	})
}

func (s *server) debugger() {
	// ponytail: one session per service, kept in this process; an idle stop of the plugin drops it.
	var mu sync.Mutex
	sessions := map[string]core.DebugSession{}
	svcAct(s, "attach", func(ctx context.Context, c core.Debugger, sv *spec.Service, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		d, err := c.Attach(context.WithoutCancel(ctx), sv, str(a["instance"]))
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if old, ok := sessions[sv.Name]; ok && old.Close != nil {
			_ = old.Close()
		}
		sessions[sv.Name] = d
		return protocol.Args{"addr": d.Addr, "hint": d.Hint}, nil
	})
	act(s, "detach", func(_ context.Context, _ core.Debugger, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		mu.Lock()
		d, ok := sessions[str(a["service"])]
		delete(sessions, str(a["service"]))
		mu.Unlock()
		if !ok || d.Close == nil {
			return nil, nil
		}
		return none(d.Close())
	})
}

func (s *server) logs() {
	stream(s, "query", func(ctx context.Context, c core.LogSource, a protocol.Args) (<-chan core.LogLine, error) {
		return c.Logs(ctx, core.LogQuery{Services: strs(a["services"]), Instance: str(a["instance"]), Since: duration(a["since"]), Tail: integer(a["tail"]),
			Follow: a["follow"] == true, Match: str(a["match"]), Regex: a["regex"] == true})
	})
}

func (s *server) database() {
	src(s, "query", func(ctx context.Context, c core.Database, a protocol.Args) (*protocol.Frame, error) {
		t, err := c.Query(ctx, str(a["database"]), str(a["query"]))
		return tableFrame(t), err
	})
	act(s, "exec", func(ctx context.Context, c core.Database, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		n, err := c.Exec(ctx, str(a["database"]), str(a["query"]))
		return protocol.Args{"rows": n}, err
	})
	src(s, "list", func(ctx context.Context, c core.Database, _ protocol.Args) (*protocol.Frame, error) {
		ds, err := c.Databases(ctx)
		f := frame("database:string")
		for _, d := range ds {
			f.Columns[0] = append(f.Columns[0], d)
		}
		return f, err
	})
	act(s, "create", func(ctx context.Context, c core.Database, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.CreateDatabase(ctx, str(a["name"])))
	})
	act(s, "drop", func(ctx context.Context, c core.Database, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.DropDatabase(ctx, str(a["name"])))
	})
}

func (s *server) cache() {
	act(s, "do", func(ctx context.Context, c core.Cache, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		r, err := c.Do(ctx, strs(a["args"])...)
		return protocol.Args{"reply": r}, err
	})
	src(s, "info", func(ctx context.Context, c core.Cache, _ protocol.Args) (*protocol.Frame, error) {
		m, err := c.Info(ctx)
		return kvFrame(m, "key", "value"), err
	})
}

func (s *server) queue(probe any) {
	src(s, "list", func(ctx context.Context, c core.Messaging, _ protocol.Args) (*protocol.Frame, error) {
		qs, err := c.Queues(ctx)
		f := frame("name:string messages:int ready:int unacked:int consumers:int in_rate:number out_rate:number")
		for _, q := range qs {
			for j, v := range []any{q.Name, q.Messages, q.Ready, q.Unacked, q.Consumers, q.InRate, q.OutRate} {
				f.Columns[j] = append(f.Columns[j], v)
			}
		}
		return f, err
	})
	act(s, "purge", func(ctx context.Context, c core.Messaging, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.Purge(ctx, str(a["queue"])))
	})
	act(s, "publish", func(ctx context.Context, c core.Messaging, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.Publish(ctx, str(a["target"]), []byte(str(a["body"]))))
	})
	if is[core.QueueInspector](probe) {
		src(s, "info", func(ctx context.Context, c core.QueueInspector, a protocol.Args) (*protocol.Frame, error) {
			t, err := c.QueueInfo(ctx, str(a["queue"]))
			return tableFrame(t), err
		})
		src(s, "peek", func(ctx context.Context, c core.QueueInspector, a protocol.Args) (*protocol.Frame, error) {
			t, err := c.Peek(ctx, str(a["queue"]), integer(a["n"]))
			return tableFrame(t), err
		})
		act(s, "delete", func(ctx context.Context, c core.QueueInspector, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(c.DeleteQueue(ctx, str(a["queue"])))
		})
	}
}

func (s *server) kv(probe any) {
	src(s, "get", func(ctx context.Context, c core.KV, a protocol.Args) (*protocol.Frame, error) {
		v, ok, err := c.Get(ctx, str(a["key"]))
		return frame("value:string found:bool", []any{string(v), ok}), err
	})
	act(s, "put", func(ctx context.Context, c core.KV, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.Put(ctx, str(a["key"]), []byte(str(a["value"]))))
	})
	act(s, "delete", func(ctx context.Context, c core.KV, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.Delete(ctx, str(a["key"])))
	})
	src(s, "list", func(ctx context.Context, c core.KV, a protocol.Args) (*protocol.Frame, error) {
		ks, err := c.List(ctx, str(a["prefix"]))
		f := frame("key:string")
		for _, k := range ks {
			f.Columns[0] = append(f.Columns[0], k)
		}
		return f, err
	})
	if is[core.KVReader](probe) {
		src(s, "all", func(ctx context.Context, c core.KVReader, a protocol.Args) (*protocol.Frame, error) {
			m, err := c.All(ctx, str(a["prefix"]))
			s := map[string]string{}
			for k, v := range m {
				s[k] = string(v)
			}
			return kvFrame(s, "key", "value"), err
		})
	}
}

func (s *server) load(probe any) {
	for name, fn := range map[string]func(core.LoadGenerator, context.Context) error{"start": core.LoadGenerator.Start, "stop": core.LoadGenerator.Stop} {
		act(s, name, func(ctx context.Context, c core.LoadGenerator, _ protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(fn(c, ctx))
		})
	}
	act(s, "rate", func(ctx context.Context, c core.LoadGenerator, a protocol.Args, _ io.Writer) (protocol.Args, error) {
		return none(c.SetRate(ctx, number(a["rps"])))
	})
	src(s, "status", func(ctx context.Context, c core.LoadGenerator, _ protocol.Args) (*protocol.Frame, error) {
		st, err := c.Status(ctx)
		return frame("running:bool rate:number sent:int failed:int p50:duration p95:duration p99:duration extra:json per_instance:json",
			[]any{st.Running, st.Rate, st.Sent, st.Failed, st.Latency.P50.Seconds(), st.Latency.P95.Seconds(), st.Latency.P99.Seconds(), st.Extra, st.PerInstance}), err
	})
	if is[core.LoadConfigured](probe) {
		src(s, "config", func(_ context.Context, c core.LoadConfigured, _ protocol.Args) (*protocol.Frame, error) {
			store, key := c.ConfigKey()
			return frame("store:string key:string services:json", []any{store, key, c.Services()}), nil
		})
	}
	if is[core.LoadScaler](probe) {
		src(s, "replicas", func(ctx context.Context, c core.LoadScaler, _ protocol.Args) (*protocol.Frame, error) {
			n, err := c.Replicas(ctx)
			return frame("replicas:int", []any{n}), err
		})
		act(s, "set-replicas", func(ctx context.Context, c core.LoadScaler, a protocol.Args, _ io.Writer) (protocol.Args, error) {
			return none(c.SetReplicas(ctx, integer(a["replicas"])))
		})
	}
}

func (s *server) hosts() { s.hostsOf("list", "shell") }
