package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/spec"
)

func TestTaskGroups(t *testing.T) {
	tasks := map[string]spec.Task{"deploy": {Group: "ship"}}
	names := []string{"clear-db", "deploy", "nexus-prune", "nexus-restart", "seed"}
	gs := taskGroups(tasks, names)
	var got []string
	for _, g := range gs {
		var items []string
		for _, n := range names {
			if g.items[n] {
				items = append(items, n)
			}
		}
		got = append(got, g.name+":"+strings.Join(items, ","))
	}
	want := "all:|nexus:nexus-prune,nexus-restart|ship:deploy|other:clear-db,seed"
	if strings.Join(got, "|") != want {
		t.Fatalf("groups = %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestHelpFilterKeepsLinesWithEveryWord(t *testing.T) {
	lines := []string{"E  switch environment", "N  switch namespace", "q  quit"}
	if got := helpFilter(lines, "switch env"); len(got) != 1 || got[0] != lines[0] {
		t.Fatalf("filter = %q", got)
	}
	if got := helpFilter(lines, ""); len(got) != 3 {
		t.Fatalf("an empty filter keeps all, got %q", got)
	}
}

func TestJobWaitsForInputAndTakesIt(t *testing.T) {
	r, w := io.Pipe()
	j := &job{stdin: w}
	j.Write([]byte("step 1\npassword: "))
	j.wrote = time.Now().Add(-time.Second)
	if p, ok := j.waiting(); !ok || p != "password:" {
		t.Fatalf("waiting = %q %v", p, ok)
	}
	got := make(chan string)
	go func() { b := make([]byte, 64); n, _ := r.Read(b); got <- string(b[:n]) }()
	if err := j.answer("s3cret"); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "s3cret\n" {
		t.Fatalf("stdin got %q", s)
	}
	if _, ok := j.waiting(); ok {
		t.Fatal("still waiting after the answer")
	}
}

func TestPinnedRowStaysOnTop(t *testing.T) {
	g := newGrid("g", col("NAME", 0))
	g.sortBy, g.desc = 0, true
	g.set([]grow{{id: "b", cells: []string{"b"}}, {id: "*all", pin: true, cells: []string{"all"}}, {id: "z", cells: []string{"z"}}})
	if g.rows[0].id != "*all" || g.rows[1].id != "z" {
		t.Fatalf("rows = %v", g.rows)
	}
}

type rootTab struct {
	servicesTab
	root bool
}

func (r *rootTab) atRoot() bool { return r.root }

func TestEscWalksBackAJump(t *testing.T) {
	a, b := &rootTab{root: true}, &rootTab{root: false}
	m := &model{tabs: []tab{a, b}, opened: map[int]bool{0: true, 1: true}, refreshed: map[int]time.Time{}}
	m.jump(1)
	if _, ok := m.back(); ok {
		t.Fatal("went back while the screen still had something to close")
	}
	b.root = true
	if _, ok := m.back(); !ok || m.active != 0 || len(m.trail) != 0 {
		t.Fatalf("back: active %d trail %v", m.active, m.trail)
	}
	m.jump(1)
	m.openTab(0)
	m.openTab(1)
	if _, ok := m.back(); ok {
		t.Fatal("a manual switch keeps no trail")
	}
}

func TestPickerEscReturnsToTheParent(t *testing.T) {
	m := &model{}
	m.pick("first", []string{"a"}, nil, 0, false, func([]string) tea.Cmd {
		m.ask("value", "", func(string) tea.Cmd { return nil })
		return nil
	})
	parent := m.picker
	m.picker.key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.prompt == nil || m.picker != nil {
		t.Fatal("enter did not open the input")
	}
	m.prompt.escape()
	if m.picker != parent {
		t.Fatal("esc on the input did not return to the picker")
	}
}
