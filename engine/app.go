// Package engine ties a project to one environment: it builds components lazily from the plugin
// registry, resolves svc:// addresses through the runtime, and runs the multi-service operations
// (up, down, status) every front end shares.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/scaffold"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

// ErrProtected stops a mutating operation on a protected environment unless the caller confirmed.
var ErrProtected = errors.New("environment is protected: pass --yes to change it")

type App struct {
	Spec *spec.Project
	Env  *spec.Environment
	// Confirmed lets mutating operations run on a protected environment.
	Confirmed bool
	// Inferred is a project with no rig.yaml, built in memory from the directory as rig init would.
	Inferred bool
	// Ask, when set, puts a yes/no question to the user (a terminal), so a protected environment
	// can be changed after a yes instead of only with --yes.
	Ask   func(question string) bool
	askMu sync.Mutex

	runtime core.Runtime

	mu      sync.Mutex
	comps   map[string]*entry
	stateMu sync.Mutex
	builds  map[string]*buildOnce

	infraOnce sync.Once
	infra     *App
	infraErr  error

	envMu        sync.Mutex
	envOverrides map[string]map[string]string // service → env set with SetEnv, nil until read

	reached sync.Map // svc:// address → what local processes reach it at (otel)
}

type entry struct {
	v       any
	adapter plugin.Adapter
	err     error
	once    sync.Once
}

// inferred are the rig.yaml paths this process built in memory (Inferred), so reopening one (another
// environment, say) infers it again instead of failing on the missing file.
var inferred sync.Map

