package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/core"
)

func TestDataBackKeepsRow(t *testing.T) {
	c := dataComp{name: "db", browse: true}
	d := &dataTab{comps: []dataComp{c}, left: newGrid("l"), right: newGrid("dright"), paths: map[string][]string{}}
	d.left.set([]grow{{id: "db"}})
	d.table = core.Table{Columns: []string{"name"}, Rows: [][]string{{"a"}, {"b"}, {"c"}, {"d"}}}
	d.fill()
	d.right.sel = 2
	d.remember(c)
	d.paths["db"] = []string{"c"}

	d.paths["db"] = nil
	d.right, d.restore = newGrid("dright"), d.picked[d.place(c)]
	d.fill()
	if r, _ := d.right.current(); r.cells[0] != "c" {
		t.Errorf("back landed on %v, want c", r.cells)
	}
}

func TestDataEscapeFromQuery(t *testing.T) {
	c := dataComp{name: "db", browse: true}
	d := &dataTab{comps: []dataComp{c}, left: newGrid("l"), right: newGrid("dright"), paths: map[string][]string{}, focus: 1}
	d.left.set([]grow{{id: "db"}})
	d.query, d.queryAt = "select 1", []string{"x"}
	m := &model{ai: &ai.Runner{}}

	d.key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.prompt == nil || m.prompt.input.Value() != "select 1" {
		t.Fatalf("esc on a query result should reopen the popup with the query, got %+v", m.prompt)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if m.prompt != nil || d.query != "" {
		t.Fatalf("a second esc should leave the query, prompt %v query %q", m.prompt, d.query)
	}
}
