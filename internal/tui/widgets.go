package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// tokenColor is a theme token: the only colours views and plugins name.
func tokenColor(tok string) color.Color {
	switch tok {
	case "ok", "green":
		return cGreen
	case "warn", "amber":
		return cAmber
	case "err", "error", "red":
		return cRed
	case "info", "purple":
		return cPurple
	case "accent", "focus":
		return cAccent
	case "muted", "dim":
		return cDim
	case "border":
		return cPanel
	case "fg", "":
		return cText
	}
	if n, ok := strings.CutPrefix(tok, "series"); ok {
		if i, err := strconv.Atoi(n); err == nil && i > 0 {
			return viz.Palette[(i-1)%len(viz.Palette)]
		}
	}
	return cText
}

func tokenStyle(tok string) lipgloss.Style { return lipgloss.NewStyle().Foreground(tokenColor(tok)) }

// leaf draws a widget's body into w×h.
func (t *viewTab) leaf(m *model, wn *wnode, x, y, w, h int, focused bool) string {
	n, f := wn.n, wn.frame
	if wn.err != nil && f == nil {
		return sRed.Render(truncate(wn.err.Error(), w*h))
	}
	if n.Bind != nil && n.Bind.Source != "" && f == nil && !needsNoData[view.Kind(n)] {
		return sDim.Render("loading…")
	}
	if !view.Supported(n) {
		if n.Fallback != nil {
			fb := &wnode{n: *n.Fallback, frame: f, err: wn.err, id: wn.id}
			return t.leaf(m, fb, x, y, w, h, focused)
		}
		return sDim.Render("no widget " + n.Widget + " in this rig")
	}
	switch view.Kind(n) {
	case "table":
		return t.tableBody(m, wn, x, y, w, h, focused)
	case "list":
		return listBody(wn, w, h, focused)
	case "kv":
		return kvBody(f, n, w, h)
	case "tree":
		return t.treeBody(m, wn, x, y, w, h)
	case "form":
		return t.formBody(m, wn, w, h, focused)
	case "stat":
		return statWidget(f, n, w, h)
	case "gauge":
		v, lo, hi := lastNumber(f, n), numOr(n.Props["min"], 0), numOr(n.Props["max"], 100)
		frac := (v - lo) / math.Max(hi-lo, 1e-9)
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, levelStyle(n, v).Bold(true).Render(viz.Human(v, view.Prop(n, "unit")))) + "\n" + viz.Gauge(frac, w)
	case "progress":
		v, hi := lastNumber(f, n), numOr(n.Props["max"], 100)
		return viz.Meter(v/math.Max(hi, 1e-9), w, view.Prop(n, "label"))
	case "bar", "bars":
		return barsWidget(f, n, w, h)
	case "sparkline":
		ls := frameLines(f, n)
		var vals []float64
		if len(ls) > 0 {
			for _, p := range ls[0].Points {
				vals = append(vals, p.V)
			}
		}
		return viz.Sparkline(vals, w, tokenColor(cmp.Or(view.Prop(n, "color"), "accent")))
	case "chart":
		return t.chartBody(m, wn, x, y, w, h)
	case "heatmap":
		return heatmapWidget(f, n, w, h)
	case "text":
		return wrapText(textOf(t, n, f), w, tokenStyle(view.Prop(n, "color")))
	case "markdown":
		lines := strings.Split(renderMarkdown(textOf(t, n, f), w), "\n")
		wn.st = min(scrollOf(wn), max(0, len(lines)-h))
		return strings.Join(lines[scrollOf(wn):], "\n")
	case "code":
		return codeBody(textOf(t, n, f), w, h, scrollOf(wn))
	case "diff":
		return diffBody(textOf(t, n, f), w, h, scrollOf(wn))
	case "logs":
		return t.logsBody(m, wn, x, y, w, h)
	case "waterfall":
		return waterfallWidget(f, w, h)
	case "graph":
		return graphWidget(wn, w, h)
	case "timeline":
		return timelineWidget(f, n, w, h, scrollOf(wn))
	case "calendar":
		return calendarWidget(f, n, w, time.Now())
	case "board":
		if wn.filter == "" && !(focused && t.filtering) {
			return boardWidget(wn, w, h, focused)
		}
		bar := sAccent.Render("/ ") + wn.filter
		if focused && t.filtering {
			bar += "▏"
		}
		return bar + sDim.Render("  esc clears") + "\n" + boardWidget(wn, w, h-1, focused)
	case "terminal":
		return t.terminalBody(m, wn, x, y, w, h, focused)
	case "canvas":
		return t.canvasBody(m, wn, w, h)
	}
	return ""
}

// needsNoData are widgets that draw without a frame.
var needsNoData = map[string]bool{"terminal": true, "text": true, "markdown": true, "code": true, "diff": true, "form": true, "canvas": true, "logs": true}

