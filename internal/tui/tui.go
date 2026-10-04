// Package tui is rig's terminal control plane: one screen per concern (services, logs, metrics,
// traces, queries, key-value, data, load, manifests, hosts), all driven by the same engine as the CLI.
// Screens load nothing until opened and refresh only while showing; the service list is one runtime
// call per refresh.
package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

// tab is one screen. Messages a tab sends itself must carry gen so results that arrive after an
// environment switch are dropped.
type tab interface {
	name() string
	open(m *model) tea.Cmd
	refresh(m *model) tea.Cmd
	update(m *model, msg tea.Msg) tea.Cmd
	view(m *model, w, h int) string
	hints() [][2]string
	typing() bool
}

// interval is how often a showing tab refreshes; tabs without one refresh every 3s.
type intervaler interface{ interval() time.Duration }

// clicker receives clicks on the zones a tab registered while rendering.
type clicker interface {
	click(m *model, z hit) tea.Cmd
}

// hit is a click inside a zone, in zone-relative cells; double is a second click on the same cell.
type hit struct {
	id     string
	x, y   int
	double bool
}

type zone struct {
	id         string
	x, y, w, h int
}

type model struct {
	ctx    context.Context
	app    *engine.App
	gen    int
	tabs   []tab
	active int
	opened map[int]bool
	w, h   int

	status    string
	statusErr bool
	statusAt  time.Time
	busy      int

	confirm *confirm
	prompt  *prompt
	picker  *picker
	help    bool
	// session is the saved session this run continues (S, rig resume); quitting updates it
	session *Session
	// mouseOff hands the mouse to the terminal, so text can be selected and copied
	mouseOff bool

	services []core.Status
	svcAt    time.Time
	svcBusy  bool

	alerts     []engine.Firing
	alertErrs  []error
	alertsAt   time.Time
	alertsBusy bool
	showAlerts bool

	refreshed map[int]time.Time
	sched     *scheduler

	zones   []zone
	originY int
	tabSpan [][2]int
	lastHit hit
	lastAt  time.Time
}

type confirm struct {
	text string
	run  tea.Cmd
}

type prompt struct {
	label  string
	input  textinput.Model
	submit func(string) tea.Cmd
}

type (
	tickMsg   struct{}
	statusMsg struct {
		text string
		err  bool
	}
	servicesMsg struct {
		gen int
		sts []core.Status
	}
	alertsMsg struct {
		gen    int
		firing []engine.Firing
		errs   []error
	}
	envMsg struct {
		app *engine.App
		err error
	}
)

func newTabs() []tab {
	return []tab{&servicesTab{}, &logsTab{}, &metricsTab{}, &tracesTab{}, &queriesTab{}, &kvTab{}, &dataTab{}, &loadTab{}, &manifestsTab{}, &hostsTab{}}
}

func Run(ctx context.Context, a *engine.App) error { return run(ctx, a, nil) }

func run(ctx context.Context, a *engine.App, s *Session) error {
	if a.Env == nil {
		return fmt.Errorf("no environment: define one under environments: and set default:")
	}
	m := &model{ctx: ctx, app: a, opened: map[int]bool{}, tabs: newTabs(), refreshed: map[int]time.Time{}}
	m.sched = newScheduler(a)
	if s != nil {
		m.restore(s)
	}
	program = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err := program.Run()
	if err == tea.ErrProgramKilled {
		return nil
	}
	return err
}

func (m *model) Init() tea.Cmd {
	return batch(m.openTab(m.active), m.fetchServices(), tick())
}

var program *tea.Program

// osc52 puts text on the terminal's clipboard.
func osc52(text string) {
	fmt.Fprint(os.Stdout, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\a")
}

// batch runs cmds concurrently. tea.Batch must not be used: bubbletea 1.3.7 runs a batch's commands
// inside its event loop, so a Tick or a kubectl call in one froze the keys for seconds (fixed in 1.3.10,
// which needs Go 1.24).
func batch(cmds ...tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		for _, c := range cmds {
			if c != nil {
				go func() { program.Send(c()) }()
			}
		}
		return nil
	}
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) openTab(i int) tea.Cmd {
	m.active = i
	m.refreshed[i] = time.Now()
	if !m.opened[i] {
		m.opened[i] = true
		return m.tabs[i].open(m)
	}
	return m.tabs[i].refresh(m)
}

