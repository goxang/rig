package engine

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/goxang/rig/contracts"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

type pluginHost struct {
	once sync.Once
	h    *host.Host
}

// Host runs the project's plugins, started on first use; the engine and the components plugins serve
// share it.
func (a *App) Host() *host.Host {
	a.plugins.once.Do(func() {
		env := ""
		if a.Env != nil {
			env = a.Env.Name
		}
		a.plugins.h = host.New(host.Options{Dir: a.Spec.Dir, File: a.Spec.File, Env: env, StateDir: a.StateDir(),
			Secret: func(name string) (string, bool) {
				if v, ok := a.KnownSecrets()[name]; ok {
					return v, true
				}
				return os.LookupEnv(name)
			},
			Components: a.componentsOf}, a.Spec.Plugins)
	})
	return a.plugins.h
}

// componentsOf are the components of type plugin, each as its rig.yaml keys.
func (a *App) componentsOf(plugin string) map[string]protocol.Args {
	out := map[string]protocol.Args{}
	for n, c := range a.Spec.Components {
		if c.Type != plugin {
			continue
		}
		var m protocol.Args
		_ = c.Node.Decode(&m)
		out[n] = m
	}
	if a.Env != nil && a.Env.Runtime != nil && a.Env.Runtime.Type == plugin {
		var m protocol.Args
		_ = a.Env.Runtime.Node.Decode(&m)
		out["runtime"] = m
	}
	return out
}

// newComponent builds c from a bundled adapter or, when its type names a plugin that implements the
// kind's contract, a bridge to that plugin.
func (a *App) newComponent(c *spec.Component) (any, plugin.Adapter, error) {
	if _, err := plugin.Resolve(c); err == nil || !a.hasPlugin(c.Type) {
		v, ad, err := plugin.New(a, c)
		if err != nil && len(plugin.List()) == 0 {
			err = fmt.Errorf("%w: no plugin named %q is installed (the official ones: %s)", err, c.Type, InstallStd)
		}
		return v, ad, err
	}
	p, kind := a.pluginKind(c)
	if p == nil {
		return plugin.New(a, c)
	}
	ad := plugin.Adapter{Kind: kind, Type: c.Type, Description: p.Description}
	v, err := contracts.Bridge(kind, a.Host(), c.Type, c.Name, p.Provides(context.Background()))
	return v, ad, err
}

// pluginKind is the plugin c's type names and the kind it serves: c's own, else its first contract's.
func (a *App) pluginKind(c *spec.Component) (*host.Plugin, core.Kind) {
	p := a.Host().Get(c.Type)
	if p == nil {
		return nil, ""
	}
	if c.Kind != "" {
		return p, core.Kind(c.Kind)
	}
	for _, id := range p.Implements {
		if ct := contracts.Get(id); ct != nil && ct.Kind != "" {
			return p, core.Kind(ct.Kind)
		}
	}
	return p, ""
}

func (a *App) hasPlugin(name string) bool {
	ms, _ := host.Discover(a.Spec.Dir, a.Spec.Plugins)
	for _, m := range ms {
		if m.Name == name {
			return true
		}
	}
	return false
}
