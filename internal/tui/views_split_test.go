package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/protocol"
)

func TestStackBorderResizes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	box := func(id int) *wnode { return &wnode{n: protocol.Node{Widget: "box"}, id: id} }
	tab := &viewTab{title: "Jira"}
	kids := []*wnode{box(1), box(2)}
	m := &model{}
	tab.stackDraw(m, kids, 0, 0, 40, 20, true)
	if kids[0].h != 10 || kids[1].h != 10 {
		t.Fatalf("even heights %d %d", kids[0].h, kids[1].h)
	}
	if m.borderAt(5, 10) != "view.Jira.2" {
		t.Fatal("no border between the panes")
	}
	m.splitting = "view.Jira.2"
	m.splitDrag(mouseEvent{Mouse: tea.Mouse{X: 5, Y: 14}, Action: mouseRelease})
	m.zones = nil
	tab.stackDraw(m, kids, 0, 0, 40, 20, true)
	if kids[0].h != 14 || kids[1].h != 6 {
		t.Fatalf("dragged heights %d %d", kids[0].h, kids[1].h)
	}
}

func TestPreviewBorderResizes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	tab := &viewTab{title: "Changes"}
	list := &wnode{n: protocol.Node{Widget: "table", Props: map[string]any{"preview": map[string]any{"widget": "markdown", "source": "x"}}}, id: 3}
	m := &model{}
	tab.draw(m, list, 0, 0, 100, 20)
	if list.w != 45 || m.borderAt(44, 5) != "view.Changes.3.preview" {
		t.Fatalf("list width %d, border %q", list.w, m.borderAt(44, 5))
	}
	m.splitting = "view.Changes.3.preview"
	m.splitDrag(mouseEvent{Mouse: tea.Mouse{X: 59, Y: 5}, Action: mouseRelease})
	m.zones = nil
	tab.draw(m, list, 0, 0, 100, 20)
	if list.w != 60 {
		t.Fatalf("dragged list width %d", list.w)
	}
}
