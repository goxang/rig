package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

type servicesTab struct {
	sel, offset int
	detail      string
	debug       map[string]core.DebugSession
	logs        []core.LogLine
	logsFor     string
}

type svcLogsMsg struct {
	gen  int
	svc  string
	logs []core.LogLine
}

// resultMsg carries an operation's outcome into the detail pane (and the footer).
type resultMsg struct {
	gen    int
	text   string
	status string
	err    bool
	debug  *debugAttach
}

type debugAttach struct {
	svc  string
	sess core.DebugSession
}

func (t *servicesTab) name() string          { return "Services" }
func (t *servicesTab) typing() bool          { return false }
func (t *servicesTab) open(m *model) tea.Cmd { return nil }

// refresh tails the selected service's log for the detail pane.
func (t *servicesTab) refresh(m *model) tea.Cmd {
	st, ok := t.current(m)
	if !ok {
		return nil
	}
	a, gen, ctx, s := m.app, m.gen, m.ctx, m.app.Spec.Services[st.Service]
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ch, err := a.Runtime().Logs(c, s, core.LogOptions{Tail: 30})
		if err != nil {
			return svcLogsMsg{gen: gen, svc: s.Name}
		}
		var lines []core.LogLine
		for l := range ch {
			lines = append(lines, l)
		}
		return svcLogsMsg{gen: gen, svc: s.Name, logs: lines}
	}
}
func (t *servicesTab) hints() [][2]string {
	return [][2]string{{"s", "start"}, {"x", "stop"}, {"r", "restart"}, {"+/-", "scale"}, {"d", "deploy"}, {"b", "build+deploy"},
		{"e", "shell"}, {"l", "logs"}, {"p", "profile"}, {"D", "debug"}}
}

func (t *servicesTab) current(m *model) (core.Status, bool) {
	if t.sel < 0 || t.sel >= len(m.services) {
		return core.Status{}, false
	}
	return m.services[t.sel], true
}

func (t *servicesTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case resultMsg:
		m.busy = max(0, m.busy-1)
		m.setStatus(msg.status, msg.err)
		if msg.gen != m.gen {
			return nil
		}
		if msg.text != "" {
			t.detail = msg.text
		}
		if msg.debug != nil {
			if t.debug == nil {
				t.debug = map[string]core.DebugSession{}
			}
			t.debug[msg.debug.svc] = msg.debug.sess
		}
		return nil
	case svcLogsMsg:
		if msg.gen == m.gen {
			t.logs, t.logsFor = msg.logs, msg.svc
		}
		return nil
	case tea.KeyMsg:
		if listKeys(msg, &t.sel, len(m.services)) {
			t.detail = ""
			return t.refresh(m)
		}
		st, ok := t.current(m)
		if !ok {
			return nil
		}
		s := m.app.Spec.Services[st.Service]
		rt := m.app.Runtime()
		switch msg.String() {
		case "s":
			return m.mutate("start "+s.Name, func(ctx context.Context) error { return rt.Start(ctx, s) })
		case "x":
			return m.mutate("stop "+s.Name, func(ctx context.Context) error { return rt.Stop(ctx, s) })
		case "r":
			return m.mutate("restart "+s.Name, func(ctx context.Context) error { return rt.Restart(ctx, s) })
		case "+", "=":
			n := st.Desired + 1
			return m.mutate(fmt.Sprintf("scale %s to %d", s.Name, n), func(ctx context.Context) error { return rt.Scale(ctx, s, n) })
		case "-":
			n := max(0, st.Desired-1)
			return m.mutate(fmt.Sprintf("scale %s to %d", s.Name, n), func(ctx context.Context) error { return rt.Scale(ctx, s, n) })
		case "d":
			return m.mutate("deploy "+s.Name, func(ctx context.Context) error { return rt.Deploy(ctx, s, core.Release{}) })
		case "b":
			a := m.app
			return m.mutate("build and deploy "+s.Name, func(ctx context.Context) error {
				tag := time.Now().Format("20060102-150405")
				img, err := a.Build(ctx, s, tag, nil)
				if err != nil {
					return err
				}
				if err := rt.Deploy(ctx, s, core.Release{Image: img}); err != nil {
					return err
				}
				return a.SetState(ctx, map[string]string{"tag": tag})
			})
		case "l":
			for i, tb := range m.tabs {
				if lt, ok := tb.(*logsTab); ok {
					lt.only = s.Name
					lt.restart = true
					return m.openTab(i)
				}
			}
		case "e":
			return t.shell(m, s.Name)
		case "p":
			m.ask("profile "+s.Name+" (kind duration)", "cpu 10s", func(v string) tea.Cmd {
				f := strings.Fields(v)
				kind, dur := "cpu", 10*time.Second
				if len(f) > 0 {
					kind = f[0]
				}
				if len(f) > 1 {
					if d, err := time.ParseDuration(f[1]); err == nil {
						dur = d
					}
				}
				a, gen, ctx := m.app, m.gen, m.ctx
				m.busy++
				m.setStatus(fmt.Sprintf("profiling %s (%s, %s)…", s.Name, kind, dur), false)
				return func() tea.Msg {
					p, _, err := engine.Get[core.Profiler](a, core.KindProfiler, "")
					if err != nil {
						return resultMsg{gen: gen, status: "profile: " + err.Error(), err: true}
					}
					res, err := p.Capture(ctx, core.ProfileRequest{Service: s, Kind: kind, Duration: dur})
					if err != nil {
						return resultMsg{gen: gen, status: "profile: " + err.Error(), err: true}
					}
					return resultMsg{gen: gen, text: sAccent.Render(res.File) + "\n" + res.Summary, status: "profile saved: " + res.File}
				}
			})
		case "D":
			return t.toggleDebug(m, s.Name)
		}
	}
	return nil
}

