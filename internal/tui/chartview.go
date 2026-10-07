package tui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/viz"
)

// chartView shows one chart of lines kept in memory full size, as the Metrics screen shows a panel:
// a legend to hide, isolate and filter series, a cursor, a time range and drag to zoom.
type chartView struct {
	zone   string // zone id prefix
	title  string
	unit   string
	lines  []viz.Line
	legend *grid
	hidden map[string]bool
	filter *regexp.Regexp
	cursor float64
	hl     bool
	win    timeWindow
	// from, to are the range of the last render, for drags and the cursor
	from, to time.Time
}

var historyRanges = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

func newChartView(zone string) *chartView {
	return &chartView{zone: zone, hidden: map[string]bool{}, cursor: -1, win: timeWindow{presets: historyRanges, rng: 2},
		legend: newGrid(zone+"leg", col("", 1), col("series", 0), rcol("min", 12), rcol("max", 12), rcol("mean", 12), rcol("last", 12), rcol("@cursor", 12))}
}

func (c *chartView) hints() [][2]string {
	return [][2]string{{"esc", "back"}, {"n", "next chart"}, {"↑↓", "series"}, {"space", "hide"}, {"enter", "only this"}, {"a", "all"}, {"/", "filter"}, {"←→", "cursor"},
		{"t", "range"}, {"drag", "zoom to a time range"}, {"Z", "zoom out"}, {", .", "shift range"}, {"ctrl+wheel", "zoom time"}}
}

// shown is the lines inside the window, minus the filtered out.
func (c *chartView) shown() ([]viz.Line, map[int]bool) {
	c.from, c.to = c.win.span(time.Now())
	var out []viz.Line
	hidden := map[int]bool{}
	for _, l := range c.lines {
		var pts []core.Point
		for _, p := range l.Points {
			if !p.T.Before(c.from) && !p.T.After(c.to) {
				pts = append(pts, p)
			}
		}
		l.Points = pts
		if c.hidden[l.Name] || c.filter != nil && !c.filter.MatchString(l.Name) {
			hidden[len(out)] = true
		}
		out = append(out, l)
	}
	return out, hidden
}

func (c *chartView) view(m *model, x, y, w, h int) string {
	ls, hidden := c.shown()
	var tb strings.Builder
	bx := 0
	button := func(id, text string) {
		s := sTabOff.Render(text)
		m.zone(c.zone+id, x+bx, y, lipgloss.Width(s), 1)
		bx += lipgloss.Width(s)
		tb.WriteString(s)
	}
	button("back", "‹ back")
	button("all", "all series")
	button("shift:-1", "‹")
	button("out", "⊖")
	button("shift:1", "›")
	button("range", "⏱ "+c.win.label()+" ▾")
	info := "  " + sTitle.Render(c.title)
	if c.filter != nil {
		info += sAmber.Render("  /" + c.filter.String() + "/")
	}
	head := truncate(tb.String()+info, w) + "\n"
	h--

	at, hasAt := time.Time{}, c.cursor >= 0
	if hasAt {
		at = c.from.Add(time.Duration(c.cursor * float64(c.to.Sub(c.from))))
	}
	var rows []grow
	for i, l := range ls {
		s := viz.LineStats(l)
		sw := lipgloss.NewStyle().Foreground(l.Color).Render("■")
		if hidden[i] {
			sw = sDim.Render("□")
		}
		cv := math.NaN()
		if hasAt {
			cv = viz.At(l, at)
		}
		rows = append(rows, grow{id: l.Name,
			cells: []string{sw, l.Name, viz.Human(s.Min, c.unit), viz.Human(s.Max, c.unit), viz.Human(s.Mean, c.unit), viz.Human(s.Last, c.unit), viz.Human(cv, c.unit)},
			keys:  []any{nil, nil, s.Min, s.Max, s.Mean, s.Last, cv}})
	}
	c.legend.set(rows)
	legendH := min(len(rows)+3, max(5, h*2/5))
	chartH := h - legendH
	highlight := -1
	if r, ok := c.legend.current(); ok && c.hl {
		for i, l := range ls {
			if l.Name == r.id {
				highlight = i
			}
		}
	}
	opts := viz.Opts{From: c.from, To: c.to, Unit: c.unit, Hidden: hidden, Highlight: highlight, Cursor: c.cursor, Band: c.win.bandOn(c.zone + "plot:")}
	if off := viz.PlotOffset(ls, chartH-2, opts); off > 0 {
		m.zone(fmt.Sprintf("%splot:%d", c.zone, w-2-off), x+1+off, y+2, w-2-off, chartH-4)
	}
	chart := viz.Plot(ls, w-2, chartH-2, opts)
	note := ""
	if hasAt {
		note = " · cursor " + at.Format("15:04:05")
	}
	lt := fmt.Sprintf("series %d/%d%s · click selects, double-click only this, ■ or ctrl-click hides", len(ls)-len(hidden), len(ls), note)
	body := c.legend.view(m, x+1, y+1+chartH+1, w-2, legendH-2, true)
	return head + panel(c.title, chart, w, chartH, true) + "\n" + panel(lt, body, w, legendH, false)
}

