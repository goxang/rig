// Package viz draws terminal charts: braille line charts with axes and legends, sparklines, gauges,
// and trace waterfalls. It returns strings, so the CLI prints them and the TUI lays them out.
package viz

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
)

// Palette follows Grafana's classic series colours.
var Palette = []lipgloss.Color{"#73BF69", "#F2CC0C", "#5794F2", "#FF780A", "#F2495C", "#B877D9", "#8AB8FF", "#FADE2A", "#96D98D", "#FFB357"}

var (
	axis  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8E8E8E", Dark: "#5C5C5C"})
	label = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#555555", Dark: "#9A9A9A"})
	faint = lipgloss.NewStyle().Faint(true)
)

type Line struct {
	Name   string
	Points []core.Point
	Color  lipgloss.Color
}

// FromSeries names each series by its labels and gives it a palette colour.
func FromSeries(ss []core.Series) []Line {
	out := make([]Line, 0, len(ss))
	for i, s := range ss {
		out = append(out, Line{Name: LabelName(s.Labels), Points: s.Points, Color: Palette[i%len(Palette)]})
	}
	return out
}

func LabelName(l map[string]string) string {
	if len(l) == 0 {
		return "value"
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 1 {
		return l[keys[0]]
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+l[k])
	}
	if n := l["__name__"]; n != "" {
		return n + "{" + strings.Join(parts, ",") + "}"
	}
	return strings.Join(parts, ",")
}

// braille dot bits for (x%2, y%4) inside one cell
var dots = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// LineChart draws lines over a shared time axis in a w×h block (axes and legend included).
func LineChart(lines []Line, w, h int, unit string) string {
	if h < 4 || w < 20 {
		return ""
	}
	var tmin, tmax time.Time
	ymin, ymax := math.Inf(1), math.Inf(-1)
	for _, l := range lines {
		for _, p := range l.Points {
			if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
				continue
			}
			if tmin.IsZero() || p.T.Before(tmin) {
				tmin = p.T
			}
			if p.T.After(tmax) {
				tmax = p.T
			}
			ymin, ymax = math.Min(ymin, p.V), math.Max(ymax, p.V)
		}
	}
	plotH := h - 3 // x axis, time labels, legend
	if math.IsInf(ymin, 1) {
		return faint.Render(center("no data", w, plotH))
	}
	if ymin >= 0 {
		ymin = 0
	}
	if ymax == ymin {
		ymax = ymin + 1
	}
	ymax = niceCeil(ymax)

	yl := []string{Human(ymax, unit), Human((ymax+ymin)/2, unit), Human(ymin, unit)}
	yw := 0
	for _, s := range yl {
		yw = max(yw, len(s))
	}
	cw := w - yw - 2
	pw, ph := cw*2, plotH*4
	cells := make([][]rune, plotH)
	colors := make([][]int, plotH)
	for i := range cells {
		cells[i] = make([]rune, cw)
		colors[i] = make([]int, cw)
		for j := range colors[i] {
			colors[i][j] = -1
		}
	}
	span := tmax.Sub(tmin).Seconds()
	if span <= 0 {
		span = 1
	}
	px := func(t time.Time) int { return int(math.Round(t.Sub(tmin).Seconds() / span * float64(pw-1))) }
	py := func(v float64) int { return ph - 1 - int(math.Round((v-ymin)/(ymax-ymin)*float64(ph-1))) }
	set := func(x, y, c int) {
		if x < 0 || y < 0 || x >= pw || y >= ph {
			return
		}
		cells[y/4][x/2] |= dots[x%2][y%4]
		colors[y/4][x/2] = c
	}
	for li, l := range lines {
		var prev *core.Point
		for i := range l.Points {
			p := l.Points[i]
			if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
				prev = nil
				continue
			}
			if prev == nil {
				set(px(p.T), py(p.V), li)
			} else {
				line(px(prev.T), py(prev.V), px(p.T), py(p.V), func(x, y int) { set(x, y, li) })
			}
			prev = &l.Points[i]
		}
	}

	var b strings.Builder
	for r := 0; r < plotH; r++ {
		lab := ""
		switch r {
		case 0:
			lab = yl[0]
		case plotH / 2:
			lab = yl[1]
		case plotH - 1:
			lab = yl[2]
		}
		b.WriteString(label.Render(fmt.Sprintf("%*s", yw, lab)))
		b.WriteString(axis.Render(" ┤"))
		for c := 0; c < cw; {
			// group runs of one colour into one styled string
			col := colors[r][c]
			var run strings.Builder
			for c < cw && colors[r][c] == col {
				if cells[r][c] == 0 {
					run.WriteRune(' ')
				} else {
					run.WriteRune(0x2800 + cells[r][c])
				}
				c++
			}
			if col < 0 {
				b.WriteString(run.String())
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(colorOf(lines, col)).Render(run.String()))
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat(" ", yw+1) + axis.Render("└"+strings.Repeat("─", cw)) + "\n")
	b.WriteString(strings.Repeat(" ", yw+2) + label.Render(timeAxis(tmin, tmax, cw)) + "\n")
	b.WriteString(Legend(lines, w, unit))
	return b.String()
}

