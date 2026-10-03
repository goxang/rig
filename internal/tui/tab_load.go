package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

type loadTab struct {
	names []string
	sel   int
	stats map[string]core.LoadStatus
	errs  map[string]error
	hist  map[string]*loadHist
}

// loadHist keeps what a generator did while the TUI watched it; Sent deltas give the actual rate.
type loadHist struct {
	target, actual, failed []core.Point
	samples                []loadSample
}

type loadSample struct {
	at           time.Time
	sent, failed int64
}

// rates compares with a sample at least 10s old: counters scraped every few seconds look idle between scrapes.
func (h *loadHist) rates(now time.Time, st core.LoadStatus) (sent, failed float64, ok bool) {
	h.samples = append(h.samples, loadSample{at: now, sent: st.Sent, failed: st.Failed})
	for len(h.samples) > 2 && now.Sub(h.samples[1].at) >= 10*time.Second {
		h.samples = h.samples[1:]
	}
	old := h.samples[0]
	secs := now.Sub(old.at).Seconds()
	if secs < 1 || st.Sent < old.sent {
		return 0, 0, false
	}
	return float64(st.Sent-old.sent) / secs, float64(st.Failed-old.failed) / secs, true
}

type loadMsg struct {
	gen   int
	stats map[string]core.LoadStatus
	errs  map[string]error
}

func (t *loadTab) name() string { return "Load" }
func (t *loadTab) typing() bool { return false }
func (t *loadTab) hints() [][2]string {
	return [][2]string{{"space", "start/stop"}, {"+/-", "step"}, {"r", "set rate"}}
}

func (t *loadTab) open(m *model) tea.Cmd {
	t.names = m.app.Names(core.KindLoad)
	t.hist = map[string]*loadHist{}
	return t.refresh(m)
}

func (t *loadTab) refresh(m *model) tea.Cmd {
	a, gen, ctx, names := m.app, m.gen, m.ctx, t.names
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		msg := loadMsg{gen: gen, stats: map[string]core.LoadStatus{}, errs: map[string]error{}}
		for _, n := range names {
			g, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, n)
			if err == nil {
				msg.stats[n], err = g.Status(c)
			}
			if err != nil {
				msg.errs[n] = err
			}
		}
		return msg
	}
}

func (t *loadTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loadMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.stats, t.errs = msg.stats, msg.errs
		now := time.Now()
		for n, st := range msg.stats {
			h := t.hist[n]
			if h == nil {
				h = &loadHist{}
				t.hist[n] = h
			}
			if sent, failed, ok := h.rates(now, st); ok {
				h.actual = keep(append(h.actual, core.Point{T: now, V: sent}))
				h.failed = keep(append(h.failed, core.Point{T: now, V: failed}))
			}
			target := st.Rate
			if !st.Running {
				target = 0
			}
			h.target = keep(append(h.target, core.Point{T: now, V: target}))
		}
	case tea.KeyMsg:
		if listKeys(msg, &t.sel, len(t.names)) || len(t.names) == 0 {
			return nil
		}
		n := t.names[t.sel]
		a := m.app
		g, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, n)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		switch msg.String() {
		case " ", "enter":
			if t.stats[n].Running {
				return m.mutate("stop "+n, g.Stop)
			}
			return m.mutate("start "+n, g.Start)
		case "+", "=":
			return m.do("raise "+n, func(ctx context.Context) error { _, err := a.Nudge(ctx, n, 1); return err })
		case "-":
			return m.do("lower "+n, func(ctx context.Context) error { _, err := a.Nudge(ctx, n, -1); return err })
		case "r":
			m.ask("rate for "+n+" (req/s)", strconv.FormatFloat(t.stats[n].Rate, 'f', -1, 64), func(v string) tea.Cmd {
				r, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					m.setStatus("rate: "+err.Error(), true)
					return nil
				}
				return m.mutate(fmt.Sprintf("set %s to %s/s", n, v), func(ctx context.Context) error { return a.SetRate(ctx, n, r, false) })
			})
		}
	}
	return nil
}

func keep(p []core.Point) []core.Point {
	if len(p) > 600 {
		return p[len(p)-600:]
	}
	return p
}

