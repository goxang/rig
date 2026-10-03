package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

// servicesTab lists services; enter opens one: its instances and its live log, which is only
// streamed while the service is open.
type servicesTab struct {
	list   *grid
	marked map[string]bool
	filter string

	open_  string // the service being shown, "" for the list
	pods   *grid
	logs   []core.LogLine
	logFor string
	stream int
	cancel context.CancelFunc
	logErr string
	detail string
	debug  map[string]core.DebugSession
}

type svcLogStart struct {
	gen, stream int
	ch          <-chan core.LogLine
	err         error
}

type svcLogBatch struct {
	gen, stream int
	lines       []core.LogLine
	done        bool
	ch          <-chan core.LogLine
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

func (t *servicesTab) name() string { return "Services" }
func (t *servicesTab) typing() bool { return false }

func (t *servicesTab) open(m *model) tea.Cmd {
	t.list = newGrid("svc", col("", 1), col("", 1), col("SERVICE", 30), col("GROUPS", 18), rcol("READY", 7), rcol("RESTARTS", 8),
		rcol("CPU", 6), rcol("MEMORY", 7), col("IMAGE", 28), col("MESSAGE", 0))
	t.list.sortBy = 2
	t.pods = newGrid("pods", col("", 1), col("INSTANCE", 0), col("HOST", 18), rcol("READY", 5), rcol("RESTARTS", 8), rcol("AGE", 8), rcol("CPU", 6), rcol("MEMORY", 7))
	t.marked = map[string]bool{}
	t.debug = map[string]core.DebugSession{}
	return nil
}

func (t *servicesTab) refresh(m *model) tea.Cmd { return nil }

func (t *servicesTab) hints() [][2]string {
	if t.open_ != "" {
		return [][2]string{{"esc", "back"}, {"enter", "logs of instance"}, {"r", "restart"}, {"s/x", "start/stop"}, {"+/-", "scale"},
			{"d", "deploy"}, {"b", "build+deploy"}, {"e", "shell"}, {"p", "profile"}, {"D", "debug"}, {"l", "logs screen"}}
	}
	return [][2]string{{"enter", "open"}, {"space", "mark"}, {"a", "mark all"}, {"r", "restart"}, {"s/x", "start/stop"}, {"+/-", "scale"},
		{"d", "deploy"}, {"b", "build+deploy"}, {"/", "filter"}, {"< >", "sort"}}
}

// targets are the marked services, else the selected (or open) one.
func (t *servicesTab) targets() []string {
	if t.open_ != "" {
		return []string{t.open_}
	}
	var out []string
	for n, on := range t.marked {
		if on {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		if r, ok := t.list.current(); ok {
			out = []string{r.id}
		}
	}
	return out
}

func label(verb string, names []string) string {
	if len(names) == 1 {
		return verb + " " + names[0]
	}
	return fmt.Sprintf("%s %d services", verb, len(names))
}

func (t *servicesTab) status(m *model, name string) core.Status {
	for _, s := range m.services {
		if s.Service == name {
			return s
		}
	}
	return core.Status{Service: name}
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
			t.debug[msg.debug.svc] = msg.debug.sess
		}
		return nil
	case svcLogStart:
		if msg.gen != m.gen || msg.stream != t.stream {
			return nil
		}
		if msg.err != nil {
			t.logErr = msg.err.Error()
			return nil
		}
		return nextSvcLog(msg.gen, msg.stream, msg.ch)
	case svcLogBatch:
		if msg.gen != m.gen || msg.stream != t.stream {
			return nil
		}
		t.logs = append(t.logs, msg.lines...)
		if len(t.logs) > 2000 {
			t.logs = t.logs[len(t.logs)-2000:]
		}
		if msg.done {
			return nil
		}
		return nextSvcLog(msg.gen, msg.stream, msg.ch)
	case tea.KeyMsg:
		if t.open_ != "" {
			return t.detailKey(m, msg)
		}
		return t.listKey(m, msg)
	}
	return nil
}

func (t *servicesTab) listKey(m *model, k tea.KeyMsg) tea.Cmd {
	if t.list.key(k) {
		return nil
	}
	switch k.String() {
	case "enter":
		if r, ok := t.list.current(); ok {
			return t.openService(m, r.id, "")
		}
	case " ":
		if r, ok := t.list.current(); ok {
			t.marked[r.id] = !t.marked[r.id]
			t.list.sel = min(t.list.sel+1, len(t.list.rows)-1)
		}
	case "a":
		all := true
		for _, r := range t.list.rows {
			all = all && t.marked[r.id]
		}
		for _, r := range t.list.rows {
			t.marked[r.id] = !all
		}
	case "/":
		m.ask("filter services (name or group)", t.filter, func(v string) tea.Cmd {
			t.filter = strings.TrimSpace(v)
			return nil
		})
	case "esc":
		t.marked, t.filter = map[string]bool{}, ""
	default:
		return t.ops(m, k.String(), t.targets())
	}
	return nil
}