func (t *viewTab) widgetKey(m *model, wn *wnode, k tea.KeyPressMsg) tea.Cmd {
	switch st := wn.st.(type) {
	case *grid:
		switch k.String() {
		case "enter":
			return t.openRow(m, wn)
		case "y":
			if r := t.currentRow(wn); r != nil {
				b, _ := json.Marshal(r)
				copyText(string(b))
				m.setStatus("copied the row", false)
			}
			return nil
		case "Y":
			copyText(frameTSV(wn.frame))
			m.setStatus(fmt.Sprintf("copied %d rows", frames.Len(wn.frame)), false)
			return nil
		}
		st.key(k)
	case *boardState:
		switch {
		case st.key(k.String()):
		case k.String() == "enter" && wn.preview != nil:
			wn.previewOff = !wn.previewOff
		case k.String() == "enter" && st.cur >= 0:
			if ref := frames.RowRef(wn.frame, st.cur); ref != "" {
				return t.openRef(m, ref)
			}
		}
	case *jsonTree:
		st.key(k)
	case *chartView:
		st.key(m, k)
	case *logView:
		st.key(m, k, true)
	case *terminal:
		switch k.String() {
		case "enter":
			st.input = true
		case "R":
			st.close()
			wn.st = nil
		default:
			st.scrollKey(k.String())
		}
	case *formState:
		return t.formKey(m, wn, st, k)
	case int:
		switch k.String() {
		case "up", "k":
			wn.st = max(0, st-1)
		case "down", "j":
			wn.st = st + 1
		case "pgup":
			wn.st = max(0, st-wn.h)
		case "pgdown":
			wn.st = st + wn.h
		}
	case *cursorState:
		switch k.String() {
		case "up", "k":
			st.sel = max(0, st.sel-1)
		case "down", "j":
			st.sel++
		case "enter":
			if st.ref != "" {
				return t.openRef(m, st.ref)
			}
		}
	case nil:
		if scrolls[view.Kind(wn.n)] {
			wn.st = 0
			return t.widgetKey(m, wn, k)
		}
	}
	return nil
}

// scrolls are the widgets ↑↓ scroll.
var scrolls = map[string]bool{"code": true, "diff": true, "timeline": true, "markdown": true, "graph": true}

func scrollOf(wn *wnode) int {
	s, _ := wn.st.(int)
	return s
}

func (t *viewTab) widgetClick(m *model, wn *wnode, h hit) tea.Cmd {
	if term, ok := wn.st.(*terminal); ok {
		term.input = true
	}
	return nil
}

// openRow opens the selected row's ref: the column ref: rule, else the frame's own.
func (t *viewTab) openRow(m *model, wn *wnode) tea.Cmd {
	row := t.currentRow(wn)
	if row == nil {
		return nil
	}
	cols, _ := wn.n.Props["columns"].(map[string]any)
	for _, c := range cols {
		if cm, ok := c.(map[string]any); ok && cm["ref"] != nil {
			return t.openRef(m, fmt.Sprint(expandRow(strings.NewReplacer("{", "${row.", "}", "}").Replace(fmt.Sprint(cm["ref"])), row)))
		}
	}
	if g, ok := wn.st.(*grid); ok {
		if r, ok := g.current(); ok {
			if i, err := strconv.Atoi(r.id); err == nil {
				if ref := frames.RowRef(wn.frame, i); ref != "" {
					return t.openRef(m, ref)
				}
			}
		}
	}
	if u, _ := row["url"].(string); strings.Contains(u, "://") {
		title, _ := row["title"].(string)
		return m.openWith(strings.TrimLeft(title, "│├└─ "), u)
	}
	return nil
}

// openRef follows a ref: svc:// opens the service, anything else resolves through the Engine.
func (t *viewTab) openRef(m *model, ref string) tea.Cmd {
	if name, ok := strings.CutPrefix(ref, "svc://"); ok {
		for i, tb := range m.tabs {
			if s, ok := tb.(*servicesTab); ok {
				m.jump(i)
				s.focusService(m, name)
				return nil
			}
		}
	}
	e := m.eng()
	return func() tea.Msg {
		r, err := e.Resolve(context.Background(), ref)
		if err != nil {
			return statusMsg{text: ref + ": " + err.Error(), err: true}
		}
		if r.URL != "" {
			return m.openWith(r.Title, r.URL)()
		}
		return statusMsg{text: cmp.Or(r.Title, string(r.Ref))}
	}
}

func frameTSV(f *protocol.Frame) string {
	if f == nil {
		return ""
	}
	var b strings.Builder
	for i, fl := range f.Fields {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(fl.Name)
	}
	for r := range frames.Len(f) {
		b.WriteByte('\n')
		for i, fl := range f.Fields {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(frames.Format(fl, f.Columns[i][r]))
		}
	}
	return b.String()
}