// shell hands the terminal to `rig exec` for the service; the TUI comes back when it exits.
func (t *servicesTab) shell(m *model, svc string) tea.Cmd {
	self, err := os.Executable()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	c := exec.Command(self, "-f", m.app.Spec.File, "-e", m.app.Env.Name, "exec", svc)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return statusMsg{text: "shell: " + err.Error(), err: true}
		}
		return statusMsg{text: "shell on " + svc + " closed"}
	})
}

func (t *servicesTab) toggleDebug(m *model, svc string) tea.Cmd {
	if t.debug == nil {
		t.debug = map[string]core.DebugSession{}
	}
	if sess, ok := t.debug[svc]; ok {
		delete(t.debug, svc)
		return m.do("detach debugger from "+svc, func(context.Context) error {
			if sess.Close != nil {
				return sess.Close()
			}
			return nil
		})
	}
	a, s, gen, ctx := m.app, m.app.Spec.Services[svc], m.gen, m.ctx
	m.busy++
	m.setStatus("attaching debugger to "+svc+"…", false)
	return func() tea.Msg {
		d, _, err := engine.Get[core.Debugger](a, core.KindDebugger, "")
		if err != nil {
			return resultMsg{gen: gen, status: "debug: " + err.Error(), err: true}
		}
		sess, err := d.Attach(ctx, s, "")
		if err != nil {
			return resultMsg{gen: gen, status: "debug: " + err.Error(), err: true}
		}
		text := sAccent.Render("debugger on "+sess.Addr) + "\n" + sess.Hint + "\n" + sDim.Render("D again detaches")
		return resultMsg{gen: gen, text: text, status: "debugger on " + sess.Addr, debug: &debugAttach{svc: svc, sess: sess}}
	}
}

