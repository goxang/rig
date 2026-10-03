package tui

import (
	"context"
	"fmt"
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

var ranges = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

type panelData struct {
	series []core.Series
	err    error
}

type panelsMsg struct {
	gen   int
	owner string
	data  []panelData
}

// fetchPanels runs every panel's range query in parallel against its source.
func fetchPanels(m *model, owner string, panels []spec.Panel, rng time.Duration, points int) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out := make([]panelData, len(panels))
		end := time.Now()
		step := max(rng/time.Duration(max(points, 10)), time.Second)
		var wg sync.WaitGroup
		for i, p := range panels {
			wg.Add(1)
			go func() {
				defer wg.Done()
				src, _, err := engine.Get[core.Metrics](a, core.KindMetrics, p.Source)
				if err != nil {
					out[i].err = err
					return
				}
				out[i].series, out[i].err = src.Range(c, p.Query, end.Add(-rng), end, step)
			}()
		}
		wg.Wait()
		return panelsMsg{gen: gen, owner: owner, data: out}
	}
}

// chartPanel renders one panel's data as a line chart, or a stat when the panel asks for one.
func chartPanel(p spec.Panel, d panelData, w, h int, focused bool) string {
	title := p.Title
	if title == "" {
		title = p.Query
	}
	var body string
	switch {
	case d.err != nil:
		body = sRed.Render(wrap(d.err.Error(), w-4))
	case d.series == nil:
		body = sDim.Render("loading…")
	case p.Kind == "stat":
		v := 0.0
		var spark []float64
		if len(d.series) > 0 && len(d.series[0].Points) > 0 {
			pts := d.series[0].Points
			v = pts[len(pts)-1].V
			for _, pt := range pts {
				spark = append(spark, pt.V)
			}
		}
		body = lipgloss.PlaceHorizontal(w-2, lipgloss.Center, big(viz.Human(v, p.Unit), cGreen)) + "\n\n" +
			viz.Sparkline(spark, w-4, viz.Palette[0])
	default:
		lines := viz.FromSeries(d.series)
		if p.Legend != "" {
			for i := range lines {
				lines[i].Name = legend(p.Legend, d.series[i].Labels)
			}
		}
		body = viz.LineChart(lines, w-2, h-2, p.Unit)
	}
	return panel(title, body, w, h, focused)
}

