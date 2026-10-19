package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/audit"
	"github.com/goxang/rig/kernel/catalog"
	"github.com/goxang/rig/kernel/events"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/inbox"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/kernel/policy"
	"github.com/goxang/rig/kernel/refs"
	"github.com/goxang/rig/protocol"
)

type sourceFunc func(ctx context.Context, args protocol.Args) (*protocol.Frame, error)

// Local is the Engine running in this process, over an App.
// ponytail: the TUI and CLI still reach App for what has no source or action yet; those calls
// move behind the seam as the adapters become plugins.
type Local struct {
	App    *App
	Bus    *events.Bus
	Host   *host.Host
	runner *actions.Runner
	jobs   *jobs.Manager
	me     identity.Subject

	mu      sync.Mutex
	sources map[string]sourceFunc
	streams map[string]bool
	live    map[string]streamFunc
	about   map[string]protocol.SourceSpec
}

var _ api.Engine = (*Local)(nil)

// NewLocal serves a; jm keeps its jobs (the TUI's own manager, so its activity view shows them).
func NewLocal(a *App, jm *jobs.Manager) *Local {
	if jm == nil {
		jm = &jobs.Manager{}
	}
	l := &Local{App: a, Bus: events.NewBus(), jobs: jm, me: identity.Local(), sources: map[string]sourceFunc{}, streams: map[string]bool{}, live: map[string]streamFunc{}, about: map[string]protocol.SourceSpec{}}
	envs := map[string]policy.Env{}
	for n, e := range a.Spec.Environments {
		envs[n] = policy.Env{Protected: e.Protected, Readonly: e.ReadOnly}
	}
	l.runner = &actions.Runner{
		Actions: actions.NewRegistry(),
		Envs:    envs,
		Policy:  a.Spec.Policy,
		Jobs:    jm,
		Audit:   audit.Open(filepath.Join(a.StateDir(), "audit.jsonl"), secretValues(a)...),
		// App.Guard asks once per session, or takes --yes or the TUI's confirm
		Ask:   func(context.Context, policy.Decision, string) error { return a.Guard() },
		Inbox: inbox.Open(filepath.Join(a.StateDir(), "inbox")),
	}
	l.builtins()
	l.observe()
	l.leases()
	l.pluginActions()
	l.plugins()
	return l
}

// plugins registers every plugin's sources and actions, named <plugin>.<name>; their effects go
// through the same policy, confirmation and audit as built-in actions.
func (l *Local) plugins() {
	a := l.App
	l.Host = a.Host()
	l.Host.Engine, l.Host.Emit = l, l.Bus.Publish
	for _, p := range l.Host.Plugins() {
		pr := p.Provides(context.Background())
		for n, s := range pr.Sources {
			name := p.Name + "." + n
			l.sources[name] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
				return l.Host.Query(ctx, name, args)
			}
			l.streams[name] = s.Stream && p.Server != nil
			l.about[name] = s
		}
		for n, s := range pr.Actions {
			name := p.Name + "." + n
			act := actions.Action{Name: name, Effect: s.Effect, Help: s.Description, Args: s.Args,
				Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
					return l.Host.Run(ctx, name, args, false)
				}}
			if s.Plan {
				act.Plan = func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
					return l.Host.Run(ctx, name, args, true)
				}
			}
			_ = l.runner.Actions.Add(act)
		}
	}
}

// Close stops the plugin processes this engine started.
func (l *Local) Close() { l.Host.Close() }

func secretValues(a *App) []string {
	var out []string
	for _, v := range a.KnownSecrets() {
		out = append(out, v)
	}
	return out
}

func (l *Local) envName() string {
	if l.App.Env == nil {
		return ""
	}
	return l.App.Env.Name
}

