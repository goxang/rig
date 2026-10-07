package engine

import (
	"context"
	"fmt"
	"github.com/goxang/rig/ai"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

// Shared services of an environment with `infra:` run in that other environment; every per-service
// operation goes through Owner so front ends never need to know where a service runs.

// InfraApp opens (once) the environment that runs this one's shared services; nil when there is none.
func (a *App) InfraApp() (*App, error) {
	if a.Env == nil || a.Env.Infra == "" || a.Env.Infra == a.Env.Name {
		return nil, nil
	}
	a.infraOnce.Do(func() {
		a.infra, a.infraErr = Open(a.Spec.File, a.Env.Infra)
		if a.infra != nil {
			a.infra.Confirmed = a.Confirmed
		}
	})
	return a.infra, a.infraErr
}

// Owner returns the runtime that runs the named service and that environment's view of it.
func (a *App) Owner(name string) (core.Runtime, *spec.Service, error) {
	s, ok := a.Spec.Services[name]
	if !ok {
		return nil, nil, fmt.Errorf("no service %q in %s", name, a.envName())
	}
	if !s.Shared {
		return a.runtime, a.withEnv(s), nil
	}
	ia, err := a.InfraApp()
	if err != nil {
		return nil, nil, fmt.Errorf("%s runs in environment %s: %w", name, a.Env.Infra, err)
	}
	if ia == nil {
		return a.runtime, s, nil
	}
	is, ok := ia.Spec.Services[name]
	if !ok {
		return nil, nil, fmt.Errorf("%s is shared but environment %s does not define it", name, a.Env.Infra)
	}
	return ia.runtime, is, nil
}

// SharedElsewhere reports whether the service runs in the infra environment rather than this one.
func (a *App) SharedElsewhere(name string) bool {
	s, ok := a.Spec.Services[name]
	return ok && s.Shared && a.Env != nil && a.Env.Infra != "" && a.Env.Infra != a.Env.Name
}

func (a *App) Start(ctx context.Context, name string) error {
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	return rt.Start(ctx, s)
}

func (a *App) Stop(ctx context.Context, name string) error {
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	return rt.Stop(ctx, s)
}

func (a *App) Restart(ctx context.Context, name string) error {
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	return rt.Restart(ctx, s)
}

func (a *App) Scale(ctx context.Context, name string, n int) error {
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	return rt.Scale(ctx, s, max(n, 0))
}

func (a *App) Status(ctx context.Context, name string) (core.Status, error) {
	rt, s, err := a.Owner(name)
	if err != nil {
		return core.Status{}, err
	}
	st, err := rt.Status(ctx, s)
	st.Service = name
	return st, err
}

func (a *App) Logs(ctx context.Context, name string, o core.LogOptions) (<-chan core.LogLine, error) {
	rt, s, err := a.Owner(name)
	if err != nil {
		return nil, err
	}
	return rt.Logs(ctx, s, o)
}

func (a *App) Exec(ctx context.Context, name string, o core.ExecOptions) error {
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	return rt.Exec(ctx, s, o)
}

// Deploy rolls out a service. A tag deploys that tag of its image from the registry without building.
func (a *App) Deploy(ctx context.Context, name, tag string) error {
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	rel := core.Release{}
	switch {
	case strings.ContainsAny(tag, "/:@"):
		rel.Image = tag
	case tag != "" && s.Build != nil:
		rel.Image = a.TagImage(s, tag)
	}
	return rt.Deploy(ctx, s, rel)
}

// TagImage is the reference of s's image at tag in this environment's registry.
func (a *App) TagImage(s *spec.Service, tag string) string {
	reg := ""
	if r, ok := a.runtime.(core.Registrar); ok {
		reg = r.Registry()
	}
	return core.ImageRef(s, reg, tag)
}

// ScaleBy moves each service's replica count by delta (or sets it, when set is true).
func (a *App) ScaleBy(ctx context.Context, names []string, delta int, set bool) error {
	return parallel(names, func(n string) error {
		target := delta
		if !set {
			st, err := a.Status(ctx, n)
			if err != nil {
				return err
			}
			target = st.Desired + delta
		}
		if err := a.Scale(ctx, n, target); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		return nil
	})
}

// StatusAll asks for every named service, in one call per runtime when the runtime can;
// errors become StateUnknown rows.
func (a *App) StatusAll(ctx context.Context, names []string) []core.Status {
	out := make([]core.Status, len(names))
	byRT := map[core.Runtime][]int{}
	svcs := map[int]*spec.Service{}
	for i, n := range names {
		rt, s, err := a.Owner(n)
		if err != nil {
			out[i] = core.Status{Service: n, State: core.StateUnknown, Message: err.Error()}
			continue
		}
		byRT[rt] = append(byRT[rt], i)
		svcs[i] = s
	}
	var wg sync.WaitGroup
	for rt, idx := range byRT {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l, ok := rt.(core.StatusLister); ok {
				list := make([]*spec.Service, len(idx))
				for j, i := range idx {
					list[j] = svcs[i]
				}
				sts, err := l.StatusAll(ctx, list)
				if err == nil && len(sts) == len(idx) {
					for j, i := range idx {
						sts[j].Service = names[i]
						out[i] = sts[j]
					}
					return
				}
			}
			var inner sync.WaitGroup
			for _, i := range idx {
				inner.Add(1)
				go func() {
					defer inner.Done()
					st, err := rt.Status(ctx, svcs[i])
					if err != nil {
						st = core.Status{State: core.StateUnknown, Message: err.Error()}
					}
					st.Service = names[i]
					out[i] = st
				}()
			}
			inner.Wait()
		}()
	}
	wg.Wait()
	return out
}

