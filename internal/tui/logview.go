package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/core"
)

// logView is a followed log shared by the Logs screen and a service's page: scroll back (which
// pauses), scroll sideways, and drag over lines to copy them.
type logView struct {
	zone    string
	cap     int
	lines   []core.LogLine
	scroll  int // lines up from the newest
	hoff    int // columns hidden on the left
	paused  bool
	shown   int
	visible []core.LogLine
	// drag selection, as indexes into visible; dragged says the mouse moved since the press
	selFrom, selTo int
	selecting      bool
	dragged        bool
}

func newLogView(zone string, capacity int) *logView {
	return &logView{zone: zone, cap: capacity, selFrom: -1, selTo: -1}
}

func (v *logView) add(lines []core.LogLine) {
	v.lines = append(v.lines, lines...)
	if len(v.lines) > v.cap {
		v.lines = v.lines[len(v.lines)-v.cap:]
	}
	if v.paused {
		v.scroll = min(v.scroll+len(lines), max(0, len(v.lines)-1))
	}
}

func (v *logView) reset() {
	v.lines, v.scroll, v.hoff, v.paused = nil, 0, 0, false
	v.clearSel()
}

func (v *logView) clearSel() { v.selFrom, v.selTo, v.selecting, v.dragged = -1, -1, false, false }

// key handles the log's own keys; vertical says whether ↑↓ and the page keys are the log's (on a
// service page they move the instance list, and the shifted ones scroll the log).
func (v *logView) key(m *model, k tea.KeyMsg, vertical bool) bool {
	s := k.String()
	if !vertical {
		switch s {
		case "up", "down", "k", "j", "pgup", "pgdown":
			return false
		}
		s = strings.TrimPrefix(s, "shift+")
	}
	switch s {
	case "up", "k":
		v.paused = true
		v.scroll = min(v.scroll+1, max(0, len(v.lines)-1))
	case "down", "j":
		v.scroll = max(0, v.scroll-1)
	case "pgup":
		v.paused = true
		v.scroll = min(v.scroll+20, max(0, len(v.lines)-1))
	case "pgdown":
		v.scroll = max(0, v.scroll-20)
	case "G", "end":
		v.scroll, v.paused = 0, false
	case "right", "L":
		v.hoff += 8
	case "left", "H":
		v.hoff = max(0, v.hoff-8)
	case "p":
		v.paused = !v.paused
		if !v.paused {
			v.scroll = 0
		}
	case "c":
		v.lines, v.scroll = nil, 0
	case "y":
		end := len(v.lines) - v.scroll
		v.copy(m, v.lines[max(0, end-v.shown):end])
	case "Y":
		v.copy(m, v.lines)
	default:
		return false
	}
	v.clearSel()
	return true
}

// render draws h lines w wide at body cell (x, y), each through line, and registers the zone that
// takes wheel and drag.
func (v *logView) render(m *model, x, y, w, h int, line func(core.LogLine) string) string {
	v.shown = h
	end := max(0, len(v.lines)-v.scroll)
	start := max(0, end-h)
	v.visible = v.lines[start:end]
	m.zone(v.zone, x, y, w, h)
	var b strings.Builder
	for i, l := range v.visible {
		s := line(l)
		if v.hoff > 0 {
			s = ansi.TruncateLeft(s, v.hoff, "")
		}
		if v.selected(i) {
			s = sTabOn.Padding(0).Render(ansi.Strip(truncate(s, w)))
		}
		b.WriteString(s + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (v *logView) selected(i int) bool {
	if v.selFrom < 0 || !v.dragged {
		return false
	}
	lo, hi := min(v.selFrom, v.selTo), max(v.selFrom, v.selTo)
	return i >= lo && i <= hi
}

// state is the title's "following" / "paused" part, with the sideways offset when there is one.
func (v *logView) state() string {
	s := sGreen.Render("● following")
	if v.paused {
		s = sAmber.Render("❚❚ paused")
	}
	if v.hoff > 0 {
		s += sDim.Render(fmt.Sprintf(" ⇢%d", v.hoff))
	}
	return s
}

func (v *logView) wheel(up bool) {
	if up {
		v.paused = true
		v.scroll = min(v.scroll+3, max(0, len(v.lines)-1))
	} else {
		v.scroll = max(0, v.scroll-3)
	}
}

// drag selects whole lines between press and release and copies them on release; a plain click
// copies nothing.
func (v *logView) drag(m *model, h hit, phase dragPhase) {
	row := min(max(h.y, 0), len(v.visible)-1)
	if row < 0 {
		return
	}
	switch phase {
	case dragPress:
		v.selFrom, v.selTo, v.selecting, v.dragged = row, row, true, false
	case dragMove:
		if v.selecting {
			v.selTo, v.dragged = row, true
			v.paused = true // the lines must stay put under the mouse
		}
	case dragRelease:
		if v.selecting && v.dragged {
			lo, hi := min(v.selFrom, v.selTo), max(v.selFrom, v.selTo)
			v.copy(m, v.visible[lo:hi+1])
		}
		v.selecting = false
		if !v.dragged {
			v.clearSel()
		}
	}
}

// copy puts lines on the clipboard (OSC 52, which most terminals and tmux honour) and in a file
// under the environment's state directory, for terminals that ignore it.
func (v *logView) copy(m *model, lines []core.LogLine) {
	if len(lines) == 0 {
		return
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Time.Local().Format("15:04:05.000") + " " + l.Service + " " + l.Text + "\n")
	}
	text := b.String()
	dir := filepath.Join(m.app.StateDir(), "logs")
	file := filepath.Join(dir, time.Now().Format("20060102-150405")+".log")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(file, []byte(text), 0o644)
	}
	osc52(text)
	m.setStatus(fmt.Sprintf("copied %d lines (also in %s)", len(lines), file), false)
}

type dragPhase int

const (
	dragPress dragPhase = iota
	dragMove
	dragRelease
)

// dragger takes a press, the moves while the button is held, and the release, on a zone it drew.
type dragger interface {
	drag(m *model, z hit, phase dragPhase) bool
}