// do runs a slow operation off the UI loop and reports its outcome in the footer.
func (m *model) do(label string, f func(ctx context.Context) error) tea.Cmd {
	m.busy++
	m.setStatus(label+"…", false)
	ctx := m.ctx
	return func() tea.Msg {
		if err := f(ctx); err != nil {
			return statusMsg{text: label + ": " + err.Error(), err: true}
		}
		return statusMsg{text: label + " ✓"}
	}
}

// needsConfirm: only dangerous changes on a real Kubernetes cluster, and every change on a protected
// environment, wait for enter. Local, docker and kind run at once.
func (m *model) needsConfirm(dangerous bool) bool {
	e := m.app.Env
	return e.Protected || dangerous && e.Runtime != nil && e.Runtime.Type == "kubernetes"
}

// act runs a change, asking first when needsConfirm says so; the answer also counts as the
// confirmation a protected environment needs.
func (m *model) act(label string, dangerous bool, f func(ctx context.Context) error) tea.Cmd {
	a, ctx := m.app, core.WithConfirmed(m.ctx)
	run := func() tea.Msg {
		a.Confirmed = true
		defer func() { a.Confirmed = false }()
		if err := f(ctx); err != nil {
			return statusMsg{text: label + ": " + err.Error(), err: true}
		}
		return statusMsg{text: label + " ✓"}
	}
	if !m.needsConfirm(dangerous) {
		m.busy++
		m.setStatus(label+"…", false)
		return run
	}
	text := label + " on " + m.app.Env.Name + "?"
	if m.app.Env.Protected {
		text = label + " on PROTECTED " + m.app.Env.Name + "?"
	}
	m.confirm = &confirm{text: text, run: run}
	return nil
}

func (m *model) ask(label, value string, submit func(string) tea.Cmd) {
	in := textinput.New()
	in.Prompt = ""
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	m.prompt = &prompt{label: label, input: in, submit: submit}
}

func (m *model) setStatus(s string, err bool) {
	m.status, m.statusErr, m.statusAt = s, err, time.Now()
}

func (m *model) fetchServices() tea.Cmd {
	if m.svcBusy {
		return nil
	}
	m.svcBusy = true
	gen, a, ctx := m.gen, m.app, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return servicesMsg{gen: gen, sts: a.StatusAll(c, a.Spec.ServiceNames())}
	}
}

func (m *model) fetchAlerts() tea.Cmd {
	m.alertsBusy = true
	gen, a, ctx := m.gen, m.app, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		f, errs := a.CheckAlerts(c)
		return alertsMsg{gen: gen, firing: f, errs: errs}
	}
}

// alertBadge is the header's summary of what is over its thresholds.
func (m *model) alertBadge() string {
	if len(m.alerts) == 0 {
		return ""
	}
	top := m.alerts[0]
	text := " ⚠ " + top.String()
	if n := len(m.alerts) - 1; n > 0 {
		text += fmt.Sprintf(" +%d", n)
	}
	text += " (A) "
	bg := cAmber
	if top.Level == engine.LevelCrit {
		bg = cRed
	}
	return lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color("#000000")).Bold(true).Render(text)
}