// legend fills {{label}} placeholders from a series' labels.
func legend(tmpl string, labels map[string]string) string {
	out := tmpl
	for k, v := range labels {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
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

type metricsTab struct {
	dash   int
	focus  int
	zoom   bool
	rng    int
	adhoc  []spec.Panel
	data   []panelData
	loaded string
}

func (t *metricsTab) name() string            { return "Metrics" }
func (t *metricsTab) interval() time.Duration { return 10 * time.Second }
func (t *metricsTab) typing() bool            { return false }
func (t *metricsTab) hints() [][2]string {
	return [][2]string{{"←→↑↓", "focus"}, {"z", "zoom"}, {"t", "range"}, {"[ ]", "dashboard"}, {"a", "add query"}, {"e", "edit"}, {"x", "remove"}}
}

func (t *metricsTab) dashboards(m *model) []string { return engine.SortedKeys(m.app.Spec.Dashboards) }

func (t *metricsTab) panels(m *model) []spec.Panel {
	var ps []spec.Panel
	if ds := t.dashboards(m); len(ds) > 0 {
		ps = append(ps, m.app.Spec.Dashboards[ds[t.dash%len(ds)]]...)
	}
	return append(ps, t.adhoc...)
}

func (t *metricsTab) open(m *model) tea.Cmd {
	t.rng = 1
	return t.refresh(m)
}

func (t *metricsTab) refresh(m *model) tea.Cmd {
	ps := t.panels(m)
	if len(ps) == 0 {
		return nil
	}
	return fetchPanels(m, "metrics", ps, ranges[t.rng], 120)
}

func (t *metricsTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case panelsMsg:
		if msg.gen == m.gen && msg.owner == "metrics" {
			t.data = msg.data
		}
	case tea.KeyMsg:
		ps := t.panels(m)
		cols := t.cols(m.w)
		switch msg.String() {
		case "left", "h":
			t.focus = max(0, t.focus-1)
		case "right", "l":
			t.focus = min(len(ps)-1, t.focus+1)
		case "up", "k":
			t.focus = max(0, t.focus-cols)
		case "down", "j":
			t.focus = min(len(ps)-1, t.focus+cols)
		case "z", "enter":
			t.zoom = !t.zoom
		case "t":
			t.rng = (t.rng + 1) % len(ranges)
			t.data = nil
			return t.refresh(m)
		case "]":
			t.dash++
			t.focus, t.data = 0, nil
			return t.refresh(m)
		case "[":
			t.dash = max(0, t.dash-1)
			t.focus, t.data = 0, nil
			return t.refresh(m)
		case "a":
			m.ask("query", "", func(q string) tea.Cmd {
				if q == "" {
					return nil
				}
				t.adhoc = append(t.adhoc, spec.Panel{Title: q, Query: q})
				t.focus = len(t.panels(m)) - 1
				t.data = nil
				return t.refresh(m)
			})
		case "e":
			if t.focus < len(ps) {
				p := ps[t.focus]
				idx := t.focus - (len(ps) - len(t.adhoc))
				m.ask("query", p.Query, func(q string) tea.Cmd {
					np := p
					np.Query, np.Title = q, q
					if idx >= 0 {
						t.adhoc[idx] = np
					} else {
						t.adhoc = append(t.adhoc, np)
					}
					t.data = nil
					return t.refresh(m)
				})
			}
		case "x":
			idx := t.focus - (len(ps) - len(t.adhoc))
			if idx >= 0 && idx < len(t.adhoc) {
				t.adhoc = append(t.adhoc[:idx], t.adhoc[idx+1:]...)
				t.focus = max(0, t.focus-1)
				t.data = nil
				return t.refresh(m)
			}
		}
	}
	return nil
}

func (t *metricsTab) cols(w int) int {
	switch {
	case w >= 180:
		return 3
	case w >= 110:
		return 2
	}
	return 1
}

func (t *metricsTab) view(m *model, w, h int) string {
	ps := t.panels(m)
	ds := t.dashboards(m)
	title := "ad hoc"
	if len(ds) > 0 {
		title = ds[t.dash%len(ds)]
	}
	bar := sTitle.Render(" dashboard "+title) + sDim.Render(fmt.Sprintf("  %d/%d", t.dash%max(1, len(ds))+1, max(1, len(ds)))) +
		sDim.Render("   range ") + sAccent.Render(ranges[t.rng].String())
	h--
	if len(ps) == 0 {
		return bar + "\n" + lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
			sDim.Render("no dashboards in rig.yaml — press a to chart a query\n\ndashboards:\n  main:\n    - { title: requests/s, query: 'sum(rate(http_requests_total[1m]))', unit: /s }"))
	}
	data := func(i int) panelData {
		if i < len(t.data) {
			return t.data[i]
		}
		return panelData{}
	}
	t.focus = min(t.focus, len(ps)-1)
	if t.zoom {
		return bar + "\n" + chartPanel(ps[t.focus], data(t.focus), w, h, true)
	}
	cols := t.cols(w)
	rows := (len(ps) + cols - 1) / cols
	ph := max(8, h/max(1, rows))
	var lines []string
	for r := 0; r < rows; r++ {
		var cells []string
		for c := 0; c < cols; c++ {
			i := r*cols + c
			cw := w / cols
			if c == cols-1 {
				cw = w - (cols-1)*(w/cols)
			}
			if i >= len(ps) {
				cells = append(cells, strings.Repeat(" ", cw))
				continue
			}
			cells = append(cells, chartPanel(ps[i], data(i), cw, ph, i == t.focus))
		}
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	// scroll so the focused row is visible
	first := max(0, t.focus/cols-(h/ph)+1)
	return bar + "\n" + strings.Join(lines[first:], "\n")
}
