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

// hostsTab is the infrastructure view: servers or cluster nodes, their load, and a shell on any of them.
type hostsTab struct {
	hosts  []core.Host
	source string
	err    string
	sel    int
	cpu    map[string][]float64
	mem    map[string][]float64
	// podsOf is the host whose pods show (p); empty shows the hosts
	podsOf string
	pods   *grid
}

type hostsMsg struct {
	gen    int
	hosts  []core.Host
	source string
	err    error
}

func (t *hostsTab) name() string            { return "Hosts" }
func (t *hostsTab) interval() time.Duration { return 3 * time.Second }
func (t *hostsTab) typing() bool            { return false }
func (t *hostsTab) hints() [][2]string {
	if t.podsOf != "" {
		return [][2]string{{"esc", "hosts"}, {"ctrl+⇧←→ ↑↓", "sort, order"}}
	}
	return [][2]string{{"enter", "shell"}, {"c", "run command"}, {"p", "pods"}}
}

func (t *hostsTab) open(m *model) tea.Cmd {
	t.cpu, t.mem = map[string][]float64{}, map[string][]float64{}
	if t.pods == nil {
		t.pods = newGrid("hosts:pods", col("POD", 0), col("NAMESPACE", 30), rcol("CPU", 8), rcol("MEM", 9), rcol("AGE", 7))
		t.pods.sortBy, t.pods.desc = 2, true
	}
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
		t.setPods()
	case tea.KeyMsg:
		if t.podsOf != "" {
			if msg.String() == "esc" {
				t.podsOf = ""
			} else {
				t.pods.key(msg)
			}
			return nil
		}
		if listKeys(msg, &t.sel, len(t.hosts)) || len(t.hosts) == 0 {
			return nil
		}
		host := t.hosts[t.sel].Name
		switch msg.String() {
		case "enter", "s":
			return t.shell(m, host, nil)
		case "p":
			if len(t.hosts[t.sel].Pods) == 0 {
				m.setStatus(t.source+" does not list the pods of "+host, true)
				return nil
			}
			t.podsOf = host
			t.setPods()
		case "c":
			m.askTemplate("command on "+host, "uptime", "a Linux shell command to run on host "+host, func(v string) tea.Cmd {
				return t.shell(m, host, []string{"sh", "-c", v + "; echo; printf 'press enter '; read _"})
			})
		}
	}
	return nil
}

func (t *hostsTab) shell(m *model, host string, command []string) tea.Cmd {
	if err := m.app.Writable(); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
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
	return execProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return statusMsg{text: "shell on " + host + ": " + err.Error(), err: true}
		}
		return statusMsg{text: "back from " + host}
	})
}

func (t *hostsTab) setPods() {
	if t.podsOf == "" {
		return
	}
	var rows []grow
	for _, h := range t.hosts {
		if h.Name != t.podsOf {
			continue
		}
		for _, p := range h.Pods {
			age := time.Since(p.Started)
			rows = append(rows, grow{id: p.Namespace + "/" + p.Name,
				cells: []string{p.Name, p.Namespace, fmt.Sprintf("%.2f", p.CPU), bytesText(p.Mem), shortAge(age)},
				keys:  []any{nil, nil, p.CPU, float64(p.Mem), age.Seconds()}})
		}
	}
	t.pods.set(rows)
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
	if t.podsOf != "" {
		return panel(fmt.Sprintf("pods on %s · %d", t.podsOf, len(t.pods.rows)), t.pods.view(m, 1, 1, w-2, h-2, true), w, h, true)
	}
	inner := w - 2
	nameW := 18
	for _, x := range t.hosts {
		nameW = max(nameW, lipgloss.Width(x.Name)+3)
	}
	meterW := max(14, (inner-nameW-13-12)/3)

	var cpu, memU, memT, diskU, diskT float64
	var cores int
	for _, x := range t.hosts {
		cpu += x.CPUUsed * float64(x.CPUs)
		cores += x.CPUs
		memU, memT = memU+float64(x.MemUsed), memT+float64(x.MemTotal)
		diskU, diskT = diskU+float64(x.DiskUsed), diskT+float64(x.DiskTotal)
	}
	var b strings.Builder
	b.WriteString(padRight(sTitle.Render(fmt.Sprintf("all %d", len(t.hosts))), nameW) + t.meters(cpu/fdiv(float64(cores)), cores, memU, memT, diskU, diskT, meterW) + "\n")
	b.WriteString(sDim.Render(strings.Repeat("─", inner)) + "\n")
	for i, x := range t.hosts {
		dot := sGreen.Render("●")
		if !x.Ready {
			dot = sRed.Render("✖")
		}
		line := dot + " " + padRight(truncate(x.Name, nameW-3), nameW-2) +
			t.meters(x.CPUUsed, x.CPUs, float64(x.MemUsed), float64(x.MemTotal), float64(x.DiskUsed), float64(x.DiskTotal), meterW)
		if x.Load1 > 0 {
			line += sDim.Render(fmt.Sprintf(" load %5.2f", x.Load1))
		} else {
			line += strings.Repeat(" ", 11)
		}
		if i == t.sel {
			line = highlight(sSelected, line, inner)
		} else if m.hovering(1, 3+i, inner, 1) {
			line = highlight(sHover, line, inner)
		}
		b.WriteString(line + "\n")
	}
	if t.sel < len(t.hosts) {
		x := t.hosts[t.sel]
		detail := fmt.Sprintf("%s  %s  %s · %s  %d cores  %s memory  %d pods (p)", x.Addr, strings.Join(x.Roles, ","), x.OS, x.Kernel, x.CPUs, bytesText(x.MemTotal), len(x.Pods))
		if !x.Ready && x.Reason != "" {
			detail = x.Addr + "  " + x.Reason
		}
		b.WriteString("\n" + sDim.Render(truncate(detail, inner)) + "\n")
		if hist := t.cpu[x.Name]; len(hist) > 1 && h-len(t.hosts)-8 > 4 {
			pts := make([]core.Point, len(hist))
			now := time.Now()
			for i, v := range hist {
				pts[i] = core.Point{T: now.Add(time.Duration(i-len(hist)) * t.interval()), V: v * 100}
			}
			b.WriteString(viz.LineChart([]viz.Line{{Name: "cpu % " + x.Name, Points: pts, Color: viz.Palette[2]}}, inner, h-len(t.hosts)-8, "%"))
		}
	}
	m.zone("hosts:rows", 1, 3, inner, len(t.hosts))
	return panel("hosts · "+t.source, b.String(), w, h, true)
}

func (t *hostsTab) click(m *model, h hit) tea.Cmd {
	if t.podsOf != "" {
		t.pods.click(h)
		return nil
	}
	if h.id != "hosts:rows" || h.y >= len(t.hosts) {
		return nil
	}
	t.sel = h.y
	if h.double {
		return t.shell(m, t.hosts[t.sel].Name, nil)
	}
	return nil
}

// meters is a host's CPU, memory and disk as htop bars; zero totals leave a blank bar.
func (t *hostsTab) meters(cpu float64, cores int, memU, memT, diskU, diskT float64, w int) string {
	return sDim.Render("CPU") + viz.Meter(cpu, w, fmt.Sprintf("%.1f%% %dc", cpu*100, cores)) +
		sDim.Render("  MEM") + viz.Meter(memU/fdiv(memT), w, bytesText(int64(memU))+"/"+bytesText(int64(memT))) +
		sDim.Render("  DSK") + viz.Meter(diskU/fdiv(diskT), w, fmt.Sprintf("%.1f%%", 100*diskU/fdiv(diskT)))
}

func fdiv(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}