func colorOf(lines []Line, i int) lipgloss.Color {
	if lines[i].Color != "" {
		return lines[i].Color
	}
	return Palette[i%len(Palette)]
}

// Legend shows each series' colour, name and last value, as many as fit on one line.
func Legend(lines []Line, w int, unit string) string {
	var parts []string
	used := 0
	for i, l := range lines {
		last := math.NaN()
		if n := len(l.Points); n > 0 {
			last = l.Points[n-1].V
		}
		name := l.Name
		if len(name) > 28 {
			name = name[:27] + "…"
		}
		txt := name + " " + Human(last, unit)
		if used+len(txt)+4 > w {
			if rest := len(lines) - i; rest > 0 {
				parts = append(parts, faint.Render(fmt.Sprintf("+%d", rest)))
			}
			break
		}
		used += len(txt) + 4
		parts = append(parts, lipgloss.NewStyle().Foreground(colorOf(lines, i)).Render("■")+" "+label.Render(txt))
	}
	return strings.Join(parts, "  ")
}

func timeAxis(a, b time.Time, w int) string {
	f := "15:04:05"
	l, m, r := a.Format(f), a.Add(b.Sub(a)/2).Format(f), b.Format(f)
	if w < len(l)+len(r)+2 {
		if w < len(l) {
			return ""
		}
		return l
	}
	if w < len(l)+len(m)+len(r)+4 {
		return l + strings.Repeat(" ", max(1, w-len(l)-len(r))) + r
	}
	gap1 := w/2 - len(l) - len(m)/2
	gap2 := w - len(l) - gap1 - len(m) - len(r)
	return l + strings.Repeat(" ", max(1, gap1)) + m + strings.Repeat(" ", max(1, gap2)) + r
}