func (m *model) alertsView() string {
	var b strings.Builder
	for _, f := range m.alerts {
		st := sAmber
		if f.Level == engine.LevelCrit {
			st = sRed
		}
		b.WriteString(st.Render("⚠ "+f.String()) + "\n")
	}
	for _, e := range m.alertErrs {
		b.WriteString(sDim.Render(e.Error()) + "\n")
	}
	if b.Len() == 0 {
		b.WriteString(sGreen.Render("nothing over its thresholds") + "\n")
	}
	var rules []string
	for _, r := range m.app.AlertRules() {
		src := r.Source
		if r.Metric != "" {
			src += " " + r.Metric
		}
		rules = append(rules, fmt.Sprintf("%s: %s, warn %v crit %v%s", r.Name, src, r.Warn, r.Crit, r.Unit))
	}
	b.WriteString("\n" + sDim.Render("checked every 15s · rules (alerts: in rig.yaml):\n"+strings.Join(rules, "\n")))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render("alerts") + "\n\n" + b.String())
}

// zone registers a clickable area of the tab body being rendered (body-relative cells).
func (m *model) zone(id string, x, y, w, h int) {
	m.zones = append(m.zones, zone{id: id, x: x, y: y + m.originY, w: w, h: h})
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		cmds := []tea.Cmd{tick(), m.sched.due(m)}
		if time.Since(m.svcAt) >= 3*time.Second {
			cmds = append(cmds, m.fetchServices())
		}
		if !m.alertsBusy && time.Since(m.alertsAt) >= 15*time.Second {
			cmds = append(cmds, m.fetchAlerts())
		}
		every := 3 * time.Second
		if iv, ok := m.tabs[m.active].(intervaler); ok {
			every = iv.interval()
		}
		if every > 0 && time.Since(m.refreshed[m.active]) >= every {
			m.refreshed[m.active] = time.Now()
			cmds = append(cmds, m.tabs[m.active].refresh(m))
		}
		return m, batch(cmds...)
	case alertsMsg:
		if msg.gen == m.gen {
			m.alerts, m.alertErrs = msg.firing, msg.errs
		}
		m.alertsAt, m.alertsBusy = time.Now(), false
		return m, nil
	case servicesMsg:
		if msg.gen == m.gen {
			m.services, m.svcAt, m.svcBusy = msg.sts, time.Now(), false
		}
		return m, nil
	case statusMsg:
		m.busy = max(0, m.busy-1)
		m.setStatus(msg.text, msg.err)
		m.svcAt = time.Time{}
		return m, m.tabs[m.active].refresh(m)
	case schedMsg:
		m.sched.done(msg)
		return m, nil
	case envMsg:
		if msg.err != nil {
			m.setStatus("switch environment: "+msg.err.Error(), true)
			return m, nil
		}
		old := m.app
		m.app, m.gen = msg.app, m.gen+1
		go old.Close()
		m.services, m.svcBusy = nil, false
		m.opened, m.refreshed = map[int]bool{}, map[int]time.Time{}
		m.tabs = newTabs()
		m.sched = newScheduler(m.app)
		m.setStatus("environment "+m.app.Env.Name, false)
		return m, batch(m.openTab(m.active), m.fetchServices())
	case tea.KeyMsg:
		return m, m.key(msg)
	case tea.MouseMsg:
		return m, m.mouse(msg)
	}
	// data arriving for a tab must reach it even when another tab is showing
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		cmds = append(cmds, t.update(m, msg))
	}
	return m, batch(cmds...)
}

