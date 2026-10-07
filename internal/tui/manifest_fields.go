package tui

import (
	"context"
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/manifest"
)

const secretMask = "••••••"

// yamlTree is a YAML node as a jnode tree, kids in the file's order; Secret values are masked.
func yamlTree(n *yaml.Node, secret bool) *jnode {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return &jnode{kind: 'z', scalar: "null"}
		}
		return yamlTree(n.Content[0], secret)
	case yaml.AliasNode:
		return yamlTree(n.Alias, secret)
	case yaml.MappingNode, yaml.SequenceNode:
		j := &jnode{kind: 'a'}
		step := 1
		if n.Kind == yaml.MappingNode {
			j.kind, step = 'o', 2
		}
		for i := 0; i+step-1 < len(n.Content); i += step {
			kid := yamlTree(n.Content[i+step-1], false)
			if step == 2 {
				kid.key = n.Content[i].Value
				if secret && (kid.key == "data" || kid.key == "stringData") && kid.kind == 'o' {
					for _, v := range kid.kids {
						b, _ := json.Marshal(secretMask)
						v.kind, v.scalar, v.kids = 's', string(b), nil
					}
				}
			}
			kid.parent = j
			j.kids = append(j.kids, kid)
		}
		return j
	}
	switch n.ShortTag() {
	case "!!int", "!!float":
		return &jnode{kind: 'n', scalar: n.Value}
	case "!!bool":
		return &jnode{kind: 'b', scalar: n.Value}
	case "!!null":
		return &jnode{kind: 'z', scalar: "null"}
	}
	b, _ := json.Marshal(n.Value)
	return &jnode{kind: 's', scalar: string(b)}
}

// yamlAt is the YAML node j stands for, found by the same child positions from root.
func yamlAt(root *yaml.Node, j *jnode) *yaml.Node {
	var idx []int
	for c := j; c.parent != nil; c = c.parent {
		idx = append([]int{c.index()}, idx...)
	}
	y := root
	for _, i := range idx {
		for y.Kind == yaml.AliasNode || y.Kind == yaml.DocumentNode {
			if y.Kind == yaml.AliasNode {
				y = y.Alias
			} else {
				y = y.Content[0]
			}
		}
		switch {
		case y.Kind == yaml.MappingNode && 2*i+1 < len(y.Content):
			y = y.Content[2*i+1]
		case y.Kind == yaml.SequenceNode && i < len(y.Content):
			y = y.Content[i]
		default:
			return nil
		}
	}
	return y
}

// toYAML is j as a fresh YAML node.
func toYAML(j *jnode) *yaml.Node {
	switch j.kind {
	case 'o', 'a':
		y := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if j.kind == 'o' {
			y.Kind, y.Tag = yaml.MappingNode, "!!map"
		}
		for _, k := range j.kids {
			if j.kind == 'o' {
				y.Content = append(y.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k.key})
			}
			y.Content = append(y.Content, toYAML(k))
		}
		return y
	case 'n':
		tag := "!!int"
		if strings.ContainsAny(j.scalar, ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: j.scalar}
	case 'b':
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: j.scalar}
	case 'z':
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: j.text()}
}

// replaceYAML puts j's value into y, keeping y's comments and, string for string, its quoting.
func replaceYAML(y *yaml.Node, j *jnode) {
	nn := toYAML(j)
	if y.Kind == yaml.ScalarNode && nn.Tag == "!!str" && y.ShortTag() == "!!str" {
		nn.Style = y.Style
	}
	nn.HeadComment, nn.LineComment, nn.FootComment = y.HeadComment, y.LineComment, y.FootComment
	nn.Line, nn.Column = y.Line, y.Column
	*y = *nn
}

// setFields shows o field by field, keeping the folds and the selected field when o is the object
// shown before (a rescan after a save gives it a new pointer).
func (t *manifestsTab) setFields(o *manifest.Object) {
	if t.fieldsObj == o {
		return
	}
	tree := &jsonTree{zone: "mf:fields", root: yamlTree(o.Node, o.Kind == "Secret")}
	if old := t.fields; old != nil && t.fieldsID == o.File+"#"+o.ID() {
		closed := map[string]bool{}
		walkNodes(old.root, func(n *jnode) bool {
			if n.closed {
				closed[n.path()] = true
			}
			return true
		})
		walkNodes(tree.root, func(n *jnode) bool {
			n.closed = closed[n.path()]
			return true
		})
		tree.flatten()
		tree.selectPath(old.current().path())
		tree.off = old.off
	} else {
		tree.flatten()
		t.inFields = false
	}
	t.fields, t.fieldsObj, t.fieldsID = tree, o, o.File+"#"+o.ID()
}

