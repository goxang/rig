package tui

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/spec"
)

var (
	ranges    = []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour}
	refreshes = []time.Duration{0, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}
)

type panelData struct {
	from, to time.Time
	series   []core.Series
	names    []string // per series, from its query's legend
	err      error
	query    string
}

type panelsMsg struct {
	gen  int
	dash string
	data []panelData
}

type dashVarsMsg struct {
	gen     int
	dash    string
	choices map[string][]string
	err     error
}

// seriesState is what the viewer did to one panel's series: hidden ones and a filter.
type seriesState struct {
	hidden map[string]bool
	filter *regexp.Regexp
}

// mitem is one entry of a dashboard: a row header (pi < 0) or a query panel (pi indexes data).
type mitem struct {
	row string
	pi  int
}

// placed is where a panel landed in the last render, in content cells (before scrolling).
type placed struct {
	pi, x, y, w, h int
}

type metricsTab struct {
	// source replaces the metrics component of panels that name none (m picks it)
	source    string
	dash      int
	focus     int
	zoom      bool
	win       timeWindow
	dragged   tea.Cmd // what a drag that zoomed asks for, taken by dragCmd
	every     int
	adhoc     []spec.Panel
	data      []panelData
	vars      map[string]map[string][]string
	choices   map[string]map[string][]string
	varsBusy  string // the dashboard whose variables are being fetched
	varErr    string
	pending   string // a service to select once the dashboard's variables are known
	collapsed map[string]bool
	series    map[string]*seriesState
	stack     map[string]bool
	cursor    float64
	hl        bool
	scroll    int
	contentH  int
	bodyH     int
	placed    []placed
	legend    *grid
}

func (t *metricsTab) name() string            { return "Metrics" }
func (t *metricsTab) typing() bool            { return false }
func (t *metricsTab) interval() time.Duration { return refreshes[t.every] }

func (t *metricsTab) hints() [][2]string {
	if t.zoom {
		return [][2]string{{"esc v", "back"}, {"↑↓", "series"}, {"space", "hide"}, {"enter", "only this"}, {"a", "all"}, {"/", "filter"}, {"←→", "cursor"}, {"s", "stack"}, {"< > I", "sort"}, {"y", "copy query"},
			{"drag", "zoom to a time range"}, {"Z", "zoom out"}, {", .", "shift range"}, {"t", "range"}}
	}
	return [][2]string{{"←→↑↓", "focus"}, {"v enter", "view"}, {"[ ] d", "dashboard"}, {"$", "variables"}, {"t", "range"}, {"drag", "zoom to a time range"}, {"Z", "zoom out"}, {", .", "shift range"}, {"ctrl+wheel", "zoom time"}, {"m", "source"}, {"R", "refresh"}, {"o O", "fold rows"}, {"a e x", "ad hoc"}, {"click legend", "only/hide"}}
}

func (t *metricsTab) init() {
	if t.vars == nil {
		t.vars, t.choices = map[string]map[string][]string{}, map[string]map[string][]string{}
		t.collapsed, t.series, t.stack = map[string]bool{}, map[string]*seriesState{}, map[string]bool{}
		t.cursor, t.every = -1, 2
		t.win.presets, t.win.rng = ranges, 1
		t.legend = newGrid("mlegend", col("", 1), col("series", 0), rcol("min", 12), rcol("max", 12), rcol("mean", 12), rcol("last", 12), rcol("@cursor", 12))
	}
}

func (t *metricsTab) dashNames(m *model) []string {
	names := m.app.Spec.DashboardOrder
	if len(names) != len(m.app.Spec.Dashboards) {
		names = engine.SortedKeys(m.app.Spec.Dashboards)
	}
	if len(names) == 0 {
		return []string{"ad hoc"}
	}
	return names
}

// promHint tells the AI completing a query which metrics this dashboard already reads.
func (t *metricsTab) promHint(m *model) string {
	h := "a PromQL expression for this environment's Prometheus"
	var qs []string
	for i, d := range t.data {
		if i == 10 {
			break
		}
		qs = append(qs, d.query)
	}
	if len(qs) > 0 {
		h += "; the dashboard's queries: " + strings.Join(qs, " ; ")
	}
	return h
}

func (t *metricsTab) dashIndex(m *model) int {
	n := len(t.dashNames(m))
	return (t.dash%n + n) % n
}

func (t *metricsTab) dashName(m *model) string { return t.dashNames(m)[t.dashIndex(m)] }

func (t *metricsTab) dashboard(m *model) *spec.Dashboard {
	if d := m.app.Spec.Dashboards[t.dashName(m)]; d != nil {
		return d
	}
	return &spec.Dashboard{}
}

// items lays the dashboard out as rows and panels, the ad hoc panels last, and returns the query
// panels in data order.
func (t *metricsTab) items(m *model) ([]mitem, []spec.Panel) {
	var items []mitem
	var ps []spec.Panel
	add := func(p spec.Panel) {
		if p.Row != "" {
			items = append(items, mitem{row: p.Row, pi: -1})
		}
		if len(p.Targets()) == 0 {
			return
		}
		items = append(items, mitem{pi: len(ps)})
		ps = append(ps, p)
	}
	for _, p := range t.dashboard(m).Panels {
		add(p)
	}
	for i, p := range t.adhoc {
		if i == 0 {
			p.Row = "ad hoc"
		}
		add(p)
	}
	return items, ps
}

func (t *metricsTab) key(m *model, pi int) string { return t.dashName(m) + "/" + strconv.Itoa(pi) }

func (t *metricsTab) state(m *model, pi int) *seriesState {
	k := t.key(m, pi)
	st := t.series[k]
	if st == nil {
		st = &seriesState{hidden: map[string]bool{}}
		t.series[k] = st
	}
	return st
}

func (t *metricsTab) open(m *model) tea.Cmd {
	t.init()
	return t.fetchAll(m)
}

func (t *metricsTab) refresh(m *model) tea.Cmd { return t.fetchAll(m) }

// fetchAll loads the dashboard's variables first when it has some, then its panels.
func (t *metricsTab) fetchAll(m *model) tea.Cmd {
	t.init()
	name, d := t.dashName(m), t.dashboard(m)
	if len(d.Vars) > 0 && t.choices[name] == nil {
		if t.varsBusy == name {
			return nil
		}
		t.varsBusy = name
		return t.fetchVars(m, name, d)
	}
	_, ps := t.items(m)
	if len(ps) == 0 {
		return nil
	}
	return t.fetch(m, name, ps)
}

