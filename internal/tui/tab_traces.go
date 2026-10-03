package tui

import (
	"context"
	"fmt"
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
	return [][2]string{{"enter", "waterfall"}, {"s", "service"}, {"o", "operation"}, {"m", "min duration"}, {"t", "time window"},
		{"/", "text"}, {"e", "errors only"}, {"n", "limit"}, {"R", "refresh"}, {"a", "auto refresh"}, {"< >", "sort"}}
}

func (t *tracesTab) interval() time.Duration {
	if t.auto {
		return 15 * time.Second
	}
	return 0
}

func (t *tracesTab) open(m *model) tea.Cmd {
	t.list = newGrid("traces", col("TIME", 8), rcol("DURATION", 9), col("", 16), rcol("SPANS", 5), col("ROOT", 0), col("SERVICES", 0), col("", 5))
	t.list.sortBy, t.list.desc = 0, true
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
		if text != "" && !strings.Contains(strings.ToLower(s.Root+" "+svcs+" "+s.ID), text) {
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
			t.spans, t.spansFor = msg.spans, msg.id
			if msg.err != nil {
				t.err = msg.err.Error()
			}
		}
	case tea.KeyMsg:
		if t.list.key(msg) {
			return nil
		}
		switch msg.String() {
		case "enter":
			if r, ok := t.list.current(); ok {
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
			m.ask("operation (span name, empty for any)", t.op, func(v string) tea.Cmd {
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
			m.ask("text in root, services or id", t.text, func(v string) tea.Cmd {
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

func (t *tracesTab) click(m *model, h hit) tea.Cmd {
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
	listH := max(6, min(h/2, len(t.list.rows)+3))
	list := panel(fmt.Sprintf("%s · %d", title, len(t.list.rows)), t.list.view(m, 1, 1, w-2, listH-2, true), w, listH, true)
	wf := sDim.Render("select a trace (enter or click)")
	if len(t.spans) > 0 {
		wf = viz.Waterfall(t.spans, w-4)
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, panel("trace "+t.spansFor, wf, w, h-listH, false))
}
