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
	"github.com/goxang/rig/spec"
)

type overviewTab struct {
	data  []panelData
	loads map[string]core.LoadStatus
	hosts []core.Host
}

type overviewMsg struct {
	gen   int
	loads map[string]core.LoadStatus
	hosts []core.Host
}

func (t *overviewTab) name() string { return "Overview" }
func (t *overviewTab) typing() bool { return false }
func (t *overviewTab) hints() [][2]string {
	return [][2]string{{"2", "services"}, {"3", "metrics"}, {"4", "load"}}
}

func (t *overviewTab) panels(m *model) []spec.Panel {
	ds := engine.SortedKeys(m.app.Spec.Dashboards)
	if len(ds) == 0 {
		return nil
	}
	ps := m.app.Spec.Dashboards[ds[0]]
	for _, pref := range []string{"overview", "main"} {
		if p, ok := m.app.Spec.Dashboards[pref]; ok {
			ps = p
			break
		}
	}
	return ps[:min(4, len(ps))]
}

func (t *overviewTab) open(m *model) tea.Cmd { return t.refresh(m) }

func (t *overviewTab) refresh(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	side := func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		msg := overviewMsg{gen: gen, loads: map[string]core.LoadStatus{}}
		gens, _ := engine.All[core.LoadGenerator](a, core.KindLoad)
		for n, g := range gens {
			if st, err := g.Status(c); err == nil {
				msg.loads[n] = st
			}
		}
		if h, _, err := engine.Get[core.Hosts](a, core.KindHosts, ""); err == nil {
			msg.hosts, _ = h.Hosts(c)
		}
		return msg
	}
	cmds := []tea.Cmd{side}
	if ps := t.panels(m); len(ps) > 0 {
		cmds = append(cmds, fetchPanels(m, "overview", ps, 15*time.Minute, 80))
	}
	return tea.Batch(cmds...)
}

func (t *overviewTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case panelsMsg:
		if msg.gen == m.gen && msg.owner == "overview" {
			t.data = msg.data
		}
	case overviewMsg:
		if msg.gen == m.gen {
			t.loads, t.hosts = msg.loads, msg.hosts
		}
	}
	return nil
}

func (t *overviewTab) view(m *model, w, h int) string {
	up, failing, restarts := 0, 0, 0
	for _, s := range m.services {
		if engine.Ready(s) {
			up++
		}
		if s.State == core.StateFailed || s.State == core.StateDegraded {
			failing++
		}
		for _, in := range s.Instances {
			restarts += in.Restarts
		}
	}
	var rate float64
	running := 0
	for _, l := range t.loads {
		if l.Running {
			rate += l.Rate
			running++
		}
	}
	var cpu, mem float64
	for _, hst := range t.hosts {
		cpu += hst.CPUUsed
		if hst.MemTotal > 0 {
			mem += float64(hst.MemUsed) / float64(hst.MemTotal)
		}
	}
	if n := len(t.hosts); n > 0 {
		cpu, mem = cpu/float64(n), mem/float64(n)
	}

	upColor := lipgloss.TerminalColor(cGreen)
	if up < len(m.services) {
		upColor = cAmber
	}
	failColor := lipgloss.TerminalColor(cGreen)
	if failing > 0 {
		failColor = cRed
	}
	tiles := []string{
		fmt.Sprintf("%d/%d", up, len(m.services)), "services up", sDim.Render(m.app.Env.Name),
		fmt.Sprint(failing), "failing", sDim.Render(fmt.Sprintf("%d restarts", restarts)),
		viz.Human(rate, ""), "load req/s", sDim.Render(fmt.Sprintf("%d of %d generators", running, len(t.loads))),
		fmt.Sprintf("%.0f%%", cpu*100), "host cpu", sDim.Render(fmt.Sprintf("%d hosts", len(t.hosts))),
		fmt.Sprintf("%.0f%%", mem*100), "host memory", sDim.Render("avg used"),
	}
	colors := []lipgloss.TerminalColor{upColor, failColor, cAccent, gaugeColor(cpu), gaugeColor(mem)}
	n := 5
	if w < 100 {
		n = 3
	}
	tw := w / n
	var row []string
	for i := 0; i < n; i++ {
		cw := tw
		if i == n-1 {
			cw = w - tw*(n-1)
		}
		row = append(row, tile(tiles[i*3+1], tiles[i*3], tiles[i*3+2], colors[i], cw))
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top, row...)
	rest := h - lipgloss.Height(top)

	chips := t.chips(m, w-2)
	chipsH := min(lipgloss.Height(chips)+2, max(4, rest/3))
	chartsH := rest - chipsH

	ps := t.panels(m)
	var charts string
	if len(ps) == 0 {
		charts = panel("metrics", sDim.Render("add dashboards: to rig.yaml to see charts here"), w, chartsH, false)
	} else {
		cw := w / len(ps)
		if len(ps) == 4 && w < 160 {
			cw = w / 2
		}
		var cells []string
		for i, p := range ps {
			d := panelData{}
			if i < len(t.data) {
				d = t.data[i]
			}
			width := cw
			if i == len(ps)-1 && cw*len(ps) != w && !(len(ps) == 4 && w < 160) {
				width = w - cw*(len(ps)-1)
			}
			ch := chartsH
			if len(ps) == 4 && w < 160 {
				ch = chartsH / 2
			}
			cells = append(cells, chartPanel(p, d, width, ch, false))
		}
		if len(ps) == 4 && w < 160 {
			charts = lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, cells[0], cells[1]), lipgloss.JoinHorizontal(lipgloss.Top, cells[2], cells[3]))
		} else {
			charts = lipgloss.JoinHorizontal(lipgloss.Top, cells...)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, charts, panel("services", chips, w, chipsH, false))
}

func (t *overviewTab) chips(m *model, w int) string {
	var lines []string
	var cur []string
	used := 0
	for _, s := range m.services {
		c := stateDot(s.State) + " " + s.Service + sDim.Render(fmt.Sprintf(" %d/%d", s.Ready, s.Desired))
		cw := lipgloss.Width(c) + 3
		if used+cw > w && len(cur) > 0 {
			lines = append(lines, strings.Join(cur, "   "))
			cur, used = nil, 0
		}
		cur = append(cur, c)
		used += cw
	}
	if len(cur) > 0 {
		lines = append(lines, strings.Join(cur, "   "))
	}
	if len(lines) == 0 {
		return sDim.Render("no services yet")
	}
	return strings.Join(lines, "\n")
}

func gaugeColor(f float64) lipgloss.TerminalColor {
	switch {
	case f >= 0.85:
		return cRed
	case f >= 0.65:
		return cAmber
	}
	return cGreen
}