func (t *metricsTab) fetchVars(m *model, name string, d *spec.Dashboard) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out := map[string][]string{}
		var errs []string
		for n, v := range d.Vars {
			vs, err := a.VarValues(c, v)
			if err != nil {
				errs = append(errs, "$"+n+": "+err.Error())
			}
			out[n] = vs
		}
		msg := dashVarsMsg{gen: gen, dash: name, choices: out}
		if len(errs) > 0 {
			sort.Strings(errs)
			msg.err = fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return msg
	}
}

// fetch runs every panel's range query in parallel against its source.
func (t *metricsTab) fetch(m *model, name string, panels []spec.Panel) tea.Cmd {
	a, gen, ctx, source := m.app, m.gen, m.ctx, t.source
	start, end := t.win.span(time.Now())
	rng := end.Sub(start)
	step := max(rng/120, time.Second)
	vars := t.vars[name]
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out := make([]panelData, len(panels))
		var wg sync.WaitGroup
		for i, p := range panels {
			wg.Add(1)
			go func() {
				defer wg.Done()
				from := p.Source
				if from == "" {
					from = source
				}
				src, _, err := engine.Get[core.Metrics](a, core.KindMetrics, from)
				if err != nil {
					out[i].err = err
					return
				}
				d := &out[i]
				d.from, d.to = end.Add(-rng), end
				var qs []string
				for _, tg := range p.Targets() {
					q := engine.ExpandQuery(tg.Query, vars, rng, step)
					qs = append(qs, q)
					ss, err := src.Range(c, q, end.Add(-rng), end, step)
					if err != nil {
						d.err = err
						continue
					}
					for _, s := range ss {
						name := viz.LabelName(s.Labels)
						if tg.Legend != "" {
							name = legend(tg.Legend, s.Labels)
						}
						d.series, d.names = append(d.series, s), append(d.names, name)
					}
				}
				d.query = strings.Join(qs, "\n")
				if d.err != nil && len(d.series) > 0 {
					d.err = nil // what answered is still worth showing
				}
				if d.series == nil && d.err == nil {
					d.series = []core.Series{}
				}
			}()
		}
		wg.Wait()
		return panelsMsg{gen: gen, dash: name, data: out}
	}
}

// reload drops what is shown and fetches it again (after a change of dashboard, range or variable).
func (t *metricsTab) reload(m *model) tea.Cmd {
	t.data = nil
	return t.fetchAll(m)
}

func (t *metricsTab) switchDash(m *model, i int) tea.Cmd {
	t.dash, t.focus, t.scroll, t.zoom, t.cursor = i, 0, 0, false, -1
	return t.reload(m)
}

// jump shows the first dashboard with a $service variable, set to service: the Services screen's g.
func (t *metricsTab) jump(m *model, service string) tea.Cmd {
	t.init()
	for i, n := range t.dashNames(m) {
		if d := m.app.Spec.Dashboards[n]; d != nil && d.Vars["service"] != nil {
			t.dash = i
			t.pending = service
			if t.choices[n] != nil {
				t.pickPending(n)
			}
			return t.switchDash(m, i)
		}
	}
	m.setStatus("no dashboard has a $service variable (vars: { service: ... })", true)
	return nil
}

// pickPending selects the pending service among the variable's values: the same name, else the
// shortest that contains it (parsersvc for parser).
func (t *metricsTab) pickPending(dash string) {
	if t.pending == "" {
		return
	}
	best := ""
	for _, v := range t.choices[dash]["service"] {
		if v == t.pending {
			best = v
			break
		}
		if strings.Contains(v, t.pending) && (best == "" || len(v) < len(best)) {
			best = v
		}
	}
	if best == "" {
		best = t.pending
	}
	if t.vars[dash] == nil {
		t.vars[dash] = map[string][]string{}
	}
	t.vars[dash]["service"] = []string{best}
	t.pending = ""
}

func (t *metricsTab) update(m *model, msg tea.Msg) tea.Cmd {
	t.init()
	switch msg := msg.(type) {
	case dashVarsMsg:
		if msg.gen != m.gen {
			return nil
		}
		if t.varsBusy == msg.dash {
			t.varsBusy = ""
		}
		t.choices[msg.dash] = msg.choices
		t.varErr = ""
		if msg.err != nil {
			t.varErr = msg.err.Error()
		}
		if t.vars[msg.dash] == nil {
			t.vars[msg.dash] = map[string][]string{}
		}
		for n, v := range m.app.Spec.Dashboards[msg.dash].Vars {
			if len(t.vars[msg.dash][n]) == 0 {
				t.vars[msg.dash][n] = engine.DefaultVar(v, msg.choices[n])
			}
		}
		t.pickPending(msg.dash)
		return t.fetchAll(m)
	case panelsMsg:
		if msg.gen == m.gen && msg.dash == t.dashName(m) {
			t.data = msg.data
		}
	case tea.KeyMsg:
		if t.zoom {
			return t.viewKey(m, msg)
		}
		return t.gridKey(m, msg)
	}
	return nil
}

