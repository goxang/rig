package tui

import (
	"github.com/goxang/rig/core"
	"strings"
	"testing"
)

func TestSelectionStaysInItsBox(t *testing.T) {
	frame := strings.Split(strings.Join([]string{
		"header",
		"tabs",
		"╭──────╮╭───────╮",
		"│ab cd ││left 1 │",
		"│ef gh ││left 2 │",
		"╰──────╯╰───────╯",
		"footer",
	}, "\n"), "\n")
	s := &selection{x0: 2, y0: 3, x1: 4, y1: 4, left: 0, right: 17, top: 3, bottom: 4}
	s.box(frame, 2, 3, 2, 6)
	if s.left != 1 || s.right != 7 || s.top != 3 || s.bottom != 5 {
		t.Fatalf("box = %d..%d x %d..%d", s.left, s.right, s.top, s.bottom)
	}
	s.frame = frame
	if got := s.text(); got != "b cd\nef g" {
		t.Fatalf("text = %q", got)
	}
}

func TestLogDragCopiesPartOfLinesAndScrollsSideways(t *testing.T) {
	v := newLogView("log", 100, nil)
	v.add([]core.LogLine{{Text: "first line here"}, {Text: "second line"}})
	m := &model{}
	v.render(m, 0, 0, 8, 2, func(l core.LogLine) string { return l.Text })
	v.drag(m, hit{x: 6, y: 0}, dragPress)
	v.drag(m, hit{x: 7, y: 0}, dragMove) // past the right edge: scrolls 4
	v.drag(m, hit{x: 1, y: 1}, dragMove)
	if v.hoff != 4 {
		t.Fatalf("hoff = %d", v.hoff)
	}
	if got := v.selection(); got != "line here\nsecond" {
		t.Fatalf("selection = %q", got)
	}
}
