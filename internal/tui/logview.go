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
	"github.com/goxang/rig/spec"
)

// logView is a followed log shared by the Logs screen and a service's page: scroll back (which
// pauses), scroll sideways, and drag over text to copy it; dragging past an edge scrolls.
type logView struct {
	zone   string
	cap    int
	lines  []core.LogLine
	scroll int // lines up from the newest
	cur    int // the line picked with the keys or a click, -1 for none
	hoff   int // columns hidden on the left
	paused bool
	shown  int
	// visible is the line drawn on each row; rowLine and rowCol are its index in lines and, when
	// wrapped, the column the row starts at
	visible []core.LogLine
	rows    []string
	rowLine []int
	rowCol  []int
	width   int
	wrap    bool
	// fmt is how JSON lines read (structured, fields hidden); it starts from ui.logs
	fmt    logFormat
	format func(core.LogLine) string
	// drag selection from anchor to head, as positions in lines; dragged says the mouse moved
	anchor, head lpos
	selecting    bool
	dragged      bool
	wasPaused    bool
}

// lpos is a column of a log line's text as drawn, before the sideways scroll.
type lpos struct{ line, col int }

func newLogView(zone string, capacity int, ui *spec.LogsUI) *logView {
	v := &logView{zone: zone, cap: capacity, fmt: newLogFormat(ui), cur: -1}
	v.wrap = ui != nil && ui.Wrap
	return v
}

func (v *logView) add(lines []core.LogLine) {
	v.lines = append(v.lines, lines...)
	if drop := len(v.lines) - v.cap; drop > 0 {
		v.lines = v.lines[drop:]
		v.anchor.line, v.head.line = max(0, v.anchor.line-drop), max(0, v.head.line-drop)
		if v.cur >= 0 {
			v.cur = max(-1, v.cur-drop)
		}
	}
	if v.paused {
		v.scroll = min(v.scroll+len(lines), max(0, len(v.lines)-1))
	}
}

func (v *logView) reset() {
	v.lines, v.scroll, v.hoff, v.paused, v.cur = nil, 0, 0, false, -1
	v.clearSel()
}