func (t *metricsTab) gridKey(m *model, k tea.KeyMsg) tea.Cmd {
	_, ps := t.items(m)
	switch k.String() {
	case "left", "h":
		t.moveFocus(-1, 0)
	case "right", "l":
		t.moveFocus(1, 0)
	case "up", "k":
		t.moveFocus(0, -1)
	case "down", "j":
		t.moveFocus(0, 1)
	case "pgup":
		t.scroll = max(0, t.scroll-t.bodyH/2)
	case "pgdown":
		t.scroll = min(max(0, t.contentH-t.bodyH), t.scroll+t.bodyH/2)
	case "home", "g":
		t.focus, t.scroll = 0, 0
	case "v", "z", "enter":
		if t.focus < len(ps) {
			t.zoom, t.hl = true, false
		}
	case "esc":
		t.cursor = -1
	case "t":
		t.win.preset(t.win.rng + 1)
		return t.reload(m)
	case "Z", "ctrl+z":
		if t.win.zoomOut() {
			return t.reload(m)
		}
	case ",", ".":
		t.win.shift(map[string]int{",": -1, ".": 1}[k.String()])
		return t.reload(m)
	case "R":
		t.every = (t.every + 1) % len(refreshes)
		m.setStatus("auto refresh "+refreshText(refreshes[t.every]), false)
	case "r":
		return t.fetchAll(m)
	case "]":
		return t.switchDash(m, t.dashIndex(m)+1)
	case "[":
		return t.switchDash(m, t.dashIndex(m)-1)
	case "d":
		t.pickDash(m)
	case "$":
		t.pickVar(m)
	case "m":
		names := append([]string{"default"}, m.app.Names(core.KindMetrics)...)
		m.pick("metrics source of the dashboards", names, nil, 0, false, func(c []string) tea.Cmd {
			if len(c) == 0 {
				return nil
			}
			t.source = c[0]
			if t.source == "default" {
				t.source = ""
			}
			return t.fetchAll(m)
		})
	case "o":
		if r := t.rowOf(m, t.focus); r != "" {
			k := t.dashName(m) + "/" + r
			t.collapsed[k] = !t.collapsed[k]
		}
	case "O":
		for k := range t.collapsed {
			if strings.HasPrefix(k, t.dashName(m)+"/") {
				delete(t.collapsed, k)
			}
		}
	case "s":
		t.stack[t.key(m, t.focus)] = !t.stack[t.key(m, t.focus)]
	case "y":
		t.copyQuery(m)
	case "a":
		m.askAI("query", "", t.promHint(m), func(q string) tea.Cmd {
			if q == "" {
				return nil
			}
			t.adhoc = append(t.adhoc, spec.Panel{Title: q, Query: q})
			_, ps := t.items(m)
			t.focus = len(ps) - 1
			return t.reload(m)
		})
	case "e":
		if t.focus < len(ps) {
			p := ps[t.focus]
			idx := t.focus - (len(ps) - len(t.adhoc))
			m.askAI("query", p.Query, t.promHint(m), func(q string) tea.Cmd {
				np := p
				np.Query, np.Title, np.Row = q, q, ""
				if idx >= 0 {
					t.adhoc[idx] = np
				} else {
					t.adhoc = append(t.adhoc, np)
				}
				return t.reload(m)
			})
		}
	case "x":
		idx := t.focus - (len(ps) - len(t.adhoc))
		if idx >= 0 && idx < len(t.adhoc) {
			t.adhoc = append(t.adhoc[:idx], t.adhoc[idx+1:]...)
			t.focus = max(0, t.focus-1)
			return t.reload(m)
		}
	}
	return nil
}

// moveFocus steps to the next panel in reading order (dx), or to the nearest one above or below (dy).
func (t *metricsTab) moveFocus(dx, dy int) {
	if len(t.placed) == 0 {
		return
	}
	cur := -1
	for i, p := range t.placed {
		if p.pi == t.focus {
			cur = i
		}
	}
	if cur < 0 {
		t.focus = t.placed[0].pi
		return
	}
	if dx != 0 {
		t.focus = t.placed[min(max(cur+dx, 0), len(t.placed)-1)].pi
		return
	}
	c := t.placed[cur]
	cx := c.x + c.w/2
	best, bestD := -1, math.MaxInt
	for i, p := range t.placed {
		if dy < 0 && p.y >= c.y || dy > 0 && p.y <= c.y {
			continue
		}
		d := iabs(p.y-c.y)*1000 + iabs(p.x+p.w/2-cx)
		if d < bestD {
			best, bestD = i, d
		}
	}
	if best >= 0 {
		t.focus = t.placed[best].pi
	}
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (t *metricsTab) rowOf(m *model, pi int) string {
	items, _ := t.items(m)
	row := ""
	for _, it := range items {
		if it.pi < 0 {
			row = it.row
		} else if it.pi == pi {
			return row
		}
	}
	return ""
}

func (t *metricsTab) copyQuery(m *model) {
	if t.focus < len(t.data) && t.data[t.focus].query != "" {
		copyText(t.data[t.focus].query)
		m.setStatus("query copied", false)
	}
}

func (t *metricsTab) pickDash(m *model) {
	names := t.dashNames(m)
	var desc []string
	for _, n := range names {
		d := ""
		if x := m.app.Spec.Dashboards[n]; x != nil {
			d = x.Help
			if d == "" {
				d = fmt.Sprintf("%d panels", len(x.Panels))
			}
		}
		desc = append(desc, d)
	}
	m.pick("dashboard", names, desc, t.dashIndex(m), false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		return t.switchDash(m, slices.Index(names, chosen[0]))
	})
}

func (t *metricsTab) pickVar(m *model) {
	names := t.dashboard(m).VarOrder
	switch len(names) {
	case 0:
		m.setStatus("this dashboard has no variables", false)
	case 1:
		t.pickValues(m, names[0])
	default:
		var desc []string
		for _, n := range names {
			desc = append(desc, varText(t.vars[t.dashName(m)][n]))
		}
		m.pick("variable", names, desc, 0, false, func(chosen []string) tea.Cmd {
			if len(chosen) == 1 {
				t.pickValues(m, chosen[0])
			}
			return nil
		})
	}
}

func (t *metricsTab) pickValues(m *model, name string) {
	dash := t.dashName(m)
	v := t.dashboard(m).Vars[name]
	if v == nil {
		return
	}
	items := slices.Clone(t.choices[dash][name])
	if v.All {
		items = append([]string{engine.AllValue}, items...)
	}
	if len(items) == 0 {
		m.setStatus("$"+name+" has no values yet", true)
		return
	}
	done := func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		if slices.Contains(chosen, engine.AllValue) {
			chosen = []string{engine.AllValue}
		}
		if t.vars[dash] == nil {
			t.vars[dash] = map[string][]string{}
		}
		t.vars[dash][name] = chosen
		return t.reload(m)
	}
	if v.Multi {
		m.pickMany("$"+name, items, nil, t.vars[dash][name], done)
		return
	}
	m.pick("$"+name, items, nil, max(0, slices.Index(items, firstOf(t.vars[dash][name]))), false, done)
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func varText(vs []string) string {
	if len(vs) == 0 {
		return "-"
	}
	if vs[0] == engine.AllValue {
		return "All"
	}
	return strings.Join(vs, " + ")
}