// key reports false for esc, which closes the view.
func (c *chartView) key(m *model, k tea.KeyMsg) bool {
	if c.legend.key(k) {
		c.hl = true
		return true
	}
	r, ok := c.legend.current()
	switch k.String() {
	case "esc", "v", "z":
		switch {
		case c.hl:
			c.hl = false
		case c.cursor >= 0:
			c.cursor = -1
		default:
			return false
		}
	case " ":
		if ok {
			c.hidden[r.id] = !c.hidden[r.id]
		}
	case "enter":
		if ok {
			c.isolate(r.id)
		}
	case "a":
		c.hidden, c.filter = map[string]bool{}, nil
	case "/":
		cur := ""
		if c.filter != nil {
			cur = c.filter.String()
		}
		var names []string
		for _, l := range c.lines {
			names = append(names, l.Name)
		}
		m.askChecked("show series matching (regex, empty: all)", cur, "a Go RE2 regexp matching series names"+within("series", names), regexCheck, func(v string) tea.Cmd {
			re, err := regexp.Compile(v)
			switch {
			case v == "":
				c.filter = nil
			case err != nil:
				m.setStatus("filter: "+err.Error(), true)
			default:
				c.filter = re
			}
			return nil
		})
	case "left", "h":
		if c.cursor < 0 {
			c.cursor = 1
		}
		c.cursor = math.Max(0, c.cursor-0.01)
	case "right", "l":
		if c.cursor < 0 {
			c.cursor = 0
		}
		c.cursor = math.Min(1, c.cursor+0.01)
	case "t":
		c.win.preset(c.win.rng + 1)
	case "Z", "ctrl+z":
		c.win.zoomOut()
	case ",":
		c.win.shift(-1)
	case ".":
		c.win.shift(1)
	}
	return true
}

func (c *chartView) isolate(name string) {
	only := true
	for _, l := range c.lines {
		if l.Name != name && !c.hidden[l.Name] {
			only = false
		}
	}
	c.hidden = map[string]bool{}
	if !only {
		for _, l := range c.lines {
			c.hidden[l.Name] = l.Name != name
		}
	}
}

// click reports false for the back button.
func (c *chartView) click(m *model, h hit) bool {
	id, ok := strings.CutPrefix(h.id, c.zone)
	if !ok {
		if c.legend.click(h) {
			c.hl = true
			if r, ok := c.legend.current(); ok {
				switch {
				case h.mod || h.x < 2:
					c.hidden[r.id] = !c.hidden[r.id]
				case h.double:
					c.isolate(r.id)
				}
			}
		}
		return true
	}
	switch {
	case id == "back":
		return false
	case id == "all":
		c.hidden, c.filter = map[string]bool{}, nil
	case id == "out":
		c.win.zoomOut()
	case id == "range":
		c.win.preset(c.win.rng + 1)
	case strings.HasPrefix(id, "shift:"):
		d, _ := strconv.Atoi(strings.TrimPrefix(id, "shift:"))
		c.win.shift(d)
	case strings.HasPrefix(id, "plot:"):
		if f, ok := c.plotFrac(h); ok {
			c.cursor = f
		}
	}
	return true
}

func (c *chartView) plotFrac(h hit) (float64, bool) {
	ws, ok := strings.CutPrefix(h.id, c.zone+"plot:")
	w, _ := strconv.Atoi(ws)
	if !ok || w < 2 {
		return 0, false
	}
	return float64(h.x) / float64(w-1), true
}

func (c *chartView) drag(m *model, h hit, phase dragPhase) bool {
	f, ok := c.plotFrac(h)
	if !ok {
		return false
	}
	if c.win.drag(h.id, f, phase, c.from, c.to) {
		c.cursor = -1
		m.setStatus("zoomed to "+c.win.label()+" · Z or ⊖ zooms out", false)
	}
	return true
}

func (c *chartView) wheel(h hit, up bool) bool {
	if h.id == c.zone+"range" {
		c.win.preset(c.win.rng + map[bool]int{true: -1, false: 1}[up])
		return true
	}
	if f, ok := c.plotFrac(h); ok && h.mod {
		c.win.zoomAround(f, up, c.from, c.to)
		return true
	}
	k := tea.KeyMsg{Type: tea.KeyDown}
	if up {
		k = tea.KeyMsg{Type: tea.KeyUp}
	}
	c.legend.key(k)
	c.hl = true
	return true
}
