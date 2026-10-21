// Package config composes a project's configuration from its layers and remembers where each value
// came from. The order, lowest first: plugin and pack defaults, imports, includes, rig.yaml, then
// .rig/local.yaml; afterwards `defaults:` and `templates:`/`extends` fill each entry. A layer above
// wins key by key, and `key: null` drops what the layers below say.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Origin struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Layer string `json:"layer"`
}

func (o Origin) String() string {
	if o.Line == 0 {
		return fmt.Sprintf("%s (%s)", o.File, o.Layer)
	}
	return fmt.Sprintf("%s:%d (%s)", o.File, o.Line, o.Layer)
}

// Doc is a configuration as a YAML tree, with each node's origin and, per path, every layer that set it.
type Doc struct {
	Root    *yaml.Node
	origins map[*yaml.Node]Origin
	trail   map[string][]Origin
	// tombs are the nulls that overrode a lower layer; a null nothing overrode stays as written
	tombs map[*yaml.Node]bool
}

func empty() *Doc {
	return &Doc{Root: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, origins: map[*yaml.Node]Origin{}, trail: map[string][]Origin{}, tombs: map[*yaml.Node]bool{}}
}

// Parse reads one layer; an empty file is an empty mapping.
func Parse(raw []byte, file, layer string) (*Doc, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	d := empty()
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		d.Root = n.Content[0]
	}
	if d.Root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a mapping", file)
	}
	d.Stamp(d.Root, file, layer)
	return d, nil
}

// FromNode makes a layer from a tree built in memory, such as an importer's fragment.
func FromNode(n *yaml.Node, file, layer string) *Doc {
	d := empty()
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n != nil && n.Kind == yaml.MappingNode {
		d.Root = n
	}
	d.Stamp(d.Root, file, layer)
	return d
}

// Stamp records file and layer as the origin of n and every node below it that has none yet.
func (d *Doc) Stamp(n *yaml.Node, file, layer string) {
	if _, ok := d.origins[n]; !ok {
		d.origins[n] = Origin{File: file, Line: n.Line, Layer: layer}
	}
	for _, c := range n.Content {
		d.Stamp(c, file, layer)
	}
}

func (d *Doc) adopt(o *Doc) {
	for n, org := range o.origins {
		if _, ok := d.origins[n]; !ok {
			d.origins[n] = org
		}
	}
	for p, t := range o.trail {
		d.trail[p] = append(t, d.trail[p]...)
	}
	for n := range o.tombs {
		d.tombs[n] = true
	}
}

// Over puts o above d: o's values win.
func (d *Doc) Over(o *Doc) {
	d.adopt(o)
	d.Root = d.merge(d.Root, o.Root, "")
}

// Under puts o below d: d's values win.
func (d *Doc) Under(o *Doc) {
	d.adopt(o)
	d.Root = d.merge(o.Root, d.Root, "")
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// merge deep-merges mapping patch onto base. Anything else in patch replaces base; a null stays as a
// tombstone until Finish, so a lower layer merged later cannot bring the key back.
func (d *Doc) merge(base, patch *yaml.Node, path string) *yaml.Node {
	if base.Kind != yaml.MappingNode || patch.Kind != yaml.MappingNode {
		if isNull(patch) {
			d.tombs[patch] = true
		}
		if path != "" {
			if len(d.trail[path]) == 0 {
				d.trail[path] = []Origin{d.origins[base]}
			}
			d.trail[path] = append(d.trail[path], d.origins[patch])
		}
		return patch
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: base.Tag, Line: base.Line, Column: base.Column}
	d.origins[out] = d.origins[base]
	idx := map[string]int{}
	for i := 0; i+1 < len(base.Content); i += 2 {
		idx[base.Content[i].Value] = len(out.Content)
		out.Content = append(out.Content, base.Content[i], base.Content[i+1])
	}
	for i := 0; i+1 < len(patch.Content); i += 2 {
		k, v := patch.Content[i], patch.Content[i+1]
		if j, ok := idx[k.Value]; ok {
			out.Content[j+1] = d.merge(out.Content[j+1], v, join(path, k.Value))
			continue
		}
		idx[k.Value] = len(out.Content)
		out.Content = append(out.Content, k, v)
	}
	return out
}

// Finish drops the tombstones: every key whose null overrode a lower layer.
func (d *Doc) Finish() {
	var drop func(n *yaml.Node)
	drop = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			out := n.Content[:0]
			for i := 0; i+1 < len(n.Content); i += 2 {
				if d.tombs[n.Content[i+1]] {
					continue
				}
				drop(n.Content[i+1])
				out = append(out, n.Content[i], n.Content[i+1])
			}
			n.Content = out
			return
		}
		for _, c := range n.Content {
			drop(c)
		}
	}
	drop(d.Root)
}