func refreshText(d time.Duration) string {
	switch {
	case d == 0:
		return "off"
	case d < time.Minute:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

func rangeText(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

func (t *metricsTab) pickRange(m *model) {
	var items []string
	for _, r := range ranges {
		items = append(items, "last "+rangeText(r))
	}
	m.pick("time range (drag across a chart zooms in, Z zooms out, , . shift)", items, nil, t.win.rng, false, func(chosen []string) tea.Cmd {
		if i := slices.Index(items, firstOf(chosen)); i >= 0 {
			t.win.preset(i)
			return t.reload(m)
		}
		return nil
	})
}

func (t *metricsTab) pickRefresh(m *model) {
	var items []string
	for _, r := range refreshes {
		items = append(items, refreshText(r))
	}
	m.pick("auto refresh", items, nil, t.every, false, func(chosen []string) tea.Cmd {
		if i := slices.Index(items, firstOf(chosen)); i >= 0 {
			t.every = i
		}
		return nil
	})
}

// ---- series ----

// seriesLines are a panel's series as chart lines, named by their query's legend.
func seriesLines(p spec.Panel, d panelData) []viz.Line {
	ls := viz.FromSeries(d.series)
	for i := range ls {
		if i < len(d.names) {
			ls[i].Name = d.names[i]
		}
	}
	return ls
}

// legend fills {{label}} placeholders from a series' labels; a label the series lacks is empty,
// and a legend left empty falls back to the labels.
func legend(tmpl string, labels map[string]string) string { return viz.LegendOf(tmpl, labels) }

func (st *seriesState) shown(name string) bool {
	return !st.hidden[name] && (st.filter == nil || st.filter.MatchString(name))
}

func (st *seriesState) hiddenSet(ls []viz.Line) map[int]bool {
	out := map[int]bool{}
	for i, l := range ls {
		if !st.shown(l.Name) {
			out[i] = true
		}
	}
	return out
}

// isolate shows only name, or every series when name is already the only one shown (Grafana's click).
func (st *seriesState) isolate(ls []viz.Line, name string) {
	only := true
	for _, l := range ls {
		if l.Name != name && st.shown(l.Name) {
			only = false
		}
	}
	st.hidden = map[string]bool{}
	if !only {
		for _, l := range ls {
			if l.Name != name {
				st.hidden[l.Name] = true
			}
		}
	}
}

func (t *metricsTab) panelLines(pi int, p spec.Panel) []viz.Line {
	if pi >= len(t.data) {
		return nil
	}
	return seriesLines(p, t.data[pi])
}

// cursorTime is the time under the shared cursor across the panel's range, false without a cursor.
func (t *metricsTab) cursorTime(d panelData) (time.Time, bool) {
	if t.cursor < 0 || d.from.IsZero() {
		return time.Time{}, false
	}
	return d.from.Add(time.Duration(t.cursor * float64(d.to.Sub(d.from)))), true
}

// value is what a series shows: its value under the cursor, else its last.
func value(l viz.Line, at time.Time, ok bool) float64 {
	if ok {
		return viz.At(l, at)
	}
	return viz.LineStats(l).Last
}

func thresholdColor(p spec.Panel, v float64, def lipgloss.Color) lipgloss.Color {
	switch {
	case p.Crit != nil && v >= *p.Crit:
		return lipgloss.Color("#F2495C")
	case p.Warn != nil && v >= *p.Warn:
		return lipgloss.Color("#FF9830")
	}
	return def
}

// ---- clicks and the wheel ----

func (t *metricsTab) click(m *model, h hit) tea.Cmd {
	if i, ok := stripHit(h, "mdash"); ok {
		return t.switchDash(m, i)
	}
	id := h.id
	switch {
	case id == "mdash-more":
		t.pickDash(m)
	case id == "mrange":
		t.pickRange(m)
	case id == "mzoomout":
		if t.win.zoomOut() {
			return t.reload(m)
		}
	case strings.HasPrefix(id, "mshift:"):
		d, _ := strconv.Atoi(strings.TrimPrefix(id, "mshift:"))
		t.win.shift(d)
		return t.reload(m)
	case id == "mrefresh":
		t.pickRefresh(m)
	case id == "mback":
		t.zoom = false
	case id == "mstack":
		t.stack[t.key(m, t.focus)] = !t.stack[t.key(m, t.focus)]
	case id == "mall":
		st := t.state(m, t.focus)
		st.hidden, st.filter = map[string]bool{}, nil
	case id == "mcopy":
		t.copyQuery(m)
	case strings.HasPrefix(id, "mvar:"):
		t.pickValues(m, strings.TrimPrefix(id, "mvar:"))
	case strings.HasPrefix(id, "mrow:"):
		k := t.dashName(m) + "/" + strings.TrimPrefix(id, "mrow:")
		t.collapsed[k] = !t.collapsed[k]
	case strings.HasPrefix(id, "mpanel:"), strings.HasPrefix(id, "mplot:"):
		f := strings.Split(id, ":")
		pi, _ := strconv.Atoi(f[1])
		t.focus = pi
		if f[0] == "mplot" && len(f) == 3 {
			if w, _ := strconv.Atoi(f[2]); w > 1 {
				t.cursor = math.Min(1, float64(h.x)/float64(w-1))
			}
		}
		if h.double {
			t.zoom, t.hl = !t.zoom, false
		}
	case strings.HasPrefix(id, "mleg:"):
		f := strings.Split(id, ":")
		pi, _ := strconv.Atoi(f[1])
		si, _ := strconv.Atoi(f[2])
		_, ps := t.items(m)
		if pi < len(ps) {
			ls := t.panelLines(pi, ps[pi])
			if si < len(ls) {
				t.focus = pi
				st := t.state(m, pi)
				if h.mod {
					st.hidden[ls[si].Name] = !st.hidden[ls[si].Name]
				} else {
					st.isolate(ls, ls[si].Name)
				}
			}
		}
	case strings.HasPrefix(id, "mlegend:"):
		if t.legend.click(h) {
			t.hl = true
			if r, ok := t.legend.current(); ok {
				st := t.state(m, t.focus)
				_, ps := t.items(m)
				switch {
				case h.mod || h.x < 2:
					st.hidden[r.id] = !st.hidden[r.id]
				case h.double:
					st.isolate(t.panelLines(t.focus, ps[t.focus]), r.id)
				}
			}
		}
	}
	return nil
}

func (t *metricsTab) wheel(m *model, z hit, up bool) (tea.Cmd, bool) {
	if z.id == "mrange" {
		if up {
			t.win.preset(t.win.rng - 1)
		} else {
			t.win.preset(t.win.rng + 1)
		}
		return t.reload(m), true
	}
	if pi, frac, ok := t.plotAt(z); ok && z.mod && pi < len(t.data) {
		t.win.zoomAround(frac, up, t.data[pi].from, t.data[pi].to)
		return t.reload(m), true
	}
	if t.zoom {
		k := tea.KeyMsg{Type: tea.KeyDown}
		if up {
			k = tea.KeyMsg{Type: tea.KeyUp}
		}
		t.legend.key(k)
		t.hl = true
		return nil, true
	}
	if up {
		t.scroll = max(0, t.scroll-3)
	} else {
		t.scroll = min(max(0, t.contentH-t.bodyH), t.scroll+3)
	}
	t.keepFocusVisible()
	return nil, true
}

// plotAt is the panel and the fraction of its time axis under a hit on a plot.
func (t *metricsTab) plotAt(h hit) (int, float64, bool) {
	f := strings.Split(h.id, ":")
	if f[0] != "mplot" || len(f) != 3 {
		return 0, 0, false
	}
	pi, _ := strconv.Atoi(f[1])
	w, _ := strconv.Atoi(f[2])
	if w < 2 {
		return 0, 0, false
	}
	return pi, float64(h.x) / float64(w-1), true
}

// drag across a plot selects a time range and zooms every panel to it, as in Grafana.
func (t *metricsTab) drag(m *model, h hit, phase dragPhase) bool {
	pi, frac, ok := t.plotAt(h)
	if !ok || pi >= len(t.data) {
		return false
	}
	if t.win.drag(h.id, frac, phase, t.data[pi].from, t.data[pi].to) {
		t.cursor = -1
		m.setStatus("zoomed to "+t.win.label()+" · Z or ⊖ zooms out", false)
		t.dragged = t.reload(m)
	}
	return true
}

func (t *metricsTab) dragCmd() tea.Cmd {
	c := t.dragged
	t.dragged = nil
	return c
}

// keepFocusVisible moves the focus onto the screen after a scroll, so rendering does not scroll back.
func (t *metricsTab) keepFocusVisible() {
	for _, p := range t.placed {
		if p.pi == t.focus && p.y >= t.scroll && p.y+p.h <= t.scroll+t.bodyH {
			return
		}
	}
	for _, p := range t.placed {
		if p.y >= t.scroll && p.y+p.h <= t.scroll+t.bodyH {
			t.focus = p.pi
			return
		}
	}
}

// ---- view mode ----

func (t *metricsTab) viewKey(m *model, k tea.KeyMsg) tea.Cmd {
	_, ps := t.items(m)
	if t.focus >= len(ps) {
		t.zoom = false
		return nil
	}
	ls := t.panelLines(t.focus, ps[t.focus])
	st := t.state(m, t.focus)
	sel := func() (string, bool) {
		r, ok := t.legend.current()
		return r.id, ok
	}
	if t.legend.key(k) {
		t.hl = true
		return nil
	}
	switch k.String() {
	case "esc":
		switch {
		case t.hl:
			t.hl = false
		case t.cursor >= 0:
			t.cursor = -1
		default:
			t.zoom = false
		}
	case "v", "z":
		t.zoom = false
	case " ":
		if n, ok := sel(); ok {
			st.hidden[n] = !st.hidden[n]
		}
	case "enter":
		if n, ok := sel(); ok {
			st.isolate(ls, n)
		}
	case "a":
		st.hidden, st.filter = map[string]bool{}, nil
	case "/":
		cur := ""
		if st.filter != nil {
			cur = st.filter.String()
		}
		m.ask("show series matching (regex, empty: all)", cur, func(v string) tea.Cmd {
			if v == "" {
				st.filter = nil
				return nil
			}
			re, err := regexp.Compile(v)
			if err != nil {
				m.setStatus("filter: "+err.Error(), true)
				return nil
			}
			st.filter = re
			return nil
		})
	case "left", "h":
		if t.cursor < 0 {
			t.cursor = 1
		}
		t.cursor = math.Max(0, t.cursor-0.01)
	case "right", "l":
		if t.cursor < 0 {
			t.cursor = 0
		}
		t.cursor = math.Min(1, t.cursor+0.01)
	case "s":
		t.stack[t.key(m, t.focus)] = !t.stack[t.key(m, t.focus)]
	case "y":
		t.copyQuery(m)
	case "t":
		t.win.preset(t.win.rng + 1)
		return t.reload(m)
	case "Z", "ctrl+z":
		if t.win.zoomOut() {
			return t.reload(m)
		}
	case ",", ".":
		t.win.shift(map[string]int{",": -1, ".": 1}[k.String()])
		return t.reload(m)
	case "r":
		return t.fetchAll(m)
	}
	return nil
}

// ---- rendering ----

func (t *metricsTab) header(m *model, w int) string {
	names := t.dashNames(m)
	rangeLab := t.win.label() + " ▾"
	refLab := refreshText(refreshes[t.every]) + " ▾"
	nav := sKey.Render("‹") + " " + sKey.Render("⊖") + " " + sKey.Render("›")
	right := " " + nav + sDim.Render(" ⏱ ") + sAccent.Render(rangeLab) + sDim.Render("  ⟳ ") + sAccent.Render(refLab) + " "
	rw := lipgloss.Width(right)
	m.zone("mshift:-1", w-rw+1, 0, 1, 1)
	m.zone("mzoomout", w-rw+3, 0, 1, 1)
	m.zone("mshift:1", w-rw+5, 0, 1, 1)
	m.zone("mrange", w-rw+6, 0, 3+lipgloss.Width(rangeLab), 1)
	m.zone("mrefresh", w-1-lipgloss.Width(refLab), 0, lipgloss.Width(refLab), 1)
	m.zone("mdash-more", 0, 0, 3, 1)
	src := ""
	if t.source != "" {
		src = sDim.Render(" from ") + sAccent.Render(t.source)
	}
	line1 := sAccent.Render(" ☰ ") + truncate(m.strip("mdash", 3, 0, names, t.dashIndex(m)), max(0, w-rw-3-lipgloss.Width(src))) + src
	line1 += strings.Repeat(" ", max(0, w-lipgloss.Width(line1)-rw)) + right

	// variables, Grafana's second line
	name, d := t.dashName(m), t.dashboard(m)
	var line2 strings.Builder
	x := 1
	line2.WriteString(" ")
	for _, n := range d.VarOrder {
		lab := sDim.Render("$"+n+" ") + sTitle.Render(varText(t.vars[name][n])) + sDim.Render(" ▾  ")
		m.zone("mvar:"+n, x, 1, lipgloss.Width(lab), 1)
		x += lipgloss.Width(lab)
		line2.WriteString(lab)
	}
	if t.varsBusy == name {
		line2.WriteString(sDim.Render("loading variables… "))
	}
	if t.varErr != "" {
		line2.WriteString(sRed.Render(t.varErr) + " ")
	}
	if d.Help != "" {
		line2.WriteString(sDim.Render(d.Help))
	}
	return line1 + "\n" + truncate(line2.String(), w)
}

func (t *metricsTab) view(m *model, w, h int) string {
	t.init()
	head := t.header(m, w)
	h -= 2
	items, ps := t.items(m)
	if len(ps) == 0 {
		return head + "\n" + lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
			sDim.Render("no dashboards in rig.yaml — press a to chart a query\n\ndashboards:\n  main:\n    - { title: requests/s, query: 'sum(rate(http_requests_total[1m]))', unit: /s }"))
	}
	t.focus = min(t.focus, len(ps)-1)
	if t.zoom {
		return head + "\n" + t.viewPanel(m, ps[t.focus], w, h)
	}
	return head + "\n" + t.grid(m, items, ps, w, h)
}

// width is a panel's share of 24 columns: its own, else by kind; narrow terminals widen panels.
func width(p spec.Panel, w int) int {
	u := p.Width
	if u <= 0 {
		u = 12
		if p.Kind == "stat" || p.Kind == "gauge" {
			u = 6
		}
	}
	switch {
	case w < 70:
		u = 24
	case w < 120:
		u = min(24, u*2)
	}
	return min(24, u)
}

func height(p spec.Panel) int {
	if p.Height > 0 {
		return max(4, p.Height)
	}
	switch p.Kind {
	case "stat", "gauge":
		return 7
	}
	return 12
}

type lineOut struct {
	row    string
	panels []placed
	h      int
}

// grid lays panels out Grafana style: rows of panels filling 24 columns, row headers that fold.
func (t *metricsTab) grid(m *model, items []mitem, ps []spec.Panel, w, h int) string {
	dash := t.dashName(m)
	var out []lineOut
	var cur lineOut
	units := 0
	folded := false
	flush := func() {
		if len(cur.panels) > 0 {
			out = append(out, cur)
		}
		cur, units = lineOut{}, 0
	}
	for _, it := range items {
		if it.pi < 0 {
			flush()
			folded = t.collapsed[dash+"/"+it.row]
			out = append(out, lineOut{row: it.row, h: 1})
			continue
		}
		if folded {
			continue
		}
		p := ps[it.pi]
		u := width(p, w)
		if units+u > 24 {
			flush()
		}
		cur.panels = append(cur.panels, placed{pi: it.pi, x: units, w: u})
		cur.h = max(cur.h, height(p))
		units += u
	}
	flush()
	// a dashboard shorter than the screen stretches its panels to fill it
	total, panelLines := 0, 0
	for _, l := range out {
		total += l.h
		if l.row == "" {
			panelLines++
		}
	}
	if extra := h - total; extra > 0 && panelLines > 0 {
		each := extra / panelLines
		for i := range out {
			if out[i].row == "" {
				out[i].h += min(each, out[i].h)
			}
		}
	}

	// a line of panels always spans the screen: their columns scale to the line's total
	t.placed = t.placed[:0]
	y := 0
	for i := range out {
		l := &out[i]
		units := 0
		for _, p := range l.panels {
			units = max(units, p.x+p.w)
		}
		for j := range l.panels {
			p := &l.panels[j]
			x0, x1 := p.x*w/units, (p.x+p.w)*w/units
			p.x, p.w, p.y, p.h = x0, x1-x0, y, l.h
			t.placed = append(t.placed, *p)
		}
		y += l.h
	}
	t.contentH, t.bodyH = y, h
	for _, p := range t.placed {
		if p.pi == t.focus {
			if p.y < t.scroll {
				t.scroll = p.y
			} else if p.y+p.h > t.scroll+h {
				t.scroll = p.y + p.h - h
			}
		}
	}
	t.scroll = max(0, min(t.scroll, y-h))

	const top = 2 // the header lines above the content
	var rows []string
	y = 0
	for _, l := range out {
		if l.row != "" {
			arrow := "▾ "
			if t.collapsed[dash+"/"+l.row] {
				arrow = "▸ "
			}
			visibleZone(m, "mrow:"+l.row, 0, y, w, 1, top-t.scroll, top, h)
			rows = append(rows, sAccent.Render(arrow+l.row)+" "+sDim.Render(strings.Repeat("─", max(0, w-lipgloss.Width(arrow+l.row)-2))))
			y++
			continue
		}
		var cells []string
		for _, p := range l.panels {
			cells = append(cells, t.renderPanel(m, ps[p.pi], p, top-t.scroll, h, p.pi == t.focus))
		}
		rows = append(rows, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, cells...), "\n")...)
		y += l.h
	}
	end := min(len(rows), t.scroll+h)
	if t.scroll >= end {
		return ""
	}
	return strings.Join(rows[t.scroll:end], "\n")
}

