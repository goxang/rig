package tui

import (
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/internal/viz"
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
	// w and h are the canvas the drawing needs; the screen shows a window of it
	w, h int
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

// flowShared are the nodes drawn on the row under the others: shared: true, or a cache or database
// with no links out that nodes in several columns link to.
func flowShared(f *spec.Flow, cols map[string]int) map[string]bool {
	out := map[string]bool{}
	for name, n := range f.Nodes {
		if n.Shared != nil {
			out[name] = *n.Shared
			continue
		}
		if n.Kind != "cache" && n.Kind != "database" {
			continue
		}
		from, leaves := map[int]bool{}, false
		for _, l := range f.Links {
			if l.To == name && f.Nodes[l.From] != nil {
				from[cols[l.From]] = true
			}
			if l.From == name {
				leaves = true
			}
		}
		out[name] = !leaves && len(from) >= 2
	}
	return out
}

func flowTitle(f *spec.Flow, name string) string {
	n := f.Nodes[name]
	return strings.TrimSpace(flowIcons[n.Kind] + " " + firstNonEmpty(n.Label, name))
}

// flowZooms are the spacings from tight to roomy: gap between columns, rows between boxes, the
// narrowest box, box height.
var flowZooms = []struct{ gap, vgap, minW, bh int }{{4, 0, 18, 3}, {6, 1, 22, 4}, {9, 1, 24, 4}, {12, 2, 26, 4}, {16, 3, 28, 5}}

// fitZoom is the roomiest zoom whose drawing fits w×h, else the tightest.
func fitZoom(f *spec.Flow, w, h int) int {
	for z := len(flowZooms) - 1; z > 0; z-- {
		if l := layoutFlow(f, z); l.w <= w && l.h <= h {
			return z
		}
	}
	return 0
}

// layoutFlow draws the nodes at a zoom on a canvas as big as they need: columns as wide as their
// longest name, links through the gaps, links over a column or back along lanes at the top, and
// shared nodes on a row at the bottom, reached over a bus.
func layoutFlow(f *spec.Flow, zoom int) flowLayout {
	z := flowZooms[min(max(zoom, 0), len(flowZooms)-1)]
	l := flowLayout{index: map[string]int{}, cols: flowColumns(f)}
	shared := flowShared(f, l.cols)
	order := flowOrder(f)
	ncol := 1
	for n, c := range l.cols {
		if !shared[n] {
			ncol = max(ncol, c+1)
		}
	}
	byCol := make([][]string, ncol)
	var bottom []string
	for _, n := range order {
		if shared[n] {
			bottom = append(bottom, n)
		} else {
			byCol[l.cols[n]] = append(byCol[l.cols[n]], n)
		}
	}
	lanes := 0
	for _, k := range f.Links {
		if f.Nodes[k.From] != nil && f.Nodes[k.To] != nil && !shared[k.To] && !shared[k.From] && l.cols[k.To]-l.cols[k.From] != 1 {
			lanes++
		}
	}
	widths, xs := make([]int, ncol), make([]int, ncol)
	x := 1
	for c, names := range byCol {
		widths[c] = z.minW
		for _, n := range names {
			widths[c] = max(widths[c], ansi.StringWidth(flowTitle(f, n))+6)
		}
		xs[c] = x
		x += widths[c] + z.gap
	}
	l.w = x - z.gap + z.gap/2 + 2
	most := 1
	for _, c := range byCol {
		most = max(most, len(c))
	}
	top := lanes + min(lanes, 1)
	tall := most*(z.bh+z.vgap) - z.vgap
	for c, names := range byCol {
		y := top + (tall-(len(names)*(z.bh+z.vgap)-z.vgap))/2
		for _, n := range names {
			l.index[n] = len(l.boxes)
			l.boxes = append(l.boxes, flowBox{name: n, col: c, x: xs[c], y: y, w: widths[c], h: z.bh})
			y += z.bh + z.vgap
		}
	}
	l.h = top + tall
	// shared nodes: one bus lane each, then their row, each under the middle of what links to it
	busTop := l.h + 1
	rowY := busTop + len(bottom) + 1
	next := 1
	for _, n := range bottom {
		w := max(z.minW, ansi.StringWidth(flowTitle(f, n))+6)
		sum, k := 0, 0
		for _, lk := range f.Links {
			if i, ok := l.index[lk.From]; ok && lk.To == n {
				sum += l.boxes[i].x + l.boxes[i].w
				k++
			}
		}
		bx := next
		if k > 0 {
			bx = max(next, sum/k-w/2)
		}
		l.index[n] = len(l.boxes)
		l.boxes = append(l.boxes, flowBox{name: n, col: -1, x: bx, y: rowY, w: w, h: z.bh})
		next = bx + w + 3
		l.w = max(l.w, next)
	}
	if len(bottom) > 0 {
		l.h = rowY + z.bh
	}
	lane := top - 2
	arrivals := map[string]int{}
	var used [][3]int
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
		xc := a.x + a.w + max(1, z.gap/2)
		xd := max(0, b.x-max(1, (z.gap+1)/2))
		var p []pt
		switch {
		case shared[k.To] && !shared[k.From]:
			// down the gap right of the source to the target's bus, along it, into the box's top
			bus := busTop + slices.Index(bottom, k.To)
			ins := 0
			for _, o := range f.Links {
				if o.To == k.To {
					ins++
				}
			}
			cx := b.x + 2 + arrivals[k.To]*max(1, (b.w-4)/max(ins, 1))
			arrivals[k.To]++
			p = route(start, pt{xc - 1, ya}, pt{xc - 1, bus}, pt{cx, bus}, pt{cx, b.y - 1})
		case shared[k.From]:
			p = route(pt{a.x + a.w/2, a.y - 1}, pt{a.x + a.w/2, busTop - 1}, pt{xd, busTop - 1}, pt{xd, yb}, end)
		case l.cols[k.To]-l.cols[k.From] == 1:
			p = route(start, pt{xc, ya}, pt{xc, yb}, end)
		default:
			if y, ok := l.detour(l.cols[k.From], l.cols[k.To], xc, xd, ya, top, &used); ok {
				p = route(start, pt{xc, ya}, pt{xc, y}, pt{xd, y}, pt{xd, yb}, end)
				break
			}
			p = route(start, pt{xc, ya}, pt{xc, max(lane, 0)}, pt{xd, max(lane, 0)}, pt{xd, yb}, end)
			lane--
		}
		l.paths = append(l.paths, p)
	}
	// rows kept for lanes that detours made unneeded go
	usedLanes := top - 2 - lane
	if d := top - usedLanes - min(usedLanes, 1); d > 0 {
		for i := range l.boxes {
			l.boxes[i].y -= d
		}
		for _, p := range l.paths {
			for i := range p {
				p[i].y -= d
			}
		}
		l.h -= d
	}
	return l
}

