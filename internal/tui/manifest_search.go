package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/manifest"
)

// manifestHit is one search row: an object, the path of one of its fields (empty for the object
// itself), the field's value and its line in the object's YAML as the yaml panel shows it.
type manifestHit struct {
	obj   *manifest.Object
	path  string
	value string
	line  int
}

// manifestHits are the object's own row and a row per scalar field, from the masked YAML the yaml
// panel shows, so a Secret's values are never searchable.
func manifestHits(o *manifest.Object) []manifestHit {
	hits := []manifestHit{{obj: o}}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if enc.Encode(masked(o)) != nil {
		return hits
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(buf.String()), &doc) != nil || len(doc.Content) == 0 {
		return hits
	}
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i+1], path+"."+n.Content[i].Value)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, path+"["+strconv.Itoa(i)+"]")
			}
		case yaml.ScalarNode:
			hits = append(hits, manifestHit{obj: o, path: strings.TrimPrefix(path, "."), value: oneLine(n.Value), line: n.Line - 1})
		}
	}
	walk(doc.Content[0], "")
	return hits
}

// search lists every object, field and value of the scanned manifests in a picker that filters as
// you type; enter selects the object and its field. The hit list is built once per scan and reused
// across searches; r rescans and invalidates it.
func (t *manifestsTab) search(m *model) tea.Cmd {
	if t.set == nil {
		return nil
	}
	if t.hitsSet != t.set {
		t.hits = nil
		for _, o := range t.set.Objects {
			t.hits = append(t.hits, manifestHits(o)...)
		}
		t.hitsSet = t.set
	}
	t.showHits(m, t.hits)
	return nil
}

// showHits reopens the search picker on a set of hits (the last search, when esc backs out of a
// detail view reached through it) without rebuilding the index.
func (t *manifestsTab) showHits(m *model, hits []manifestHit) {
	items, desc := make([]string, len(hits)), make([]string, len(hits))
	for i, h := range hits {
		items[i] = h.obj.ID()
		if h.path != "" {
			items[i] += "  " + h.path
		}
		desc[i] = h.value
	}
	m.pick("search manifests: type part of a kind/name, field or value", items, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		for i, it := range items {
			if it == c[0] {
				t.jump(hits[i])
				return nil
			}
		}
		return nil
	})
}

// jump shows every object again and selects the hit's object and field.
func (t *manifestsTab) jump(h manifestHit) {
	t.tree, t.file, t.filter, t.offset = false, "", "", 0
	for i, e := range t.entries() {
		if e.obj == h.obj {
			t.sel = i
			break
		}
	}
	t.jumpPath = ""
	if h.path != "" {
		t.jumpPath = "$." + h.path
	}
	t.viaSearch = true
}