func (l *Local) builtins() {
	reg := l.runner.Actions
	svc := func(name, help string, f func(ctx context.Context, n string) error) {
		_ = reg.Add(actions.Action{Name: name, Effect: protocol.EffectWrite, Help: help,
			Args: protocol.Schema{"type": "object", "required": []any{"service"}, "properties": map[string]any{"service": map[string]any{"type": "string"}}},
			Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
				n, _ := args["service"].(string)
				if err := f(ctx, n); err != nil {
					return nil, err
				}
				return &protocol.RunResult{Message: name + " " + n, Refs: []protocol.Ref{"svc://" + n}}, nil
			}})
	}
	svc("runtime.start", "start a service, deploying it when it is not there yet", l.App.Start)
	svc("runtime.stop", "stop a service, keeping its definition", l.App.Stop)
	svc("runtime.restart", "restart a service", l.App.Restart)
	_ = reg.Add(actions.Action{Name: "env.up", Effect: protocol.EffectWrite, Help: "deploy services and their dependencies, in order",
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			o, _ := args["options"].(UpOptions)
			o.Out = jobs.Out(ctx)
			return nil, l.App.Up(ctx, strs(args["targets"]), o)
		}})
	_ = reg.Add(actions.Action{Name: "env.down", Effect: protocol.EffectWrite, Help: "stop services in reverse dependency order",
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			return nil, l.App.Down(ctx, strs(args["targets"]), jobs.Out(ctx))
		}})
	for name, t := range l.App.Tasks() {
		effect := protocol.EffectWrite
		if t.ReadOnly {
			effect = protocol.EffectRead
		}
		_ = reg.Add(actions.Action{Name: "task." + name, Effect: effect, Help: l.App.TaskHelp(name),
			Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
				return nil, l.App.RunTask(ctx, name, strs(args["args"]), jobs.Out(ctx))
			}})
	}

	l.about["services.status"] = protocol.SourceSpec{Description: "every service: state, readiness, restarts, image"}
	l.sources["services.status"] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		names := strs(args["services"])
		if len(names) == 0 {
			names = SortedKeys(l.App.Spec.Services)
		}
		var rows []map[string]any
		for _, st := range l.App.StatusAll(ctx, names) {
			rows = append(rows, map[string]any{"service": st.Service, "state": string(st.State), "ready": st.Ready, "desired": st.Desired, "image": st.Image, "message": st.Message})
		}
		return frames.FromRows([]protocol.Field{
			{Name: "service", Type: protocol.TypeString, Ref: "svc://{service}"}, {Name: "state", Type: protocol.TypeString},
			{Name: "ready", Type: protocol.TypeInt}, {Name: "desired", Type: protocol.TypeInt},
			{Name: "image", Type: protocol.TypeString}, {Name: "message", Type: protocol.TypeString},
		}, rows), nil
	}
	l.about["plugins"] = protocol.SourceSpec{Description: "installed plugins: version, state, what they implement"}
	l.sources["plugins"] = func(ctx context.Context, _ protocol.Args) (*protocol.Frame, error) {
		ms, warns := host.Discover(l.App.Spec.Dir, l.App.Spec.Plugins)
		var rows []map[string]any
		for _, m := range ms {
			rows = append(rows, map[string]any{"name": m.Name, "version": m.Version, "state": "on", "kind": m.Kind(),
				"permissions": m.Permissions.String(), "description": m.Description})
		}
		for _, n := range SortedKeys(l.App.Spec.Plugins) {
			if p := l.App.Spec.Plugins[n]; p.Off {
				rows = append(rows, map[string]any{"name": n, "version": p.Version, "state": "off"})
			}
		}
		for _, w := range warns {
			rows = append(rows, map[string]any{"state": "warn", "description": w})
		}
		var fields []protocol.Field
		for _, c := range []string{"name", "version", "state", "kind", "permissions", "description"} {
			fields = append(fields, protocol.Field{Name: c, Type: protocol.TypeString})
		}
		return frames.FromRows(fields, rows), nil
	}
	l.about["queries.run"] = protocol.SourceSpec{Description: "run a saved query from rig.yaml by name"}
	l.sources["queries.run"] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		name, _ := args["name"].(string)
		var t core.Table
		var err error
		if name != "" {
			params := map[string]string{}
			if p, ok := args["params"].(map[string]any); ok {
				for k, v := range p {
					params[k] = fmt.Sprint(v)
				}
			}
			t, err = l.App.RunSaved(ctx, name, params)
		} else {
			comp, _ := args["component"].(string)
			text, _ := args["text"].(string)
			t, err = l.App.RunQuery(ctx, comp, text)
		}
		if err != nil {
			return nil, err
		}
		return tableFrame(t), nil
	}
}

func tableFrame(t core.Table) *protocol.Frame {
	f := &protocol.Frame{Columns: make([][]any, len(t.Columns))}
	for i, c := range t.Columns {
		f.Fields = append(f.Fields, protocol.Field{Name: c, Type: protocol.TypeString})
		col := make([]any, len(t.Rows))
		for j, r := range t.Rows {
			if i < len(r) {
				col[j] = r[i]
			}
		}
		f.Columns[i] = col
	}
	if t.Note != "" {
		f.Meta = &protocol.FrameMeta{Notice: t.Note}
	}
	return f
}

