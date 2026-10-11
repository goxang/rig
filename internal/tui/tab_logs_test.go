package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/core"
)

func TestInspectWalksLogLines(t *testing.T) {
	m := &model{ctx: context.Background()}
	lt := &logsTab{id: "svc", fixed: true, services: []string{"a"}, log: newLogView("svc:body", 100, nil)}
	lt.log.add([]core.LogLine{{Text: `{"msg":"one"}`}, {Text: `{"msg":"two"}`}, {Text: `{"msg":"three"}`}})
	lt.log.cur = 2
	key := func(t rune) { lt.keyHandled(m, tea.KeyPressMsg{Code: t}) }

	key(tea.KeyEnter)
	if lt.inspect == nil || lt.inTree {
		t.Fatal("enter on a picked line should open the inspector, focused on the lines")
	}
	key(tea.KeyUp)
	if l, _ := lt.log.picked(); l.Text != `{"msg":"two"}` {
		t.Fatalf("up should pick the previous line, got %q", l.Text)
	}
	key(tea.KeyEnter)
	key(tea.KeyDown)
	if l, _ := lt.log.picked(); !lt.inTree || l.Text != `{"msg":"two"}` {
		t.Fatalf("in the fields, down moves in the tree, not the lines (inTree %v, line %q)", lt.inTree, l.Text)
	}
	key(tea.KeyEsc)
	key(tea.KeyEsc)
	if lt.inspect != nil {
		t.Fatal("esc from the fields goes back to the lines, a second esc closes")
	}
	if _, handled := lt.keyHandled(m, keyOf("f")); handled {
		t.Fatal("a log pinned to one service leaves f to its page")
	}
}