func (t *manifestsTab) fieldKey(m *model, k tea.KeyMsg) tea.Cmd {
	n := t.fields.current()
	switch k.String() {
	case "esc":
		t.inFields = false
		if t.viaSearch && len(t.hits) > 0 {
			t.showHits(m, t.hits)
		}
	case "q":
		t.inFields = false
	case "enter", "e":
		if n.container() {
			n.closed = !n.closed
			t.fields.flatten()
			return nil
		}
		return t.editField(m)
	case "a":
		parent := n
		if !n.container() {
			parent = n.parent
		}
		if parent == nil {
			return nil
		}
		value := func(key string) {
			m.ask("value of "+parent.path()+"."+key+" (JSON, or text)", "", func(v string) tea.Cmd {
				kid := &jnode{key: key, parent: parent}
				kid.set(v)
				return t.saveField("add "+parent.path()+"."+key, m, func(o *manifest.Object) (*yaml.Node, bool) {
					y := yamlAt(o.Node, parent)
					switch {
					case y == nil:
						return nil, false
					case y.Kind == yaml.MappingNode:
						y.Content = append(y.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key})
					case y.Kind != yaml.SequenceNode:
						return nil, false
					}
					y.Content = append(y.Content, toYAML(kid))
					return nil, true
				})
			})
		}
		if parent.kind == 'a' {
			value("")
			return nil
		}
		m.ask("new field in "+parent.path(), "", func(key string) tea.Cmd {
			if key = strings.TrimSpace(key); key != "" {
				value(key)
			}
			return nil
		})
	case "D", "delete":
		p := n.parent
		if p == nil {
			return nil
		}
		i := n.index()
		return t.saveField("delete "+n.path(), m, func(o *manifest.Object) (*yaml.Node, bool) {
			y := yamlAt(o.Node, p)
			switch {
			case y != nil && y.Kind == yaml.MappingNode && 2*i+1 < len(y.Content):
				y.Content = append(y.Content[:2*i], y.Content[2*i+2:]...)
			case y != nil && y.Kind == yaml.SequenceNode && i < len(y.Content):
				y.Content = append(y.Content[:i], y.Content[i+1:]...)
			default:
				return nil, false
			}
			return nil, true
		})
	case "y":
		copyText(n.text())
		m.setStatus("copied "+n.path(), false)
	default:
		t.fields.key(k)
	}
	return nil
}

func (t *manifestsTab) editField(m *model) tea.Cmd {
	n := t.fields.current()
	cur := n.text()
	if cur == secretMask {
		cur = ""
	}
	o := t.fieldsObj
	m.askAI(o.ID()+" "+n.path(), cur, "the new value of field "+n.path()+" of Kubernetes object "+o.ID()+", same type as now", func(v string) tea.Cmd {
		if v == cur {
			return nil
		}
		j := &jnode{kind: n.kind, scalar: n.scalar}
		j.set(v)
		return t.saveField("set "+n.path(), m, func(o *manifest.Object) (*yaml.Node, bool) {
			y := yamlAt(o.Node, n)
			if y != nil {
				replaceYAML(y, j)
			}
			return y, y != nil
		})
	})
	return nil
}

// saveField changes the shown object's YAML with edit and writes it into its file: a changed scalar
// in place, anything else as the re-encoded document, comments and the other documents kept. The
// rescan shows the file as it is now either way.
func (t *manifestsTab) saveField(what string, m *model, edit func(o *manifest.Object) (scalar *yaml.Node, ok bool)) tea.Cmd {
	o := t.fieldsObj
	if isLive(o) {
		return t.applyField(what, m, edit)
	}
	defer func() {
		t.fieldsObj = nil
		m.rescanManifests()
	}()
	scalar, ok := edit(o)
	if !ok {
		m.setStatus(what+": the file changed underneath, r rescans", true)
		return nil
	}
	var err error
	if scalar != nil {
		ok, err = manifest.PatchScalar(o, scalar)
	}
	if err == nil && (scalar == nil || !ok) {
		var doc []byte
		if doc, err = o.YAML(); err == nil {
			err = manifest.ReplaceDoc(o, doc)
		}
	}
	if err != nil {
		m.setStatus(what+": "+err.Error(), true)
		return nil
	}
	m.setStatus(what+" · saved in "+relTo(m.app.Spec.Dir, o.File)+" · a applies it", false)
	return nil
}

// applyField edits a live object and applies it to the cluster; the list reloads once it has run.
func (t *manifestsTab) applyField(what string, m *model, edit func(o *manifest.Object) (*yaml.Node, bool)) tea.Cmd {
	o := t.fieldsObj
	ap, ok := m.app.Runtime().(applier)
	if !ok {
		return nil
	}
	if o.Kind == "Secret" {
		m.setStatus("a secret's values are not read here: L edits it on the cluster", true)
		return nil
	}
	if _, ok := edit(o); !ok {
		m.setStatus(what+": the object changed, r reloads", true)
		return nil
	}
	t.fieldsObj, t.liveStale = nil, true
	return m.act(what+" on "+o.ID()+" in "+m.app.Env.Name, true, func(ctx context.Context) error {
		return ap.ApplyManifests(ctx, []*manifest.Object{o})
	})
}
