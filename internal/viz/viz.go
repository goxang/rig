// Package viz draws terminal charts: braille line charts with axes and legends, sparklines, gauges,
// and trace waterfalls. It returns strings, so the CLI prints them and the TUI lays them out.
package viz

import (
	"fmt"
	"math"
	"regexp"
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

var seriesLegendLabel = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// LegendOf fills a {{label}} template from a series' labels; empty, it names the series by them.
func LegendOf(tmpl string, labels map[string]string) string {
	out := strings.TrimSpace(seriesLegendLabel.ReplaceAllStringFunc(tmpl, func(m string) string {
		return labels[seriesLegendLabel.FindStringSubmatch(m)[1]]
	}))
	if out == "" {
		return LabelName(labels)
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
	return Plot(lines, w, h-1, Opts{Unit: unit, Highlight: -1, Cursor: -1}) + "\n" + Legend(lines, w, unit)
}

// Opts tune Plot: Hidden lines are left out (the axes fit the rest), Highlight draws one line on
// top with the others faded, Cursor (0..1 across the time axis, <0 none) draws a vertical marker,
// Stack piles the lines up.
type Opts struct {
	// From and To fix the time axis (a dashboard's range); zero fits it to the data.
	From, To  time.Time
	Unit      string
	Hidden    map[int]bool
	Highlight int
	Cursor    float64
	Stack     bool
	// Band shades the columns between two fractions of the time axis (a range being dragged); off when Band[1] <= Band[0]
	Band [2]float64
}

// Span is the time range the lines cover.
func Span(lines []Line) (tmin, tmax time.Time) {
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
		}
	}
	return tmin, tmax
}

// Stacked adds each line's values to the ones before it, point by point at equal times.
func Stacked(lines []Line, hidden map[int]bool) []Line {
	out := make([]Line, len(lines))
	acc := map[int64]float64{}
	for i, l := range lines {
		out[i] = l
		if hidden[i] {
			continue
		}
		pts := make([]core.Point, len(l.Points))
		for j, p := range l.Points {
			if !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
				acc[p.T.UnixNano()] += p.V
				p.V = acc[p.T.UnixNano()]
			}
			pts[j] = p
		}
		out[i].Points = pts
	}
	return out
}