func (v *logView) clearSel() { v.selecting, v.dragged = false, false }

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
	if vertical && v.moveCursor(s) {
		v.clearSel()
		return true
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
		v.scroll, v.paused, v.cur = 0, false, -1
	case "right", "L":
		if v.wrap {
			return true
		}
		v.hoff += 8
	case "left", "H":
		v.hoff = max(0, v.hoff-8)
	case "p":
		v.paused = !v.paused
		if !v.paused {
			v.scroll = 0
		}
	case "c":
		v.lines, v.scroll, v.cur = nil, 0, -1
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

// moveCursor walks the picked line with the arrow and page keys; the first press picks the newest
// line on screen and pauses.
func (v *logView) moveCursor(s string) bool {
	if len(v.lines) == 0 {
		return false
	}
	step := map[string]int{"up": -1, "k": -1, "down": 1, "j": 1, "pgup": -20, "pgdown": 20, "home": -1 << 30}[s]
	if step == 0 {
		return false
	}
	if v.cur < 0 {
		v.cur = len(v.lines) - 1 - v.scroll
		if step < 0 {
			step = 0
		}
	}
	v.paused = true
	v.cur = min(max(0, v.cur+step), len(v.lines)-1)
	return true
}

// picked is the line under the cursor.
func (v *logView) picked() (core.LogLine, bool) {
	if v.cur < 0 || v.cur >= len(v.lines) {
		return core.LogLine{}, false
	}
	return v.lines[v.cur], true
}

// render draws h rows w wide at body cell (x, y), each line through line (wrapped over several rows
// when wrap is on), and registers the zone that takes wheel and drag.
func (v *logView) render(m *model, x, y, w, h int, line func(core.LogLine) string) string {
	v.shown, v.width, v.format = h, w, line
	if v.wrap {
		v.hoff = 0
	}
	if v.cur >= len(v.lines) {
		v.cur = -1
	}
	if v.cur >= 0 && v.cur > len(v.lines)-1-v.scroll {
		v.scroll = len(v.lines) - 1 - v.cur
	}
	for range 50 {
		v.layout(m, x, y, w, h, line)
		if v.cur < 0 || len(v.rowLine) == 0 || v.rowLine[0] < v.cur || v.rowLine[0] == v.cur && v.rowCol[0] == 0 {
			break
		}
		v.scroll++
	}
	var b strings.Builder
	for i, s := range v.rows {
		v.visible = append(v.visible, v.lines[v.rowLine[i]])
		if v.hoff > 0 {
			s = ansi.TruncateLeft(s, v.hoff, "")
		}
		if v.rowLine[i] == v.cur {
			s = highlight(sCursor, ansi.Truncate(s, w, ""), w)
		}
		b.WriteString(s + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// layout lays the lines out on h rows of w, from the scroll position up.
func (v *logView) layout(m *model, x, y, w, h int, line func(core.LogLine) string) {
	v.visible, v.rowLine, v.rowCol = v.visible[:0], v.rowLine[:0], v.rowCol[:0]
	m.zone(v.zone, x, y, w, h)
	var rows []string
	for i := len(v.lines) - v.scroll - 1; i >= 0 && len(rows) < h; i-- {
		s := line(v.lines[i])
		if a, z, ok := v.span(i); ok {
			p := ansi.Strip(s)
			s = ansi.Cut(p, 0, a) + sSel.Render(ansi.Cut(p, a, z)) + ansi.Cut(p, z, ansi.StringWidth(p))
		}
		parts := []string{s}
		if v.wrap && w > 0 {
			parts = strings.Split(ansi.Hardwrap(s, w, true), "\n")
		}
		rows = append(parts, rows...)
		lines, cols := make([]int, len(parts)), make([]int, len(parts))
		for k := range parts {
			lines[k], cols[k] = i, k*w
		}
		v.rowLine, v.rowCol = append(lines, v.rowLine...), append(cols, v.rowCol...)
	}
	if extra := len(rows) - h; extra > 0 {
		rows, v.rowLine, v.rowCol = rows[extra:], v.rowLine[extra:], v.rowCol[extra:]
	}
	v.rows = rows
}

// span is the columns [a, z) of line i inside the selection.
func (v *logView) span(i int) (int, int, bool) {
	if !v.dragged {
		return 0, 0, false
	}
	lo, hi := v.anchor, v.head
	if hi.line < lo.line || hi.line == lo.line && hi.col < lo.col {
		lo, hi = hi, lo
	}
	if i < lo.line || i > hi.line {
		return 0, 0, false
	}
	a, z := 0, 1<<30
	if i == lo.line {
		a = lo.col
	}
	if i == hi.line {
		z = hi.col + 1
	}
	return a, z, true
}

func (v *logView) selection() string {
	var out []string
	for i := min(v.anchor.line, v.head.line); i <= max(v.anchor.line, v.head.line) && i < len(v.lines); i++ {
		a, z, _ := v.span(i)
		out = append(out, strings.TrimRight(ansi.Cut(ansi.Strip(v.format(v.lines[i])), a, z), " "))
	}
	return strings.Join(out, "\n")
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
	if v.wrap {
		s += sDim.Render(" ⏎wrap")
	}
	if v.fmt.raw {
		s += sDim.Render(" raw")
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

// drag selects text from the press to the mouse and copies it on release; past the left or right
// edge it scrolls sideways, past the top or bottom up or down. A plain click copies nothing.
func (v *logView) drag(m *model, h hit, phase dragPhase) {
	if len(v.visible) == 0 {
		return
	}
	if phase == dragMove && v.selecting {
		switch {
		case h.x >= v.width-1:
			v.hoff += 4
		case h.x <= 0 && v.hoff > 0:
			v.hoff = max(0, v.hoff-4)
		}
		switch {
		case h.y < 0:
			v.scroll = min(v.scroll+1, max(0, len(v.lines)-1))
		case h.y >= v.shown && v.scroll > 0:
			v.scroll--
		}
	}
	row := min(max(h.y, 0), len(v.visible)-1)
	at := lpos{v.rowLine[row], v.rowCol[row] + min(max(h.x, 0), v.width-1) + v.hoff}
	switch phase {
	case dragPress:
		// the lines must stay put under the mouse; a plain click lets them go again
		v.anchor, v.head, v.selecting, v.dragged, v.wasPaused, v.paused = at, at, true, false, v.paused, true
	case dragMove:
		if v.selecting && at != v.anchor {
			v.head, v.dragged = at, true
		}
	case dragRelease:
		if v.selecting && v.dragged {
			if text := v.selection(); text != "" {
				copyText(text)
				m.setStatus(fmt.Sprintf("copied %d characters", len([]rune(text))), false)
			}
		}
		v.selecting = false
		if !v.dragged {
			v.cur = at.line
		}
	}
}

// copy puts lines on the clipboard (copyText) and in a file
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
	copyText(text)
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
