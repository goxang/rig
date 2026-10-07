package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/core"

	"github.com/goxang/rig/engine"
)

// TestZonesDoNotOverlap renders every screen and fails when two click targets on a row cross
// without one holding the other: a click there would reach the wrong one.
func TestZonesDoNotOverlap(t *testing.T) {
	a, err := engine.Open("../../examples/shop/rig.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	m := &model{ctx: context.Background(), app: a, opened: map[int]bool{}, all: allTabs(), refreshed: map[int]time.Time{}, hx: -1, hy: -1}
	m.tabs = newTabs(a, m.all, false)
	m.sched = newScheduler(a)
	for _, w := range []int{100, 160, 220} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		for i := range m.tabs {
			if !m.opened[i] {
				m.opened[i] = true
				m.tabs[i].open(m)
			}
			m.active = i
			m.View()
			for x, a := range m.zones {
				for _, b := range m.zones[x+1:] {
					if a.h != 1 || b.h != 1 || a.y != b.y || a.x+a.w <= b.x || b.x+b.w <= a.x {
						continue
					}
					if (a.x <= b.x && b.x+b.w <= a.x+a.w) || (b.x <= a.x && a.x+a.w <= b.x+b.w) {
						continue
					}
					t.Errorf("%s at width %d: %s [%d,%d) crosses %s [%d,%d) on row %d", m.tabs[i].name(), w, a.id, a.x, a.x+a.w, b.id, b.x, b.x+b.w, a.y)
				}
			}
		}
	}
}

func TestFindInstance(t *testing.T) {
	sts := []core.Status{{Service: "domainsvc", Instances: []core.Instance{{ID: "domainsvc-7d9-abc", IP: "10.0.0.5"}}}, {Service: "parsersvc"}}
	for _, c := range []struct {
		labels    map[string]string
		svc, inst string
	}{
		{map[string]string{"pod": "domainsvc-7d9-abc", "job": "x"}, "domainsvc", "domainsvc-7d9-abc"},
		{map[string]string{"instance": "10.0.0.5:9090"}, "domainsvc", "domainsvc-7d9-abc"},
		{map[string]string{"job": "parsersvc"}, "parsersvc", ""},
		{map[string]string{"service_name": "parser"}, "parsersvc", ""},
		{map[string]string{"job": "nope"}, "", ""},
	} {
		if s, i := findInstance(sts, map[string]string{"parser": "parsersvc"}, c.labels); s != c.svc || i != c.inst {
			t.Errorf("%v: got %s %s", c.labels, s, i)
		}
	}
}
