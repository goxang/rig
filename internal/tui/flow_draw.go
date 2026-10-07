package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/spec"
)

// The Flow screen draws on a canvas of cells: links first (box-drawing lines that join where they
// meet), packets on them, then the node boxes over everything.

type pt struct{ x, y int }

type flowBox struct {
	name       string
	col        int
	x, y, w, h int
}

type flowLayout struct {
	boxes []flowBox
	index map[string]int
	// paths are each link's cells from the source's edge to the arrow at the target, nil when an
	// end is missing
	paths [][]pt
	cols  map[string]int
}

func flowOrder(f *spec.Flow) []string {
	if len(f.NodeOrder) == len(f.Nodes) {
		return f.NodeOrder
	}
	var out []string
	for n := range f.Nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// flowColumns places each node: its column: when set, else one right of its rightmost input. Links
// that close a cycle (found depth-first from the nodes in rig.yaml order) do not push columns.
func flowColumns(f *spec.Flow) map[string]int {
	order := flowOrder(f)
	out := map[string][]int{}
	for i, l := range f.Links {
		if f.Nodes[l.From] != nil && f.Nodes[l.To] != nil {
			out[l.From] = append(out[l.From], i)
		}
	}
	back := map[int]bool{}
	state := map[string]int{} // 1 on the stack, 2 done
	var visit func(n string)
	visit = func(n string) {
		state[n] = 1
		for _, i := range out[n] {
			switch to := f.Links[i].To; state[to] {
			case 1:
				back[i] = true
			case 0:
				visit(to)
			}
		}
		state[n] = 2
	}
	for _, n := range order {
		if state[n] == 0 {
			visit(n)
		}
	}
	col := map[string]int{}
	for _, n := range order {
		if c := f.Nodes[n].Column; c != nil {
			col[n] = max(*c, 0)
		}
	}
	for range order {
		changed := false
		for i, l := range f.Links {
			to, from := f.Nodes[l.To], f.Nodes[l.From]
			if back[i] || to == nil || from == nil || to.Column != nil {
				continue
			}
			if c := col[l.From] + 1; c > col[l.To] && c < len(order) {
				col[l.To], changed = c, true
			}
		}
		if !changed {
			break
		}
	}
	return col
}

// layoutFlow fits the nodes into w×h: columns spread across, each column's nodes spread down,
// links routed through the gaps; links going left or within a column run along lanes at the bottom.
func layoutFlow(f *spec.Flow, w, h int) flowLayout {
	l := flowLayout{index: map[string]int{}, cols: flowColumns(f)}
	order := flowOrder(f)
	ncol := 1
	for _, c := range l.cols {
		ncol = max(ncol, c+1)
	}
	byCol := make([][]string, ncol)
	for _, n := range order {
		byCol[l.cols[n]] = append(byCol[l.cols[n]], n)
	}
	back, skip := 0, 0
	for _, k := range f.Links {
		if f.Nodes[k.From] == nil || f.Nodes[k.To] == nil {
			continue
		}
		switch d := l.cols[k.To] - l.cols[k.From]; {
		case d <= 0:
			back++
		case d > 1:
			skip++
		}
	}
	// links back run along lanes at the bottom, links over a column along lanes at the top
	back, skip = min(back, h/4), min(skip, h/4)
	bw := max(12, min(26, (w-(ncol-1)*12)/ncol))
	gap := 0
	if ncol > 1 {
		gap = max(3, min(22, (w-ncol*bw)/(ncol-1)))
	}
	left := max(0, (w-ncol*bw-(ncol-1)*gap)/2)
	avail := max(3, h-back-skip)
	most := 1
	for _, c := range byCol {
		most = max(most, len(c))
	}
	bh := 4
	if most*bh > avail {
		bh = 3
	}
	for c, names := range byCol {
		slot := avail / max(len(names), 1)
		for r, n := range names {
			l.index[n] = len(l.boxes)
			l.boxes = append(l.boxes, flowBox{name: n, col: c, x: left + c*(bw+gap), y: skip + r*slot + max(0, (slot-bh)/2), w: bw, h: bh})
		}
	}
	lane, top := h-1, skip-1
	for _, k := range f.Links {
		ai, ok1 := l.index[k.From]
		bi, ok2 := l.index[k.To]
		if !ok1 || !ok2 {
			l.paths = append(l.paths, nil)
			continue
		}
		a, b := l.boxes[ai], l.boxes[bi]
		ya, yb := a.y+a.h/2, b.y+b.h/2
		start, end := pt{a.x + a.w, ya}, pt{b.x - 1, yb}
		xc := min(w-1, a.x+a.w+max(1, gap/2))
		var p []pt
		xd := max(0, b.x-max(1, (gap+1)/2))
		switch d := l.cols[k.To] - l.cols[k.From]; {
		case d == 1:
			p = route(start, pt{xc, ya}, pt{xc, yb}, end)
		case d > 1:
			p = route(start, pt{xc, ya}, pt{xc, max(top, 0)}, pt{xd, max(top, 0)}, pt{xd, yb}, end)
			top--
		default:
			p = route(start, pt{xc, ya}, pt{xc, lane}, pt{xd, lane}, pt{xd, yb}, end)
			lane--
		}
		l.paths = append(l.paths, p)
	}
	return l
}

// route is the cells of straight runs through the corners, one step at a time.
func route(corners ...pt) []pt {
	out := []pt{corners[0]}
	for _, c := range corners[1:] {
		cur := out[len(out)-1]
		for cur != c {
			switch {
			case cur.x < c.x:
				cur.x++
			case cur.x > c.x:
				cur.x--
			case cur.y < c.y:
				cur.y++
			default:
				cur.y--
			}
			out = append(out, cur)
		}
	}
	return out
}

// ---- canvas ----

type flowStyle int8

const (
	fsNone flowStyle = iota
	fsDim
	fsText
	fsTitle
	fsGreen
	fsAmber
	fsRed
	fsAccent
	fsPanel
	fsRedRev
	fsPacket
)

func (s flowStyle) style() lipgloss.Style {
	switch s {
	case fsDim:
		return sDim
	case fsText:
		return lipgloss.NewStyle().Foreground(cText)
	case fsTitle:
		return sTitle
	case fsGreen:
		return sGreen
	case fsAmber:
		return sAmber
	case fsRed:
		return sRed
	case fsAccent:
		return sAccent
	case fsPanel:
		return lipgloss.NewStyle().Foreground(cPanel)
	case fsRedRev:
		return lipgloss.NewStyle().Foreground(cRed).Reverse(true).Bold(true)
	case fsPacket:
		return lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	}
	return lipgloss.NewStyle()
}

const (
	dirL uint8 = 1 << iota
	dirR
	dirU
	dirD
)

type fcell struct {
	r    rune
	st   flowStyle
	bits uint8
}

type canvas struct {
	w, h  int
	cells []fcell
}

func newCanvas(w, h int) *canvas {
	return &canvas{w: max(w, 0), h: max(h, 0), cells: make([]fcell, max(w, 0)*max(h, 0))}
}

func (c *canvas) at(x, y int) *fcell {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return nil
	}
	return &c.cells[y*c.w+x]
}

