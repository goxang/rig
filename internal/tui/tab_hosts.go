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

// hostsTab is the infrastructure view: servers or cluster nodes, their load, and a shell on any of them.
type hostsTab struct {
	hosts  []core.Host
	source string
	err    string
	sel    int
	cpu    map[string][]float64
	mem    map[string][]float64
}

type hostsMsg struct {
	gen    int
	hosts  []core.Host
	source string
	err    error
}

func (t *hostsTab) name() string { return "Hosts" }
func (t *hostsTab) typing() bool { return false }
func (t *hostsTab) hints() [][2]string {
	return [][2]string{{"enter", "shell"}, {"c", "run command"}}
}

func (t *hostsTab) open(m *model) tea.Cmd {
	t.cpu, t.mem = map[string][]float64{}, map[string][]float64{}
	return t.refresh(m)
}

func (t *hostsTab) refresh(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		h, name, err := engine.Get[core.Hosts](a, core.KindHosts, "")
		if err != nil {
			return hostsMsg{gen: gen, err: err}
		}
		hs, err := h.Hosts(c)
		return hostsMsg{gen: gen, hosts: hs, source: name, err: err}
	}
}

func (t *hostsTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case hostsMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.err = ""
		if msg.err != nil {
			t.err = msg.err.Error()
			return nil
		}
		t.hosts, t.source = msg.hosts, msg.source
		for _, h := range msg.hosts {
			t.cpu[h.Name] = last(append(t.cpu[h.Name], h.CPUUsed), 60)
			if h.MemTotal > 0 {
				t.mem[h.Name] = last(append(t.mem[h.Name], float64(h.MemUsed)/float64(h.MemTotal)), 60)
			}
		}
	case tea.KeyMsg:
		if listKeys(msg, &t.sel, len(t.hosts)) || len(t.hosts) == 0 {
			return nil
		}
		host := t.hosts[t.sel].Name
		switch msg.String() {
		case "enter", "s":
			return t.shell(m, host, nil)
		case "c":
			m.ask("command on "+host, "uptime", func(v string) tea.Cmd {
				return t.shell(m, host, []string{"sh", "-c", v + "; echo; printf 'press enter '; read _"})
			})
		}
	}
	return nil
}

func (t *hostsTab) shell(m *model, host string, command []string) tea.Cmd {
	h, _, err := engine.Get[core.Hosts](m.app, core.KindHosts, "")
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	cmd, err := h.Shell(m.ctx, host, command)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return statusMsg{text: "shell on " + host + ": " + err.Error(), err: true}
		}
		return statusMsg{text: "back from " + host}
	})
}

func last(v []float64, n int) []float64 {
	if len(v) > n {
		return v[len(v)-n:]
	}
	return v
}

func (t *hostsTab) view(m *model, w, h int) string {
	if t.err != "" {
		return panel("hosts", sRed.Render(t.err)+"\n\n"+sDim.Render("add a hosts component (type: ssh) or use a runtime that knows its nodes (kubernetes, kind, local)"), w, h, true)
	}
	if len(t.hosts) == 0 {
		return panel("hosts", sDim.Render("loading…"), w, h, true)
	}
	cardW := 54
	cols := max(1, w/cardW)
	cardW = w / cols
	cardH := 9
	var rows []string
	var row []string
	for i, x := range t.hosts {
		row = append(row, t.card(x, cardW, cardH, i == t.sel))
		if len(row) == cols || i == len(t.hosts)-1 {
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
			row = nil
		}
	}
	perScreen := max(1, h/cardH)
	first := max(0, t.sel/cols-perScreen+1)
	return strings.Join(rows[first:], "\n")
}

func (t *hostsTab) card(x core.Host, w, h int, focused bool) string {
	state := sGreen.Render("● ready")
	if !x.Ready {
		state = sRed.Render("✖ unreachable")
	}
	mem := 0.0
	if x.MemTotal > 0 {
		mem = float64(x.MemUsed) / float64(x.MemTotal)
	}
	gw := w - 24
	var b strings.Builder
	b.WriteString(state + sDim.Render("  "+x.Addr+"  "+strings.Join(x.Roles, ",")) + "\n")
	b.WriteString(sDim.Render(truncate(x.OS+" · "+x.Kernel, w-4)) + "\n\n")
	b.WriteString(padRight("cpu", 5) + viz.Gauge(x.CPUUsed, gw) + fmt.Sprintf(" %3.0f%% %2dc", x.CPUUsed*100, x.CPUs) + "\n")
	b.WriteString(padRight("mem", 5) + viz.Gauge(mem, gw) + fmt.Sprintf(" %3.0f%% %s", mem*100, bytesText(x.MemTotal)) + "\n")
	b.WriteString(sDim.Render("cpu  ") + viz.Sparkline(t.cpu[x.Name], gw, viz.Palette[2]))
	if x.Load1 > 0 {
		b.WriteString(sDim.Render(fmt.Sprintf(" load %.2f", x.Load1)))
	}
	return panel(x.Name, b.String(), w, h, focused)
}