// detour is the row a forward link over columns runs along: its own row when nothing in the columns
// between is on it, else the first free row just above them; false sends it to the top lanes.
func (l *flowLayout) detour(from, to, x0, x1, y, top int, used *[][3]int) (int, bool) {
	if to <= from {
		return 0, false
	}
	topmost := l.h
	free := func(y int) bool {
		for _, b := range l.boxes {
			if b.col > from && b.col < to && y >= b.y-1 && y <= b.y+b.h {
				return false
			}
		}
		for _, u := range *used {
			if u[0] == y && x0 <= u[2] && u[1] <= x1 {
				return false
			}
		}
		return true
	}
	for _, b := range l.boxes {
		if b.col > from && b.col < to {
			topmost = min(topmost, b.y)
		}
	}
	try := []int{y}
	for c := topmost - 1; c >= top; c-- {
		try = append(try, c)
	}
	for _, c := range try {
		if free(c) {
			*used = append(*used, [3]int{c, x0, x1})
			return c, true
		}
	}
	return 0, false
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
	// fsOrigin+i is the colour of the i-th node requests start from, and of its packets
	fsOrigin
)

func originStyle(i int) flowStyle { return fsOrigin + flowStyle(i%len(originColors())) }

func originColors() []lipgloss.TerminalColor {
	return []lipgloss.TerminalColor{cAccent, cPurple, viz.Palette[2], viz.Palette[3], viz.Palette[6], viz.Palette[1]}
}

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
	if s >= fsOrigin {
		return lipgloss.NewStyle().Foreground(originColors()[int(s-fsOrigin)%len(originColors())]).Bold(true)
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
	if n := len(p); n > 1 {
		end, prev := p[n-1], p[n-2]
		arrow := '▶'
		switch {
		case end.y > prev.y:
			arrow = '▼'
		case end.y < prev.y:
			arrow = '▲'
		case end.x < prev.x:
			arrow = '◀'
		}
		if cl := c.at(end.x, end.y); cl != nil {
			cl.r = arrow
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

// window is the w×h part of c whose top-left is at (-dx, -dy) in c.
func (c *canvas) window(dx, dy, w, h int) *canvas {
	out := newCanvas(w, h)
	for y := 0; y < out.h; y++ {
		for x := 0; x < out.w; x++ {
			if cl := c.at(x-dx, y-dy); cl != nil {
				out.cells[y*out.w+x] = *cl
			}
		}
	}
	return out
}

// runIn is the last straight horizontal run of a path.
func runIn(p []pt) (x0, x1, y int) {
	end, j := p[len(p)-1], len(p)-1
	for j > 0 && p[j-1].y == end.y {
		j--
	}
	return min(p[j].x, end.x), max(p[j].x, end.x), end.y
}
