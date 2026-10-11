package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// Version is the rig version plugins see at initialize.
var Version = "dev"

type Options struct {
	// Dir is the project, File its rig.yaml; Env the environment calls run in.
	Dir  string
	File string
	Env  string
	// StateDir keeps host/state values, per plugin: the project's .rig/<env>.
	StateDir string
	// Engine serves host/query and host/run.
	Engine api.Engine
	Ask    func(context.Context, protocol.AskParams) (protocol.AskResult, error)
	AI     func(context.Context, protocol.AIParams) (string, error)
	// Secret looks up a declared secret; nil reads the process environment.
	Secret func(name string) (string, bool)
	Emit   func(protocol.Event)
	// Components are the project's components a plugin serves (type: <plugin>), by name; nil is none.
	Components func(plugin string) map[string]protocol.Args
	// Idle stops a plugin process after this long unused; zero is 10 minutes.
	Idle time.Duration
}

// Host runs the project's plugins.
type Host struct {
	Options
	Warnings []string

	plugins map[string]*Plugin
	names   []string
	stateMu sync.Mutex
	seq     atomic.Int64
}

func New(o Options, cfg map[string]*spec.Plugin) *Host {
	ms, warn := Discover(o.Dir, cfg)
	h := &Host{Options: o, Warnings: warn, plugins: map[string]*Plugin{}}
	for _, m := range ms {
		var c map[string]any
		if pc := cfg[m.Name]; pc != nil {
			c = pc.Config
		}
		h.add(m, c)
	}
	return h
}

func (h *Host) add(m *Manifest, c map[string]any) *Plugin {
	conf := map[string]any{}
	p := &Plugin{Manifest: m, Config: conf, h: h}
	for k, s := range m.Config {
		if d, ok := s["default"]; ok {
			if str, ok := d.(string); ok {
				d = p.expand(str)
			}
			conf[k] = d
		}
	}
	for k, v := range c {
		conf[k] = v
	}
	if _, dup := h.plugins[m.Name]; !dup {
		h.names = append(h.names, m.Name)
	}
	h.plugins[m.Name] = p
	return p
}

func (h *Host) Plugins() []*Plugin {
	out := make([]*Plugin, 0, len(h.names))
	for _, n := range h.names {
		out = append(out, h.plugins[n])
	}
	return out
}

func (h *Host) Get(name string) *Plugin { return h.plugins[name] }

// owner splits plugin.rest; source and action names are namespaced by their plugin.
func (h *Host) owner(name string) (*Plugin, string, error) {
	pn, rest, ok := strings.Cut(name, ".")
	p := h.plugins[pn]
	if !ok || p == nil {
		return nil, "", notFound("plugin for", name)
	}
	return p, rest, nil
}

func (h *Host) context() *protocol.Context {
	return &protocol.Context{Project: h.Dir, Env: h.Env, User: os.Getenv("USER")}
}

func withDeadline(ctx context.Context, c *protocol.Context) *protocol.Context {
	if d, ok := ctx.Deadline(); ok {
		c.Deadline = d.Format(time.RFC3339)
	}
	return c
}

