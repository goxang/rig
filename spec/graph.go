package spec

import (
	"fmt"
	"sort"
	"strings"
)

// Select expands targets (service names, group names, roles, or "all") to service names,
// sorted and without duplicates. No targets means every app and infra service.
func (p *Project) Select(targets []string) []string {
	seen := map[string]bool{}
	add := func(n string) { seen[n] = true }
	if len(targets) == 0 {
		targets = []string{"all"}
	}
	for _, t := range targets {
		if _, ok := p.Services[t]; ok {
			add(t)
			continue
		}
		for n, s := range p.Services {
			switch {
			case t == "all" && s.Role != RoleLoad && !s.Manual, s.InGroup(t), s.Role == t && !s.Manual:
				add(n)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Unknown lists targets that match no service, group or role.
func (p *Project) Unknown(targets []string) []string {
	var out []string
	for _, t := range targets {
		if t != "all" && len(p.Select([]string{t})) == 0 {
			out = append(out, t)
		}
	}
	return out
}

// WithDeps adds every transitive dependency of names.
func (p *Project) WithDeps(names []string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		if s, ok := p.Services[n]; ok {
			for _, d := range s.DependsOn {
				walk(d)
			}
		}
	}
	for _, n := range names {
		walk(n)
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Order groups names into start layers: every service comes after what it depends on,
// and services in one layer can start together. Dependencies outside names are ignored.
func (p *Project) Order(names []string) ([][]string, error) {
	in := map[string]bool{}
	for _, n := range names {
		in[n] = true
	}
	pending := map[string]int{}
	users := map[string][]string{}
	for _, n := range names {
		pending[n] = 0
		for _, d := range p.Services[n].DependsOn {
			if in[d] {
				pending[n]++
				users[d] = append(users[d], n)
			}
		}
	}
	var layers [][]string
	for len(pending) > 0 {
		var layer []string
		for n, c := range pending {
			if c == 0 {
				layer = append(layer, n)
			}
		}
		if len(layer) == 0 {
			var cyc []string
			for n := range pending {
				cyc = append(cyc, n)
			}
			sort.Strings(cyc)
			return nil, fmt.Errorf("dependency cycle among: %s", strings.Join(cyc, ", "))
		}
		sort.Strings(layer)
		for _, n := range layer {
			delete(pending, n)
			for _, u := range users[n] {
				pending[u]--
			}
		}
		layers = append(layers, layer)
	}
	return layers, nil
}
