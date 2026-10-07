package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

var lookbacks = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// tracesTab searches traces by service, operation, minimum duration and time window, sorts them like
// any grid, and shows the selected one as a waterfall.
type tracesTab struct {
	list     *grid
	found    []core.TraceSummary
	services []string
	service  string
	op       string
	text     string
	min      time.Duration
	back     int
	limit    int
	errsOnly bool
	spans    []core.Span
	spansFor string
	err      string
	auto     bool
	// inSpans moves the selection over the waterfall's spans instead of the trace list
	inSpans bool
	spanSel int
	spanOff int
	wf      viz.WaterfallView
	wfWidth int
}

type tracesMsg struct {
	gen      int
	list     []core.TraceSummary
	services []string
	err      error
}

type spansMsg struct {
	gen   int
	id    string
	spans []core.Span
	err   error
}

func (t *tracesTab) name() string { return "Traces" }
func (t *tracesTab) typing() bool { return false }
func (t *tracesTab) hints() [][2]string {
	if t.inSpans {
		return [][2]string{{"↑↓ wheel", "select span"}, {"y", "copy span"}, {"esc", "back to traces"}}
	}
	return [][2]string{{"enter", "walk spans"}, {"s", "service"}, {"o", "operation"}, {"m", "min duration"}, {"t", "time window"},
		{"/", "text"}, {"e", "errors only"}, {"n", "limit"}, {"R", "refresh"}, {"a", "auto refresh"}, {"ctrl+alt+←→↑↓", "sort"}}
}

func (t *tracesTab) interval() time.Duration {
	if t.auto {
		return 15 * time.Second
	}
	return 0
}

func (t *tracesTab) open(m *model) tea.Cmd {
	t.list = newGrid("traces", col("TIME", 8), rcol("DURATION", 9), col("", 16), rcol("SPANS", 5), col("ROOT", 0), col("SERVICES", 0), col("", 5))
	t.list.sortDefault(0, true)
	t.list.simple = []int{0, 1, 4, 6}
	t.back, t.limit, t.auto = 1, 100, true
	return t.refresh(m)
}

func (t *tracesTab) refresh(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	q := core.TraceQuery{Service: t.service, Operation: t.op, MinDuration: t.min, Lookback: lookbacks[t.back], Limit: t.limit}
	need := t.services == nil
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		tr, _, err := engine.Get[core.Tracing](a, core.KindTracing, "")
		if err != nil {
			return tracesMsg{gen: gen, err: err}
		}
		var svcs []string
		if need {
			svcs, _ = tr.Services(c)
		}
		list, err := tr.Search(c, q)
		return tracesMsg{gen: gen, list: list, services: svcs, err: err}
	}
}

func (t *tracesTab) load(m *model, id string) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		tr, _, err := engine.Get[core.Tracing](a, core.KindTracing, "")
		if err != nil {
			return spansMsg{gen: gen, id: id, err: err}
		}
		spans, err := tr.Trace(ctx, id)
		return spansMsg{gen: gen, id: id, spans: spans, err: err}
	}
}

func (t *tracesTab) rows() []grow {
	maxDur := time.Duration(1)
	for _, s := range t.found {
		maxDur = max(maxDur, s.Duration)
	}
	text := strings.ToLower(t.text)
	var rows []grow
	for _, s := range t.found {
		if t.errsOnly && !s.Error {
			continue
		}
		svcs := strings.Join(s.Services, ",")
		if !fuzzy(s.Root+" "+svcs+" "+s.ID, text) {
			continue
		}
		bar := lipgloss.NewStyle().Foreground(viz.Palette[2]).Render(strings.Repeat("▇", max(1, int(float64(s.Duration)/float64(maxDur)*16))))
		e := ""
		if s.Error {
			e = sRed.Render("error")
		}
		rows = append(rows, grow{id: s.ID, cells: []string{s.Start.Local().Format("15:04:05"), latency(s.Duration), bar, fmt.Sprint(s.Spans), s.Root, svcs, e},
			keys: []any{float64(s.Start.UnixNano()), float64(s.Duration), float64(s.Duration), float64(s.Spans), s.Root, svcs, map[bool]string{true: "a", false: "b"}[s.Error]}})
	}
	return rows
}

func (t *tracesTab) requery(m *model) tea.Cmd {
	t.spansFor, t.spans = "", nil
	return t.refresh(m)
}

