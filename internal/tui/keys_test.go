package tui

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"testing"
	"time"

	"github.com/goxang/rig/contracts"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

type namedTab struct {
	rootTab
	title string
}

func (n *namedTab) name() string { return n.title }

func TestKeyPresets(t *testing.T) {
	a, b := &namedTab{title: "Services"}, &namedTab{title: "Logs"}
	m := &model{tabs: []tab{a, b}, opened: map[int]bool{0: true, 1: true}, refreshed: map[int]time.Time{},
		app: &engine.App{Spec: &spec.Project{Keys: "rig"}}}
	if _, ok := m.globalKey("g"); ok {
		t.Fatal("g is the screen's")
	}
	if m.globalKey(":"); m.prompt == nil {
		t.Fatal(": opened no palette")
	}
	m.prompt = nil
	m.command("open services")
	if m.active != 0 {
		t.Fatal(":open services")
	}
	if _, ok := m.globalKey("S"); ok {
		t.Fatal("S is the screen's under rig")
	}
}

func TestBuiltinViewsLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var v view.View
	if err := v.Load(pluginsView); err != nil || v.Root.Widget != "table" || v.Root.Bind == nil || v.Root.Bind.Source != "plugins" {
		t.Fatalf("plugins view: %v %+v", err, v.Root)
	}
	a := &engine.App{Spec: &spec.Project{Dir: t.TempDir(), ViewOrder: []string{"a", "b"}, Views: map[string]*view.View{
		"a": {Title: "A", Slot: "home", Root: protocol.Node{Widget: "text"}},
		"b": {Title: "B", Slot: "tab", Root: protocol.Node{Widget: "text"}},
	}}}
	a.Spec.Plugins = map[string]*spec.Plugin{"x": {Off: true}}
	all := map[string]tab{}
	tabs := addViews(a, all)
	if len(tabs) != 2 || tabs[0].name() != "Home" || tabs[1].name() != "B" || all["view:Plugins"] == nil {
		t.Fatalf("tabs = %v", tabs)
	}
	m := &model{tabs: tabs, all: all, opened: map[int]bool{0: true}, refreshed: map[int]time.Time{}, app: a, ctx: t.Context()}
	m.pluginsScreen()
	if len(m.tabs) != 3 || m.active != 2 {
		t.Fatalf(":plugins: %d tabs, active %d", len(m.tabs), m.active)
	}
	m.openTab(0)
	if len(m.tabs) != 2 || m.transient {
		t.Fatal("Plugins stayed after leaving it")
	}
	var scm view.View
	if err := yaml.Unmarshal(contracts.Get("scm").View, &scm); err != nil || scm.Title != "Merge requests" {
		t.Fatal(err)
	}
	served(&scm.Root, "gl")
	var got []string
	view.Walk(&scm.Root, func(n *protocol.Node) {
		if n.Bind != nil {
			got = append(got, n.Bind.Source)
		}
		acts, _ := n.Props["actions"].([]any)
		for _, a := range acts {
			got = append(got, a.(map[string]any)["action"].(string))
		}
	})
	if strings.Join(got, " ") != "gl.scm.mrs gl.scm.comment gl.scm.pipelines" {
		t.Fatalf("served: %v", got)
	}
}

func TestAlignTabs(t *testing.T) {
	got := strings.Split(alignTabs("jira dev\nKIND\tNAME\nsource\tjira.tracker.board\n\nend"), "\x00")
	want := []string{"jira dev", "\x01KIND    NAME", "\x01source  jira.tracker.board", "", "end"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}
