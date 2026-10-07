package tui

import (
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPickerGroups(t *testing.T) {
	m := &model{w: 100}
	m.pickMany("ship", []string{"api", "db", "web"}, nil, []string{"web"}, func([]string) tea.Cmd { return nil })
	p := m.picker
	p.groups = []pickGroup{{name: "all"}, {name: "core", items: map[string]bool{"api": true, "web": true}}, {name: "infra", items: map[string]bool{"db": true}}}
	names := func() (out []string) {
		for _, i := range p.visible() {
			out = append(out, p.items[i])
		}
		return
	}
	p.key(m, tea.KeyMsg{Type: tea.KeyTab})
	if got := names(); !slices.Equal(got, []string{"api", "web"}) {
		t.Fatalf("core = %v", got)
	}
	p.key(m, tea.KeyMsg{Type: tea.KeyCtrlA})
	p.key(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	p.key(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := names(); !slices.Equal(got, []string{"db"}) || !p.marked["api"] || p.marked["db"] {
		t.Fatalf("infra = %v, marked %v", got, p.marked)
	}
	if v := p.view(m, 30); v == "" {
		t.Fatal("empty view")
	}
}
