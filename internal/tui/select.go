package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var sSel = lipgloss.NewStyle().Reverse(true)

// selection is a drag over any text a screen draws, outside the zones that take their own drags
// (logs): terminal style, from the press to the mouse, kept inside the bordered box it began in.
// The screen freezes while the button is down; the release copies.
type selection struct {
	x0, y0, x1, y1           int
	left, right, top, bottom int // right and bottom exclusive
	frame                    []string
	dragged                  bool
}

func (m *model) selectStart(x, y int) {
	lines := strings.Split(m.frame, "\n")
	if y < 0 || y >= len(lines) {
		return
	}
	s := &selection{x0: x, y0: y, x1: x, y1: y, left: 0, right: m.w, top: y, bottom: y + 1}
	if y >= m.originY && y < m.bodyEnd {
		s.box(lines, x, y, m.originY, m.bodyEnd)
	}
	m.sel = s
}

// box finds the vertical borders left and right of (x, y), then follows one of them up and down.
func (s *selection) box(lines []string, x, y, top, bottom int) {
	row := cells(lines[y])
	for i := min(x, len(row)) - 1; i >= 0; i-- {
		if vbar(row[i]) {
			s.left = i + 1
			break
		}
	}
	for i := x; i < len(row); i++ {
		if vbar(row[i]) {
			s.right = i
			break
		}
	}
	edge := s.left - 1
	if edge < 0 && s.right < len(row) {
		edge = s.right
	}
	if edge < 0 {
		s.top, s.bottom = top, bottom
		return
	}
	at := func(y int) bool { r := cells(lines[y]); return edge < len(r) && vbar(r[edge]) }
	for s.top > top && at(s.top-1) {
		s.top--
	}
	for s.bottom < bottom && at(s.bottom) {
		s.bottom++
	}
}

func vbar(r rune) bool { return r == '│' || r == '┃' || r == '║' }

// cells is a line's text by screen column; a wide rune is followed by a zero filler.
func cells(line string) []rune {
	var out []rune
	for _, r := range ansi.Strip(line) {
		out = append(out, r)
		for range ansi.StringWidth(string(r)) - 1 {
			out = append(out, 0)
		}
	}
	return out
}

func (m *model) selectTo(e tea.MouseMsg) tea.Cmd {
	s := m.sel
	held := e.Action == tea.MouseActionMotion && e.Button == tea.MouseButtonLeft
	x, y := min(max(e.X, s.left), s.right-1), min(max(e.Y, s.top), s.bottom-1)
	if held {
		if !s.dragged && x == s.x0 && y == s.y0 {
			return nil
		}
		if !s.dragged {
			s.dragged, s.frame = true, strings.Split(m.frame, "\n")
		}
		s.x1, s.y1 = x, y
		return nil
	}
	// a release, or a motion without the button when the release happened outside the window
	m.sel = nil
	if !s.dragged {
		return nil
	}
	if text := s.text(); text != "" {
		copyText(text)
		m.setStatus("copied", false)
	}
	return nil
}

// span is the columns [a, b) selected on screen row y.
func (s *selection) span(y int) (int, int, bool) {
	ay, ax, by, bx := s.y0, s.x0, s.y1, s.x1
	if by < ay || by == ay && bx < ax {
		ay, ax, by, bx = by, bx, ay, ax
	}
	if y < ay || y > by {
		return 0, 0, false
	}
	a, b := s.left, s.right
	if y == ay {
		a = ax
	}
	if y == by {
		b = bx + 1
	}
	return a, b, true
}

func (s *selection) text() string {
	var out []string
	for y := range s.frame {
		if a, b, ok := s.span(y); ok {
			out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(s.frame[y], a, b)), " "))
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

func (s *selection) paint() string {
	out := make([]string, len(s.frame))
	for y, l := range s.frame {
		a, b, ok := s.span(y)
		if !ok {
			out[y] = l
			continue
		}
		out[y] = ansi.Cut(l, 0, a) + "\x1b[0m" + sSel.Render(ansi.Strip(ansi.Cut(l, a, b))) + ansi.Cut(l, b, ansi.StringWidth(l))
	}
	return strings.Join(out, "\n")
}