func (m *model) mouse(e tea.MouseMsg) tea.Cmd {
	if m.confirm != nil || m.prompt != nil {
		return nil
	}
	switch e.Button {
	case tea.MouseButtonWheelUp:
		if m.picker != nil {
			return m.picker.key(m, tea.KeyMsg{Type: tea.KeyUp})
		}
		return m.tabs[m.active].update(m, tea.KeyMsg{Type: tea.KeyUp})
	case tea.MouseButtonWheelDown:
		if m.picker != nil {
			return m.picker.key(m, tea.KeyMsg{Type: tea.KeyDown})
		}
		return m.tabs[m.active].update(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if e.Action != tea.MouseActionPress || e.Button != tea.MouseButtonLeft {
		return nil
	}
	if m.help {
		m.help = false
		return nil
	}
	if e.Y == 1 && m.picker == nil {
		for i, sp := range m.tabSpan {
			if e.X >= sp[0] && e.X < sp[1] {
				return m.openTab(i)
			}
		}
		return nil
	}
	for i := len(m.zones) - 1; i >= 0; i-- {
		z := m.zones[i]
		if e.X >= z.x && e.X < z.x+z.w && e.Y >= z.y && e.Y < z.y+z.h {
			h := hit{id: z.id, x: e.X - z.x, y: e.Y - z.y}
			h.double = m.lastHit.id == h.id && m.lastHit.y == h.y && time.Since(m.lastAt) < 400*time.Millisecond
			m.lastHit, m.lastAt = h, time.Now()
			if m.picker != nil {
				return m.picker.click(m, h)
			}
			if c, ok := m.tabs[m.active].(clicker); ok {
				return c.click(m, h)
			}
			return nil
		}
	}
	return nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	if k.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if k.String() == "enter" {
			m.busy++
			m.setStatus(strings.TrimSuffix(c.text, "?")+"…", false)
			return c.run
		}
		m.setStatus("cancelled", false)
		return nil
	}
	if m.prompt != nil {
		switch k.String() {
		case "esc":
			m.prompt = nil
			return nil
		case "enter":
			p := m.prompt
			m.prompt = nil
			return p.submit(p.input.Value())
		}
		var cmd tea.Cmd
		m.prompt.input, cmd = m.prompt.input.Update(k)
		return cmd
	}
	if m.picker != nil {
		switch k.String() {
		case "tab", "shift+tab":
			m.picker = nil
		default:
			return m.picker.key(m, k)
		}
	}
	if m.help || m.showAlerts {
		m.help, m.showAlerts = false, false
		return nil
	}
	t := m.tabs[m.active]
	if !t.typing() {
		switch s := k.String(); s {
		case "q":
			if m.session != nil {
				_, _ = m.saveSession()
			}
			return tea.Quit
		case "S":
			id, err := m.saveSession()
			if err != nil {
				m.setStatus("save session: "+err.Error(), true)
			} else {
				m.setStatus("session saved: rig resume "+id+" (quitting with q keeps it current)", false)
			}
			return nil
		case "?", "f1":
			m.help = true
			return nil
		case "A":
			m.showAlerts = true
			return nil
		case "E":
			m.pickEnv()
			return nil
		case "T":
			m.pickTask("")
			return nil
		case "M":
			m.mouseOff = !m.mouseOff
			if m.mouseOff {
				m.setStatus("mouse off: select and copy with the mouse; M turns it back on", false)
				return tea.DisableMouse
			}
			m.setStatus("mouse on", false)
			return tea.EnableMouseCellMotion
		case "tab":
			return m.openTab((m.active + 1) % len(m.tabs))
		case "shift+tab":
			return m.openTab((m.active + len(m.tabs) - 1) % len(m.tabs))
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
			i := int(s[0]-'0') - 1
			if s == "0" {
				i = 9
			}
			if i < len(m.tabs) {
				return m.openTab(i)
			}
		}
	}
	return t.update(m, k)
}

// pickTask runs a rig.yaml task (clear-db, ship, ...) in the terminal: tasks print as they go and
// may read input, so the TUI steps aside until it ends. Enter on the confirmation is the task's --yes.
// prefix narrows the list (the KV screen's kv-*).
func (m *model) pickTask(prefix string) {
	var names []string
	for _, n := range m.app.TaskNames() {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		m.setStatus("no "+prefix+"* tasks in "+m.app.Spec.File, true)
		return
	}
	tasks := m.app.Tasks()
	var desc []string
	for _, n := range names {
		var steps []string
		for _, st := range tasks[n] {
			first, _, _ := strings.Cut(strings.TrimSpace(st), "\n")
			if !strings.HasPrefix(first, `[ -n "${RIG_YES`) {
				steps = append(steps, first)
			}
		}
		desc = append(desc, strings.Join(steps, " && "))
	}
	m.pick("task", names, desc, 0, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		name := chosen[0]
		run := func() tea.Msg {
			self, err := os.Executable()
			if err != nil {
				return statusMsg{text: err.Error(), err: true}
			}
			script := `"$0" "$@"; rc=$?; echo; [ $rc = 0 ] && echo "✓ done" || echo "✖ failed ($rc)"; printf "press enter "; read _; exit $rc`
			cmd := exec.Command("sh", "-c", script, self, "-f", m.app.Spec.File, "-e", m.app.Env.Name, "--yes", "task", name)
			return tea.ExecProcess(cmd, func(err error) tea.Msg {
				if err != nil {
					return statusMsg{text: "task " + name + ": " + err.Error(), err: true}
				}
				return statusMsg{text: "task " + name + " ✓"}
			})()
		}
		m.confirm = &confirm{text: "run task " + name + " on " + m.app.Env.Name + "?", run: run}
		return nil
	})
}

