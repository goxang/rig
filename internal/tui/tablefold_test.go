package tui

import (
	"testing"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

func TestTableFoldAndNameSearch(t *testing.T) {
	f := frames.FromRows([]protocol.Field{{Name: "id", Type: protocol.TypeString}, {Name: "parent", Type: protocol.TypeString}},
		[]map[string]any{{"id": "a", "parent": ""}, {"id": "b", "parent": "a"}, {"id": "c", "parent": "b"}})
	wn := &wnode{}
	fo := wn.index(f, "parent")
	g := &grid{rows: []grow{{id: "0"}, {id: "1"}, {id: "2"}}}
	if !fo.key(g, "left") || !fo.collapsed["a"] {
		t.Fatal("left on an open parent folds it")
	}
	if _, hidden := fo.depthOf("c"); !hidden {
		t.Fatal("a folded ancestor hides c")
	}
	if !fo.key(g, "right") || fo.collapsed["a"] {
		t.Fatal("right unfolds")
	}
	if d, _ := fo.depthOf("c"); d != 2 {
		t.Fatalf("depth %d", d)
	}
	hits := searchNames([]string{"cmd/rig/main.go", "docs/plan.md"}, "plan")
	if len(hits) != 1 || hits[0].path != "docs/plan.md" {
		t.Fatalf("hits %v", hits)
	}
}