func (h *Host) Query(ctx context.Context, name string, args protocol.Args) (*protocol.Frame, error) {
	p, local, err := h.owner(name)
	if err != nil {
		return nil, err
	}
	var f protocol.Frame
	if err := p.call(ctx, protocol.MethodQuery, protocol.QueryParams{Source: local, Args: args, Context: withDeadline(ctx, h.context())}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Widget asks a plugin to draw one of its canvas widgets, named <plugin>.<widget>, into p.Size.
func (h *Host) Widget(ctx context.Context, name string, params protocol.WidgetRenderParams) ([]string, error) {
	p, local, err := h.owner(name)
	if err != nil {
		return nil, err
	}
	params.Widget = local
	var r protocol.WidgetRenderResult
	if err := p.call(ctx, protocol.MethodWidgetRender, params, &r); err != nil {
		return nil, err
	}
	return r.Lines, nil
}

// View asks a plugin for a view it renders itself (render), named <plugin>.<view>.
func (h *Host) View(ctx context.Context, name string, args protocol.Args) (*protocol.Node, error) {
	p, local, err := h.owner(name)
	if err != nil {
		return nil, err
	}
	var n protocol.Node
	if err := p.call(ctx, protocol.MethodRender, protocol.RenderParams{View: local, Args: args, Context: h.context()}, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Run runs or plans an action; progress goes to the job in ctx.
func (h *Host) Run(ctx context.Context, name string, args protocol.Args, dryRun bool) (*protocol.RunResult, error) {
	p, local, err := h.owner(name)
	if err != nil {
		return nil, err
	}
	method := protocol.MethodRun
	if dryRun {
		method = protocol.MethodPlan
	}
	params := protocol.RunParams{Action: local, Args: args, Context: withDeadline(ctx, h.context())}
	params.Context.DryRun = dryRun
	if j := jobs.From(ctx); j != nil {
		params.Job = j.ID
		p.jobs.Store(j.ID, j)
		defer p.jobs.Delete(j.ID)
	}
	var res protocol.RunResult
	if err := p.call(ctx, method, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Stream opens a server plugin's stream; batches arrive until Done, an error, or ctx ends.
func (h *Host) Stream(ctx context.Context, name string, args protocol.Args) (<-chan protocol.StreamBatch, error) {
	p, local, err := h.owner(name)
	if err != nil {
		return nil, err
	}
	if p.Server == nil {
		return nil, unavailable("%s does not stream", name)
	}
	c, err := p.conn(ctx)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%s-%d", p.Name, h.seq.Add(1))
	in := make(chan protocol.StreamBatch, 16)
	out := make(chan protocol.StreamBatch)
	p.streams.Store(id, in)
	p.enter()
	end := func() { p.streams.Delete(id); p.leave() }
	if err := c.Call(ctx, protocol.MethodStream, protocol.StreamParams{ID: id, Source: local, Args: args, Context: h.context()}, nil); err != nil {
		end()
		return nil, err
	}
	go func() {
		defer close(out)
		defer end()
		for {
			select {
			case <-ctx.Done():
				_ = c.Notify(protocol.MethodNotifyCancel, protocol.CancelParams{ID: id})
				return
			case <-c.Done():
				out <- protocol.StreamBatch{ID: id, Done: true, Error: &protocol.Error{Code: protocol.CodeUnavailable, Message: "plugin " + p.Name + " exited"}}
				return
			case b := <-in:
				select {
				case out <- b:
				case <-ctx.Done():
					_ = c.Notify(protocol.MethodNotifyCancel, protocol.CancelParams{ID: id})
					return
				}
				_ = c.Notify(protocol.MethodNotifyAck, protocol.Ack{ID: id, Seq: b.Seq})
				if b.Done || b.Error != nil {
					return
				}
			}
		}
	}()
	return out, nil
}

// Import runs a plugin importer, returning its rig.yaml fragment.
func (h *Host) Import(ctx context.Context, name string, files []string) (*protocol.ImportResult, error) {
	p, local, err := h.owner(name)
	if err != nil {
		return nil, err
	}
	var r protocol.ImportResult
	err = p.call(ctx, protocol.MethodImport, protocol.ImportParams{Importer: local, Files: files, Dir: h.Dir, Config: p.Config}, &r)
	return &r, err
}

// Close stops every plugin process.
func (h *Host) Close() {
	for _, p := range h.plugins {
		p.stop()
	}
}

func (h *Host) secret(name string) (string, bool) {
	if h.Secret != nil {
		return h.Secret(name)
	}
	return os.LookupEnv(name)
}

func (h *Host) stateFile(plugin string) string {
	dir := h.StateDir
	if dir == "" {
		dir = filepath.Join(h.Dir, ".rig")
	}
	return filepath.Join(dir, "plugins", plugin+".json")
}

func (h *Host) readState(plugin string) map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(h.stateFile(plugin)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (h *Host) stateGet(plugin, key string) (any, bool, error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	v, ok := h.readState(plugin)[key]
	return v, ok, nil
}

func (h *Host) stateSet(plugin, key string, v any) error {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	m := h.readState(plugin)
	if v == nil {
		delete(m, key)
	} else {
		m[key] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f := h.stateFile(plugin)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	return writeAtomic(f, b, 0o600)
}

func (h *Host) components(plugin string) map[string]protocol.Args {
	if h.Components == nil {
		return nil
	}
	return h.Components(plugin)
}