func (m *model) pickEnv() {
	names := m.app.Spec.EnvironmentNames()
	var desc []string
	sel := 0
	for i, n := range names {
		e := m.app.Spec.Environments[n]
		d := ""
		if e.Runtime != nil {
			d = padRight(e.Runtime.Type, 11)
		}
		if e.Protected {
			d += sRed.Render("protected ")
		}
		desc = append(desc, d+e.Description)
		if n == m.app.Env.Name {
			sel = i
		}
	}
	file := m.app.Spec.File
	m.pick("environment", names, desc, sel, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 || chosen[0] == m.app.Env.Name {
			return nil
		}
		name := chosen[0]
		m.setStatus("opening "+name+"…", false)
		return func() tea.Msg {
			a, err := engine.Open(file, name)
			return envMsg{app: a, err: err}
		}
	})
}

func (m *model) View() string {
	if m.w == 0 {
		return "loading…"
	}
	m.zones = m.zones[:0]
	header := m.header()
	tabs := m.tabBar()
	footer := m.footer()
	m.originY = lipgloss.Height(header) + lipgloss.Height(tabs)
	bodyH := m.h - m.originY - lipgloss.Height(footer)
	var body string
	switch {
	case m.help:
		body = m.overlay(m.helpView(), bodyH)
	case m.showAlerts:
		body = m.overlay(m.alertsView(), bodyH)
	case m.picker != nil:
		body = m.picker.view(m, bodyH)
	default:
		body = m.tabs[m.active].view(m, m.w, bodyH)
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, body, footer)
}

func (m *model) overlay(box string, h int) string {
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m *model) header() string {
	a := m.app
	left := sAccent.Render(" ◆ rig ") + sTitle.Render(a.Spec.Name) + sDim.Render("  env ") + sTitle.Render(a.Env.Name) +
		sDim.Render(" ("+a.Env.Runtime.Type+")")
	if a.Env.Protected {
		left += " " + lipgloss.NewStyle().Background(cRed).Foreground(lipgloss.Color("#FFFFFF")).Bold(true).Render(" PROTECTED ")
	}
	if b := m.alertBadge(); b != "" {
		left += " " + b
	}
	up, bad := 0, 0
	for _, s := range m.services {
		if engine.Ready(s) {
			up++
		}
		if s.State == core.StateFailed || s.State == core.StateDegraded {
			bad++
		}
	}
	right := fmt.Sprintf("%s %d/%d up", sGreen.Render("●"), up, len(m.services))
	if bad > 0 {
		right += sRed.Render(fmt.Sprintf("  ✖ %d failing", bad))
	}
	if n := m.sched.activeCount(); n > 0 {
		right += sDim.Render(fmt.Sprintf("  ⏱ %d scheduled", n))
	}
	right += "  " + sDim.Render(time.Now().Format("15:04:05")) + " "
	if m.busy > 0 {
		right = sAmber.Render("⟳ working  ") + right
	}
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	return sHeader.Width(m.w).Render(left + strings.Repeat(" ", gap) + right)
}