// Open loads the project file (found from dir when file is "") for env. With no rig.yaml anywhere
// up, it infers one in memory from what the directory has, as rig init would write it (Inferred).
func Open(file, env string) (*App, error) {
	var raw []byte
	if file == "" {
		f, err := spec.Find(".")
		if errors.Is(err, spec.ErrNotFound) {
			f, raw = infer(".")
		}
		if raw == nil && err != nil {
			return nil, err
		}
		file = f
	} else if _, ok := inferred.Load(file); ok {
		if _, err := os.Stat(file); err != nil {
			_, raw = infer(filepath.Dir(file))
		}
	}
	read := func(over map[string]string) (*spec.Project, *spec.Environment, error) {
		if raw != nil {
			return spec.LoadData(raw, file, env, over)
		}
		return spec.LoadWith(file, env, over)
	}
	p, e, err := read(nil)
	if err != nil {
		return nil, err
	}
	a := &App{Spec: p, Env: e, comps: map[string]*entry{}, Inferred: raw != nil}
	if !a.Inferred {
		ignoreState(p.Dir)
	}
	if e == nil {
		return a, nil
	}
	if e.Runtime == nil || e.Runtime.Type == "" {
		return nil, fmt.Errorf("environment %s: no runtime", e.Name)
	}
	if err := a.pickNamespace(); err != nil {
		return nil, err
	}
	rc := *e.Runtime
	rc.Name, rc.Kind = "runtime", string(core.KindRuntime)
	v, _, err := plugin.New(a, &rc)
	if err != nil {
		return nil, err
	}
	a.runtime = v.(core.Runtime)
	// `rig vars set` lives in the environment's state, which needs the runtime; values in rig.yaml
	// (queries, tasks, components) were expanded without it, so expand again with the overrides
	if over := a.varOverrides(); len(over) > 0 {
		if p, e, err = read(over); err != nil {
			return nil, err
		}
		a.Spec, a.Env = p, e
		if err := a.pickNamespace(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *App) varOverrides() map[string]string {
	state, err := a.LoadState(context.Background())
	if err != nil {
		return nil
	}
	over := map[string]string{}
	for k, v := range state {
		if n, ok := strings.CutPrefix(k, "var."); ok {
			over[n] = v
		}
	}
	return over
}

func (a *App) Close() error {
	var errs []error
	a.mu.Lock()
	for _, e := range a.comps {
		if c, ok := e.v.(io.Closer); ok {
			errs = append(errs, c.Close())
		}
	}
	a.mu.Unlock()
	if c, ok := a.runtime.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	if a.infra != nil {
		errs = append(errs, a.infra.Close())
	}
	return errors.Join(errs...)
}

func (a *App) Project() *spec.Project { return a.Spec }

// core.Env

func (a *App) Environment() *spec.Environment { return a.Env }
func (a *App) Runtime() core.Runtime          { return a.runtime }

func (a *App) StateDir() string {
	env := "default"
	if a.Env != nil {
		env = a.Env.Name
	}
	return filepath.Join(a.Spec.Dir, ".rig", env)
}

// Component builds (once) and returns the named component.
func (a *App) Component(name string) (any, error) {
	if name == "runtime" && a.runtime != nil {
		return a.runtime, nil
	}
	a.mu.Lock()
	e, ok := a.comps[name]
	if !ok {
		e = &entry{}
		a.comps[name] = e
	}
	c, ok := a.Spec.Components[name]
	a.mu.Unlock()
	e.once.Do(func() {
		if !ok {
			e.err = fmt.Errorf("no component %q in %s (have %v)", name, a.Spec.File, a.componentNames())
			return
		}
		e.v, e.adapter, e.err = plugin.New(a, c)
	})
	return e.v, e.err
}

func (a *App) componentNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.Spec.Components))
	for n := range a.Spec.Components {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Kind tells which kind a configured component is, without building it.
func (a *App) Kind(name string) (core.Kind, string, error) {
	if name == "runtime" && a.Env != nil {
		return core.KindRuntime, a.Env.Runtime.Type, nil
	}
	a.mu.Lock()
	c, ok := a.Spec.Components[name]
	a.mu.Unlock()
	if !ok {
		return "", "", fmt.Errorf("no component %q in %s (have %v)", name, a.Spec.File, a.componentNames())
	}
	ad, err := plugin.Resolve(c)
	return ad.Kind, ad.Type, err
}

// Names lists configured components of a kind, sorted.
func (a *App) Names(kind core.Kind) []string {
	var out []string
	for _, n := range a.componentNames() {
		if strings.HasPrefix(n, "default:") {
			continue
		}
		if k, _, err := a.Kind(n); err == nil && k == kind {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Get returns component name of kind as T; with name "" the only (or first) one of that kind,
// falling back to what the runtime itself provides (logs, hosts) or to a default adapter.
func Get[T any](a *App, kind core.Kind, name string) (T, string, error) {
	var zero T
	if name == "" {
		names := a.Names(kind)
		if len(names) > 0 {
			name = names[0]
		}
	}
	if name != "" {
		v, err := a.Component(name)
		if err != nil {
			return zero, name, err
		}
		t, ok := v.(T)
		if !ok {
			return zero, name, fmt.Errorf("component %s is not a %s", name, kind)
		}
		return t, name, nil
	}
	if t, ok := a.runtime.(T); ok && a.runtime != nil {
		return t, "runtime", nil
	}
	if def, ok := defaults[kind]; ok {
		v, err := a.defaultComponent(kind, def)
		if err != nil {
			return zero, def, err
		}
		if t, ok := v.(T); ok {
			return t, def, nil
		}
	}
	return zero, "", fmt.Errorf("no %s component configured in this environment", kind)
}

var defaults = map[core.Kind]string{
	core.KindProfiler: "pprof",
	core.KindDebugger: "delve",
	core.KindLogs:     "runtime",
}

func (a *App) defaultComponent(kind core.Kind, typ string) (any, error) {
	key := "default:" + string(kind)
	a.mu.Lock()
	if _, ok := a.Spec.Components[key]; !ok {
		a.Spec.Components[key] = &spec.Component{Name: key, Kind: string(kind), Type: typ}
	}
	a.mu.Unlock()
	return a.Component(key)
}

// All builds every configured component of kind; failures come back per name.
func All[T any](a *App, kind core.Kind) (map[string]T, map[string]error) {
	out, errs := map[string]T{}, map[string]error{}
	for _, n := range a.Names(kind) {
		t, _, err := Get[T](a, kind, n)
		if err != nil {
			errs[n] = err
			continue
		}
		out[n] = t
	}
	return out, errs
}

// Queriers lists every component that answers ad hoc queries, the runtime included.
func (a *App) Queriers() map[string]core.Querier {
	out := map[string]core.Querier{}
	if q, ok := a.runtime.(core.Querier); ok {
		out["runtime"] = q
	}
	for _, n := range a.componentNames() {
		if strings.HasPrefix(n, "default:") {
			continue
		}
		if v, err := a.Component(n); err == nil {
			if q, ok := v.(core.Querier); ok {
				out[n] = q
			}
		}
	}
	return out
}

func (a *App) Service(name string) (*spec.Service, error) {
	s, ok := a.Spec.Services[name]
	if !ok {
		return nil, fmt.Errorf("no service %q in %s", name, a.envName())
	}
	return s, nil
}

func (a *App) envName() string {
	if a.Env == nil {
		return "project"
	}
	return "environment " + a.Env.Name
}

// Guard refuses a mutating operation on a read-only environment, and on a protected one the caller
// did not confirm. Every change passes here (or through Writable), so readonly holds whoever calls.
func (a *App) Guard() error {
	if err := a.Writable(); err != nil {
		return err
	}
	if a.Env == nil || !a.Env.Protected {
		return nil
	}
	a.askMu.Lock()
	defer a.askMu.Unlock()
	if !a.Confirmed && a.Ask != nil {
		a.Confirmed = a.Ask(a.Env.Name + " is protected (others use it): change it?")
		a.Ask = nil
	}
	if !a.Confirmed {
		return fmt.Errorf("%s: %w", a.Env.Name, ErrProtected)
	}
	return nil
}

// Writable refuses any change to a read-only environment; --yes and a confirmation do not lift it.
func (a *App) Writable() error { return core.Writable(a) }

// changing is Owner for an operation that changes the service.
func (a *App) changing(name string) (core.Runtime, *spec.Service, error) {
	if err := a.Writable(); err != nil {
		return nil, nil, err
	}
	return a.Owner(name)
}

// Resolve turns svc://service:port[/path] into an address reachable from here; anything else is returned as is.
func (a *App) Resolve(ctx context.Context, addr string) (string, error) {
	rest, ok := strings.CutPrefix(addr, "svc://")
	if !ok {
		return addr, nil
	}
	hostport, path, _ := strings.Cut(rest, "/")
	host, port, _ := strings.Cut(hostport, ":")
	if a.SharedElsewhere(host) {
		if ia, err := a.InfraApp(); err != nil {
			return "", err
		} else if ia != nil {
			return ia.Resolve(ctx, addr)
		}
	}
	n := 0
	if s, ok := a.Spec.Services[host]; ok {
		n = s.PortNumber(port)
		if n == 0 && port == "" {
			n = s.FirstPort()
		}
	} else {
		n, _ = strconv.Atoi(port)
	}
	if n == 0 {
		return "", fmt.Errorf("%s: no port", addr)
	}
	f, ok := a.runtime.(core.Forwarder)
	if !ok {
		return "", fmt.Errorf("%s: runtime cannot reach services (no forwarding)", addr)
	}
	out, err := f.Forward(ctx, core.Target{Service: host, Port: n})
	if err != nil {
		return "", err
	}
	if path != "" {
		out += "/" + path
	}
	return out, nil
}

// ---- state ----

func (a *App) LoadState(ctx context.Context) (map[string]string, error) {
	if s, ok := a.runtime.(core.StateStore); ok {
		return s.LoadState(ctx)
	}
	raw, err := os.ReadFile(filepath.Join(a.StateDir(), "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(raw, &m)
}

// SetState merges kv into the state; an empty value deletes the key.
func (a *App) SetState(ctx context.Context, kv map[string]string) error {
	if err := a.Writable(); err != nil {
		return err
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	m, err := a.LoadState(ctx)
	if err != nil {
		return err
	}
	for k, v := range kv {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	if s, ok := a.runtime.(core.StateStore); ok {
		return s.SaveState(ctx, m)
	}
	if err := os.MkdirAll(a.StateDir(), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(a.StateDir(), "state.json"), raw, 0o644)
}

// ManifestDirs is where this project keeps Kubernetes manifests: the project's manifests:, else the
// runtime's manifests option, else the project directory.
func (a *App) ManifestDirs() []string {
	abs := func(ds []string) []string {
		out := make([]string, 0, len(ds))
		for _, d := range ds {
			if !filepath.IsAbs(d) {
				d = filepath.Join(a.Spec.Dir, d)
			}
			out = append(out, d)
		}
		return out
	}
	if len(a.Spec.Manifests) > 0 {
		return abs(a.Spec.Manifests)
	}
	if a.Env != nil && a.Env.Runtime != nil {
		var o struct {
			Manifests []string `yaml:"manifests"`
		}
		_ = a.Env.Runtime.Decode(&o)
		if len(o.Manifests) > 0 {
			return abs(o.Manifests)
		}
	}
	if ds := a.Spec.ImportPaths("kubernetes", "manifests"); len(ds) > 0 {
		return ds
	}
	return []string{a.Spec.Dir}
}

func (a *App) DefaultImage(ctx context.Context, s *spec.Service) string {
	if s.Build == nil {
		return s.Image
	}
	st, err := a.LoadState(ctx)
	if err != nil {
		return ""
	}
	return st["image."+s.Name]
}

// ignoreState adds .rig/ to the project's .gitignore once rig keeps state there, so pids, logs,
// profiles and reports never show up as changes to commit. Projects outside git are left alone.
// infer builds a rig.yaml for dir from its compose files, manifests and sources (rig init's
// scaffold); nil when it finds nothing to run.
func infer(dir string) (string, []byte) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil
	}
	plan, err := scaffold.Detect(context.Background(), abs, scaffold.Options{})
	if err != nil || len(plan.Services) == 0 {
		return "", nil
	}
	raw, err := plan.YAML()
	if err != nil {
		return "", nil
	}
	file := filepath.Join(abs, "rig.yaml")
	inferred.Store(file, true)
	return file, raw
}

func ignoreState(dir string) {
	if _, err := os.Stat(filepath.Join(dir, ".rig")); err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return
	}
	file := filepath.Join(dir, ".gitignore")
	raw, _ := os.ReadFile(file)
	for _, l := range strings.Split(string(raw), "\n") {
		switch strings.TrimSpace(l) {
		case ".rig", ".rig/", "/.rig", "/.rig/":
			return
		}
	}
	text := "\n# rig: pids, logs, profiles, reports, test runs (rig.yaml)\n.rig/\n"
	if len(raw) == 0 || raw[len(raw)-1] == '\n' {
		text = text[1:]
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(text)
}

func lastEnvFile(dir string) string { return filepath.Join(dir, ".rig", "last-env") }

// LastEnv is the environment the UI last showed in the project of file ("" finds rig.yaml from here up).
func LastEnv(file string) string {
	if file == "" {
		f, err := spec.Find(".")
		if err != nil {
			return ""
		}
		file = f
	}
	raw, _ := os.ReadFile(lastEnvFile(filepath.Dir(file)))
	return strings.TrimSpace(string(raw))
}

// RememberEnv makes this environment the one the next `rig` opens on.
func (a *App) RememberEnv() {
	if a.Env == nil || a.Inferred {
		return
	}
	f := lastEnvFile(a.Spec.Dir)
	if os.MkdirAll(filepath.Dir(f), 0o755) == nil {
		_ = os.WriteFile(f, []byte(a.Env.Name+"\n"), 0o644)
	}
}