func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, s := range x {
			out = append(out, fmt.Sprint(s))
		}
		return out
	}
	return nil
}

func (l *Local) Catalog(context.Context) (*catalog.Catalog, error) {
	return catalog.Build(l.App.Spec), nil
}

func (l *Local) Query(ctx context.Context, source string, args protocol.Args) (*protocol.Frame, error) {
	l.mu.Lock()
	f, ok := l.sources[source]
	l.mu.Unlock()
	if ok {
		return f(ctx, args)
	}
	if _, comp := l.App.Spec.Components[source]; comp || source == "runtime" {
		return l.componentQuery(ctx, source, args)
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no source " + source}
}

// componentQuery lets a view bind to a component by name: a metrics component answers query: as
// a range (range: 15m, step: 30s; instant: true for one sample per series) in long form, time,
// series, value; anything else that answers queries returns its table.
func (l *Local) componentQuery(ctx context.Context, name string, args protocol.Args) (*protocol.Frame, error) {
	text, _ := args["query"].(string)
	c, err := l.App.Component(name)
	if name == "runtime" {
		c, err = l.App.runtime, nil
	}
	if err != nil {
		return nil, err
	}
	m, ok := core.As[core.Metrics](c)
	if !ok {
		t, err := l.App.RunQuery(ctx, name, text)
		if err != nil {
			return nil, err
		}
		return tableFrame(t), nil
	}
	return metricsFrame(ctx, m, text, args)
}

// metricsFrame answers text as a range (range, step) or, with instant, one sample per series.
func metricsFrame(ctx context.Context, m core.Metrics, text string, args protocol.Args) (*protocol.Frame, error) {
	fields := []protocol.Field{{Name: "time", Type: protocol.TypeTime}, {Name: "series", Type: protocol.TypeString}, {Name: "value", Type: protocol.TypeNumber}}
	var rows []map[string]any
	if inst, _ := args["instant"].(bool); inst {
		ss, err := m.Instant(ctx, text)
		if err != nil {
			return nil, err
		}
		for _, s := range ss {
			rows = append(rows, map[string]any{"time": time.Now(), "series": seriesName(s.Labels), "value": s.Value})
		}
		return frames.FromRows(fields, rows), nil
	}
	rng, step := dur(args["range"], 15*time.Minute), dur(args["step"], 0)
	if step == 0 {
		step = max(rng/240, time.Second)
	}
	end := time.Now()
	ss, err := m.Range(ctx, text, end.Add(-rng), end, step)
	if err != nil {
		return nil, err
	}
	for _, s := range ss {
		name := seriesName(s.Labels)
		for _, p := range s.Points {
			rows = append(rows, map[string]any{"time": p.T, "series": name, "value": p.V})
		}
	}
	return frames.FromRows(fields, rows), nil
}

func dur(v any, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(fmt.Sprint(v)); err == nil && v != nil {
		return d
	}
	return def
}

// seriesName is a series' labels as k=v, sorted, without __name__.
func seriesName(labels map[string]string) string {
	var parts []string
	for k, v := range labels {
		if k != "__name__" {
			parts = append(parts, k+"="+v)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Stream answers a source that only queries with one batch.
func (l *Local) Stream(ctx context.Context, source string, args protocol.Args) (<-chan protocol.StreamBatch, error) {
	l.mu.Lock()
	live, own := l.streams[source], l.live[source]
	l.mu.Unlock()
	if own != nil {
		return own(ctx, args)
	}
	if live {
		return l.Host.Stream(ctx, source, args)
	}
	f, err := l.Query(ctx, source, args)
	if err != nil {
		return nil, err
	}
	ch := make(chan protocol.StreamBatch, 2)
	ch <- protocol.StreamBatch{ID: source, Seq: 1, Frame: f}
	ch <- protocol.StreamBatch{ID: source, Seq: 2, Done: true}
	close(ch)
	return ch, nil
}

func (l *Local) Run(ctx context.Context, action string, args protocol.Args, o api.RunOpts) (*jobs.Job, *protocol.RunResult, error) {
	who := l.Session(ctx)
	if o.As != nil {
		who = *o.As
	}
	if err := l.leased(action, who); err != nil {
		return nil, nil, err
	}
	j, res, err := l.runner.Run(ctx, actions.Call{Subject: who, Env: l.envName(), Action: action, Args: args, DryRun: o.DryRun})
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.Code == protocol.CodeDenied {
		if werr := l.App.Writable(); werr != nil {
			err = werr // the read-only error callers know, with its hint
		}
	}
	topic := "action.finished"
	if err != nil {
		topic = "action.failed"
	}
	l.Bus.Publish(protocol.Event{Topic: topic, Source: "core", Data: protocol.Args{"action": action, "env": l.envName()}})
	return j, res, err
}

// Service runs a runtime action on one service.
func (l *Local) Service(ctx context.Context, action, name string) error {
	_, _, err := l.Run(ctx, action, protocol.Args{"service": name}, api.RunOpts{})
	return err
}

func (l *Local) Jobs(context.Context) []*jobs.Job { return l.jobs.List() }

func (l *Local) Events(ctx context.Context, pattern string) (<-chan protocol.Event, error) {
	ch := make(chan protocol.Event, 64)
	stop := l.Bus.Subscribe(pattern, func(e protocol.Event) {
		select {
		case ch <- e:
		default: // a reader that falls behind loses events rather than stalling the bus
		}
	})
	go func() { <-ctx.Done(); stop() }()
	return ch, nil
}

func (l *Local) Search(ctx context.Context, q string) ([]protocol.Hit, error) {
	c, _ := l.Catalog(ctx)
	q = strings.ToLower(q)
	var hits []protocol.Hit
	for _, r := range c.List("") {
		if strings.Contains(strings.ToLower(r.Name), q) {
			hits = append(hits, protocol.Hit{Ref: r.Ref(), Title: r.Name, Kind: r.Kind, Detail: r.Origin.String()})
		}
	}
	return hits, nil
}

func (l *Local) Resolve(ctx context.Context, ref string) (*protocol.Resolved, error) {
	r, err := refs.Parse(ref)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	kind := map[string]string{"svc": "service", "env": "environment"}[r.Scheme]
	name, _, _ := strings.Cut(r.Path, ":")
	c, _ := l.Catalog(ctx)
	res, ok := c.Get(kind, name)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "nothing at " + ref}
	}
	out := &protocol.Resolved{Ref: ref, Kind: res.Kind, Title: res.Name, Fields: protocol.Args{"origin": res.Origin.String()}}
	if kind == "service" {
		out.Actions = []string{"runtime.start", "runtime.stop", "runtime.restart"}
	}
	for _, link := range res.Links {
		out.Links = append(out.Links, protocol.Link{Ref: link, Rel: "depends_on"})
	}
	return out, nil
}

// Complete has no languages until plugins bring them.
func (l *Local) Complete(context.Context, string, string, int) ([]protocol.Completion, error) {
	return nil, nil
}

func (l *Local) Session(ctx context.Context) identity.Subject {
	if s, ok := identity.From(ctx); ok {
		return s
	}
	return l.me
}

// Provides is every source and action this engine serves, for finding them by what they do.
func (l *Local) Provides(context.Context) protocol.Provides {
	out := protocol.Provides{Sources: map[string]protocol.SourceSpec{}, Actions: map[string]protocol.ActionSpec{}}
	l.mu.Lock()
	for n := range l.sources {
		out.Sources[n] = l.about[n]
	}
	l.mu.Unlock()
	for _, n := range l.runner.Actions.Names() {
		a, _ := l.runner.Actions.Get(n)
		out.Actions[n] = protocol.ActionSpec{Description: a.Help, Effect: a.Effect, Args: a.Args, Plan: a.Plan != nil}
	}
	return out
}

func (l *Local) Inbox(context.Context) []inbox.Item { return l.runner.Inbox.List() }

func (l *Local) Decide(ctx context.Context, id string, approve bool) (*protocol.RunResult, error) {
	res, err := l.runner.Decide(ctx, id, approve, l.Session(ctx))
	l.Bus.Publish(protocol.Event{Topic: "inbox.decided", Source: "core", Data: protocol.Args{"id": id, "approved": approve}})
	return res, err
}

// Action is a registered action by name: its effect, help and argument schema.
func (l *Local) Action(name string) (actions.Action, bool) { return l.runner.Actions.Get(name) }
func (l *Local) Actions() []string                         { return l.runner.Actions.Names() }
