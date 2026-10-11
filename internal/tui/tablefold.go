package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// tableFold is a table whose rows form a tree (fold: <parent field> on the widget, ids in the id
// field): parents fold and unfold, a folded parent still walks like any row.
type tableFold struct {
	collapsed map[string]bool
	parent    map[string]string
	kids      map[string]bool
	byRow     map[string]string // grid row id to tree id
	byTree    map[string]string
}

func foldField(wn *wnode) string { return view.Prop(wn.n, "fold") }

// index reads the frame's tree: who is whose parent, who has children.
func (wn *wnode) index(f *protocol.Frame, parentField string) *tableFold {
	fo := wn.fold
	if fo == nil {
		fo = &tableFold{collapsed: map[string]bool{}}
		wn.fold = fo
	}
	fo.parent, fo.kids, fo.byRow, fo.byTree = map[string]string{}, map[string]bool{}, map[string]string{}, map[string]string{}
	idc, pc := fieldIndex(f, "id"), fieldIndex(f, parentField)
	if idc < 0 || pc < 0 {
		return fo
	}
	for i := range frames.Len(f) {
		id, p := fmt.Sprint(f.Columns[idc][i]), strings.TrimSpace(fmt.Sprint(f.Columns[pc][i]))
		if f.Columns[pc][i] == nil || p == "<nil>" {
			p = ""
		}
		fo.byRow[fmt.Sprint(i)], fo.byTree[id] = id, fmt.Sprint(i)
		if p != "" {
			fo.parent[id] = p
			fo.kids[p] = true
		}
	}
	return fo
}

func fieldIndex(f *protocol.Frame, name string) int {
	return slices.IndexFunc(f.Fields, func(fl protocol.Field) bool { return fl.Name == name })
}

// depthOf is how deep a row sits, and whether a folded ancestor hides it.
func (fo *tableFold) depthOf(id string) (depth int, hidden bool) {
	for p := fo.parent[id]; p != "" && depth < 64; p = fo.parent[p] {
		if _, known := fo.byTree[p]; !known {
			break
		}
		depth++
		hidden = hidden || fo.collapsed[p]
	}
	return
}

// key folds the selected row; it reports whether k was a fold key (←→ fold and climb, space, + - z).
func (fo *tableFold) key(g *grid, k string) bool {
	r, ok := g.current()
	if !ok {
		return false
	}
	id := fo.byRow[r.id]
	switch k {
	case "right", "space":
		if fo.kids[id] && fo.collapsed[id] {
			delete(fo.collapsed, id)
			return true
		}
		if k == "space" && fo.kids[id] {
			fo.collapsed[id] = true
			return true
		}
	case "left":
		if fo.kids[id] && !fo.collapsed[id] {
			fo.collapsed[id] = true
			return true
		}
		if p, ok := fo.byTree[fo.parent[id]]; ok {
			g.want = p
			g.sel = slices.IndexFunc(g.rows, func(x grow) bool { return x.id == p })
			g.sel = max(g.sel, 0)
			return true
		}
	case "+":
		clear(fo.collapsed)
		return true
	case "-", "z":
		all := k == "-" || len(fo.collapsed) == 0
		if !all {
			clear(fo.collapsed)
			return true
		}
		for p := range fo.kids {
			fo.collapsed[p] = true
		}
		return true
	}
	return false
}