func (t *loadTab) view(m *model, w, h int) string {
	if len(t.names) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sDim.Render(`no load generators — add one to rig.yaml:

components:
  load-api:
    type: http                       # or kv (a generator service reading its rate from Consul), command
    target: "svc://api:8080/ping"
    rate: 20
    max: 500
    step: 10`))
	}
	lw := min(44, w/3)
	var rows [][]string
	for _, n := range t.names {
		st, ok := t.stats[n]
		dot := sDim.Render("○")
		if t.errs[n] != nil {
			dot = sRed.Render("✖")
		} else if ok && st.Running {
			dot = sGreen.Render("●")
		}
		_, typ, _ := m.app.Kind(n)
		rows = append(rows, []string{dot, n, typ, viz.Human(st.Rate, "/s")})
	}
	t.sel = min(t.sel, len(rows)-1)
	list := panel("generators", table([]string{"", "NAME", "TYPE", "RATE"}, []int{1, lw - 26, 8, 9}, rows, t.sel, 0, h-2), lw, h, true)

	n := t.names[t.sel]
	st := t.stats[n]
	rw := w - lw
	if err := t.errs[n]; err != nil {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, panel(n, sRed.Render(wrap(err.Error(), rw-4)), rw, h, false))
	}
	lim := m.app.LoadLimits(n)
	hist := t.hist[n]
	if hist == nil {
		hist = &loadHist{}
	}
	actual := 0.0
	if len(hist.actual) > 0 {
		actual = hist.actual[len(hist.actual)-1].V
	}
	failPct := 0.0
	if st.Sent > 0 {
		failPct = float64(st.Failed) / float64(st.Sent) * 100
	}
	state, stateColor := "IDLE", lipgloss.TerminalColor(cDim)
	if st.Running {
		state, stateColor = "RUNNING", cGreen
	}
	tw := rw / 4
	tiles := lipgloss.JoinHorizontal(lipgloss.Top,
		tile("target", viz.Human(st.Rate, ""), "req/s · "+lipgloss.NewStyle().Foreground(stateColor).Bold(true).Render(state), cAccent, tw),
		tile("actual", viz.Human(actual, ""), "req/s sent", cGreen, tw),
		tile("errors", fmt.Sprintf("%.1f%%", failPct), fmt.Sprintf("%d of %d", st.Failed, st.Sent), failColor(failPct), tw),
		tile("p99", latency(st.Latency.P99), "p50 "+latency(st.Latency.P50)+" · p95 "+latency(st.Latency.P95), cPurple, rw-3*tw),
	)
	gauge := ""
	if lim.Max > 0 {
		gauge = sDim.Render(" rate ") + viz.Gauge(st.Rate/lim.Max, rw-30) + sDim.Render(fmt.Sprintf(" %s / max %s", viz.Human(st.Rate, "/s"), viz.Human(lim.Max, "/s")))
	} else {
		gauge = sDim.Render(fmt.Sprintf(" step %s/s · no max set", viz.Human(lim.Step, "")))
	}
	chartH := h - lipgloss.Height(tiles) - 1
	lines := []viz.Line{
		{Name: "target", Points: hist.target, Color: "#5794F2"},
		{Name: "actual", Points: hist.actual, Color: "#73BF69"},
		{Name: "failed", Points: hist.failed, Color: "#F2495C"},
	}
	chart := panel(n+" · req/s while watching", viz.LineChart(lines, rw-2, chartH-2, "/s"), rw, chartH, false)
	if len(st.Extra) > 0 {
		var ex []string
		for _, k := range engine.SortedKeys(st.Extra) {
			ex = append(ex, k+" "+st.Extra[k])
		}
		gauge += sDim.Render("   " + strings.Join(ex, " · "))
	}
	right := lipgloss.JoinVertical(lipgloss.Left, tiles, truncate(gauge, rw), chart)
	return lipgloss.JoinHorizontal(lipgloss.Top, list, right)
}

func failColor(pct float64) lipgloss.TerminalColor {
	switch {
	case pct >= 5:
		return cRed
	case pct > 0:
		return cAmber
	}
	return cGreen
}

func latency(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
}