// bridge makes shared services reachable inside this environment's runtime, when it needs that.
func (a *App) bridge(ctx context.Context, name string) error {
	if err := a.Writable(); err != nil {
		return err
	}
	b, ok := a.runtime.(core.Bridger)
	if !ok || !a.SharedElsewhere(name) {
		return nil
	}
	ia, err := a.InfraApp()
	if err != nil || ia == nil {
		return err
	}
	s := a.Spec.Services[name]
	ports := map[int]string{}
	for _, p := range s.Ports {
		addr, err := ia.Resolve(ctx, "svc://"+name+":"+strconv.Itoa(p))
		if err != nil {
			return fmt.Errorf("bridge %s: %w", name, err)
		}
		ports[p] = addr
	}
	return b.Bridge(ctx, s, ports)
}

// ---- saved queries ----

// Queries are the project's saved queries with this environment's replacing those of the same name.
func (a *App) Queries() map[string]*spec.Query {
	out := map[string]*spec.Query{}
	for n, q := range a.Spec.Queries {
		out[n] = q
	}
	if a.Env != nil {
		for n, q := range a.Env.Queries {
			out[n] = q
		}
	}
	return out
}

func (a *App) QueryNames() []string {
	names := SortedKeys(a.Queries())
	sort.SliceStable(names, func(i, j int) bool {
		qs := a.Queries()
		return qs[names[i]].Group < qs[names[j]].Group
	})
	return names
}

// FillQuery replaces {{name}} placeholders with args, then the query's defaults; it reports
// placeholders left without a value.
func FillQuery(q *spec.Query, args map[string]string) (string, []string) {
	text := q.Query
	var missing []string
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			break
		}
		j := strings.Index(text[i:], "}}")
		if j < 0 {
			break
		}
		key := strings.TrimSpace(text[i+2 : i+j])
		v, ok := args[key]
		if !ok {
			v, ok = q.Params[key]
		}
		if !ok {
			missing = append(missing, key)
			v = ""
		}
		text = text[:i] + v + text[i+j+2:]
	}
	return text, missing
}

// RunSaved runs a saved query with placeholder values.
func (a *App) RunSaved(ctx context.Context, name string, args map[string]string) (core.Table, error) {
	q, ok := a.Queries()[name]
	if !ok {
		return core.Table{}, fmt.Errorf("no saved query %q (have %v)", name, a.QueryNames())
	}
	text, missing := FillQuery(q, args)
	if len(missing) > 0 {
		return core.Table{}, fmt.Errorf("%s needs %s", name, strings.Join(missing, ", "))
	}
	return a.RunQuery(ctx, q.Source, text)
}