// visibleZone registers a zone of content row y (shifted by dy onto the body) where it is on screen,
// the screen being body rows top..top+h.
func visibleZone(m *model, id string, x, y, w, zh, dy, top, h int) {
	y0, y1 := max(y+dy, top), min(y+dy+zh, top+h)
	if y1 > y0 {
		m.zone(id, x, y0, w, y1-y0)
	}
}

// renderPanel draws one panel at its place and registers its zones; dy maps content rows to body rows.
func (t *metricsTab) renderPanel(m *model, p spec.Panel, pl placed, dy, bodyH int, focused bool) string {
	title := panelTitle(p)
	var d panelData
	if pl.pi < len(t.data) {
		d = t.data[pl.pi]
	}
	iw, ih := pl.w-2, pl.h-2
	visibleZone(m, fmt.Sprintf("mpanel:%d", pl.pi), pl.x, pl.y, pl.w, pl.h, dy, 2, bodyH)
	var body string
	switch {
	case d.err != nil:
		body = sRed.Render(wrap(d.err.Error(), iw))
	case d.series == nil:
		body = sDim.Render("loading…")
	default:
		ls := seriesLines(p, d)
		st := t.state(m, pl.pi)
		hidden := st.hiddenSet(ls)
		if n := len(hidden); n > 0 {
			title += fmt.Sprintf(" · %d/%d", len(ls)-n, len(ls))
		}
		at, hasAt := t.cursorTime(d)
		switch p.Kind {
		case "stat":
			body = statBody(p, ls, hidden, at, hasAt, iw, ih)
		case "gauge":
			body = gaugeBody(p, ls, hidden, at, hasAt, iw, ih)
		case "bar":
			body = barBody(p, ls, hidden, at, hasAt, iw, ih)
		case "table":
			body = tableBody(p, ls, hidden, at, hasAt, iw, ih)
		default:
			legendH := 1
			if ih >= 14 && len(ls) > 3 {
				legendH = 2
			}
			opts := viz.Opts{From: d.from, To: d.to, Unit: p.Unit, Hidden: hidden, Highlight: -1, Cursor: t.cursor, Stack: p.Stack != t.stack[t.key(m, pl.pi)], Band: t.win.bandOn(fmt.Sprintf("mplot:%d:", pl.pi))}
			plotH := ih - legendH
			if off := viz.PlotOffset(ls, plotH, opts); off > 0 {
				visibleZone(m, fmt.Sprintf("mplot:%d:%d", pl.pi, iw-off), pl.x+1+off, pl.y+1, iw-off, plotH-2, dy, 2, bodyH)
			}
			leg := t.legendLines(m, pl, ls, hidden, at, hasAt, p.Unit, iw, legendH, dy, bodyH, pl.y+1+plotH)
			body = viz.Plot(ls, iw, plotH, opts) + "\n" + leg
		}
	}
	return panel(title, body, pl.w, pl.h, focused)
}