func (c *canvas) set(x, y int, r rune, st flowStyle) {
	if p := c.at(x, y); p != nil {
		p.r, p.st = r, st
	}
}

// put writes s from x, at most w cells; it returns the column after it.
func (c *canvas) put(x, y int, s string, st flowStyle, w int) int {
	for _, r := range s {
		if w <= 0 {
			break
		}
		c.set(x, y, r, st)
		x++
		w--
	}
	return x
}

// line joins a path's cells with box-drawing lines; where lines meet they join (├ ┼ ┴ ...) and the
// more severe style wins.
func (c *canvas) line(p []pt, st flowStyle) {
	for i := 0; i+1 < len(p); i++ {
		a, b := p[i], p[i+1]
		var da, db uint8
		switch {
		case b.x > a.x:
			da, db = dirR, dirL
		case b.x < a.x:
			da, db = dirL, dirR
		case b.y > a.y:
			da, db = dirD, dirU
		default:
			da, db = dirU, dirD
		}
		for _, e := range []struct {
			q pt
			d uint8
		}{{a, da}, {b, db}} {
			if cl := c.at(e.q.x, e.q.y); cl != nil {
				cl.bits |= e.d
				cl.st = max(cl.st, st)
			}
		}
	}
	if len(p) > 0 {
		end := p[len(p)-1]
		if cl := c.at(end.x, end.y); cl != nil {
			cl.r = '▶'
		}
	}
}