// tableBody is the grid over the frame, with column rules: columns: { status: { color: { FAILED: err } } }.
func (t *viewTab) tableBody(m *model, wn *wnode, x, y, w, h int, focused bool) string {
	f := wn.frame
	if f == nil {
		return sDim.Render("no rows")
	}
	g, _ := wn.st.(*grid)
	rules, _ := wn.n.Props["columns"].(map[string]any)
	var shown []int
	for j, fl := range f.Fields {
		if r, ok := rules[fl.Name].(map[string]any); !ok || r["hide"] != true {
			shown = append(shown, j)
		}
	}
	if g == nil || len(g.cols) != len(shown) {
		var cols []gcol
		for _, j := range shown {
			fl := f.Fields[j]
			c := col(strings.ToUpper(fl.Name), 0)
			if fl.Type == protocol.TypeNumber || fl.Type == protocol.TypeInt {
				c = rcol(strings.ToUpper(fl.Name), 0)
			}
			if r, ok := rules[fl.Name].(map[string]any); ok {
				if wd, err := strconv.Atoi(fmt.Sprint(r["width"])); err == nil {
					c.width = wd
				}
			}
			cols = append(cols, c)
		}
		g = newGrid(fmt.Sprintf("vg%d:", wn.id), cols...)
		g.sortBy = -1
		wn.st = g
	}
	rows := make([]grow, 0, frames.Len(f))
	var match func([]string) bool
	if wn.filter != "" {
		names := make([]string, len(shown))
		for c, j := range shown {
			names[c] = f.Fields[j].Name
		}
		match = rowMatcher(wn.filter, names)
	}
	var fo *tableFold
	if pf := foldField(wn); pf != "" && wn.filter == "" {
		fo = wn.index(f, pf)
	} else {
		wn.fold = nil
		if pf != "" {
			wn.index(f, pf)
		}
	}
	for i := range frames.Len(f) {
		depth := 0
		if fo != nil {
			var hidden bool
			depth, hidden = fo.depthOf(fo.byRow[strconv.Itoa(i)])
			if hidden {
				continue
			}
		}
		cells := make([]string, len(shown))
		for c, j := range shown {
			fl := f.Fields[j]
			v := f.Columns[j][i]
			s := frames.Format(fl, v)
			if r, ok := rules[fl.Name].(map[string]any); ok {
				if cm, ok := r["color"].(map[string]any); ok {
					if tok, ok := cm[s]; ok {
						s = tokenStyle(fmt.Sprint(tok)).Render(s)
					}
				}
			}
			cells[c] = s
		}
		if mf := markField(wn); mf != "" && len(cells) > 0 {
			if wn.marks[rowMark(wn, i, mf)] {
				cells[0] = sAccent.Render("● ") + cells[0]
			} else {
				cells[0] = "  " + cells[0]
			}
		}
		if fo != nil && len(cells) > 0 {
			id, mark := fo.byRow[strconv.Itoa(i)], "  "
			if fo.kids[id] {
				mark = map[bool]string{true: "▸ ", false: "▾ "}[fo.collapsed[id]]
			}
			cells[0] = strings.Repeat("  ", depth) + mark + cells[0]
		}
		keys := make([]any, len(shown))
		for c, j := range shown {
			if x, ok := toFloat(f.Columns[j][i]); ok {
				keys[c] = x
			}
		}
		if match != nil {
			plain := make([]string, len(cells))
			for c := range cells {
				plain[c] = stripANSI(cells[c])
			}
			if !match(plain) {
				continue
			}
		}
		rows = append(rows, grow{id: strconv.Itoa(i), cells: cells, keys: keys})
	}
	g.set(rows)
	wn.matches = len(rows)
	if wn.filter == "" && !(focused && t.filtering) {
		return g.view(m, x, y, w, h, focused)
	}
	bar := sAccent.Render("/ ") + wn.filter
	if focused && t.filtering {
		bar += "▏"
	}
	if wn.filter == "" {
		bar += sDim.Render("words · col=value col!=value col~text · *glob* · @?plain words")
	}
	bar += sDim.Render(fmt.Sprintf("  %d of %d  esc clears", len(rows), frames.Len(f)))
	if len(rows) == 0 && searchable(wn) {
		bar += sAmber.Render("  enter searches everything")
	}
	return bar + "\n" + g.view(m, x, y+1, w, h-1, focused)
}

// cursorState is a list or board's selected item.
type cursorState struct {
	sel int
	ref string
}

func listBody(wn *wnode, w, h int, focused bool) string {
	f := wn.frame
	st, _ := wn.st.(*cursorState)
	if st == nil {
		st = &cursorState{}
		wn.st = st
	}
	n := frames.Len(f)
	if n == 0 {
		return sDim.Render("empty")
	}
	st.sel = min(st.sel, n-1)
	off := scroll(st.sel, 0, h, n)
	var out []string
	for i := off; i < min(n, off+h); i++ {
		line := padRight(frames.Format(f.Fields[0], f.Columns[0][i]), w)
		if i == st.sel && focused {
			line = sCursor.Render(line)
		}
		out = append(out, line)
	}
	st.ref = frames.RowRef(f, st.sel)
	return strings.Join(out, "\n")
}

// kvBody shows a key/value frame (fields key, value), else the first row's fields.
func kvBody(f *protocol.Frame, n protocol.Node, w, h int) string {
	var keys, vals []string
	if ks, vs := frames.Column(f, "key"), frames.Column(f, "value"); ks != nil && vs != nil {
		for i := range ks {
			keys, vals = append(keys, fmt.Sprint(ks[i])), append(vals, fmt.Sprint(vs[i]))
		}
	} else if frames.Len(f) > 0 {
		for j, fl := range f.Fields {
			keys, vals = append(keys, fl.Name), append(vals, frames.Format(fl, f.Columns[j][0]))
		}
	}
	kw := 0
	for _, k := range keys {
		kw = max(kw, lipgloss.Width(k))
	}
	kw = min(kw, w/2)
	var out []string
	for i := range keys {
		out = append(out, sDim.Render(padRight(keys[i], kw))+"  "+truncate(vals[i], w-kw-2))
	}
	return strings.Join(out[:min(len(out), h)], "\n")
}

