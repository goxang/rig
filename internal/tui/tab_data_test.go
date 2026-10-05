package tui

import (
	"testing"

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