func (t *tracesTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tracesMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.err = ""
		if msg.err != nil {
			t.err = msg.err.Error()
			return nil
		}
		t.found = msg.list
		if msg.services != nil {
			t.services = msg.services
		}
		t.list.set(t.rows())
		if t.spansFor == "" {
			if r, ok := t.list.current(); ok {
				return t.load(m, r.id)
			}
		}
	case spansMsg:
		if msg.gen == m.gen {
			if msg.id != t.spansFor {
				t.spanSel, t.spanOff = 0, 0
			}
			t.spans, t.spansFor, t.wfWidth = msg.spans, msg.id, 0
			if msg.err != nil {
				t.err = msg.err.Error()
			}
		}
	case tea.KeyMsg:
		if t.inSpans {
			switch msg.String() {
			case "esc", "left", "q":
				t.inSpans = false
			case "y":
				if s, ok := t.span(); ok {
					copyText(spanText(s))
					m.setStatus("copied span "+s.Name, false)
				}
			default:
				listKeys(msg, &t.spanSel, len(t.wf.Rows))
			}
			return nil
		}
		if t.list.key(msg) {
			if r, ok := t.list.current(); ok {
				return t.load(m, r.id)
			}
			return nil
		}
		switch msg.String() {
		case "enter", "right":
			if r, ok := t.list.current(); ok {
				if r.id == t.spansFor && len(t.spans) > 0 {
					t.inSpans = true
					return nil
				}
				t.inSpans = true
				return t.load(m, r.id)
			}
		case "s":
			opts := append([]string{"(any service)"}, t.services...)
			m.pick("traces through service", opts, nil, 0, false, func(c []string) tea.Cmd {
				t.service = ""
				if len(c) > 0 && c[0] != "(any service)" {
					t.service = c[0]
				}
				return t.requery(m)
			})
		case "o":
			var ops []string
			for _, s := range t.found {
				if !slices.Contains(ops, s.Root) {
					ops = append(ops, s.Root)
				}
			}
			m.askAI("operation (span name, empty for any)", t.op, "one exact span (operation) name to search traces by"+within("root spans seen", ops), func(v string) tea.Cmd {
				t.op = strings.TrimSpace(v)
				return t.requery(m)
			})
		case "m":
			m.ask("minimum duration (e.g. 200ms, 0 for any)", t.min.String(), func(v string) tea.Cmd {
				if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
					t.min = d
				}
				return t.requery(m)
			})
		case "t":
			t.back = (t.back + 1) % len(lookbacks)
			return t.requery(m)
		case "n":
			m.ask("how many traces", fmt.Sprint(t.limit), func(v string) tea.Cmd {
				fmt.Sscanf(v, "%d", &t.limit)
				t.limit = max(t.limit, 1)
				return t.requery(m)
			})
		case "/":
			var items []string
			for _, s := range t.found {
				items = append(items, s.Root+" "+strings.Join(s.Services, ",")+" "+s.ID)
			}
			m.askChecked("text in root, services or id", t.text, "a filter of traces (root span, services, id): "+fuzzyHint+within("traces", items), matchesSome(items, fuzzy), func(v string) tea.Cmd {
				t.text = strings.TrimSpace(v)
				t.list.set(t.rows())
				return nil
			})
		case "e":
			t.errsOnly = !t.errsOnly
			t.list.set(t.rows())
		case "a":
			t.auto = !t.auto
		case "R":
			return t.refresh(m)
		}
	}
	return nil
}

func (t *tracesTab) span() (core.Span, bool) {
	if t.spanSel < 0 || t.spanSel >= len(t.wf.Order) || t.wf.Order[t.spanSel] >= len(t.spans) {
		return core.Span{}, false
	}
	return t.spans[t.wf.Order[t.spanSel]], true
}

func spanText(s core.Span) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\ntrace %s span %s parent %s\nstart %s duration %s error %v\n", s.Service, s.Name, s.TraceID, s.ID, s.Parent,
		s.Start.Local().Format("15:04:05.000000"), s.Duration, s.Error)
	for _, k := range engine.SortedKeys(s.Tags) {
		fmt.Fprintf(&b, "%s = %s\n", k, s.Tags[k])
	}
	return b.String()
}

func (t *tracesTab) wheel(m *model, h hit, up bool) (tea.Cmd, bool) {
	if h.id != "traces:spans" {
		return nil, false
	}
	t.inSpans = true
	if up {
		t.spanSel = max(0, t.spanSel-3)
	} else {
		t.spanSel = min(len(t.wf.Rows)-1, t.spanSel+3)
	}
	return nil, true
}

