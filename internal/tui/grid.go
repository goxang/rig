package tui

import (
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// grid is a sortable, scrollable, clickable table, htop style: ctrl+alt+←→ pick the sort column, ctrl+alt+↑↓ (or alt+↑↓) order it,
// clicking a header sorts by it, clicking a row selects it. Rows keep their selection by id across
// refreshes.
type grid struct {
	cols   []gcol
	rows   []grow
	sel    int
	offset int
	sortBy int
	desc   bool
	id     string // zone id prefix
	shown  int    // rows that fit, from the last render
	x0     []int  // column start offsets, from the last render
	xcol   []int  // the column each x0 starts
	// simple are the columns the simple view shows; empty shows them all
	simple []int
}

type gcol struct {
	title string
	width int // 0: share what is left
	right bool
}

type grow struct {
	id    string
	cells []string
	keys  []any // per column: float64 sorts numerically, anything else as text; nil uses the cell
	// pin keeps the row on top whatever the sort ("all instances")
	pin bool
}

func newGrid(id string, cols ...gcol) *grid {
	g := &grid{id: id, cols: cols, sortBy: -1}
	if s, ok := gridSorts[id]; ok && s.Col < len(cols) && cols[s.Col].title == s.Title {
		g.sortBy, g.desc = s.Col, s.Desc
	}
	return g
}

// gridSort is how the user last sorted a grid; gridSorts keeps it by grid id, so a grid built
// again (a screen reopened, a session resumed) sorts as it was left.
type gridSort struct {
	Col   int    `json:"col"`
	Title string `json:"title"`
	Desc  bool   `json:"desc"`
}

var gridSorts = map[string]gridSort{}

// sortDefault sorts on c unless the user sorted this grid before.
func (g *grid) sortDefault(c int, desc bool) {
	if _, ok := gridSorts[g.id]; !ok {
		g.sortBy, g.desc = c, desc
	}
}

func (g *grid) remember() {
	if g.sortBy >= 0 && g.sortBy < len(g.cols) {
		gridSorts[g.id] = gridSort{g.sortBy, g.cols[g.sortBy].title, g.desc}
	}
}

func col(title string, width int) gcol  { return gcol{title: title, width: width} }
func rcol(title string, width int) gcol { return gcol{title: title, width: width, right: true} }

// set replaces the rows, keeping the selected row (by id) selected.
func (g *grid) set(rows []grow) {
	cur := ""
	if g.sel < len(g.rows) {
		cur = g.rows[g.sel].id
	}
	g.rows = rows
	g.sortRows()
	g.sel = min(g.sel, max(0, len(g.rows)-1))
	for i, r := range g.rows {
		if r.id == cur && cur != "" {
			g.sel = i
		}
	}
}

func (g *grid) current() (grow, bool) {
	if g.sel < 0 || g.sel >= len(g.rows) {
		return grow{}, false
	}
	return g.rows[g.sel], true
}

func (g *grid) sortRows() {
	if g.sortBy < 0 || g.sortBy >= len(g.cols) {
		return
	}
	c := g.sortBy
	key := func(r grow) any {
		if c < len(r.keys) && r.keys[c] != nil {
			return r.keys[c]
		}
		if c < len(r.cells) {
			s := stripANSI(r.cells[c])
			if f, err := strconv.ParseFloat(strings.TrimRight(s, "%smµ/"), 64); err == nil {
				return f
			}
			return strings.ToLower(s)
		}
		return ""
	}
	sort.SliceStable(g.rows, func(i, j int) bool {
		if g.rows[i].pin != g.rows[j].pin {
			return g.rows[i].pin
		}
		c := compare(key(g.rows[i]), key(g.rows[j]))
		if c == 0 {
			c = strings.Compare(g.rows[i].id, g.rows[j].id)
		}
		if g.desc {
			return c > 0
		}
		return c < 0
	})
}

// compare orders numbers before text, numbers by value and text case-insensitively.
func compare(a, b any) int {
	af, aNum := a.(float64)
	bf, bNum := b.(float64)
	switch {
	case aNum && bNum:
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return strings.Compare(strings.ToLower(toString(a)), strings.ToLower(toString(b)))
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func stripANSI(s string) string { return strings.TrimSpace(stripANSIKeepSpace(s)) }

func (g *grid) sortOn(c int) {
	if g.sortBy == c {
		g.desc = !g.desc
	} else {
		g.sortBy, g.desc = c, false
	}
	g.sortRows()
	g.remember()
}

// key handles movement and sorting keys; false when the key is not one of them.
func (g *grid) key(k tea.KeyMsg) bool {
	if listKeys(k, &g.sel, len(g.rows)) {
		return true
	}
	switch k.String() {
	case "alt+ctrl+right", "ctrl+shift+right", ">", ".":
		g.sortBy = (g.sortBy + 1) % len(g.cols)
		g.sortRows()
	case "alt+ctrl+left", "ctrl+shift+left", "<", ",":
		g.sortBy = (max(g.sortBy, 0) - 1 + len(g.cols)) % len(g.cols)
		g.sortRows()
	// VTE (GNOME's terminal) keeps ctrl+shift+↑↓ and GNOME ctrl+alt+arrows: alt+↑↓ and < > I always work
	case "alt+ctrl+up", "alt+ctrl+down", "ctrl+shift+up", "ctrl+shift+down", "alt+up", "alt+down", "I":
		g.sortBy = max(g.sortBy, 0)
		switch k.String() {
		case "alt+ctrl+up", "ctrl+shift+up", "alt+up":
			g.desc = false
		case "alt+ctrl+down", "ctrl+shift+down", "alt+down":
			g.desc = true
		default:
			g.desc = !g.desc
		}
		g.sortRows()
	default:
		return false
	}
	g.remember()
	return true
}

// click handles a hit on this grid's zones: true when it selected a row (activate on double).
func (g *grid) click(h hit) (selected bool) {
	switch h.id {
	case g.id + ":head":
		for i := len(g.x0) - 1; i >= 0; i-- {
			if h.x >= g.x0[i] {
				g.sortOn(g.xcol[i])
				return false
			}
		}
	case g.id + ":rows":
		if i := g.offset + h.y; i < len(g.rows) {
			g.sel = i
			return true
		}
	}
	return false
}

// shows are the columns drawn: the simple ones in the simple view, else all.
func (g *grid) shows(m *model) []int {
	if m != nil && m.simple && len(g.simple) > 0 {
		return g.simple
	}
	out := make([]int, len(g.cols))
	for i := range out {
		out[i] = i
	}
	return out
}

// widths of every column for width w; columns not in show get 0 and take no room.
func (g *grid) widths(w int, show []int) []int {
	ws := make([]int, len(g.cols))
	fixed, flex := len(show)-1, 0
	for _, i := range show {
		c := g.cols[i]
		ws[i] = c.width
		fixed += c.width
		if c.width == 0 {
			flex++
		}
	}
	if flex > 0 {
		each := max(6, (w-fixed)/flex)
		for _, i := range show {
			if ws[i] == 0 {
				ws[i] = each
			}
		}
	}
	return ws
}

// view renders the grid in w×h cells at (x, y) of the tab body and registers its click zones.
func (g *grid) view(m *model, x, y, w, h int, focused bool) string {
	show := g.shows(m)
	ws := g.widths(w, show)
	g.x0, g.xcol = g.x0[:0], g.xcol[:0]
	var head []string
	off := 0
	for _, i := range show {
		c := g.cols[i]
		g.x0, g.xcol = append(g.x0, off), append(g.xcol, i)
		t := c.title
		if i == g.sortBy {
			if g.desc {
				t += "▼"
			} else {
				t += "▲"
			}
		}
		cell := padRight(t, ws[i])
		if c.right {
			cell = padLeft(t, ws[i])
		}
		if i == g.sortBy {
			cell = sAccent.Render(cell)
		} else {
			cell = sDim.Render(cell)
		}
		head = append(head, cell)
		off += ws[i] + 1
	}
	g.shown = max(0, h-1)
	g.offset = scroll(g.sel, g.offset, g.shown, len(g.rows))
	var b strings.Builder
	b.WriteString(strings.Join(head, " "))
	for i := g.offset; i < len(g.rows) && i-g.offset < g.shown; i++ {
		r := g.rows[i]
		cells := make([]string, 0, len(show))
		for _, j := range show {
			c := g.cols[j]
			v := ""
			if j < len(r.cells) {
				v = printable(r.cells[j])
			}
			if c.right {
				cells = append(cells, padLeft(v, ws[j]))
			} else {
				cells = append(cells, padRight(v, ws[j]))
			}
		}
		line := strings.Join(cells, " ")
		if i != g.sel && m.hovering(x, y+1+i-g.offset, w, 1) {
			line = highlight(sHover, line, w)
		} else if i == g.sel && focused {
			line = highlight(sSelected, line, w)
		} else if i == g.sel {
			line = highlight(sUnderline, line, w)
		}
		b.WriteString("\n" + line)
	}
	if len(g.rows) == 0 {
		b.WriteString("\n" + sDim.Render("nothing yet"))
	}
	m.zone(g.id+":head", x, y, w, 1)
	m.zone(g.id+":rows", x, y+1, w, g.shown)
	return b.String()
}

// highlight lays st (a background, bold, underline) over a styled line padded to w and keeps the
// cells' own colours: lipgloss would end st at the first reset inside the line, or, for underline,
// split the escape codes into visible text, so st's codes are re-opened after every reset instead.
func highlight(st lipgloss.Style, line string, w int) string {
	open, _, _ := strings.Cut(st.Render("\x00"), "\x00")
	line = padRight(line, w)
	if open == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+open)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+open)
	return open + line + "\x1b[0m"
}

func stripANSIKeepSpace(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func padLeft(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return truncate(s, w)
}

// printable keeps a cell one line of known width: a tab or newline from SQL text or kubectl output
// moves the terminal's cursor where lipgloss does not count it, leaving stale text of the last frame.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20 && r != '\x1b', r == 0x7f:
			return -1
		}
		return r
	}, s)
}