// legendLines list series as "■ name value" in up to n lines, each clickable: only this one, or
// with ctrl/alt/shift, hide or show it.
func (t *metricsTab) legendLines(m *model, pl placed, ls []viz.Line, hidden map[int]bool, at time.Time, hasAt bool, unit string, w, n, dy, bodyH, y int) string {
	var rows []string
	var cur strings.Builder
	x, row := 0, 0
	for i, l := range ls {
		name := l.Name
		if r := []rune(name); len(r) > 30 {
			name = string(r[:29]) + "…"
		}
		txt := name + " " + viz.Human(value(l, at, hasAt), unit)
		item := lipgloss.NewStyle().Foreground(l.Color).Render("■") + " " + sDim.Render(txt)
		if hidden[i] {
			item = sDim.Render("□ ") + lipgloss.NewStyle().Foreground(cPanel).Render(txt)
		}
		iw := lipgloss.Width(item)
		if x > 0 && x+iw+2 > w {
			rows = append(rows, cur.String())
			cur.Reset()
			x, row = 0, row+1
		}
		if row >= n {
			if r := rows[len(rows)-1]; lipgloss.Width(r)+6 < w {
				rows[len(rows)-1] = r + sDim.Render(fmt.Sprintf("  +%d", len(ls)-i))
			}
			break
		}
		if x > 0 {
			cur.WriteString("  ")
			x += 2
		}
		visibleZone(m, fmt.Sprintf("mleg:%d:%d", pl.pi, i), pl.x+1+x, y+row, iw, 1, dy, 2, bodyH)
		cur.WriteString(item)
		x += iw
	}
	if cur.Len() > 0 && len(rows) < n {
		rows = append(rows, cur.String())
	}
	return strings.Join(rows, "\n")
}

