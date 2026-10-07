package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

const testFlow = `
nodes:
  load:   { kind: load }
  api:    { kind: service }
  queue:  { kind: queue }
  cache:  { kind: cache }
  worker: { kind: service }
  db:     { kind: database }
links:
  - { from: load, to: api }
  - { from: api, to: queue }
  - { from: api, to: cache }
  - { from: queue, to: worker }
  - { from: worker, to: db }
  - { from: worker, to: api }
  - { from: worker, to: cache }
`

func TestLayoutFlowRoutesEveryLinkBetweenItsBoxes(t *testing.T) {
	var f spec.Flow
	if err := yaml.Unmarshal([]byte(testFlow), &f); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.NodeOrder, ","); got != "load,api,queue,cache,worker,db" {
		t.Fatalf("order %s", got)
	}
	f.Nodes["worker"].Label = "a worker with a name far longer than any box used to be"
	l := layoutFlow(&f, 1)
	w, h := l.w, l.h
	if c := l.cols; c["load"] != 0 || c["api"] != 1 || c["queue"] != 2 || c["worker"] != 3 || c["db"] != 4 {
		t.Fatalf("columns %v", c)
	}
	cache := l.boxes[l.index["cache"]]
	if cache.col != -1 || cache.y <= l.boxes[l.index["worker"]].y {
		t.Fatalf("cache, used from two columns, should be on the shared row under the others: %+v", cache)
	}
	for i, a := range l.boxes {
		if a.x < 0 || a.y < 0 || a.x+a.w > w || a.y+a.h > h {
			t.Fatalf("%s outside the canvas: %+v", a.name, a)
		}
		for _, b := range l.boxes[i+1:] {
			if a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h {
				t.Fatalf("%s overlaps %s", a.name, b.name)
			}
		}
	}
	for i, k := range f.Links {
		p := l.paths[i]
		from, to := l.boxes[l.index[k.From]], l.boxes[l.index[k.To]]
		if k.To == "cache" {
			if end := p[len(p)-1]; end.y != to.y-1 || end.x <= to.x || end.x >= to.x+to.w {
				t.Fatalf("link %s→cache should come down into its top: %v", k.From, p)
			}
			continue
		}
		if len(p) < 2 || p[0] != (pt{from.x + from.w, from.y + from.h/2}) || p[len(p)-1] != (pt{to.x - 1, to.y + to.h/2}) {
			t.Fatalf("link %s→%s path %v", k.From, k.To, p)
		}
	}
	cv := newCanvas(w, h)
	for _, p := range l.paths {
		cv.line(p, fsDim)
	}
	for _, b := range l.boxes {
		cv.box(b, flowTitle(&f, b.name), fsGreen, fsTitle, b.name == "api", [][]seg{{{"12/s", fsTitle}}})
	}
	lines := strings.Split(cv.String(), "\n")
	if len(lines) != h {
		t.Fatalf("%d lines", len(lines))
	}
	for _, ln := range lines {
		if n := ansi.StringWidth(ln); n != w {
			t.Fatalf("line width %d: %q", n, ansi.Strip(ln))
		}
	}
	if s := ansi.Strip(cv.String()); !strings.Contains(s, "┏") || !strings.Contains(s, "▶") || !strings.Contains(s, "▼") || !strings.Contains(s, "12/s") || !strings.Contains(s, f.Nodes["worker"].Label) {
		t.Fatalf("missing the picked box, arrows or numbers:\n%s", s)
	}
}

func TestFlowStateFromTheNumbers(t *testing.T) {
	f := &spec.Flow{Nodes: map[string]*spec.FlowNode{"api": {Max: 100, Slow: 200}}, NodeOrder: []string{"api"}}
	m := &model{app: &engine.App{Spec: &spec.Project{Flow: f}}}
	ft := &flowTab{stats: map[string]*flowStat{}}
	for _, c := range []struct {
		s    flowStat
		want int
	}{
		{flowStat{rate: 10, has: hasRate}, stOK},
		{flowStat{rate: 75, has: hasRate}, stHot},
		{flowStat{rate: 95, has: hasRate}, stOver},
		{flowStat{rate: 10, lat: 250, has: hasRate | hasLat}, stOver},
		{flowStat{rate: 10, errs: 0.2, has: hasRate | hasErrs}, stHot},
		{flowStat{rate: 0, has: hasRate}, stIdle},
	} {
		ft.stats["api"] = &c.s
		if got, why := ft.state(m, "api"); got != c.want {
			t.Fatalf("%+v: state %d (%v), want %d", c.s, got, why, c.want)
		}
	}
	ft.levels = map[string][3]float64{"api": {20, 40, 60}}
	for rate, want := range map[float64]int{10: stLow, 30: stOK, 50: stHot, 70: stOver} {
		ft.stats["api"] = &flowStat{rate: rate, has: hasRate}
		if got, _ := ft.state(m, "api"); got != want {
			t.Fatalf("rate %v with levels 20/40/60: state %d, want %d", rate, got, want)
		}
	}
}

func TestFitZoomPicksTheRoomiestThatFits(t *testing.T) {
	var f spec.Flow
	if err := yaml.Unmarshal([]byte(testFlow), &f); err != nil {
		t.Fatal(err)
	}
	big := layoutFlow(&f, len(flowZooms)-1)
	if z := fitZoom(&f, big.w, big.h); z != len(flowZooms)-1 {
		t.Fatalf("zoom %d on a screen the roomiest fits", z)
	}
	if z := fitZoom(&f, 20, 5); z != 0 {
		t.Fatalf("zoom %d where nothing fits", z)
	}
}

func TestDirectJumpComesBackOnTheFirstEsc(t *testing.T) {
	a, b := &rootTab{root: true}, &rootTab{root: false}
	m := &model{tabs: []tab{a, b}, opened: map[int]bool{0: true, 1: true}, refreshed: map[int]time.Time{}}
	m.jumpDirect(1)
	if _, ok := m.back(); !ok || m.active != 0 {
		t.Fatal("a direct jump should come back even with something open")
	}
}