func line(x0, y0, x1, y1 int, plot func(int, int)) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := sign(x1-x0), sign(y1-y0)
	e := dx + dy
	for {
		plot(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

func niceCeil(v float64) float64 {
	if v <= 0 {
		return v
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if m*exp >= v {
			return m * exp
		}
	}
	return v
}

func center(s string, w, h int) string {
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i == h/2 {
			b.WriteString(strings.Repeat(" ", max(0, (w-len(s))/2)) + s)
		}
		if i < h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Human formats a value with SI prefixes and its unit: 1.2k/s, 340ms, 2.1GiB.
func Human(v float64, unit string) string {
	if math.IsNaN(v) {
		return "-"
	}
	switch unit {
	case "s", "seconds":
		return time.Duration(v * float64(time.Second)).Round(roundFor(v)).String()
	case "ms":
		return time.Duration(v * float64(time.Millisecond)).Round(roundFor(v / 1000)).String()
	case "bytes", "B":
		return bytes(v)
	case "%", "percent":
		return fmt.Sprintf("%.1f%%", v)
	case "ratio":
		return fmt.Sprintf("%.1f%%", v*100)
	}
	return si(v) + unit
}

func roundFor(secs float64) time.Duration {
	switch {
	case secs >= 10:
		return time.Second
	case secs >= 0.01:
		return time.Millisecond
	case secs >= 0.00001:
		return time.Microsecond
	}
	return time.Nanosecond
}

func si(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return trim(v/1e9) + "G"
	case a >= 1e6:
		return trim(v/1e6) + "M"
	case a >= 1e3:
		return trim(v/1e3) + "k"
	case a == 0:
		return "0"
	case a < 0.01:
		return fmt.Sprintf("%.1e", v)
	}
	return trim(v)
}

func trim(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

func bytes(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for math.Abs(v) >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return trim(v) + units[i]
}

var blocks = []rune("▁▂▃▄▅▆▇█")

// Sparkline compresses vals into w block characters (the most recent at the right).
func Sparkline(vals []float64, w int, color lipgloss.Color) string {
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if lo > 0 {
		lo = 0
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", w-len(vals)))
	for _, v := range vals {
		i := 0
		if hi > lo {
			i = int((v - lo) / (hi - lo) * float64(len(blocks)-1))
		}
		b.WriteRune(blocks[i])
	}
	return lipgloss.NewStyle().Foreground(color).Render(b.String())
}

// Gauge is a bar filled to frac (0..1), green → amber → red as it fills.
func Gauge(frac float64, w int) string {
	frac = math.Max(0, math.Min(1, frac))
	full := int(math.Round(frac * float64(w)))
	c := lipgloss.Color("#73BF69")
	switch {
	case frac >= 0.85:
		c = "#F2495C"
	case frac >= 0.65:
		c = "#FF9830"
	}
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("█", full)) + axis.Render(strings.Repeat("░", w-full))
}

// Meter is an htop bar, w cells wide: [||||      label], the label right-aligned inside it.
func Meter(frac float64, w int, label string) string {
	frac = math.Max(0, math.Min(1, frac))
	inner := max(1, w-2)
	full := int(math.Round(frac * float64(inner)))
	c := lipgloss.Color("#73BF69")
	switch {
	case frac >= 0.9:
		c = "#F2495C"
	case frac >= 0.7:
		c = "#FF9830"
	}
	if len(label) > inner {
		label = ""
	}
	cells := []rune(strings.Repeat("|", full) + strings.Repeat(" ", inner-full))
	copy(cells[inner-len(label):], []rune(label))
	bar := lipgloss.NewStyle().Foreground(c).Render(string(cells[:full])) + string(cells[full:])
	return axis.Render("[") + bar + axis.Render("]")
}

// Waterfall draws a trace: one row per span, indented by depth, with a bar placed in time.
func Waterfall(spans []core.Span, w int) string {
	if len(spans) == 0 {
		return faint.Render("no spans")
	}
	children := map[string][]int{}
	ids := map[string]bool{}
	for _, s := range spans {
		ids[s.ID] = true
	}
	var roots []int
	t0, t1 := spans[0].Start, spans[0].Start
	for i, s := range spans {
		if s.Parent == "" || !ids[s.Parent] {
			roots = append(roots, i)
		} else {
			children[s.Parent] = append(children[s.Parent], i)
		}
		if s.Start.Before(t0) {
			t0 = s.Start
		}
		if e := s.Start.Add(s.Duration); e.After(t1) {
			t1 = e
		}
	}
	total := t1.Sub(t0)
	if total <= 0 {
		total = time.Microsecond
	}
	colors := map[string]lipgloss.Color{}
	nameW := min(46, w/2)
	barW := w - nameW - 12
	var b strings.Builder
	b.WriteString(label.Render(fmt.Sprintf("%-*s %s %s", nameW, "span", strings.Repeat(" ", barW), "duration")) + "\n")
	var walk func(i, depth int)
	walk = func(i, depth int) {
		s := spans[i]
		c, ok := colors[s.Service]
		if !ok {
			c = Palette[len(colors)%len(Palette)]
			colors[s.Service] = c
		}
		name := strings.Repeat("  ", depth) + s.Service + ": " + s.Name
		if len([]rune(name)) > nameW {
			name = string([]rune(name)[:nameW-1]) + "…"
		}
		off := int(float64(s.Start.Sub(t0)) / float64(total) * float64(barW))
		ln := max(1, int(math.Round(float64(s.Duration)/float64(total)*float64(barW))))
		if off+ln > barW {
			ln = barW - off
		}
		style := lipgloss.NewStyle().Foreground(c)
		if s.Error {
			style = style.Foreground(lipgloss.Color("#F2495C"))
		}
		bar := strings.Repeat(" ", off) + style.Render(strings.Repeat("━", max(ln, 1))) + strings.Repeat(" ", max(0, barW-off-ln))
		b.WriteString(fmt.Sprintf("%s %s %8s\n", style.Render(fmt.Sprintf("%-*s", nameW, name)), bar, s.Duration.Round(time.Microsecond)))
		for _, ch := range children[s.ID] {
			walk(ch, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	b.WriteString(faint.Render(fmt.Sprintf("%d spans, %s, %d services", len(spans), total.Round(time.Microsecond), len(colors))))
	return b.String()
}
