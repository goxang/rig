// Package contracts holds rig's contracts: the named sources and actions a plugin provides to stand
// in for a kind of tool (metrics/1, scm/1, ...), with their arguments and frame fields, a conformance
// check, bridges that let a plugin serve rig's built-in screens, and standard views.
package contracts

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/protocol"
)

//go:embed */v1.yaml */view.yaml
var files embed.FS

// Contract is one contract version. Args and fields map a name to a protocol type; an arg type ending
// in ! is required.
type Contract struct {
	ID          string                `yaml:"contract"`
	Kind        string                `yaml:"kind"`
	Description string                `yaml:"description"`
	Sources     map[string]Source     `yaml:"sources"`
	Actions     map[string]Action     `yaml:"actions"`
	Caps        map[string]Capability `yaml:"capabilities"`
	// View is the standard view's YAML, for contracts no built-in screen shows.
	View []byte `yaml:"-"`
}

type Source struct {
	Stream bool              `yaml:"stream"`
	Args   map[string]string `yaml:"args"`
	Fields map[string]string `yaml:"fields"`
}

type Action struct {
	Effect  string            `yaml:"effect"`
	Args    map[string]string `yaml:"args"`
	Outputs map[string]string `yaml:"outputs"`
}

// ArgsSchema is the JSON Schema of a source's or action's arguments, as a plugin declares them.
func (c *Contract) ArgsSchema(name string) protocol.Schema {
	args := c.Sources[name].Args
	if a, ok := c.Actions[name]; ok {
		args = a.Args
	}
	props := map[string]any{}
	var req []string
	for _, n := range sortedKeys(args) {
		t, required := strings.CutSuffix(args[n], "!")
		p := map[string]any{}
		switch t {
		case protocol.TypeInt:
			p["type"] = "integer"
		case protocol.TypeNumber:
			p["type"] = "number"
		case protocol.TypeBool:
			p["type"] = "boolean"
		case protocol.TypeJSON:
		default:
			p["type"] = "string"
		}
		props[n] = p
		if required {
			req = append(req, n)
		}
	}
	s := protocol.Schema{"type": "object", "properties": props}
	if req != nil {
		s["required"] = req
	}
	return s
}

// Capability is an optional group of sources and actions a plugin provides all of or none.
type Capability struct {
	Sources []string `yaml:"sources"`
	Actions []string `yaml:"actions"`
}

var all = sync.OnceValue(func() map[string]*Contract {
	out := map[string]*Contract{}
	dirs, _ := fs.Glob(files, "*/v1.yaml")
	for _, f := range dirs {
		raw, _ := files.ReadFile(f)
		var c Contract
		if err := yaml.Unmarshal(raw, &c); err != nil {
			panic(fmt.Sprintf("contracts: %s: %v", f, err))
		}
		c.View, _ = files.ReadFile(path.Join(path.Dir(f), "view.yaml"))
		out[c.ID] = &c
	}
	return out
})

// Get returns a contract by id (metrics/1); a bare name means its version 1.
func Get(id string) *Contract {
	if !strings.Contains(id, "/") {
		id += "/1"
	}
	return all()[id]
}

// IDs are every contract's id, sorted.
func IDs() []string {
	out := make([]string, 0, len(all()))
	for id := range all() {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ForKind is the contract that stands in for a component kind (runtime, metrics, ...), or nil.
func ForKind(kind string) *Contract {
	for _, id := range IDs() {
		if all()[id].Kind == kind {
			return all()[id]
		}
	}
	return nil
}

// Problem is one way a plugin falls short of a contract.
type Problem struct {
	Name, Text string
}

func (p Problem) String() string { return p.Name + ": " + p.Text }

// Check compares what a plugin says it provides with c: every source and action outside a capability
// is there, streams stream, and a capability is all there or not at all.
func (c *Contract) Check(p *protocol.Provides) []Problem {
	var out []Problem
	optional := map[string]bool{}
	for _, cp := range c.Caps {
		for _, n := range slices.Concat(cp.Sources, cp.Actions) {
			optional[n] = true
		}
	}
	has := func(name string, src bool) bool {
		if src {
			_, ok := p.Sources[name]
			return ok
		}
		_, ok := p.Actions[name]
		return ok
	}
	for _, n := range sortedKeys(c.Sources) {
		s, ok := p.Sources[n]
		switch {
		case !ok && optional[n]:
		case !ok:
			out = append(out, Problem{n, "source missing"})
		case c.Sources[n].Stream && !s.Stream:
			out = append(out, Problem{n, "must stream"})
		}
	}
	for _, n := range sortedKeys(c.Actions) {
		if a, ok := p.Actions[n]; !ok {
			if !optional[n] {
				out = append(out, Problem{n, "action missing"})
			}
		} else if want := c.Actions[n].Effect; want != "" && a.Effect != want {
			out = append(out, Problem{n, "effect is " + a.Effect + ", the contract says " + want})
		}
	}
	for _, n := range sortedKeys(c.Caps) {
		cp := c.Caps[n]
		var got, missing []string
		for _, s := range cp.Sources {
			if has(s, true) {
				got = append(got, s)
			} else {
				missing = append(missing, s)
			}
		}
		for _, a := range cp.Actions {
			if has(a, false) {
				got = append(got, a)
			} else {
				missing = append(missing, a)
			}
		}
		if len(got) > 0 && len(missing) > 0 {
			out = append(out, Problem{n, "capability is partial: missing " + strings.Join(missing, ", ")})
		}
	}
	return out
}

// Has says whether p provides every source and action of capability name.
func (c *Contract) Has(p *protocol.Provides, name string) bool {
	cp, ok := c.Caps[name]
	if !ok {
		return false
	}
	for _, s := range cp.Sources {
		if _, ok := p.Sources[s]; !ok {
			return false
		}
	}
	for _, a := range cp.Actions {
		if _, ok := p.Actions[a]; !ok {
			return false
		}
	}
	return true
}

// Querier is what a live check calls: the host, with names already namespaced by the plugin.
type Querier interface {
	Query(ctx context.Context, name string, args protocol.Args) (*protocol.Frame, error)
}

// CheckFrames queries every source p provides that needs no arguments and checks the frame's fields:
// each one the contract names is present with its type (a plugin may add more).
func (c *Contract) CheckFrames(ctx context.Context, q Querier, plugin string, p *protocol.Provides) []Problem {
	var out []Problem
	for _, n := range sortedKeys(c.Sources) {
		s := c.Sources[n]
		if _, ok := p.Sources[n]; !ok || s.Stream || slices.ContainsFunc(sortedKeys(s.Args), func(a string) bool { return strings.HasSuffix(s.Args[a], "!") }) {
			continue
		}
		f, err := q.Query(ctx, plugin+"."+n, protocol.Args{})
		if protocol.Unmet(err) {
			continue
		}
		if err != nil {
			out = append(out, Problem{n, err.Error()})
			continue
		}
		out = append(out, fieldProblems(n, s.Fields, f)...)
	}
	return out
}

func fieldProblems(name string, want map[string]string, f *protocol.Frame) []Problem {
	var out []Problem
	got := map[string]string{}
	for _, fl := range f.Fields {
		got[fl.Name] = fl.Type
	}
	for _, n := range sortedKeys(want) {
		t, ok := got[n]
		switch {
		case !ok:
			out = append(out, Problem{name, "field " + n + " missing"})
		case t != want[n] && !(want[n] == protocol.TypeNumber && t == protocol.TypeInt):
			out = append(out, Problem{name, "field " + n + " is " + t + ", the contract says " + want[n]})
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