func visible(ls []viz.Line, hidden map[int]bool) []int {
	var out []int
	for i := range ls {
		if !hidden[i] {
			out = append(out, i)
		}
	}
	return out
}

func statBody(p spec.Panel, ls []viz.Line, hidden map[int]bool, at time.Time, hasAt bool, w, h int) string {
	vis := visible(ls, hidden)
	if len(vis) == 0 {
		return sDim.Render("no data")
	}
	if len(vis) == 1 || h < 4 {
		l := ls[vis[0]]
		v := value(l, at, hasAt)
		c := thresholdColor(p, v, cGreen)
		text := viz.Human(v, p.Unit)
		var spark []float64
		for _, pt := range l.Points {
			spark = append(spark, pt.V)
		}
		b := big(text, c)
		if lipgloss.Width(b) > w || h < 5 {
			b = lipgloss.NewStyle().Foreground(c).Bold(true).Render(text)
		}
		out := lipgloss.PlaceHorizontal(w, lipgloss.Center, b)
		if rest := h - lipgloss.Height(out); rest > 0 {
			out += strings.Repeat("\n", rest) + viz.Sparkline(spark, w, c)
		}
		return out
	}
	var rows []string
	for _, i := range vis {
		v := value(ls[i], at, hasAt)
		val := lipgloss.NewStyle().Foreground(thresholdColor(p, v, ls[i].Color)).Bold(true).Render(viz.Human(v, p.Unit))
		rows = append(rows, padRight(sDim.Render(ls[i].Name), w-lipgloss.Width(val))+val)
	}
	return strings.Join(rows, "\n")
}

func gaugeRange(p spec.Panel, vals []float64) (lo, hi float64) {
	switch p.Unit {
	case "%", "percent":
		hi = 100
	case "ratio":
		hi = 1
	default:
		for _, v := range vals {
			if !math.IsNaN(v) {
				hi = math.Max(hi, v)
			}
		}
	}
	if p.Min != nil {
		lo = *p.Min
	}
	if p.Max != nil {
		hi = *p.Max
	}
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

func gaugeBody(p spec.Panel, ls []viz.Line, hidden map[int]bool, at time.Time, hasAt bool, w, h int) string {
	vis := visible(ls, hidden)
	if len(vis) == 0 {
		return sDim.Render("no data")
	}
	var vals []float64
	for _, i := range vis {
		vals = append(vals, value(ls[i], at, hasAt))
	}
	lo, hi := gaugeRange(p, vals)
	frac := func(v float64) float64 { return (v - lo) / (hi - lo) }
	if len(vis) == 1 {
		v := vals[0]
		c := thresholdColor(p, v, cGreen)
		b := big(viz.Human(v, p.Unit), c)
		if lipgloss.Width(b) > w || h < 5 {
			b = lipgloss.NewStyle().Foreground(c).Bold(true).Render(viz.Human(v, p.Unit))
		}
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, b) + "\n" + viz.Gauge(frac(v), w) + "\n" +
			sDim.Render(padRight(viz.Human(lo, p.Unit), w/2)+padLeft(viz.Human(hi, p.Unit), w-w/2))
	}
	nameW := 0
	for _, i := range vis {
		nameW = max(nameW, lipgloss.Width(ls[i].Name))
	}
	nameW = min(nameW, w/3)
	var rows []string
	for k, i := range vis {
		rows = append(rows, padRight(sDim.Render(ls[i].Name), nameW)+" "+viz.Meter(frac(vals[k]), max(4, w-nameW-1), viz.Human(vals[k], p.Unit)))
	}
	return strings.Join(rows, "\n")
}