func (t *viewTab) treeBody(m *model, wn *wnode, x, y, w, h int) string {
	tr, _ := wn.st.(*jsonTree)
	var data any = frames.Rows(wn.frame)
	if wn.frame == nil {
		data = wn.n.Props["data"]
	} else if frames.Len(wn.frame) == 1 {
		data = frames.Row(wn.frame, 0)
	}
	raw, _ := json.Marshal(data)
	if tr == nil || string(tr.root.bytes()) != string(raw) {
		nt, err := newJSONTree(fmt.Sprintf("vt%d:", wn.id), raw)
		if err != nil {
			return sRed.Render(err.Error())
		}
		if tr != nil {
			nt.selectPath(tr.current().path())
		}
		tr = nt
		wn.st = tr
	}
	return tr.view(m, x, y, w, h)
}

func (t *viewTab) chartBody(m *model, wn *wnode, x, y, w, h int) string {
	c, _ := wn.st.(*chartView)
	if c == nil {
		c = newChartView(fmt.Sprintf("vc%d:", wn.id))
		c.unit = view.Prop(wn.n, "unit")
		wn.st = c
	}
	c.lines = frameLines(wn.frame, wn.n)
	return c.view(m, x, y, w, h)
}

// frameLines reads series from a frame: long form (time, series, value), or a time column and one
// series per number column.
func frameLines(f *protocol.Frame, n protocol.Node) []viz.Line {
	if f == nil {
		return nil
	}
	tcol := -1
	for i, fl := range f.Fields {
		if fl.Type == protocol.TypeTime || fl.Name == "time" || fl.Name == "ts" {
			tcol = i
			break
		}
	}
	at := func(r int) time.Time {
		if tcol < 0 {
			return time.Unix(int64(r), 0)
		}
		return toTime(f.Columns[tcol][r])
	}
	var out []viz.Line
	if names, vals := frames.Column(f, cmp.Or(view.Prop(n, "series"), "series")), frames.Column(f, cmp.Or(view.Prop(n, "value"), "value")); names != nil && vals != nil {
		idx := map[string]int{}
		for r := range names {
			name := fmt.Sprint(names[r])
			i, ok := idx[name]
			if !ok {
				i = len(out)
				idx[name] = i
				out = append(out, viz.Line{Name: name, Color: viz.Palette[i%len(viz.Palette)]})
			}
			if v, ok := toFloat(vals[r]); ok {
				out[i].Points = append(out[i].Points, core.Point{T: at(r), V: v})
			}
		}
		return out
	}
	for i, fl := range f.Fields {
		if i == tcol || (fl.Type != protocol.TypeNumber && fl.Type != protocol.TypeInt) {
			continue
		}
		l := viz.Line{Name: fl.Name, Color: viz.Palette[len(out)%len(viz.Palette)]}
		for r, v := range f.Columns[i] {
			if x, ok := toFloat(v); ok {
				l.Points = append(l.Points, core.Point{T: at(r), V: x})
			}
		}
		out = append(out, l)
	}
	return out
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

func toTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		if tm, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return tm
		}
	}
	if f, ok := toFloat(v); ok {
		if f > 1e12 {
			return time.UnixMilli(int64(f))
		}
		return time.Unix(int64(f), int64((f-math.Floor(f))*1e9))
	}
	return time.Time{}
}

func numOr(v any, def float64) float64 {
	if f, ok := toFloat(v); ok {
		return f
	}
	return def
}

// lastNumber is the frame's newest value: the value: column, else the last number column.
func lastNumber(f *protocol.Frame, n protocol.Node) float64 {
	// value: 42 with no source is a static number; value: latency names a column
	if v, ok := toFloat(n.Props["value"]); ok && f == nil {
		return v
	}
	if ls := frameLines(f, n); len(ls) > 0 && len(ls[0].Points) > 0 {
		return ls[0].Points[len(ls[0].Points)-1].V
	}
	if frames.Len(f) > 0 {
		for j := len(f.Fields) - 1; j >= 0; j-- {
			if v, ok := toFloat(f.Columns[j][frames.Len(f)-1]); ok {
				return v
			}
		}
	}
	return math.NaN()
}

// levelStyle colours v by thresholds: { warn: 10, err: 100 } (below: true flips them).
func levelStyle(n protocol.Node, v float64) lipgloss.Style {
	th, _ := n.Props["thresholds"].(map[string]any)
	below, _ := th["below"].(bool)
	past := func(k string) bool {
		x, ok := toFloat(th[k])
		return ok && (!below && v >= x || below && v <= x)
	}
	switch {
	case past("err"):
		return lipgloss.NewStyle().Foreground(cRed)
	case past("warn"):
		return lipgloss.NewStyle().Foreground(cAmber)
	}
	return lipgloss.NewStyle().Foreground(tokenColor(cmp.Or(view.Prop(n, "color"), "ok")))
}

func statWidget(f *protocol.Frame, n protocol.Node, w, h int) string {
	v := lastNumber(f, n)
	if math.IsNaN(v) {
		return sDim.Render("no data")
	}
	text := viz.Human(v, view.Prop(n, "unit"))
	st := levelStyle(n, v)
	b := big(text, st.GetForeground())
	if lipgloss.Width(b) > w || h < 5 {
		b = st.Bold(true).Render(text)
	}
	out := lipgloss.PlaceHorizontal(w, lipgloss.Center, b)
	if rest := h - lipgloss.Height(out); rest > 0 {
		if ls := frameLines(f, n); len(ls) > 0 && len(ls[0].Points) > 1 {
			var vals []float64
			for _, p := range ls[0].Points {
				vals = append(vals, p.V)
			}
			out += strings.Repeat("\n", rest) + viz.Sparkline(vals, w, st.GetForeground())
		}
	}
	return out
}