// Get finds the node at a dotted path; [i] indexes a sequence.
func (d *Doc) Get(path string) *yaml.Node {
	n := d.Root
	for _, part := range splitPath(path) {
		switch {
		case n.Kind == yaml.MappingNode:
			n = Value(n, part)
		case n.Kind == yaml.SequenceNode && strings.HasPrefix(part, "["):
			i, err := strconv.Atoi(strings.Trim(part, "[]"))
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
		default:
			return nil
		}
		if n == nil {
			return nil
		}
	}
	return n
}

func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(p, ".") {
		for {
			i := strings.Index(part, "[")
			if i <= 0 {
				break
			}
			out = append(out, part[:i])
			part = part[i:]
			if j := strings.Index(part, "]"); j > 0 && j < len(part)-1 {
				out = append(out, part[:j+1])
				part = part[j+1:]
				continue
			}
			break
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Value is the value of key in mapping n, or nil.
func Value(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func del(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

// Origin is where the value at path comes from.
func (d *Doc) Origin(path string) (Origin, bool) {
	n := d.Get(path)
	if n == nil {
		return Origin{}, false
	}
	o, ok := d.origins[n]
	return o, ok
}

// Explain is every layer that set path, lowest first; the last one won.
func (d *Doc) Explain(path string) []Origin {
	if t := d.trail[path]; len(t) > 0 {
		return t
	}
	if o, ok := d.Origin(path); ok {
		return []Origin{o}
	}
	return nil
}

// Options feed Compose what lives outside rig.yaml.
type Options struct {
	// Defaults are plugin and pack fragments, lowest first.
	Defaults []*Doc
	// Import runs one `imports:` entry and returns its fragment.
	Import func(kind, path string, project *yaml.Node) (*Doc, error)
	// NoLocal skips .rig/local.yaml.
	NoLocal bool
	// NoTemplates leaves `defaults:` and `extends` to ApplyTemplates, for a first pass that only
	// finds the plugins whose fragments may hold the templates.
	NoTemplates bool
}

// Compose builds a project's configuration from rig.yaml (raw, at file) and every layer around it.
func Compose(raw []byte, file string, o Options) (*Doc, error) {
	dir := filepath.Dir(file)
	d, err := Parse(raw, file, "rig.yaml")
	if err != nil {
		return nil, err
	}
	incs, err := includes(d.Root, dir)
	if err != nil {
		return nil, err
	}
	for i := len(incs) - 1; i >= 0; i-- {
		b, err := os.ReadFile(incs[i])
		if err != nil {
			return nil, err
		}
		inc, err := Parse(b, incs[i], "include")
		if err != nil {
			return nil, err
		}
		d.Under(inc)
	}
	if o.Import != nil {
		if imps := Value(d.Root, "imports"); imps != nil && imps.Kind == yaml.SequenceNode {
			frags := empty()
			for _, imp := range imps.Content {
				for i := 0; i+1 < len(imp.Content); i += 2 {
					kind, path := imp.Content[i].Value, imp.Content[i+1].Value
					f, err := o.Import(kind, path, d.Root)
					if err != nil {
						return nil, err
					}
					if f != nil {
						frags.Under(f)
					}
				}
			}
			d.Under(frags)
		}
	}
	for i := len(o.Defaults) - 1; i >= 0; i-- {
		d.Under(o.Defaults[i])
	}
	if !o.NoLocal {
		local := filepath.Join(dir, ".rig", "local.yaml")
		if b, err := os.ReadFile(local); err == nil {
			l, err := Parse(b, local, "local")
			if err != nil {
				return nil, err
			}
			d.Over(l)
		}
	}
	if !o.NoTemplates {
		if err := d.ApplyTemplates(file); err != nil {
			return nil, err
		}
	}
	d.Finish()
	return d, nil
}

func includes(root *yaml.Node, dir string) ([]string, error) {
	inc := Value(root, "include")
	if inc == nil {
		return nil, nil
	}
	var pats []string
	if err := inc.Decode(&pats); err != nil {
		var one string
		if inc.Decode(&one) != nil {
			return nil, fmt.Errorf("include: want a list of globs")
		}
		pats = []string{one}
	}
	var out []string
	for _, p := range pats {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		m, err := filepath.Glob(p)
		if err != nil {
			return nil, fmt.Errorf("include %s: %w", p, err)
		}
		sort.Strings(m)
		out = append(out, m...)
	}
	return out, nil
}

// templated are the sections whose entries take `defaults:` and `extends`.
var templated = []string{"services", "components", "tasks", "queries", "environments", "agents", "views"}

// ApplyTemplates is what Compose does last unless NoTemplates.
func (d *Doc) ApplyTemplates(file string) error {
	if err := d.applyTemplates(); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

// applyTemplates fills every entry of a templated section: its section's defaults, then its templates
// in order, then the entry itself. ${name} in a default or a template is the entry's name.
func (d *Doc) applyTemplates() error {
	tmpls := Value(d.Root, "templates")
	defs := Value(d.Root, "defaults")
	for _, sec := range templated {
		entries := Value(d.Root, sec)
		if entries == nil || entries.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(entries.Content); i += 2 {
			name, entry := entries.Content[i].Value, entries.Content[i+1]
			if entry.Kind != yaml.MappingNode {
				continue
			}
			base := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if def := Value(defs, sec); def != nil {
				base = d.merge(base, d.named(def, name), "")
			}
			chain, err := extends(entry, tmpls, nil)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", sec, name, err)
			}
			for _, t := range chain {
				base = d.merge(base, d.named(t, name), "")
			}
			if len(base.Content) == 0 {
				continue
			}
			del(entry, "extends")
			entries.Content[i+1] = d.merge(base, entry, join(sec, name))
		}
	}
	del(d.Root, "templates")
	del(d.Root, "defaults")
	return nil
}

// extends lists the templates n extends, deepest first, each after the ones it extends itself.
func extends(n, tmpls *yaml.Node, seen []string) ([]*yaml.Node, error) {
	ext := Value(n, "extends")
	if ext == nil {
		return nil, nil
	}
	var names []string
	if ext.Kind == yaml.SequenceNode {
		for _, c := range ext.Content {
			names = append(names, c.Value)
		}
	} else {
		names = []string{ext.Value}
	}
	var out []*yaml.Node
	for _, name := range names {
		if slices.Contains(seen, name) {
			return nil, fmt.Errorf("templates extend each other in a loop: %s", strings.Join(append(seen, name), " → "))
		}
		t := Value(tmpls, name)
		if t == nil {
			return nil, fmt.Errorf("extends unknown template %q", name)
		}
		deeper, err := extends(t, tmpls, append(seen, name))
		if err != nil {
			return nil, err
		}
		cp := clone(t)
		del(cp, "extends")
		out = append(append(out, deeper...), cp)
	}
	return out, nil
}

// named copies n with ${name} replaced by name in every scalar.
func (d *Doc) named(n *yaml.Node, name string) *yaml.Node {
	c := clone(n)
	var walk func(src, dst *yaml.Node)
	walk = func(src, dst *yaml.Node) {
		d.origins[dst] = d.origins[src]
		if dst.Kind == yaml.ScalarNode {
			dst.Value = strings.ReplaceAll(dst.Value, "${name}", name)
		}
		for i := range dst.Content {
			walk(src.Content[i], dst.Content[i])
		}
	}
	walk(n, c)
	return c
}

func clone(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, x := range n.Content {
		c.Content[i] = clone(x)
	}
	return &c
}

// Bytes is the composed document as YAML.
func (d *Doc) Bytes() ([]byte, error) {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(d.Root); err != nil {
		return nil, err
	}
	return b.Bytes(), e.Close()
}

// Cache keeps importers' fragments in dir, keyed by what they read, so a warm load parses no
// foreign files.
type Cache struct{ Dir string }

// Key is the sha256 of the importer, its version and the files it watches (directories walked).
func Key(importer, version string, watch ...string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", importer, version)
	for _, w := range watch {
		err := filepath.WalkDir(w, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if p != w && strings.HasPrefix(e.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
			h.Write(b)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c Cache) Get(key string) (*yaml.Node, bool) {
	b, err := os.ReadFile(filepath.Join(c.Dir, key+".yaml"))
	if err != nil {
		return nil, false
	}
	var n yaml.Node
	if yaml.Unmarshal(b, &n) != nil || len(n.Content) == 0 {
		return nil, false
	}
	return n.Content[0], true
}

func (c Cache) Put(key string, n *yaml.Node) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(c.Dir, key+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(c.Dir, key+".yaml"))
}