// RunQuery runs an ad hoc query against a component (or "runtime").
func (a *App) RunQuery(ctx context.Context, comp, text string) (core.Table, error) {
	var v any = a.runtime
	if comp != "runtime" {
		var err error
		if v, err = a.Component(comp); err != nil {
			return core.Table{}, err
		}
	}
	q, ok := v.(core.Querier)
	if !ok {
		return core.Table{}, fmt.Errorf("%s does not answer queries", comp)
	}
	if writes(q.QueryLanguage(), text) {
		if err := a.Writable(); err != nil {
			return core.Table{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return q.RunQuery(ctx, text)
}

// writes tells whether a query in lang may change something. The SQL adapter also runs every query
// of a read-only environment in a read-only transaction; the other languages only read.
func writes(lang, q string) bool {
	switch lang {
	case "sql", "redis":
		return ai.ClassifyQuery(q) != ai.Read
	case "http":
		m, _, _ := strings.Cut(strings.TrimSpace(q), " ")
		m = strings.ToUpper(m)
		return m != "GET" && m != "HEAD" && !strings.HasPrefix(m, "/")
	}
	return false
}

// ---- per-service env overrides (rig setenv, the Load screen), kept in the environment's state ----

const envPrefix = "env."

func (a *App) overrides() map[string]map[string]string {
	a.envMu.Lock()
	defer a.envMu.Unlock()
	if a.envOverrides == nil {
		a.envOverrides = map[string]map[string]string{}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		state, _ := a.LoadState(ctx)
		for k, v := range state {
			rest, ok := strings.CutPrefix(k, envPrefix)
			if !ok {
				continue
			}
			if svc, name, ok := strings.Cut(rest, "."); ok {
				if a.envOverrides[svc] == nil {
					a.envOverrides[svc] = map[string]string{}
				}
				a.envOverrides[svc][name] = v
			}
		}
	}
	return a.envOverrides
}

// withEnv is s with the OTEL_* variables under its env and its env overrides on top, as a copy; s itself is the project's and stays as written.
func (a *App) withEnv(s *spec.Service) *spec.Service {
	o, otel := a.overrides()[s.Name], a.otelEnv(s)
	if len(o) == 0 && len(otel) == 0 {
		return s
	}
	c := *s
	c.Env = map[string]string{}
	for _, m := range []map[string]string{otel, s.Env, o} {
		for k, v := range m {
			c.Env[k] = v
		}
	}
	return &c
}

// ServiceEnv is what a service runs with: its live workload's env where the runtime reads one
// (Kubernetes), else rig.yaml's with the overrides on top; overridden names what SetEnv set.
func (a *App) ServiceEnv(ctx context.Context, name string) (env map[string]string, overridden map[string]bool, err error) {
	rt, s, err := a.Owner(name)
	if err != nil {
		return nil, nil, err
	}
	overridden = map[string]bool{}
	for k := range a.EnvOverrides(name) {
		overridden[k] = true
	}
	if r, ok := rt.(core.EnvReader); ok {
		if env, err = r.LiveEnv(ctx, s); err == nil {
			return env, overridden, nil
		}
	}
	return a.withEnv(s).Env, overridden, nil
}

// EnvOverrides are the variables SetEnv gave a service.
func (a *App) EnvOverrides(name string) map[string]string { return a.overrides()[name] }

// SetEnv sets (an empty value removes) environment variables of services in this environment's state,
// so every later deploy keeps them, and redeploys the services that run so they take effect.
func (a *App) SetEnv(ctx context.Context, names []string, kv map[string]string) error {
	if err := a.Guard(); err != nil {
		return err
	}
	st := map[string]string{}
	for _, n := range names {
		for k, v := range kv {
			st[envPrefix+n+"."+k] = v
		}
	}
	if err := a.SetState(ctx, st); err != nil {
		return err
	}
	a.envMu.Lock()
	a.envOverrides = nil
	a.envMu.Unlock()
	for _, n := range names {
		st, err := a.Status(ctx, n)
		if err != nil || st.Desired == 0 {
			continue
		}
		rt, s, err := a.Owner(n)
		if err == nil {
			// same image and replica count, new env
			err = rt.Deploy(ctx, s, core.Release{Replicas: st.Desired})
		}
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	return nil
}

// StartInOrder starts names layer by layer in dependency order, waiting for a layer to be ready
// before the next: marking a whole chain and starting it never races its own dependencies.
func (a *App) StartInOrder(ctx context.Context, names []string, wait time.Duration) error {
	layers, err := a.Spec.Order(names)
	if err != nil {
		return err
	}
	for i, layer := range layers {
		if err := parallel(layer, func(n string) error {
			if s := a.Spec.Services[n]; s != nil && s.Delay > 0 && i > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(s.Delay):
				}
			}
			return a.Start(ctx, n)
		}); err != nil {
			return err
		}
		if i == len(layers)-1 {
			break
		}
		if err := parallel(layer, func(n string) error { _, err := a.Wait(ctx, n, wait); return err }); err != nil {
			return err
		}
	}
	return nil
}

// SetAutoscale moves the autoscaler bounds of a service.
func (a *App) SetAutoscale(ctx context.Context, name string, b core.Bounds) error {
	if err := a.Guard(); err != nil {
		return err
	}
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	as, ok := rt.(core.Autoscaler)
	if !ok {
		return fmt.Errorf("%s: the runtime has no autoscalers: %w", name, core.ErrUnsupported)
	}
	return as.SetAutoscale(ctx, s, b)
}

// Resources reads the requests and limits of a service's containers.
func (a *App) Resources(ctx context.Context, name string) ([]core.Resources, error) {
	rt, s, err := a.Owner(name)
	if err != nil {
		return nil, err
	}
	rs, ok := rt.(core.Resourcer)
	if !ok {
		return nil, fmt.Errorf("%s: the runtime has no requests and limits: %w", name, core.ErrUnsupported)
	}
	return rs.Resources(ctx, s)
}

// SetResources changes one container's requests and limits (which restarts its pods).
func (a *App) SetResources(ctx context.Context, name string, r core.Resources) error {
	if err := a.Guard(); err != nil {
		return err
	}
	rt, s, err := a.changing(name)
	if err != nil {
		return err
	}
	rs, ok := rt.(core.Resourcer)
	if !ok {
		return fmt.Errorf("%s: the runtime has no requests and limits: %w", name, core.ErrUnsupported)
	}
	return rs.SetResources(ctx, s, r)
}