// barsWidget is one bar per row: label: (first text column) and value: (last number column).
func barsWidget(f *protocol.Frame, n protocol.Node, w, h int) string {
	label, value := view.Prop(n, "label"), view.Prop(n, "value")
	var items []viz.Bar
	for _, r := range frames.Rows(f) {
		var name string
		var v float64
		var ok bool
		for _, fl := range f.Fields {
			if label == "" && name == "" && fl.Type != protocol.TypeNumber && fl.Type != protocol.TypeInt {
				name = fmt.Sprint(r[fl.Name])
			}
			if x, isNum := toFloat(r[fl.Name]); isNum && (value == "" || value == fl.Name) {
				v, ok = x, true
			}
		}
		if label != "" {
			name = fmt.Sprint(r[label])
		}
		if ok {
			items = append(items, viz.Bar{Name: name, Value: v, Color: levelStyle(n, v).GetForeground()})
		}
	}
	if view.Kind(n) == "bars" {
		hi := numOr(n.Props["max"], 100)
		var out []string
		for _, it := range items[:min(len(items), h)] {
			out = append(out, viz.Meter(it.Value/math.Max(hi, 1e-9), w, it.Name))
		}
		return strings.Join(out, "\n")
	}
	if len(items) > h {
		items = items[:h]
	}
	return viz.BarGauge(items, w, view.Prop(n, "unit"), numOr(n.Props["max"], 0))
}

// heatmapWidget: a row per series, time bucketed across the width, shaded by value.
func heatmapWidget(f *protocol.Frame, n protocol.Node, w, h int) string {
	ls := frameLines(f, n)
	if len(ls) == 0 {
		return sDim.Render("no data")
	}
	nameW := 0
	for _, l := range ls {
		nameW = max(nameW, lipgloss.Width(l.Name))
	}
	nameW = min(nameW, w/3)
	cells := max(1, w-nameW-1)
	from, to := viz.Span(ls)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, l := range ls {
		for _, p := range l.Points {
			lo, hi = math.Min(lo, p.V), math.Max(hi, p.V)
		}
	}
	shades := []rune(" ░▒▓█")
	scale := []color.Color{cGreen, cGreen, cAmber, cRed, cRed}
	var out []string
	for _, l := range ls[:min(len(ls), h)] {
		buckets := make([]float64, cells)
		seen := make([]bool, cells)
		for _, p := range l.Points {
			i := 0
			if span := to.Sub(from); span > 0 {
				i = min(cells-1, int(float64(cells)*float64(p.T.Sub(from))/float64(span)))
			}
			buckets[i], seen[i] = math.Max(buckets[i], p.V), true
		}
		var b strings.Builder
		for i, v := range buckets {
			if !seen[i] {
				b.WriteRune(' ')
				continue
			}
			k := 4
			if hi > lo {
				k = int(4 * (v - lo) / (hi - lo))
			}
			b.WriteString(lipgloss.NewStyle().Foreground(scale[k]).Render(string(shades[max(1, k)])))
		}
		out = append(out, sDim.Render(padRight(l.Name, nameW))+" "+b.String())
	}
	return strings.Join(out, "\n")
}

// textOf is text: or file: (relative to the view), else the frame's first cell.
func textOf(t *viewTab, n protocol.Node, f *protocol.Frame) string {
	if s := view.Prop(n, "text"); s != "" {
		return s
	}
	if fl := view.Prop(n, "field"); fl != "" {
		if c := frames.Column(f, fl); len(c) > 0 {
			return fmt.Sprint(c[0])
		}
		return ""
	}
	if p := view.Prop(n, "file"); p != "" {
		raw, err := os.ReadFile(filepath.Join(t.dir, p))
		if err != nil {
			return err.Error()
		}
		return string(raw)
	}
	if frames.Len(f) == 0 {
		return ""
	}
	var lines []string
	for r := range frames.Len(f) {
		lines = append(lines, fmt.Sprint(f.Columns[0][r]))
	}
	return strings.Join(lines, "\n")
}

func wrapText(s string, w int, st lipgloss.Style) string {
	return st.Width(w).Render(s)
}

// codeBody numbers lines and dims comments.
// ponytail: comment and string colouring only, a real highlighter (chroma) when someone needs it.
func codeBody(s string, w, h, off int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	nw := len(strconv.Itoa(len(lines)))
	off = max(0, min(off, len(lines)-h))
	var out []string
	for i := off; i < min(len(lines), off+h); i++ {
		l := strings.ReplaceAll(lines[i], "\t", "    ")
		trim := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trim, "//"), strings.HasPrefix(trim, "#"), strings.HasPrefix(trim, "--"):
			l = sDim.Render(l)
		default:
			l = quoted(l)
		}
		out = append(out, sDim.Render(fmt.Sprintf("%*d ", nw, i+1))+truncate(l, w-nw-1))
	}
	return strings.Join(out, "\n")
}

// quoted colours "strings" in a line.
func quoted(l string) string {
	parts := strings.Split(l, `"`)
	if len(parts) < 3 {
		return l
	}
	for i := 1; i < len(parts)-1; i += 2 {
		parts[i] = sGreen.Render(`"` + parts[i] + `"`)
	}
	var b strings.Builder
	for i, p := range parts {
		b.WriteString(p)
		if i%2 == 0 && i > 0 && i == len(parts)-1 && len(parts)%2 == 0 {
			b.WriteString(`"`)
		}
	}
	return b.String()
}