func barBody(p spec.Panel, ls []viz.Line, hidden map[int]bool, at time.Time, hasAt bool, w, h int) string {
	var bars []viz.Bar
	for _, i := range visible(ls, hidden) {
		v := value(ls[i], at, hasAt)
		bars = append(bars, viz.Bar{Name: ls[i].Name, Value: v, Color: thresholdColor(p, v, ls[i].Color)})
	}
	if len(bars) == 0 {
		return sDim.Render("no data")
	}
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Value > bars[j].Value })
	more := ""
	if len(bars) > h && h > 1 {
		more = sDim.Render(fmt.Sprintf("\n+%d more (v shows all)", len(bars)-h+1))
		bars = bars[:h-1]
	}
	top := 0.0
	if p.Max != nil {
		top = *p.Max
	}
	return viz.BarGauge(bars, w, p.Unit, top) + more
}

func tableBody(p spec.Panel, ls []viz.Line, hidden map[int]bool, at time.Time, hasAt bool, w, h int) string {
	type row struct {
		name string
		s    viz.Stats
		v    float64
	}
	var rows []row
	for _, i := range visible(ls, hidden) {
		rows = append(rows, row{ls[i].Name, viz.LineStats(ls[i]), value(ls[i], at, hasAt)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].v > rows[j].v })
	nw := max(10, w-4*10)
	var b strings.Builder
	b.WriteString(sDim.Render(padRight("series", nw) + padLeft("value", 10) + padLeft("min", 10) + padLeft("max", 10) + padLeft("mean", 10)))
	for i, r := range rows {
		if i >= h-1 {
			break
		}
		val := padLeft(viz.Human(r.v, p.Unit), 10)
		if c := thresholdColor(p, r.v, ""); c != "" {
			val = lipgloss.NewStyle().Foreground(c).Render(val)
		}
		b.WriteString("\n" + padRight(r.name, nw) + val + padLeft(viz.Human(r.s.Min, p.Unit), 10) + padLeft(viz.Human(r.s.Max, p.Unit), 10) + padLeft(viz.Human(r.s.Mean, p.Unit), 10))
	}
	return b.String()
}

// viewPanel is one panel over the whole screen (Grafana's view): the chart, the expanded query,
// and a table legend to sort, hide, isolate and highlight series.
func (t *metricsTab) viewPanel(m *model, p spec.Panel, w, h int) string {
	var d panelData
	if t.focus < len(t.data) {
		d = t.data[t.focus]
	}
	title := panelTitle(p)
	st := t.state(m, t.focus)
	stacked := p.Stack != t.stack[t.key(m, t.focus)]

	// the toolbar, on the body's third line
	var tb strings.Builder
	x := 0
	button := func(id, text string, on bool) {
		s := sTabOff.Render(text)
		if on {
			s = sTabOn.Render(text)
		}
		m.zone(id, x, 2, lipgloss.Width(s), 1)
		x += lipgloss.Width(s)
		tb.WriteString(s)
	}
	button("mback", "‹ back", false)
	button("mstack", "stack", stacked)
	button("mall", "all series", false)
	button("mcopy", "copy query", false)
	info := "  " + sTitle.Render(title)
	if st.filter != nil {
		info += sAmber.Render("  /" + st.filter.String() + "/")
	}
	if p.Help != "" {
		info += sDim.Render("  " + p.Help)
	}
	query := strings.ReplaceAll(firstNonEmpty(d.query, p.Query), "\n", "  ·  ")
	head := truncate(tb.String()+info, w) + "\n" + sDim.Render(truncate(" "+query, w)) + "\n"
	h -= 2

	if d.err != nil {
		return head + panel(title, sRed.Render(wrap(d.err.Error(), w-4)), w, h, true)
	}
	if d.series == nil {
		return head + panel(title, sDim.Render("loading…"), w, h, true)
	}
	ls := seriesLines(p, d)
	hidden := st.hiddenSet(ls)
	at, hasAt := t.cursorTime(d)

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
			cells: []string{sw, l.Name, viz.Human(s.Min, p.Unit), viz.Human(s.Max, p.Unit), viz.Human(s.Mean, p.Unit), viz.Human(s.Last, p.Unit), viz.Human(cv, p.Unit)},
			keys:  []any{nil, nil, s.Min, s.Max, s.Mean, s.Last, cv}})
	}
	t.legend.set(rows)
	legendH := min(len(rows)+3, max(5, h*2/5))
	chartH := h - legendH
	highlight := -1
	if r, ok := t.legend.current(); ok && t.hl {
		for i, l := range ls {
			if l.Name == r.id {
				highlight = i
			}
		}
	}

	var chart string
	switch p.Kind {
	case "stat":
		chart = statBody(p, ls, hidden, at, hasAt, w-2, chartH-2)
	case "gauge":
		chart = gaugeBody(p, ls, hidden, at, hasAt, w-2, chartH-2)
	case "bar":
		chart = barBody(p, ls, hidden, at, hasAt, w-2, chartH-2)
	case "table":
		chart = tableBody(p, ls, hidden, at, hasAt, w-2, chartH-2)
	default:
		opts := viz.Opts{From: d.from, To: d.to, Unit: p.Unit, Hidden: hidden, Highlight: highlight, Cursor: t.cursor, Stack: stacked, Band: t.win.bandOn(fmt.Sprintf("mplot:%d:", t.focus))}
		if off := viz.PlotOffset(ls, chartH-2, opts); off > 0 {
			// the chart's panel starts on body line 4, its plot one line and one column in
			m.zone(fmt.Sprintf("mplot:%d:%d", t.focus, w-2-off), 1+off, 5, w-2-off, chartH-4)
		}
		chart = viz.Plot(ls, w-2, chartH-2, opts)
	}
	cursorNote := ""
	if hasAt {
		cursorNote = " · cursor " + at.Format("15:04:05")
	}
	legendTitle := fmt.Sprintf("series %d/%d%s · click selects, double-click only this, ■ or ctrl-click hides", len(ls)-len(hidden), len(ls), cursorNote)
	legendBody := t.legend.view(m, 1, 4+chartH+1, w-2, legendH-2, true)
	return head + panel(title, chart, w, chartH, true) + "\n" + panel(legendTitle, legendBody, w, legendH, false)
}

func panelTitle(p spec.Panel) string {
	if p.Title != "" {
		return p.Title
	}
	if ts := p.Targets(); len(ts) > 0 {
		return ts[0].Query
	}
	return ""
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	var b strings.Builder
	for len(s) > w {
		b.WriteString(s[:w] + "\n")
		s = s[w:]
	}
	b.WriteString(s)
	return b.String()
}
