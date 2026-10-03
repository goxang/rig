package tui

import (
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// grid is a sortable, scrollable, clickable table, htop style: < > pick the sort column, I inverts,
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
}

func newGrid(id string, cols ...gcol) *grid { return &grid{id: id, cols: cols, sortBy: -1} }

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
		c := compare(key(g.rows[i]), key(g.rows[j]))
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
}

// key handles movement and sorting keys; false when the key is not one of them.
func (g *grid) key(k tea.KeyMsg) bool {
	if listKeys(k, &g.sel, len(g.rows)) {
		return true
	}
	switch k.String() {
	case ">", ".":
		g.sortBy = (g.sortBy + 1) % len(g.cols)
		g.sortRows()
	case "<", ",":
		g.sortBy = (g.sortBy - 1 + len(g.cols)) % len(g.cols)
		if g.sortBy < 0 {
			g.sortBy = len(g.cols) - 1
		}
		g.sortRows()
	case "I":
		if g.sortBy < 0 {
			g.sortBy = 0
		}
		g.desc = !g.desc
		g.sortRows()
	default:
		return false
	}
	return true
}

// click handles a hit on this grid's zones: true when it selected a row (activate on double).
func (g *grid) click(h hit) (selected bool) {
	switch h.id {
	case g.id + ":head":
		for i := len(g.x0) - 1; i >= 0; i-- {
			if h.x >= g.x0[i] {
				g.sortOn(i)
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

func (g *grid) widths(w int) []int {
	ws := make([]int, len(g.cols))
	fixed, flex := len(g.cols)-1, 0
	for i, c := range g.cols {
		ws[i] = c.width
		fixed += c.width
		if c.width == 0 {
			flex++
		}
	}
	if flex > 0 {
		each := max(6, (w-fixed)/flex)
		for i := range ws {
			if ws[i] == 0 {
				ws[i] = each
			}
		}
	}
	return ws
}

// view renders the grid in w×h cells at (x, y) of the tab body and registers its click zones.
func (g *grid) view(m *model, x, y, w, h int, focused bool) string {
	ws := g.widths(w)
	g.x0 = g.x0[:0]
	var head []string
	off := 0
	for i, c := range g.cols {
		g.x0 = append(g.x0, off)
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
		cells := make([]string, len(g.cols))
		for j, c := range g.cols {
			v := ""
			if j < len(r.cells) {
				v = r.cells[j]
			}
			if c.right {
				cells[j] = padLeft(v, ws[j])
			} else {
				cells[j] = padRight(v, ws[j])
			}
		}
		line := strings.Join(cells, " ")
		if i == g.sel && focused {
			line = sSelected.Render(stripStyles(line, w))
		} else if i == g.sel {
			line = lipgloss.NewStyle().Underline(true).Render(line)
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

// stripStyles keeps a selected row readable: the highlight replaces the cells' own colours.
func stripStyles(s string, w int) string { return padRight(stripANSIKeepSpace(s), w) }

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