func diffBody(s string, w, h, off int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	off = max(0, min(off, len(lines)-h))
	var out []string
	for _, l := range lines[off:min(len(lines), off+h)] {
		l = truncate(strings.ReplaceAll(l, "\t", "    "), w)
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			l = sTitle.Render(l)
		case strings.HasPrefix(l, "+"):
			l = sGreen.Render(l)
		case strings.HasPrefix(l, "-"):
			l = lipgloss.NewStyle().Foreground(cRed).Render(l)
		case strings.HasPrefix(l, "@@"):
			l = sAccent.Render(l)
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// logsBody shows rows as log lines: time, service and message (or line), parsed as JSON or logfmt.
func (t *viewTab) logsBody(m *model, wn *wnode, x, y, w, h int) string {
	lv, _ := wn.st.(*logView)
	if lv == nil {
		lv = newLogView(fmt.Sprintf("vl%d:", wn.id), keepRows(wn.n), nil)
		wn.st = lv
	}
	f := wn.frame
	if f != nil && frames.Len(f) != len(lv.lines) {
		lv.reset()
		var lines []core.LogLine
		for _, r := range frames.Rows(f) {
			l := core.LogLine{Text: fmt.Sprint(firstNonNil(r["line"], r["message"], r["msg"], r["text"])), Service: fmt.Sprint(cmp.Or(r["service"], any("")))}
			if l.Service == "<nil>" {
				l.Service = ""
			}
			if ts := firstNonNil(r["time"], r["ts"]); ts != nil {
				l.Time = toTime(ts)
			}
			lines = append(lines, l)
		}
		lv.add(lines)
	}
	if len(lv.lines) == 0 {
		if wn.streaming {
			return sDim.Render("waiting for lines…")
		}
		return sDim.Render("no lines")
	}
	return lv.render(m, x, y, w, h, func(l core.LogLine) string {
		head := ""
		if !l.Time.IsZero() {
			head = sDim.Render(l.Time.Local().Format("15:04:05.000")) + " "
		}
		if l.Service != "" {
			head += lipgloss.NewStyle().Foreground(colorFor(l.Service)).Render(l.Service) + " "
		}
		return head + lv.fmt.render(l.Text)
	})
}

// waterfallWidget draws spans: trace_id, id, parent, service, name, start, duration (ms).
func waterfallWidget(f *protocol.Frame, w, h int) string {
	var spans []core.Span
	for _, r := range frames.Rows(f) {
		d, _ := toFloat(r["duration"])
		spans = append(spans, core.Span{TraceID: str(r["trace_id"]), ID: str(r["id"]), Parent: str(r["parent"]), Service: str(r["service"]),
			Name: str(r["name"]), Start: toTime(r["start"]), Duration: time.Duration(d * float64(time.Millisecond))})
	}
	if len(spans) == 0 {
		return sDim.Render("no spans")
	}
	return clip(viz.Waterfall(spans, w), w, h)
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// graphWidget draws edges (from, to) around center: (else the first from).
func graphWidget(wn *wnode, w, h int) string {
	f, n := wn.frame, wn.n
	center := view.Prop(n, "center")
	var in, out []viz.Rel
	rows := frames.Rows(f)
	if center == "" && len(rows) > 0 {
		center = str(rows[0]["from"])
	}
	for _, r := range rows {
		switch {
		case str(r["from"]) == center:
			out = append(out, viz.Rel{Node: str(r["to"]), Label: str(r["label"])})
		case str(r["to"]) == center:
			in = append(in, viz.Rel{Node: str(r["from"]), Label: str(r["label"])})
		}
	}
	lines := strings.Split(viz.RelationGraph(center, in, out), "\n")
	wn.st = min(scrollOf(wn), max(0, len(lines)-h))
	return clip(strings.Join(lines[scrollOf(wn):], "\n"), w, h)
}

// timelineWidget lists events newest first: time, title, and kind (or state) coloured.
func timelineWidget(f *protocol.Frame, n protocol.Node, w, h, off int) string {
	rows := frames.Rows(f)
	tf, title, kind := cmp.Or(view.Prop(n, "time"), "time"), cmp.Or(view.Prop(n, "title"), "title"), cmp.Or(view.Prop(n, "kind"), "kind")
	sort.SliceStable(rows, func(i, j int) bool { return toTime(rows[i][tf]).After(toTime(rows[j][tf])) })
	colors, _ := n.Props["colors"].(map[string]any)
	off = max(0, min(off, len(rows)-h))
	var out []string
	for _, r := range rows[off:min(len(rows), off+h)] {
		dot := sDim.Render("●")
		if c, ok := colors[str(r[kind])]; ok {
			dot = tokenStyle(fmt.Sprint(c)).Render("●")
		}
		when := toTime(r[tf]).Local().Format("01-02 15:04")
		out = append(out, sDim.Render(when)+" "+dot+" "+truncate(str(firstNonNil(r[title], r["name"], r["message"])), w-14))
	}
	if len(out) == 0 {
		return sDim.Render("nothing yet")
	}
	return strings.Join(out, "\n")
}

// calendarWidget is the month of now as a grid, Monday first; days with events show their count.
func calendarWidget(f *protocol.Frame, n protocol.Node, w int, now time.Time) string {
	tf := cmp.Or(view.Prop(n, "time"), "time")
	now = now.Local()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	count := map[int]int{}
	for _, r := range frames.Rows(f) {
		if t := toTime(r[tf]).Local(); t.Year() == now.Year() && t.Month() == now.Month() {
			count[t.Day()]++
		}
	}
	cw := max(4, min(10, w/7))
	cell := func(s string) string { return lipgloss.PlaceHorizontal(cw, lipgloss.Left, s) }
	head := []string{lipgloss.PlaceHorizontal(cw*7, lipgloss.Center, sAccent.Render(now.Format("January 2006")))}
	var days []string
	for _, d := range []string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"} {
		days = append(days, cell(sDim.Render(d)))
	}
	head = append(head, strings.Join(days, ""))
	row := make([]string, (int(first.Weekday())+6)%7)
	for i := range row {
		row[i] = cell("")
	}
	for d := 1; d <= first.AddDate(0, 1, -1).Day(); d++ {
		s := fmt.Sprintf("%2d", d)
		switch {
		case d == now.Day():
			s = lipgloss.NewStyle().Bold(true).Reverse(true).Render(s)
		case count[d] == 0:
			s = sDim.Render(s)
		}
		if count[d] > 0 {
			s += sAccent.Render(fmt.Sprintf("•%d", count[d]))
		}
		if row = append(row, cell(s)); len(row) == 7 {
			head, row = append(head, strings.Join(row, "")), nil
		}
	}
	if len(row) > 0 {
		head = append(head, strings.Join(row, ""))
	}
	return strings.Join(head, "\n")
}

// boardState is a board's cursor: a column, a card in it, and each column's scroll.
type boardState struct {
	col, card int
	off       map[string]int
	// cur is the frame row under the cursor, -1 when the board is empty
	cur int
	// cols are the cards' row indexes per column, as last drawn
	cols [][]int
	// fields are the frame's fields the cards sort by; sortBy indexes them, -1 for frame order
	fields []string
	sortBy int
	desc   bool
	// first is the leftmost column showing when they do not all fit
	first int
}

func (b *boardState) key(k string) bool {
	switch k {
	case "left", "h":
		if b.col == 0 {
			return false
		}
		b.col--
	case "right", "l":
		if b.col >= len(b.cols)-1 {
			return false
		}
		b.col++
	case "up", "k":
		b.card = max(0, b.card-1)
	case "down", "j":
		b.card++
	case "home", "g":
		b.card = 0
	case "end", "G":
		b.card = 1 << 30
	case "ctrl+alt+right", "ctrl+shift+right", ">", "ctrl+alt+left", "ctrl+shift+left", "<":
		if len(b.fields) == 0 {
			return true
		}
		step := 1
		if strings.HasSuffix(k, "left") || k == "<" {
			step = len(b.fields) - 1
		}
		b.sortBy = (max(b.sortBy, 0) + step) % len(b.fields)
	case "ctrl+alt+up", "ctrl+shift+up", "alt+up", "ctrl+alt+down", "ctrl+shift+down", "alt+down":
		b.sortBy, b.desc = max(b.sortBy, 0), strings.HasSuffix(k, "down")
	default:
		return false
	}
	return true
}

// cardKey sorts numbers as numbers and issue keys like PIN-123 by their number.
func cardKey(v any) any {
	if f, ok := v.(float64); ok {
		return f
	}
	s := str(v)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	if i := strings.LastIndexByte(s, '-'); i > 0 {
		if n, err := strconv.Atoi(s[i+1:]); err == nil {
			return fmt.Sprintf("%s-%012d", s[:i], n)
		}
	}
	return s
}

// cardH is the lines a board card takes: its key and meta, then the title over two lines.
const cardH = 3

// boardWidget is Kanban: a column per value of by: (in columns: order), a card per row showing
// card: (else the title field, else the first), led by its key: field when the frame has one.
func boardWidget(wn *wnode, w, h int, focused bool) string {
	n, f := wn.n, wn.frame
	by, card := cmp.Or(view.Prop(n, "by"), "status"), view.Prop(n, "card")
	if card == "" && f != nil {
		card = f.Fields[0].Name
		if _, ok := field(f, "title"); ok {
			card = "title"
		}
	}
	key := cmp.Or(view.Prop(n, "key"), "key")
	var metas []string
	if ms, ok := n.Props["meta"].([]any); ok {
		for _, x := range ms {
			metas = append(metas, fmt.Sprint(x))
		}
	}
	st, _ := wn.st.(*boardState)
	if st == nil {
		st = &boardState{off: map[string]int{}, cur: -1, sortBy: -1}
		wn.st = st
	}
	var order []string
	if cs, ok := n.Props["columns"].([]any); ok {
		for _, c := range cs {
			order = append(order, fmt.Sprint(c))
		}
	}
	cards := map[string][]int{}
	rows := frames.Rows(f)
	for i, r := range rows {
		k := str(r[by])
		if !slices.Contains(order, k) {
			order = append(order, k)
		}
		if wn.filter != "" && !fuzzy(rowText(r), wn.filter) {
			continue
		}
		cards[k] = append(cards[k], i)
	}
	if len(order) == 0 {
		st.cur = -1
		return sDim.Render("no cards")
	}
	st.fields = st.fields[:0]
	for _, fl := range f.Fields {
		st.fields = append(st.fields, fl.Name)
	}
	sortLabel := ""
	if st.sortBy >= 0 && st.sortBy < len(st.fields) {
		sf := st.fields[st.sortBy]
		for _, ids := range cards {
			sort.SliceStable(ids, func(a, b int) bool {
				c := compare(cardKey(rows[ids[a]][sf]), cardKey(rows[ids[b]][sf]))
				if st.desc {
					return c > 0
				}
				return c < 0
			})
		}
		sortLabel = " ↑" + sf
		if st.desc {
			sortLabel = " ↓" + sf
		}
	}
	st.cols = st.cols[:0]
	for _, c := range order {
		st.cols = append(st.cols, cards[c])
	}
	st.col = min(st.col, len(order)-1)
	st.card = max(0, min(st.card, len(cards[order[st.col]])-1))
	st.cur = -1
	// columns narrower than this scroll sideways instead of squeezing cards into a few letters
	fit := max(1, min(len(order), w/24))
	st.first = min(max(st.first, st.col-fit+1), st.col, len(order)-fit)
	cw := w / fit
	var out []string
	for ci := st.first; ci < st.first+fit; ci++ {
		c := order[ci]
		label := c
		if ci == st.first && ci > 0 {
			label = "‹ " + label
		}
		if ci == st.first+fit-1 && ci < len(order)-1 {
			label += " ›"
		}
		head := sTitle.Render(padRight(fmt.Sprintf("%s %s", label, sDim.Render(strconv.Itoa(len(cards[c])))), cw-1))
		if ci == st.col && focused {
			head = sAccent.Render(padRight(fmt.Sprintf("%s %d%s", label, len(cards[c]), sortLabel), cw-1))
		}
		lines := []string{head + " ", sDim.Render(strings.Repeat("─", cw-1)) + " "}
		ids := cards[c]
		vis := max(1, (h-2)/cardH)
		off := st.off[c]
		if ci == st.col {
			off = scroll(st.card, off, vis, len(ids))
			st.off[c] = off
		}
		for k := off; k < min(len(ids), off+vis); k++ {
			i := ids[k]
			title := strings.Join(strings.Fields(str(rows[i][card])), " ")
			head := str(rows[i][key])
			var meta []string
			for _, mf := range metas {
				if v := str(rows[i][mf]); v != "" {
					meta = append(meta, v)
				}
			}
			if len(meta) > 0 {
				head = strings.TrimSpace(head + "  " + strings.Join(meta, " · "))
			}
			body := strings.Split(ansi.Wrap(title, cw-3, ""), "\n")
			if len(body) > 2 {
				body = []string{body[0], ansi.Truncate(body[1], cw-4, "") + "…"}
			}
			for len(body) < 2 {
				body = append(body, "")
			}
			card3 := []string{truncate(head, cw-3), body[0], body[1]}
			for n, l := range card3 {
				line := " " + l
				switch {
				case ci == st.col && k == st.card && focused:
					line = sCursor.Render(padRight(" "+stripANSI(l), cw-1))
				case ci == st.col && k == st.card:
					line = sSelected.Render(padRight(" "+stripANSI(l), cw-1))
				case n == 0:
					line = sDim.Render(padRight(line, cw-1))
				default:
					line = padRight(line, cw-1)
				}
				lines = append(lines, line+" ")
			}
			if ci == st.col && k == st.card {
				st.cur = i
			}
		}
		for len(lines) < h {
			lines = append(lines, strings.Repeat(" ", cw))
		}
		out = append(out, strings.Join(lines[:h], "\n"))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, out...)
}

func rowText(r map[string]any) string {
	var b strings.Builder
	for _, v := range r {
		b.WriteString(fmt.Sprint(v))
		b.WriteByte(' ')
	}
	return b.String()
}

// canvasBody asks the plugin that owns the widget to draw it: widget: <plugin>.<name> via canvas.
func (t *viewTab) canvasBody(m *model, wn *wnode, w, h int) string {
	cv, _ := wn.st.(*canvasState)
	if cv == nil {
		cv = &canvasState{}
		wn.st = cv
	}
	name := view.Prop(wn.n, "plugin")
	if name == "" {
		return sDim.Render("canvas needs plugin: <plugin>.<widget>")
	}
	if !cv.busy && (cv.w != w || cv.h != h || cv.frame != wn.frame) {
		cv.busy, cv.w, cv.h, cv.frame = true, w, h, wn.frame
		e, props := m.eng(), wn.n.Props
		go func() {
			lines, err := e.Host.Widget(context.Background(), name, protocol.WidgetRenderParams{Widget: name, Props: props, Frame: wn.frame, Size: protocol.Size{W: w, H: h}})
			program.Send(canvasMsg{cv: cv, lines: lines, err: err})
		}()
	}
	if cv.err != nil {
		return sRed.Render(cv.err.Error())
	}
	return clip(strings.Join(cv.lines, "\n"), w, h)
}

type canvasState struct {
	w, h  int
	frame *protocol.Frame
	busy  bool
	lines []string
	err   error
}

type canvasMsg struct {
	cv    *canvasState
	lines []string
	err   error
}

func field(f *protocol.Frame, name string) (protocol.Field, bool) {
	for _, fl := range f.Fields {
		if fl.Name == name {
			return fl, true
		}
	}
	return protocol.Field{}, false
}