func (t *servicesTab) view(m *model, w, h int) string {
	listH := h * 3 / 5
	if len(m.services) < listH-3 {
		listH = max(len(m.services)+3, 6)
	}
	detailH := h - listH

	cols := []string{"", "SERVICE", "ROLE", "READY", "RESTARTS", "CPU", "MEMORY", "IMAGE", "MESSAGE"}
	widths := []int{1, 22, 6, 7, 8, 7, 8, 34, max(10, w-4-1-22-6-7-8-7-8-34-9)}
	var rows [][]string
	for _, st := range m.services {
		s := m.app.Spec.Services[st.Service]
		var cpu float64
		var mem int64
		restarts := 0
		for _, in := range st.Instances {
			cpu += in.CPU
			mem += in.Memory
			restarts += in.Restarts
		}
		cpuText := "-"
		if cpu > 0 {
			cpuText = fmt.Sprintf("%.0fm", cpu*1000)
		}
		rs := strconv.Itoa(restarts)
		if restarts > 0 {
			rs = sAmber.Render(rs)
		}
		name := st.Service
		if _, dbg := t.debug[st.Service]; dbg {
			name += sAccent.Render(" ⬢")
		}
		msg := st.Message
		if msg != "" {
			msg = sAmber.Render(msg)
		}
		rows = append(rows, []string{stateDot(st.State), name, s.Role, fmt.Sprintf("%d/%d", st.Ready, st.Desired), rs, cpuText, bytesText(mem), lastPath(st.Image), msg})
	}
	t.sel = min(t.sel, max(0, len(rows)-1))
	t.offset = scroll(t.sel, t.offset, listH-3, len(rows))
	list := panel(fmt.Sprintf("services · %d", len(rows)), table(cols, widths, rows, t.sel, t.offset, listH-2), w, listH, true)

	st, ok := t.current(m)
	if !ok {
		return list
	}
	s := m.app.Spec.Services[st.Service]
	var b strings.Builder
	b.WriteString(sTitle.Render(st.Service) + "  " + stateText(st.State))
	if len(s.Groups) > 0 {
		b.WriteString(sDim.Render("  groups " + strings.Join(s.Groups, ",")))
	}
	if len(s.DependsOn) > 0 {
		b.WriteString(sDim.Render("  needs " + strings.Join(s.DependsOn, ",")))
	}
	if len(s.Ports) > 0 {
		var ps []string
		for _, k := range engine.SortedKeys(s.Ports) {
			ps = append(ps, fmt.Sprintf("%s:%d", k, s.Ports[k]))
		}
		b.WriteString(sDim.Render("  ports " + strings.Join(ps, " ")))
	}
	b.WriteString("\n")
	if st.Image != "" {
		b.WriteString(sDim.Render("image ") + st.Image + "\n")
	}
	icol := []string{"", "INSTANCE", "HOST", "IP", "READY", "RESTARTS", "AGE", "CPU", "MEMORY"}
	iw := []int{1, 30, 22, 13, 5, 8, 8, 6, 7}
	var irows [][]string
	for _, in := range st.Instances {
		age := "-"
		if !in.Started.IsZero() {
			age = time.Since(in.Started).Round(time.Second).String()
		}
		cpu := "-"
		if in.CPU > 0 {
			cpu = fmt.Sprintf("%.0fm", in.CPU*1000)
		}
		irows = append(irows, []string{stateDot(in.State), in.ID, in.Host, in.IP, fmt.Sprint(in.Ready), strconv.Itoa(in.Restarts), age, cpu, bytesText(in.Memory)})
	}
	left := b.String() + table(icol, iw, irows, -1, 0, detailH-5)
	lw := w / 2
	right, title := t.detail, "result"
	if right == "" {
		title = "recent logs"
		var lb strings.Builder
		if t.logsFor == st.Service {
			lines := t.logs
			if keep := detailH - 2; len(lines) > keep {
				lines = lines[len(lines)-keep:]
			}
			for _, l := range lines {
				lb.WriteString(sDim.Render(l.Time.Local().Format("15:04:05")) + " " + pretty(l.Text) + "\n")
			}
		}
		right = lb.String()
		if right == "" {
			right = sDim.Render("no log lines")
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, lipgloss.JoinHorizontal(lipgloss.Top,
		panel("detail", left, lw, detailH, false), panel(title, right, w-lw, detailH, false)))
}

func lastPath(img string) string {
	if i := strings.LastIndex(img, "/"); i >= 0 {
		return img[i+1:]
	}
	return img
}