var joins = map[uint8]rune{
	dirL: '─', dirR: '─', dirL | dirR: '─', dirU: '│', dirD: '│', dirU | dirD: '│',
	dirR | dirD: '╭', dirL | dirD: '╮', dirR | dirU: '╰', dirL | dirU: '╯',
	dirL | dirR | dirD: '┬', dirL | dirR | dirU: '┴', dirU | dirD | dirR: '├', dirU | dirD | dirL: '┤',
	dirL | dirR | dirU | dirD: '┼',
}

// box draws a node: a rounded border (heavy when picked) with the title in it, then the lines.
func (c *canvas) box(b flowBox, title string, border, titleSt flowStyle, heavy bool, lines [][]seg) {
	tl, tr, bl, br, hz, vt := '╭', '╮', '╰', '╯', '─', '│'
	if heavy {
		tl, tr, bl, br, hz, vt = '┏', '┓', '┗', '┛', '━', '┃'
	}
	x1, y1 := b.x+b.w-1, b.y+b.h-1
	for x := b.x; x <= x1; x++ {
		c.set(x, b.y, hz, border)
		c.set(x, y1, hz, border)
	}
	for y := b.y; y <= y1; y++ {
		c.set(b.x, y, vt, border)
		c.set(x1, y, vt, border)
		if y > b.y && y < y1 {
			for x := b.x + 1; x < x1; x++ {
				c.set(x, y, ' ', fsNone)
			}
		}
	}
	c.set(b.x, b.y, tl, border)
	c.set(x1, b.y, tr, border)
	c.set(b.x, y1, bl, border)
	c.set(x1, y1, br, border)
	c.put(b.x+2, b.y, " "+title+" ", titleSt, b.w-4)
	for i, l := range lines {
		y := b.y + 1 + i
		if y >= y1 {
			break
		}
		x := b.x + 2
		for _, s := range l {
			x = c.put(x, y, s.text, s.st, x1-x)
		}
	}
}

type seg struct {
	text string
	st   flowStyle
}

func (c *canvas) String() string {
	var b strings.Builder
	for y := 0; y < c.h; y++ {
		run, cur := []rune{}, fsNone
		flush := func() {
			if len(run) > 0 {
				if cur == fsNone {
					b.WriteString(string(run))
				} else {
					b.WriteString(cur.style().Render(string(run)))
				}
			}
			run = run[:0]
		}
		for x := 0; x < c.w; x++ {
			cl := c.cells[y*c.w+x]
			r := cl.r
			if r == 0 && cl.bits != 0 {
				r = joins[cl.bits]
			}
			if r == 0 {
				r = ' '
			}
			st := cl.st
			if r == ' ' {
				st = fsNone
			}
			if st != cur {
				flush()
				cur = st
			}
			run = append(run, r)
		}
		flush()
		if y < c.h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// runIn is the last straight horizontal run of a path.
func runIn(p []pt) (x0, x1, y int) {
	end, j := p[len(p)-1], len(p)-1
	for j > 0 && p[j-1].y == end.y {
		j--
	}
	return min(p[j].x, end.x), max(p[j].x, end.x), end.y
}