func (m *model) tabBar() string {
	var parts []string
	m.tabSpan = m.tabSpan[:0]
	x := 0
	for i, t := range m.tabs {
		key := fmt.Sprint((i + 1) % 10)
		var p string
		if i == m.active {
			p = sTabOn.Render(key + " " + t.name())
		} else {
			p = sTabOff.Render(sKey.Render(key) + " " + t.name())
		}
		w := lipgloss.Width(p)
		m.tabSpan = append(m.tabSpan, [2]int{x, x + w})
		x += w
		parts = append(parts, p)
	}
	return truncate(strings.Join(parts, ""), m.w)
}

func (m *model) footer() string {
	var line string
	switch {
	case m.confirm != nil:
		line = sAmber.Bold(true).Render(" "+m.confirm.text) + sDim.Render("   ") + sKey.Render("enter") + sDim.Render(" confirm · any other key cancels")
	case m.prompt != nil:
		line = sAccent.Render(" "+m.prompt.label+": ") + m.prompt.input.View() + sDim.Render("   enter ok · esc cancel")
	case m.picker != nil:
		line = " " + m.picker.hints()
	default:
		var hs []string
		for _, h := range m.tabs[m.active].hints() {
			hs = append(hs, sKey.Render(h[0])+" "+sDim.Render(h[1]))
		}
		hs = append(hs, sKey.Render("?")+" "+sDim.Render("help"), sKey.Render("E")+" "+sDim.Render("env"), sKey.Render("T")+" "+sDim.Render("tasks"), sKey.Render("q")+" "+sDim.Render("quit"))
		line = " " + strings.Join(hs, "  ")
	}
	status := ""
	if m.status != "" && time.Since(m.statusAt) < 30*time.Second {
		st := sDim
		if m.statusErr {
			st = sRed
		}
		status = " " + st.Render(truncate(m.status, m.w-2))
	}
	return truncate(line, m.w) + "\n" + status
}

func (m *model) helpView() string {
	rows := [][2]string{
		{"1-9 0  tab", "switch screen (or click its name)"}, {"E", "switch environment"}, {"T", "run a task (clear-db, clear-queues, ship, ...)"},
		{"↑↓ / wheel", "move"}, {"enter / dbl-click", "open, run"}, {"< >  I", "sort column, invert (or click a header)"},
		{"esc", "back"}, {"A", "alerts (header badge)"}, {"S", "save this session (rig resume <id>)"}, {"M", "mouse on/off (off: select text)"}, {"?", "this help"}, {"q  ctrl+c", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(sKey.Render(padRight(r[0], 18)) + " " + r[1] + "\n")
	}
	b.WriteString("\n" + sTitle.Render(m.tabs[m.active].name()) + "\n")
	for _, r := range m.tabs[m.active].hints() {
		b.WriteString(sKey.Render(padRight(r[0], 18)) + " " + r[1] + "\n")
	}
	ask := "nothing asks for confirmation here"
	if m.needsConfirm(true) {
		ask = "dangerous changes (stop, deploy, delete, edits) ask first: enter confirms"
	}
	if m.app.Env.Protected {
		ask = "protected: every change asks first, enter confirms"
	}
	b.WriteString("\n" + sDim.Render(ask) + "\n")
	b.WriteString(sDim.Render("screens load only when opened; services, components and environments come from " + m.app.Spec.File))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render("rig — keys") + "\n\n" + b.String())
}

// listKeys moves a selection with the usual keys and reports whether the key was one of them.
func listKeys(k tea.KeyMsg, sel *int, n int) bool {
	switch k.String() {
	case "up", "k":
		*sel = max(0, *sel-1)
	case "down", "j":
		*sel = min(max(n-1, 0), *sel+1)
	case "pgup":
		*sel = max(0, *sel-10)
	case "pgdown":
		*sel = min(max(n-1, 0), *sel+10)
	case "home", "g":
		*sel = 0
	case "end", "G":
		*sel = max(n-1, 0)
	default:
		return false
	}
	return true
}
