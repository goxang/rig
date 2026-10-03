// Package plugin is the adapter registry. An adapter registers a factory for one kind
// from its package init; a binary picks its adapters by importing their packages
// (adapters/all brings every bundled one).
package plugin

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

type Factory func(env core.Env, c *spec.Component) (any, error)

type Adapter struct {
	Kind        core.Kind
	Type        string
	Description string
	New         Factory
}

var (
	mu       sync.RWMutex
	adapters = map[core.Kind]map[string]Adapter{}
)

func Register(kind core.Kind, typ, description string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if adapters[kind] == nil {
		adapters[kind] = map[string]Adapter{}
	}
	if _, dup := adapters[kind][typ]; dup {
		panic(fmt.Sprintf("plugin: %s/%s registered twice", kind, typ))
	}
	adapters[kind][typ] = Adapter{Kind: kind, Type: typ, Description: description, New: f}
}

func List() []Adapter {
	mu.RLock()
	defer mu.RUnlock()
	var out []Adapter
	for _, byType := range adapters {
		for _, a := range byType {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Resolve finds the adapter for a component; kind may be omitted when the type is unique across kinds.
func Resolve(c *spec.Component) (Adapter, error) {
	mu.RLock()
	defer mu.RUnlock()
	if c.Type == "" {
		return Adapter{}, fmt.Errorf("component %s: no type", c.Name)
	}
	if c.Kind != "" {
		a, ok := adapters[core.Kind(c.Kind)][c.Type]
		if !ok {
			return Adapter{}, fmt.Errorf("component %s: no %s adapter %q (have %s)", c.Name, c.Kind, c.Type, typesOf(core.Kind(c.Kind)))
		}
		return a, nil
	}
	var found []Adapter
	for _, byType := range adapters {
		if a, ok := byType[c.Type]; ok {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Adapter{}, fmt.Errorf("component %s: unknown type %q (see `rig plugins`)", c.Name, c.Type)
	}
	var kinds []string
	for _, a := range found {
		kinds = append(kinds, string(a.Kind))
	}
	sort.Strings(kinds)
	return Adapter{}, fmt.Errorf("component %s: type %q exists for %s; set kind", c.Name, c.Type, strings.Join(kinds, ", "))
}

// New builds a component and checks it implements its kind's interface.
func New(env core.Env, c *spec.Component) (any, Adapter, error) {
	a, err := Resolve(c)
	if err != nil {
		return nil, a, err
	}
	v, err := a.New(env, c)
	if err != nil {
		return nil, a, fmt.Errorf("component %s (%s/%s): %w", c.Name, a.Kind, a.Type, err)
	}
	if !Implements(a.Kind, v) {
		return nil, a, fmt.Errorf("adapter %s/%s does not implement the %s interface", a.Kind, a.Type, a.Kind)
	}
	return v, a, nil
}

func Implements(kind core.Kind, v any) bool {
	var ok bool
	switch kind {
	case core.KindRuntime:
		_, ok = v.(core.Runtime)
	case core.KindBuilder:
		_, ok = v.(core.Builder)
	case core.KindMetrics:
		_, ok = v.(core.Metrics)
	case core.KindTracing:
		_, ok = v.(core.Tracing)
	case core.KindProfiler:
		_, ok = v.(core.Profiler)
	case core.KindDebugger:
		_, ok = v.(core.Debugger)
	case core.KindLogs:
		_, ok = v.(core.LogSource)
	case core.KindDatabase:
		_, ok = v.(core.Database)
	case core.KindCache:
		_, ok = v.(core.Cache)
	case core.KindMessaging:
		_, ok = v.(core.Messaging)
	case core.KindKV:
		_, ok = v.(core.KV)
	case core.KindLoad:
		_, ok = v.(core.LoadGenerator)
	case core.KindHosts:
		_, ok = v.(core.Hosts)
	case core.KindQuery:
		_, ok = v.(core.Querier)
	default:
		ok = true
	}
	return ok
}

func typesOf(k core.Kind) string {
	var out []string
	for t := range adapters[k] {
		out = append(out, t)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