// Plot draws the chart without a legend: h-2 rows of plot, the x axis and the time labels.
func Plot(lines []Line, w, h int, o Opts) string {
	if h < 3 || w < 20 {
		return ""
	}
	if o.Stack {
		lines = Stacked(lines, o.Hidden)
	}
	var tmin, tmax time.Time
	ymin, ymax := math.Inf(1), math.Inf(-1)
	for i, l := range lines {
		if o.Hidden[i] {
			continue
		}
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
	if !o.From.IsZero() {
		tmin, tmax = o.From, o.To
	}
	plotH := h - 2 // x axis, time labels
	if math.IsInf(ymin, 1) {
		return faint.Render(center("no data", w, plotH))
	}
	ymin = floor0(ymin, ymax)
	if ymax == ymin {
		ymax = ymin + 1
	}
	ymax = niceCeil(ymax)

	ticks := 3
	if plotH >= 10 {
		ticks = 5
	}
	tickRow := map[int]string{}
	yw := 0
	for i := 0; i < ticks; i++ {
		r := i * (plotH - 1) / (ticks - 1)
		tickRow[r] = Human(tick(ymin, ymax, r, plotH), o.Unit)
		yw = max(yw, len(tickRow[r]))
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
	order := make([]int, 0, len(lines))
	for i := range lines {
		if !o.Hidden[i] && i != o.Highlight {
			order = append(order, i)
		}
	}
	if o.Highlight >= 0 && o.Highlight < len(lines) && !o.Hidden[o.Highlight] {
		order = append(order, o.Highlight)
	}
	for _, li := range order {
		l := lines[li]
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
	cursorCol := -1
	if o.Cursor >= 0 {
		cursorCol = int(math.Round(math.Min(o.Cursor, 1) * float64(cw-1)))
	}
	faded := lipgloss.AdaptiveColor{Light: "#C4C4C4", Dark: "#3A3F44"}
	bandBg := lipgloss.AdaptiveColor{Light: "#D6E4FF", Dark: "#24395C"}
	inBand := func(c int) bool {
		if o.Band[1] <= o.Band[0] || cw <= 1 {
			return false
		}
		f := float64(c) / float64(cw-1)
		return f >= o.Band[0] && f <= o.Band[1]
	}

	var b strings.Builder
	for r := 0; r < plotH; r++ {
		lab, tick := tickRow[r]
		b.WriteString(label.Render(fmt.Sprintf("%*s", yw, lab)))
		if tick {
			b.WriteString(axis.Render(" ┤"))
		} else {
			b.WriteString(axis.Render(" │"))
		}
		for c := 0; c < cw; {
			if c == cursorCol && cells[r][c] == 0 {
				b.WriteString(axis.Render("│"))
				c++
				continue
			}
			// group runs of one colour into one styled string
			col := colors[r][c]
			band := inBand(c)
			var run strings.Builder
			for c < cw && colors[r][c] == col && inBand(c) == band && !(c == cursorCol && cells[r][c] == 0) {
				if cells[r][c] == 0 {
					run.WriteRune(' ')
				} else {
					run.WriteRune(0x2800 + cells[r][c])
				}
				c++
			}
			st := lipgloss.NewStyle()
			if band {
				st = st.Background(bandBg)
			}
			switch {
			case col < 0:
			case o.Highlight >= 0 && col != o.Highlight:
				st = st.Foreground(faded)
			default:
				st = st.Foreground(colorOf(lines, col))
			}
			b.WriteString(st.Render(run.String()))
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat(" ", yw+1) + axis.Render("└"+strings.Repeat("─", cw)) + "\n")
	b.WriteString(strings.Repeat(" ", yw+2) + label.Render(timeAxis(tmin, tmax, cw)))
	return b.String()
}

// tick is the value at plot row r of plotH, with float noise around zero rounded away.
func tick(ymin, ymax float64, r, plotH int) float64 {
	v := ymax - (ymax-ymin)*float64(r)/float64(max(plotH-1, 1))
	if math.Abs(v) < 1e-9*math.Max(math.Abs(ymax), math.Abs(ymin)) {
		return 0
	}
	return v
}

// floor0 starts the y axis at zero unless values go clearly below it (a rate's float noise of -1e-16 does not).
func floor0(ymin, ymax float64) float64 {
	if ymin >= -1e-9*math.Max(math.Abs(ymax), 1) {
		return 0
	}
	return ymin
}

// PlotOffset is how many cells the plot area starts right of the chart's left edge, for a chart
// drawn with these lines and options: clicks on the plot map to a cursor through it.
func PlotOffset(lines []Line, h int, o Opts) int {
	if o.Stack {
		lines = Stacked(lines, o.Hidden)
	}
	ymin, ymax := math.Inf(1), math.Inf(-1)
	for i, l := range lines {
		if o.Hidden[i] {
			continue
		}
		for _, p := range l.Points {
			if !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
				ymin, ymax = math.Min(ymin, p.V), math.Max(ymax, p.V)
			}
		}
	}
	if math.IsInf(ymin, 1) {
		return 0
	}
	ymin = floor0(ymin, ymax)
	if ymax == ymin {
		ymax = ymin + 1
	}
	ymax = niceCeil(ymax)
	plotH := h - 2
	ticks := 3
	if plotH >= 10 {
		ticks = 5
	}
	yw := 0
	for i := 0; i < ticks; i++ {
		r := i * (plotH - 1) / (ticks - 1)
		yw = max(yw, len(Human(tick(ymin, ymax, r, plotH), o.Unit)))
	}
	return yw + 2
}

// Stats summarises a line the way a Grafana table legend does.
type Stats struct {
	Min, Max, Mean, Last, Total float64
	N                           int
}

func LineStats(l Line) Stats {
	s := Stats{Min: math.NaN(), Max: math.NaN(), Mean: math.NaN(), Last: math.NaN()}
	for _, p := range l.Points {
		if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
			continue
		}
		if s.N == 0 || p.V < s.Min {
			s.Min = p.V
		}
		if s.N == 0 || p.V > s.Max {
			s.Max = p.V
		}
		s.Total += p.V
		s.Last = p.V
		s.N++
	}
	if s.N > 0 {
		s.Mean = s.Total / float64(s.N)
	}
	return s
}

// At is the line's value nearest to t, NaN when it has none.
func At(l Line, t time.Time) float64 {
	best, v := time.Duration(math.MaxInt64), math.NaN()
	for _, p := range l.Points {
		d := p.T.Sub(t)
		if d < 0 {
			d = -d
		}
		if d < best {
			best, v = d, p.V
		}
	}
	return v
}

// Bar is one row of a bar gauge.
type Bar struct {
	Name  string
	Value float64
	Color lipgloss.Color
}

// BarGauge draws one horizontal bar per item, scaled to top (the largest value when 0), name on
// the left and value on the right, coloured by the item.
func BarGauge(items []Bar, w int, unit string, top float64) string {
	if top <= 0 {
		for _, it := range items {
			top = math.Max(top, it.Value)
		}
	}
	nameW, valW := 0, 0
	for _, it := range items {
		nameW = max(nameW, lipgloss.Width(it.Name))
		valW = max(valW, len(Human(it.Value, unit)))
	}
	nameW = min(nameW, max(8, w/3))
	barW := max(4, w-nameW-valW-2)
	var b strings.Builder
	for i, it := range items {
		name := it.Name
		if lipgloss.Width(name) > nameW {
			name = string([]rune(name)[:max(1, nameW-1)]) + "…"
		}
		frac := 0.0
		if top > 0 && !math.IsNaN(it.Value) {
			frac = math.Max(0, math.Min(1, it.Value/top))
		}
		full := int(math.Round(frac * float64(barW)))
		b.WriteString(label.Render(fmt.Sprintf("%-*s", nameW, name)) + " ")
		b.WriteString(lipgloss.NewStyle().Foreground(it.Color).Render(strings.Repeat("■", full)) + axis.Render(strings.Repeat("·", barW-full)))
		b.WriteString(" " + lipgloss.NewStyle().Foreground(it.Color).Render(fmt.Sprintf("%*s", valW, Human(it.Value, unit))))
		if i < len(items)-1 {
			b.WriteByte('\n')
		}
	}
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
	case a < 1e-6:
		return trim(v*1e9) + "n"
	case a < 1e-3:
		return trim(v*1e6) + "µ"
	case a < 0.01:
		return trim(v*1e3) + "m"
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
	wf := Waterfalls(spans, w)
	return wf.Header + "\n" + strings.Join(wf.Rows, "\n") + "\n" + wf.Footer
}

// WaterfallView is a waterfall split in rows, so a screen can scroll it and select a span: Order[i]
// is the index in spans of Rows[i].
type WaterfallView struct {
	Header, Footer string
	Rows           []string
	Order          []int
}

func Waterfalls(spans []core.Span, w int) WaterfallView {
	var out WaterfallView
	if len(spans) == 0 {
		return out
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
	barW := max(1, w-nameW-12)
	out.Header = label.Render(fmt.Sprintf("%-*s %s %s", nameW, "span", strings.Repeat(" ", barW), "duration"))
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
		out.Rows = append(out.Rows, fmt.Sprintf("%s %s %8s", style.Render(fmt.Sprintf("%-*s", nameW, name)), bar, s.Duration.Round(time.Microsecond)))
		out.Order = append(out.Order, i)
		for _, ch := range children[s.ID] {
			walk(ch, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	out.Footer = faint.Render(fmt.Sprintf("%d spans, %s, %d services", len(spans), total.Round(time.Microsecond), len(colors)))
	return out
}
