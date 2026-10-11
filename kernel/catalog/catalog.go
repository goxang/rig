// Package catalog lists what a project has, as typed resources: core kinds from rig.yaml and
// kinds plugins add, such as terraform.stack.
package catalog

import (
	"fmt"
	"sort"
	"sync"

	"github.com/goxang/rig/kernel/config"
	"github.com/goxang/rig/spec"
)

type Resource struct {
	Kind string
	Name string
	Spec any
	// Links are refs this resource points at, such as the services it depends on.
	Links  []string
	Origin config.Origin
}

func (r Resource) ID() string { return r.Kind + "/" + r.Name }

// Ref is the resource's own ref, for the kinds core owns a scheme for.
func (r Resource) Ref() string {
	switch r.Kind {
	case "service":
		return "svc://" + r.Name
	case "environment":
		return "env://" + r.Name
	case "view":
		return "view://" + r.Name
	}
	return ""
}

type Catalog struct {
	mu sync.RWMutex
	m  map[string]Resource
}

func New() *Catalog { return &Catalog{m: map[string]Resource{}} }

// Build is the catalog of p's own resources.
func Build(p *spec.Project) *Catalog {
	c := New()
	origin := func(path string) config.Origin {
		if p.Config == nil {
			return config.Origin{File: p.File}
		}
		o, _ := p.Config.Origin(path)
		return o
	}
	for n, s := range p.Services {
		var links []string
		for _, d := range s.DependsOn {
			links = append(links, "svc://"+d)
		}
		c.Add(Resource{Kind: "service", Name: n, Spec: s, Links: links, Origin: origin("services." + n)})
	}
	add := func(kind, section string, names []string, spec func(string) any) {
		for _, n := range names {
			c.Add(Resource{Kind: kind, Name: n, Spec: spec(n), Origin: origin(section + "." + n)})
		}
	}
	add("component", "components", keys(p.Components), func(n string) any { return p.Components[n] })
	add("environment", "environments", keys(p.Environments), func(n string) any { return p.Environments[n] })
	add("task", "tasks", keys(p.Tasks), func(n string) any { return p.Tasks[n] })
	add("query", "queries", keys(p.Queries), func(n string) any { return p.Queries[n] })
	add("dashboard", "dashboards", keys(p.Dashboards), func(n string) any { return p.Dashboards[n] })
	add("suite", "tests", keys(p.Tests), func(n string) any { return p.Tests[n] })
	add("report", "reports", keys(p.Reports), func(n string) any { return p.Reports[n] })
	return c
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Add puts r in the catalog, over any resource of the same kind and name.
func (c *Catalog) Add(r Resource) {
	c.mu.Lock()
	c.m[r.ID()] = r
	c.mu.Unlock()
}

func (c *Catalog) Get(kind, name string) (Resource, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.m[kind+"/"+name]
	return r, ok
}

// List is the resources of kind, or of every kind for "", sorted by kind and name.
func (c *Catalog) List(kind string) []Resource {
	c.mu.RLock()
	var out []Resource
	for _, r := range c.m {
		if kind == "" || r.Kind == kind {
			out = append(out, r)
		}
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Kinds is every kind in the catalog with how many resources it has.
func (c *Catalog) Kinds() map[string]int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]int{}
	for _, r := range c.m {
		out[r.Kind]++
	}
	return out
}

func (r Resource) String() string { return fmt.Sprintf("%s (%s)", r.ID(), r.Origin) }
