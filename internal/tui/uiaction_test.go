package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/engine"
)

func TestUIWalk(t *testing.T) {
	a, err := engine.Open("../../examples/shop/rig.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	m := &model{ctx: context.Background(), app: a, opened: map[int]bool{}, all: allTabs(), refreshed: map[int]time.Time{}, hx: -1, hy: -1}
	m.tabs = newTabs(a, m.all, false)
	m.sched = newScheduler(a)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})

	if ok, text, _ := m.uiPanels("main", "nope"); ok || !strings.Contains(text, "nope") {
		t.Fatalf("unknown panel accepted: %s", text)
	}
	if ok, text, _ := m.uiPanels("MAIN", "Orders/s\napi latency (avg)"); !ok {
		t.Fatal(text)
	}
	mt := m.tabs[m.active].(*metricsTab)
	if _, ps := mt.items(m); len(ps) != 2 || ps[1].Title != "api latency (avg)" {
		t.Fatalf("panels %v", ps)
	}
	if s := m.uiState(); !strings.Contains(s, "open: Metrics") || !strings.Contains(s, "only 2 of 4 panels") {
		t.Fatalf("state:\n%s", s)
	}
	m.uiKeys([]string{"O"})
	if _, ps := mt.items(m); len(ps) != 4 {
		t.Fatalf("O kept %d panels", len(ps))
	}

	if k := keyOf("ctrl+r"); k.Type != tea.KeyCtrlR {
		t.Fatalf("ctrl+r is %v", k)
	}
	if k := keyOf("api"); k.Type != tea.KeyRunes || string(k.Runes) != "api" {
		t.Fatalf("text is %v", k)
	}
	if k := keyOf("enter"); k.Type != tea.KeyEnter {
		t.Fatalf("enter is %v", k)
	}
}
