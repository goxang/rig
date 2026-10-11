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