func (t *tracesTab) click(m *model, h hit) tea.Cmd {
	if h.id == "traces:spans" {
		if i := t.spanOff + h.y; i < len(t.wf.Rows) {
			t.spanSel, t.inSpans = i, true
		}
		return nil
	}
	t.inSpans = false
	if t.list.click(h) {
		if r, ok := t.list.current(); ok {
			return t.load(m, r.id)
		}
	}
	return nil
}

func (t *tracesTab) view(m *model, w, h int) string {
	var f []string
	f = append(f, "last "+lookbacks[t.back].String())
	if t.service != "" {
		f = append(f, "service "+t.service)
	}
	if t.op != "" {
		f = append(f, "op "+t.op)
	}
	if t.min > 0 {
		f = append(f, "≥"+t.min.String())
	}
	if t.text != "" {
		f = append(f, "text "+t.text)
	}
	if t.errsOnly {
		f = append(f, "errors")
	}
	if !t.auto {
		f = append(f, "paused")
	}
	title := "traces · " + strings.Join(f, " · ")
	if t.err != "" && len(t.found) == 0 {
		return panel(title, sRed.Render(wrap(t.err, w-4))+"\n\n"+sDim.Render("configure a tracing component (zipkin, jaeger) in rig.yaml"), w, h, true)
	}
	listH := m.paneSize("traces", splitGeo{total: h, minA: 5, minB: 6, down: true}, max(6, min(h/2, len(t.list.rows)+3)), 0, 0, w)
	list := panel(fmt.Sprintf("%s · %d", title, len(t.list.rows)), t.list.view(m, 1, 1, w-2, listH-2, true), w, listH, true)
	if len(t.spans) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, list, panel("trace", sDim.Render("select a trace (enter or click)"), w, h-listH, false))
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, t.spansView(m, listH, w, h-listH))
}

// spansView is the waterfall with one span selected, scrolled to keep it in sight, and the selected
// span's tags beside it.
func (t *tracesTab) spansView(m *model, y, w, h int) string {
	dw := 0
	if w >= 110 {
		dw = m.paneSize("traces.span", splitGeo{total: w, minA: 24, minB: 40, fromEnd: true}, min(60, w/3), 0, y, h)
	}
	ww := w - dw
	if t.wfWidth != ww-4 {
		t.wf, t.wfWidth = viz.Waterfalls(t.spans, ww-4), ww-4
	}
	t.spanSel = min(max(t.spanSel, 0), max(0, len(t.wf.Rows)-1))
	rowsH := max(1, h-4)
	t.spanOff = scroll(t.spanSel, t.spanOff, rowsH, len(t.wf.Rows))
	m.zone("traces:spans", 1, y+2, ww-2, rowsH)
	var b strings.Builder
	b.WriteString(t.wf.Header + "\n")
	for i := t.spanOff; i < min(len(t.wf.Rows), t.spanOff+rowsH); i++ {
		r := t.wf.Rows[i]
		if i == t.spanSel && t.inSpans {
			r = highlight(sSelected, r, ww-4)
		}
		b.WriteString(r + "\n")
	}
	foot := t.wf.Footer
	if len(t.wf.Rows) > rowsH {
		foot += sDim.Render(fmt.Sprintf("  · %d-%d of %d", t.spanOff+1, min(len(t.wf.Rows), t.spanOff+rowsH), len(t.wf.Rows)))
	}
	b.WriteString(foot)
	title := "trace " + t.spansFor
	if !t.inSpans {
		title += sDim.Render("  (enter or click: walk spans)")
	}
	wf := panel(title, b.String(), ww, h, t.inSpans)
	if dw == 0 {
		return wf
	}
	detail := sDim.Render("select a span")
	if s, ok := t.span(); ok && t.inSpans {
		var d strings.Builder
		st := sTitle
		if s.Error {
			st = sRed.Bold(true)
		}
		d.WriteString(st.Render(s.Service+": "+s.Name) + "\n")
		d.WriteString(sDim.Render("duration ") + s.Duration.String() + sDim.Render("  start ") + s.Start.Local().Format("15:04:05.000000") + "\n")
		d.WriteString(sDim.Render("span ") + s.ID + "\n\n")
		for _, k := range engine.SortedKeys(s.Tags) {
			d.WriteString(sAccent.Render(k) + " " + wrap(s.Tags[k], dw-4-len(k)) + "\n")
		}
		detail = d.String()
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, wf, panel("span", detail, dw, h, false))
}

func (t *tracesTab) atRoot() bool { return !t.inSpans }