// ops are the actions that work on one service or on every marked one.
func (t *servicesTab) ops(m *model, key string, names []string) tea.Cmd {
	if len(names) == 0 {
		return nil
	}
	a := m.app
	switch key {
	case "s":
		return m.act(label("start", names), false, func(ctx context.Context) error {
			return each(names, func(n string) error { return a.Start(ctx, n) })
		})
	case "x":
		return m.act(label("stop", names), true, func(ctx context.Context) error {
			return each(names, func(n string) error { return a.Stop(ctx, n) })
		})
	case "r":
		return m.act(label("restart", names), false, func(ctx context.Context) error {
			return each(names, func(n string) error { return a.Restart(ctx, n) })
		})
	case "+", "=":
		return m.act(label("scale up", names), false, func(ctx context.Context) error { return a.ScaleBy(ctx, names, 1, false) })
	case "-":
		dangerous := false
		for _, n := range names {
			dangerous = dangerous || t.status(m, n).Desired <= 1
		}
		return m.act(label("scale down", names), dangerous, func(ctx context.Context) error { return a.ScaleBy(ctx, names, -1, false) })
	case "d":
		m.ask(label("deploy", names)+": image tag (empty: the last built one)", "", func(tag string) tea.Cmd {
			tag = strings.TrimSpace(tag)
			return m.act(label("deploy", names)+tagNote(tag), true, func(ctx context.Context) error {
				return each(names, func(n string) error { return a.Deploy(ctx, n, tag) })
			})
		})
	case "b":
		return m.act(label("build and deploy", names), true, func(ctx context.Context) error {
			tag := time.Now().Format("20060102-150405")
			err := each(names, func(n string) error {
				rt, s, err := a.Owner(n)
				if err != nil || s.Build == nil || !a.ImageBased() {
					if err == nil {
						err = rt.Deploy(ctx, s, core.Release{})
					}
					return err
				}
				img, err := a.Build(ctx, s, tag, nil)
				if err != nil {
					return err
				}
				return rt.Deploy(ctx, s, core.Release{Image: img})
			})
			if err != nil {
				return err
			}
			return a.SetState(ctx, map[string]string{"tag": tag})
		})
	case "l":
		for i, tb := range m.tabs {
			if lt, ok := tb.(*logsTab); ok {
				lt.services, lt.instance = names, ""
				lt.restart = true
				return m.openTab(i)
			}
		}
	}
	return nil
}

func tagNote(tag string) string {
	if tag == "" {
		return ""
	}
	return " at " + tag
}

func each(names []string, f func(string) error) error {
	errs := make([]error, len(names))
	done := make(chan struct{}, len(names))
	for i, n := range names {
		go func() {
			if err := f(n); err != nil {
				errs[i] = fmt.Errorf("%s: %w", n, err)
			}
			done <- struct{}{}
		}()
	}
	for range names {
		<-done
	}
	var msgs []string
	for _, e := range errs {
		if e != nil {
			msgs = append(msgs, e.Error())
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return nil
}

// openService shows one service and streams its log (or one instance's) until it is closed.
func (t *servicesTab) openService(m *model, name, instance string) tea.Cmd {
	t.closeLogs()
	t.open_, t.logs, t.logErr, t.detail = name, nil, "", ""
	t.logFor = instance
	t.stream++
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	a, gen, stream := m.app, m.gen, t.stream
	return func() tea.Msg {
		ch, err := a.Logs(ctx, name, core.LogOptions{Follow: true, Tail: 200, Instance: instance})
		return svcLogStart{gen: gen, stream: stream, ch: ch, err: err}
	}
}

func (t *servicesTab) closeLogs() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

func nextSvcLog(gen, stream int, ch <-chan core.LogLine) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return svcLogBatch{gen: gen, stream: stream, done: true}
		}
		batch := []core.LogLine{first}
		deadline := time.After(100 * time.Millisecond)
		for len(batch) < 500 {
			select {
			case l, ok := <-ch:
				if !ok {
					return svcLogBatch{gen: gen, stream: stream, lines: batch, done: true}
				}
				batch = append(batch, l)
			case <-deadline:
				return svcLogBatch{gen: gen, stream: stream, lines: batch, ch: ch}
			}
		}
		return svcLogBatch{gen: gen, stream: stream, lines: batch, ch: ch}
	}
}

