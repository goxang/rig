package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/engine"
	"github.com/MohammadmahdiAhmadi/rig/internal/viz"
)

type tracesTab struct {
	list     []core.TraceSummary
	services []string
	service  string
	min      time.Duration
	sel      int
	offset   int
	spans    []core.Span
	spansFor string
	err      string
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
	return [][2]string{{"enter", "waterfall"}, {"s", "service"}, {"m", "min duration"}}
}

func (t *tracesTab) open(m *model) tea.Cmd { return t.refresh(m) }

func (t *tracesTab) refresh(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	q := core.TraceQuery{Service: t.service, MinDuration: t.min, Lookback: time.Hour, Limit: 100}
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		tr, _, err := engine.Get[core.Tracing](a, core.KindTracing, "")
		if err != nil {
			return tracesMsg{gen: gen, err: err}
		}
		svcs, _ := tr.Services(c)
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
		t.list, t.services = msg.list, msg.services
		if t.spansFor == "" && len(t.list) > 0 {
			return t.load(m, t.list[0].ID)
		}
	case spansMsg:
		if msg.gen == m.gen {
			t.spans, t.spansFor = msg.spans, msg.id
			if msg.err != nil {
				t.err = msg.err.Error()
			}
		}
	case tea.KeyMsg:
		if listKeys(msg, &t.sel, len(t.list)) {
			return nil
		}
		switch msg.String() {
		case "enter":
			if t.sel < len(t.list) {
				return t.load(m, t.list[t.sel].ID)
			}
		case "s":
			opts := append([]string{""}, t.services...)
			for i, s := range opts {
				if s == t.service {
					t.service = opts[(i+1)%len(opts)]
					break
				}
			}
			t.sel, t.spansFor = 0, ""
			return t.refresh(m)
		case "m":
			m.ask("min duration", t.min.String(), func(v string) tea.Cmd {
				if d, err := time.ParseDuration(v); err == nil {
					t.min = d
				}
				t.sel, t.spansFor = 0, ""
				return t.refresh(m)
			})
		}
	}
	return nil
}

func (t *tracesTab) view(m *model, w, h int) string {
	title := "traces · last hour"
	if t.service != "" {
		title += " · " + t.service
	}
	if t.min > 0 {
		title += " · ≥" + t.min.String()
	}
	if t.err != "" && len(t.list) == 0 {
		return panel(title, sRed.Render(wrap(t.err, w-4))+"\n\n"+sDim.Render("configure a tracing component (zipkin, jaeger) in rig.yaml"), w, h, true)
	}
	listH := min(h/2, len(t.list)+3)
	listH = max(listH, 5)
	maxDur := time.Duration(1)
	for _, s := range t.list {
		maxDur = max(maxDur, s.Duration)
	}
	barW := 20
	var rows [][]string
	for _, s := range t.list {
		bar := lipgloss.NewStyle().Foreground(viz.Palette[2]).Render(strings.Repeat("▇", max(1, int(float64(s.Duration)/float64(maxDur)*float64(barW)))))
		e := ""
		if s.Error {
			e = sRed.Render("error")
		}
		rows = append(rows, []string{s.Start.Format("15:04:05"), padRight(bar, barW) + " " + latency(s.Duration), fmt.Sprint(s.Spans), s.Root, strings.Join(s.Services, ","), e})
	}
	t.sel = min(t.sel, max(0, len(rows)-1))
	t.offset = scroll(t.sel, t.offset, listH-3, len(rows))
	cw := w - 4 - 8 - (barW + 9) - 5 - 6 - 6
	list := panel(title+fmt.Sprintf(" · %d", len(rows)), table([]string{"TIME", "DURATION", "SPANS", "ROOT", "SERVICES", ""},
		[]int{8, barW + 9, 5, cw * 3 / 5, cw - cw*3/5, 5}, rows, t.sel, t.offset, listH-2), w, listH, true)
	wf := sDim.Render("select a trace and press enter")
	if len(t.spans) > 0 {
		wf = viz.Waterfall(t.spans, w-4)
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, panel("trace "+t.spansFor, wf, w, h-listH, false))
}