func (t *servicesTab) detailKey(m *model, k tea.KeyMsg) tea.Cmd {
	if t.pods.key(k) {
		return nil
	}
	name := t.open_
	switch k.String() {
	case "esc", "backspace", "left":
		t.closeLogs()
		t.open_ = ""
		return nil
	case "enter":
		if r, ok := t.pods.current(); ok {
			inst := r.id
			if t.logFor == inst {
				inst = ""
			}
			return t.openService(m, name, inst)
		}
	case "e":
		inst := ""
		if r, ok := t.pods.current(); ok {
			inst = r.id
		}
		return t.shell(m, name, inst)
	case "c":
		t.logs = nil
	case "p":
		return t.profile(m, name)
	case "D":
		return t.toggleDebug(m, name)
	default:
		return t.ops(m, k.String(), []string{name})
	}
	return nil
}

func (t *servicesTab) click(m *model, h hit) tea.Cmd {
	if t.open_ == "" {
		if t.list.click(h) && h.double {
			if r, ok := t.list.current(); ok {
				return t.openService(m, r.id, "")
			}
		}
		if h.id == "svc:mark" {
			if i := t.list.offset + h.y; i < len(t.list.rows) {
				id := t.list.rows[i].id
				t.marked[id] = !t.marked[id]
			}
		}
		return nil
	}
	if h.id == "back" {
		t.closeLogs()
		t.open_ = ""
		return nil
	}
	if t.pods.click(h) && h.double {
		return t.detailKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	return nil
}

// shell hands the terminal to `rig exec` for the service; the TUI comes back when it exits.
func (t *servicesTab) shell(m *model, svc, instance string) tea.Cmd {
	self, err := os.Executable()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	args := []string{"-f", m.app.Spec.File, "-e", m.app.Env.Name, "exec", svc}
	if instance != "" {
		args = append(args, "-i", instance)
	}
	return tea.ExecProcess(exec.Command(self, args...), func(err error) tea.Msg {
		if err != nil {
			return statusMsg{text: "shell: " + err.Error(), err: true}
		}
		return statusMsg{text: "shell on " + svc + " closed"}
	})
}

func (t *servicesTab) profile(m *model, svc string) tea.Cmd {
	s := m.app.Spec.Services[svc]
	m.ask("profile "+svc+" (kind duration)", "cpu 10s", func(v string) tea.Cmd {
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
		m.setStatus(fmt.Sprintf("profiling %s (%s, %s)…", svc, kind, dur), false)
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
	return nil
}

func (t *servicesTab) toggleDebug(m *model, svc string) tea.Cmd {
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

func usage(st core.Status) (cpu float64, mem int64, restarts int) {
	for _, in := range st.Instances {
		cpu += in.CPU
		mem += in.Memory
		restarts += in.Restarts
	}
	return
}

func cpuText(c float64) string {
	if c <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0fm", c*1000)
}

func (t *servicesTab) rows(m *model) []grow {
	var rows []grow
	f := strings.ToLower(t.filter)
	for _, st := range m.services {
		s := m.app.Spec.Services[st.Service]
		if s == nil {
			continue
		}
		groups := strings.Join(s.Groups, ",")
		if f != "" && !strings.Contains(strings.ToLower(st.Service+" "+groups+" "+s.Role), f) {
			continue
		}
		cpu, mem, restarts := usage(st)
		name := st.Service
		if _, dbg := t.debug[st.Service]; dbg {
			name += sAccent.Render(" ⬢")
		}
		if m.app.SharedElsewhere(st.Service) {
			name += sDim.Render(" ⇄" + m.app.Env.Infra)
		} else if s.Role != "app" {
			name += sDim.Render(" " + s.Role)
		}
		rs := fmt.Sprint(restarts)
		if restarts > 0 {
			rs = sAmber.Render(rs)
		}
		msg := st.Message
		if msg != "" {
			msg = sAmber.Render(msg)
		}
		mark := " "
		if t.marked[st.Service] {
			mark = sAccent.Render("●")
		}
		rows = append(rows, grow{id: st.Service,
			cells: []string{mark, stateDot(st.State), name, sDim.Render(groups), fmt.Sprintf("%d/%d", st.Ready, st.Desired), rs, cpuText(cpu), bytesText(mem), tagOf(st.Image), msg},
			keys:  []any{nil, string(st.State), st.Service, groups, float64(st.Ready), float64(restarts), cpu, float64(mem), nil, nil}})
	}
	return rows
}

func tagOf(img string) string {
	img = lastPath(img)
	if len(img) > 28 {
		return "…" + img[len(img)-27:]
	}
	return img
}

func (t *servicesTab) view(m *model, w, h int) string {
	if t.open_ != "" {
		return t.serviceView(m, w, h)
	}
	t.list.set(t.rows(m))
	marked := 0
	for _, on := range t.marked {
		if on {
			marked++
		}
	}
	title := fmt.Sprintf("services · %d", len(t.list.rows))
	if t.filter != "" {
		title += " · filter " + t.filter
	}
	if marked > 0 {
		title += fmt.Sprintf(" · %d marked (actions apply to all; esc clears)", marked)
	}
	if len(m.services) == 0 {
		return panel(title, sDim.Render("asking the runtime…"), w, h, true)
	}
	body := t.list.view(m, 1, 1, w-2, h-2, true)
	m.zone("svc:mark", 1, 2, 2, t.list.shown)
	return panel(title, body, w, h, true)
}

func (t *servicesTab) serviceView(m *model, w, h int) string {
	name := t.open_
	st := t.status(m, name)
	s := m.app.Spec.Services[name]
	var b strings.Builder
	b.WriteString(sKey.Render("‹ back") + "  " + sTitle.Render(name) + "  " + stateText(st.State) + fmt.Sprintf("  %d/%d ready", st.Ready, st.Desired))
	m.zone("back", 1, 0, 6, 1)
	if len(s.Groups) > 0 {
		b.WriteString(sDim.Render("  groups " + strings.Join(s.Groups, ",")))
	}
	if len(s.DependsOn) > 0 {
		b.WriteString(sDim.Render("  needs " + strings.Join(s.DependsOn, ",")))
	}
	b.WriteString("\n")
	if st.Image != "" {
		b.WriteString(sDim.Render("image ") + st.Image)
	}
	if len(s.Ports) > 0 {
		var ps []string
		for _, k := range engine.SortedKeys(s.Ports) {
			ps = append(ps, fmt.Sprintf("%s:%d", k, s.Ports[k]))
		}
		b.WriteString(sDim.Render("   ports " + strings.Join(ps, " ")))
	}
	if st.Message != "" {
		b.WriteString("\n" + sAmber.Render(st.Message))
	}
	head := b.String()
	headH := lipgloss.Height(head)

	var rows []grow
	for _, in := range st.Instances {
		age := "-"
		if !in.Started.IsZero() {
			age = shortAge(time.Since(in.Started))
		}
		id := in.ID
		if t.logFor == in.ID {
			id = sAccent.Render("▸ ") + id
		}
		rows = append(rows, grow{id: in.ID, cells: []string{stateDot(in.State), id, in.Host, fmt.Sprint(in.Ready), fmt.Sprint(in.Restarts), age, cpuText(in.CPU), bytesText(in.Memory)},
			keys: []any{nil, in.ID, in.Host, nil, float64(in.Restarts), float64(-in.Started.Unix()), in.CPU, float64(in.Memory)}})
	}
	t.pods.set(rows)
	podsH := min(len(rows)+3, max(5, (h-headH)/3))
	podsBox := panel(fmt.Sprintf("instances · %d", len(rows)), t.pods.view(m, 1, headH+2, w-2, podsH-2, true), w, podsH, true)

	logH := h - headH - podsH - 1
	src := "all instances"
	if t.logFor != "" {
		src = t.logFor
	}
	var lb strings.Builder
	switch {
	case t.detail != "":
		lb.WriteString(t.detail)
	case t.logErr != "":
		lb.WriteString(sRed.Render(wrap(t.logErr, w-4)))
	case len(t.logs) == 0:
		lb.WriteString(sDim.Render("waiting for log lines…"))
	default:
		lines := t.logs
		if keep := logH - 2; len(lines) > keep {
			lines = lines[len(lines)-keep:]
		}
		for _, l := range lines {
			inst := ""
			if t.logFor == "" && len(st.Instances) > 1 && l.Instance != "" {
				inst = lipgloss.NewStyle().Foreground(colorFor(l.Instance)).Render(shortInstance(l.Instance)) + " "
			}
			lb.WriteString(sDim.Render(l.Time.Local().Format("15:04:05")) + " " + inst + pretty(l.Text) + "\n")
		}
	}
	logs := panel("log · "+src+" · following", strings.TrimRight(lb.String(), "\n"), w, logH, false)
	return lipgloss.JoinVertical(lipgloss.Left, " "+head, "", podsBox, logs)
}

func shortInstance(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 && len(id)-i <= 6 {
		return id[i+1:]
	}
	return id
}

func shortAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func lastPath(img string) string {
	if i := strings.LastIndex(img, "/"); i >= 0 {
		return img[i+1:]
	}
	return img
}
