// Package tui is rig's terminal control plane: one screen per concern (services, logs, metrics,
// traces, queries, key-value, data, load, manifests, hosts), all driven by the same engine as the CLI.
// Screens load nothing until opened and refresh only while showing; the service list is one runtime
// call per refresh.
package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
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

// wheeler scrolls what is under the mouse; false leaves the wheel to the default (up/down keys).
type wheeler interface {
	wheel(m *model, z hit, up bool) (tea.Cmd, bool)
}

// hit is a click inside a zone, in zone-relative cells; double is a second click on the same cell,
// mod a click with ctrl, alt or shift held.
type hit struct {
	id     string
	x, y   int
	double bool
	mod    bool
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
	all    map[string]tab
	active int
	// simple is the newcomer's view: fewer screens, columns and keys (V)
	simple bool
	opened map[int]bool
	w, h   int

	status    string
	statusErr bool
	statusAt  time.Time
	busy      int
	// workCtx is what one-off operations (queries, actions, loads) run under: ctrl+c ends them
	// without leaving rig
	workCtx  context.Context
	stopWork context.CancelFunc

	confirm *confirm
	prompt  *prompt
	picker  *picker
	help    bool
	// helpScroll is lines down from the top of the help text; helpQuery, when set, is highlighted
	// and jumped to with / and n
	helpScroll int
	helpQuery  string
	// session is the saved session this run continues (S, rig resume); quitting updates it
	session *Session
	// mouseOff hands the mouse to the terminal, so text can be selected and copied
	mouseOff bool
	// chat is the assistant's drawer (@); sock is where its tools reach this UI
	chat *chat
	// aiAllowed are the assistant's commands the user said "always" to, for this run of rig
	aiAllowed map[string]bool
	sock      string
	ai        *ai.Runner
	// completeCancel stops the AI completion in flight
	completeCancel context.CancelFunc

	services []core.Status
	svcAt    time.Time
	svcBusy  bool

	alerts     []engine.Firing
	alertErrs  []error
	alertsAt   time.Time
	alertsBusy bool
	showAlerts bool
	// envInfo is the ctrl+e box: what this environment resolves to, nil when closed
	envInfo *envBox
	// watch is ctrl+w's live rebuild, nil when off
	watch *watching

	refreshed map[int]time.Time
	sched     *scheduler

	zones   []zone
	originY int
	// stripAt is where the active label of the last strip drawn starts and ends, from its left
	stripAt [2]int
	// hx, hy is the cell under the mouse (-1 when unknown), for hover highlights
	hx, hy  int
	tabSpan [][2]int
	lastHit hit
	lastAt  time.Time
	// dragZone is the zone a press started a drag on; moves and the release go to its tab
	dragZone string
	sel      *selection
	// inputDrag is the mouse selecting in the footer input, which starts at column inputX
	inputDrag bool
	inputX    int
	// frame is the last screen drawn; bodyEnd the first row below the body
	frame   string
	bodyEnd int
}

type confirm struct {
	text   string
	run    tea.Cmd
	cancel func()
	// always, when set, is what "a" runs: yes now and to the same question for the rest of the session
	always tea.Cmd
}

type prompt struct {
	label  string
	input  textinput.Model
	submit func(string) tea.Cmd
	// hint, when set, is what the AI completes the input with: the language and where it runs
	hint    string
	seq     int
	waiting bool
	// template is offered while the input is empty: tab fills it in, enter runs it as is
	template string
	took     time.Duration
	// described is what the AI wrote for the "@?description" in describedFor, the input it saw
	described, describedFor string
	// popup draws the input as a box over the screen, wrapped (queries)
	popup bool
	sel   textSel
	// off is the first rune the one-line input shows
	off int
	// escape, when set, runs when esc drops the prompt
	escape func() tea.Cmd
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

func allTabs() map[string]tab {
	return map[string]tab{"services": &servicesTab{}, "logs": &logsTab{}, "metrics": &metricsTab{}, "traces": &tracesTab{},
		"queries": &queriesTab{}, "kv": &kvTab{}, "data": &dataTab{}, "load": &loadTab{}, "manifests": &manifestsTab{},
		"hosts": &hostsTab{}, "tests": &testsTab{}}
}

// advanced are the screens the simple view leaves out (unless ui.tabs names them).
var advanced = map[string]bool{"queries": true, "load": true, "manifests": true, "hosts": true}

// newTabs picks from all the screens ui.tabs lists, else every screen the project gives something
// to show, minus the advanced ones in the simple view.
func newTabs(a *engine.App, all map[string]tab, simple bool) []tab {
	if ui := a.Spec.UI; ui != nil && len(ui.Tabs) > 0 {
		var out []tab
		for _, n := range ui.Tabs {
			out = append(out, all[n])
		}
		return out
	}
	var out []tab
	for _, n := range spec.Screens {
		if configured(a, n) && !(simple && advanced[n]) {
			out = append(out, all[n])
		}
	}
	return out
}

func modeFile(a *engine.App) string { return filepath.Join(a.Spec.Dir, ".rig", "ui-mode") }

// startSimple is the view V last chose in this project, else ui.mode.
func startSimple(a *engine.App) bool {
	if raw, err := os.ReadFile(modeFile(a)); err == nil {
		return strings.TrimSpace(string(raw)) == "simple"
	}
	return a.Spec.UI != nil && a.Spec.UI.Mode == "simple"
}

// toggleMode switches between the simple and the detailed view, keeping each screen's state.
func (m *model) toggleMode() tea.Cmd {
	m.simple = !m.simple
	mode := map[bool]string{true: "simple", false: "detailed"}[m.simple]
	if err := os.MkdirAll(filepath.Dir(modeFile(m.app)), 0o755); err == nil {
		_ = os.WriteFile(modeFile(m.app), []byte(mode+"\n"), 0o644)
	}
	cur := m.tabs[m.active]
	opened := map[tab]bool{}
	for i, t := range m.tabs {
		opened[t] = m.opened[i]
	}
	m.tabs = newTabs(m.app, m.all, m.simple)
	m.opened, m.active = map[int]bool{}, 0
	for i, t := range m.tabs {
		m.opened[i] = opened[t]
		if t == cur {
			m.active = i
		}
	}
	m.setStatus(mode+" view (V switches)", false)
	return m.openTab(m.active)
}

// configured says whether the project has anything for screen n to show.
func configured(a *engine.App, n string) bool {
	has := func(kinds ...core.Kind) bool {
		for _, k := range kinds {
			if len(a.Names(k)) > 0 {
				return true
			}
		}
		return false
	}
	rt := a.Env.Runtime.Type
	switch n {
	case "metrics":
		if len(a.Spec.Dashboards) > 0 || has(core.KindMetrics) {
			return true
		}
		for _, s := range a.Spec.Services {
			if s.Metrics != nil {
				return true
			}
		}
		return false
	case "traces":
		return has(core.KindTracing)
	case "queries":
		return len(a.Queries()) > 0 || len(a.Queriers()) > 0
	case "kv":
		return has(core.KindKV)
	case "data":
		return has(core.KindDatabase, core.KindCache, core.KindMessaging)
	case "load":
		return has(core.KindLoad)
	case "manifests":
		return rt == "kubernetes" || rt == "kind" || len(a.Spec.Manifests) > 0
	case "hosts":
		_, ok := a.Runtime().(core.Hosts)
		return ok || has(core.KindHosts)
	case "tests":
		return len(a.SuiteNames()) > 0
	}
	return true
}

func Run(ctx context.Context, a *engine.App) error { return run(ctx, a, nil, nil) }

// Chat opens the UI with the assistant's chat open, on conversation id ("" a new one, "last" the newest).
func Chat(ctx context.Context, a *engine.App, id string) error {
	return run(ctx, a, nil, func(m *model) {
		c := m.chatOpen()
		if id == "" {
			return
		}
		if id == "last" {
			ss, _ := ai.ListSessions(a.AIDir())
			for _, s := range ss {
				if s.Env == a.Env.Name {
					id = s.ID
					break
				}
			}
		}
		if err := c.load(m, id); err != nil && id != "last" {
			c.err = err.Error()
		}
	})
}

func run(ctx context.Context, a *engine.App, s *Session, init func(m *model)) error {
	if a.Env == nil {
		return fmt.Errorf("no environment: define one under environments: and set default")
	}
	m := &model{ctx: ctx, app: a, opened: map[int]bool{}, all: allTabs(), simple: startSimple(a), refreshed: map[int]time.Time{}, hx: -1, hy: -1}
	m.tabs = newTabs(a, m.all, m.simple)
	m.sched = newScheduler(a)
	if s != nil {
		m.restore(s)
	}
	m.sock = ai.SocketPath()
	stop, err := ai.Serve(m.sock, func(r ai.Request) ai.Reply {
		reply := make(chan ai.Reply, 1)
		program.Send(bridgeMsg{req: r, reply: reply})
		select {
		case rep := <-reply:
			return rep
		case <-time.After(10 * time.Minute):
			return ai.Reply{Text: "no answer in rig"}
		}
	})
	if err != nil {
		m.sock = ""
	} else {
		defer stop()
	}
	if init != nil {
		init(m)
	}
	program = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithContext(ctx))
	_, err = program.Run()
	if err == tea.ErrProgramKilled {
		return nil
	}
	return err
}

func (m *model) Init() tea.Cmd {
	return batch(m.openTab(m.active), m.fetchServices(), tick())
}

var program *tea.Program

// copyText puts text on the system clipboard (wl-copy, xclip or xsel) and also sends OSC 52, which
// reaches the local clipboard over SSH; VTE terminals such as GNOME Terminal ignore OSC 52.
func copyText(text string) {
	_ = clipboard.WriteAll(text)
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

// execDoneMsg carries an exec callback's message: bubbletea's RestoreTerminal brings back the alt
// screen but not the mouse, so Update turns it on again.
type execDoneMsg struct{ msg tea.Msg }

func execProcess(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
	return tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{fn(err)} })
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

// work is the context of the next one-off operation; ctrl+c cancels every one in flight.
func (m *model) work() context.Context {
	if m.workCtx == nil || m.workCtx.Err() != nil {
		m.workCtx, m.stopWork = context.WithCancel(m.ctx)
	}
	return m.workCtx
}

// working says whether ctrl+c has something to stop: an operation, a query or a screen loading.
func (m *model) working() bool {
	if m.busy > 0 {
		return true
	}
	if m.sched != nil {
		m.sched.mu.Lock()
		defer m.sched.mu.Unlock()
		for _, r := range m.sched.running {
			if r {
				return true
			}
		}
	}
	for _, t := range m.tabs {
		if d, ok := t.(*dataTab); ok && d.loading {
			return true
		}
	}
	return false
}

// failed is the footer line of an operation that ended with err; a ctrl+c is not a failure.
func failed(label string, err error) statusMsg {
	if errors.Is(err, context.Canceled) {
		return statusMsg{text: label + ": stopped"}
	}
	return statusMsg{text: label + ": " + err.Error(), err: true}
}

// do runs a slow operation off the UI loop and reports its outcome in the footer.
func (m *model) do(label string, f func(ctx context.Context) error) tea.Cmd {
	m.busy++
	m.setStatus(label+"…", false)
	ctx := m.work()
	return func() tea.Msg {
		if err := f(ctx); err != nil {
			return failed(label, err)
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
	if err := m.app.Writable(); err != nil {
		m.setStatus(label+": "+err.Error(), true)
		return nil
	}
	a, ctx := m.app, core.WithConfirmed(m.work())
	run := func() tea.Msg {
		a.Confirmed = true
		defer func() { a.Confirmed = false }()
		if err := f(ctx); err != nil {
			return failed(label, err)
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
	in := newInput()
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
	b.WriteString(m.watchFailures())
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

// hovering reports whether the mouse is over a body-relative area.
func (m *model) hovering(x, y, w, h int) bool {
	y += m.originY
	return m.hx >= x && m.hx < x+w && m.hy >= y && m.hy < y+h
}

// buttons draws clickable yes/no buttons on the footer's first line, starting at column x.
// buttons draws them on screen row y.
func (m *model) buttons(yes, no string, x, y int) string {
	by := sTabOn.Render(yes)
	bn := sTabOff.Render(no)
	if m.hy == y && m.hx >= x+lipgloss.Width(by)+1 && m.hx < x+lipgloss.Width(by)+1+lipgloss.Width(bn) {
		bn = sTabHover.Render(no)
	}
	m.zones = append(m.zones, zone{id: "confirm:yes", x: x, y: y, w: lipgloss.Width(by), h: 1},
		zone{id: "confirm:no", x: x + lipgloss.Width(by) + 1, y: y, w: lipgloss.Width(bn), h: 1})
	return by + " " + bn
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
	case execDoneMsg:
		inner := func() tea.Msg { return msg.msg }
		if m.mouseOff {
			return m, inner
		}
		return m, batch(tea.EnableMouseAllMotion, inner)
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
	case envInfoMsg:
		if msg.gen == m.gen {
			m.envInfo = msg.box
			m.setStatus("", false)
		}
		return m, nil
	case alertsMsg:
		if msg.gen == m.gen {
			m.alerts, m.alertErrs = msg.firing, msg.errs
		}
		m.alertsAt, m.alertsBusy = time.Now(), false
		return m, nil
	case servicesMsg:
		if msg.gen == m.gen {
			m.services, m.svcAt, m.svcBusy = msg.sts, time.Now(), false
			for _, t := range m.tabs {
				if st, ok := t.(*servicesTab); ok {
					for _, s := range msg.sts {
						st.noteScale(s)
					}
				}
			}
		}
		return m, nil
	case statusMsg:
		m.busy = max(0, m.busy-1)
		m.setStatus(msg.text, msg.err)
		m.svcAt = time.Time{}
		for _, t := range m.tabs {
			if d, ok := t.(*dataTab); ok {
				if cmd := d.changed(m, msg.text); cmd != nil {
					return m, cmd
				}
			}
		}
		return m, m.tabs[m.active].refresh(m)
	case schedMsg:
		m.sched.done(msg)
		return m, nil
	case completeTickMsg:
		return m, m.completeDue(msg)
	case completeMsg:
		m.completed(msg)
		return m, nil
	case describeMsg:
		m.describedMsg(msg)
		return m, nil
	case namespacesMsg:
		return m, m.showNamespaces(msg)
	case manifestEditedMsg:
		return m, m.manifestEdited(msg)
	case syncedMsg:
		return m, m.synced(msg)
	case resourcesMsg:
		return m, m.gotResources(msg)
	case watchMsg:
		return m, m.onWatch(msg)
	case envMsg:
		if msg.err != nil {
			m.setStatus("switch environment: "+msg.err.Error(), true)
			return m, nil
		}
		if m.watch != nil {
			m.watch.cancel()
			m.watch = nil
		}
		old := m.app
		m.app, m.gen = msg.app, m.gen+1
		go old.Close()
		m.services, m.svcBusy = nil, false
		m.opened, m.refreshed = map[int]bool{}, map[int]time.Time{}
		m.all = allTabs()
		m.tabs = newTabs(m.app, m.all, m.simple)
		m.sched = newScheduler(m.app)
		m.setStatus("environment "+m.app.Env.Name, false)
		m.ai = nil
		if m.chat != nil {
			m.chat.reset()
			if m.chat.open {
				focus := m.chat.focus
				m.chatOpen().focus = focus
			}
		}
		return m, batch(m.openTab(m.active), m.fetchServices())
	case tea.KeyMsg:
		return m, m.key(msg)
	case tea.MouseMsg:
		return m, m.mouse(msg)
	}
	if cmd, ok := m.chatUpdate(msg); ok {
		return m, cmd
	}
	// data arriving for a tab must reach it even when another tab is showing
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		cmds = append(cmds, t.update(m, msg))
	}
	return m, batch(cmds...)
}

func (m *model) mouse(e tea.MouseMsg) tea.Cmd {
	m.hx, m.hy = e.X, e.Y
	if m.dragZone != "" && (e.Action == tea.MouseActionMotion || e.Action == tea.MouseActionRelease) {
		return m.dragTo(e)
	}
	if p := m.prompt; p != nil && !p.popup {
		if m.inputDrag && (e.Action == tea.MouseActionMotion || e.Action == tea.MouseActionRelease) {
			p.input.SetCursor(p.off + e.X - m.inputX)
			m.inputDrag = e.Action != tea.MouseActionRelease
			return nil
		}
		if z, ok := m.zoneAt(e.X, e.Y); ok && z.id == "prompt:input" && e.Action == tea.MouseActionPress && e.Button == tea.MouseButtonLeft {
			p.input.SetCursor(p.off + z.x)
			p.sel = textSel{on: true, anchor: p.input.Position()}
			m.inputDrag, m.inputX = true, e.X-z.x
			return nil
		}
	}
	if m.sel != nil && (e.Action == tea.MouseActionMotion || e.Action == tea.MouseActionRelease) {
		return m.selectTo(e)
	}
	if e.Action == tea.MouseActionPress && e.Button == tea.MouseButtonLeft && e.Y != 1 {
		m.selectStart(e.X, e.Y)
	}
	if e.Action == tea.MouseActionMotion {
		return nil
	}
	if (m.confirm != nil || m.prompt != nil) && e.Action == tea.MouseActionPress && e.Button == tea.MouseButtonLeft {
		if z, ok := m.zoneAt(e.X, e.Y); ok && (z.id == "confirm:yes" || z.id == "confirm:no") {
			key := tea.KeyMsg{Type: tea.KeyEnter}
			if z.id == "confirm:no" {
				key = tea.KeyMsg{Type: tea.KeyEsc}
			}
			return m.key(key)
		}
	}
	if m.confirm != nil || m.prompt != nil {
		return nil
	}
	if c := m.chat; c != nil && c.open && m.picker == nil && !m.help && !m.showAlerts && e.Action == tea.MouseActionPress {
		z, ok := m.zoneAt(e.X, e.Y)
		inChat := ok && z.id == "chat"
		switch {
		case inChat && e.Button == tea.MouseButtonWheelUp:
			c.scroll += 3
			return nil
		case inChat && e.Button == tea.MouseButtonWheelDown:
			c.scroll = max(0, c.scroll-3)
			return nil
		case inChat && e.Button == tea.MouseButtonLeft:
			c.focus = true
			c.input.Focus()
			return nil
		case e.Button == tea.MouseButtonLeft:
			c.focus = false
		}
	}
	switch e.Button {
	case tea.MouseButtonWheelLeft, tea.MouseButtonWheelRight:
		key := tea.KeyMsg{Type: tea.KeyLeft}
		if e.Button == tea.MouseButtonWheelRight {
			key = tea.KeyMsg{Type: tea.KeyRight}
		}
		if m.picker == nil && !m.help && !m.showAlerts {
			return m.tabs[m.active].update(m, key)
		}
		return nil
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if e.Shift && m.picker == nil {
			// shift+wheel scrolls sideways, as most terminals do
			key := tea.KeyMsg{Type: tea.KeyRight}
			if e.Button == tea.MouseButtonWheelUp {
				key = tea.KeyMsg{Type: tea.KeyLeft}
			}
			return m.tabs[m.active].update(m, key)
		}
		up := e.Button == tea.MouseButtonWheelUp
		key := tea.KeyMsg{Type: tea.KeyDown}
		if up {
			key = tea.KeyMsg{Type: tea.KeyUp}
		}
		if m.help {
			return m.helpKey(key)
		}
		if m.picker != nil {
			return m.picker.key(m, key)
		}
		if wh, ok := m.tabs[m.active].(wheeler); ok && !m.help && !m.showAlerts {
			if z, ok := m.zoneAt(e.X, e.Y); ok {
				z.mod = e.Ctrl || e.Alt || e.Shift
				if cmd, done := wh.wheel(m, z, up); done {
					return cmd
				}
			}
		}
		return m.tabs[m.active].update(m, key)
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
	h, ok := m.zoneAt(e.X, e.Y)
	if !ok {
		return nil
	}
	h.mod = e.Ctrl || e.Alt || e.Shift
	h.double = m.lastHit.id == h.id && m.lastHit.y == h.y && time.Since(m.lastAt) < 400*time.Millisecond
	m.lastHit, m.lastAt = h, time.Now()
	if m.picker != nil {
		return m.picker.click(m, h)
	}
	if d, ok := m.tabs[m.active].(dragger); ok && !h.double && d.drag(m, h, dragPress) {
		m.dragZone, m.sel = h.id, nil
	}
	if c, ok := m.tabs[m.active].(clicker); ok {
		return c.click(m, h)
	}
	return nil
}

// dragTo hands a move or the release of a drag to the tab, in cells relative to the zone it began on.
func (m *model) dragTo(e tea.MouseMsg) tea.Cmd {
	phase := dragMove
	if e.Action == tea.MouseActionRelease {
		phase = dragRelease
	}
	id := m.dragZone
	if phase == dragRelease {
		m.dragZone = ""
	}
	d, ok := m.tabs[m.active].(dragger)
	if !ok {
		return nil
	}
	for _, z := range m.zones {
		if z.id == id {
			d.drag(m, hit{id: id, x: e.X - z.x, y: e.Y - z.y}, phase)
			if c, ok := d.(interface{ dragCmd() tea.Cmd }); ok {
				return c.dragCmd()
			}
			return nil
		}
	}
	return nil
}

// showMetrics opens the Metrics screen on the first dashboard with a $service variable, set to service.
func (m *model) showMetrics(service string) tea.Cmd {
	for i, t := range m.tabs {
		if mt, ok := t.(*metricsTab); ok {
			open := m.openTab(i)
			return batch(open, mt.jump(m, service))
		}
	}
	return nil
}

// showService opens the Services screen on one service's page.
func (m *model) showService(name string) tea.Cmd {
	for i, t := range m.tabs {
		if st, ok := t.(*servicesTab); ok {
			open := m.openTab(i)
			return batch(open, st.openService(m, name, ""))
		}
	}
	m.setStatus("the Services screen is not in ui.tabs", true)
	return nil
}

// zoneAt is the topmost zone under a screen cell, as a hit in zone-relative cells.
func (m *model) zoneAt(x, y int) (hit, bool) {
	for i := len(m.zones) - 1; i >= 0; i-- {
		z := m.zones[i]
		if x >= z.x && x < z.x+z.w && y >= z.y && y < z.y+z.h {
			return hit{id: z.id, x: x - z.x, y: y - z.y}, true
		}
	}
	return hit{}, false
}

// strip renders labels as a row of sub-tabs at (x, y) of the tab body, set apart from the screen
// tabs: the active one in accent, the others dim, divided by bars; stripRule underlines it. A click
// on label i arrives as a hit with id "<id>:<i>" (stripHit reads it back).
func (m *model) strip(id string, x, y int, labels []string, active int) string {
	return m.stripFrom(id, x, y, labels, active, 0)
}

// stripFit is strip within w cells: when the labels do not fit, the first ones give way (‹ marks
// them) so the active one always shows.
func (m *model) stripFit(id string, x, y int, labels []string, active, w int) string {
	width := func(from int) int {
		n := 0
		for _, l := range labels[from : min(active, len(labels)-1)+1] {
			n += lipgloss.Width(sSubOff.Render(l)) + 1
		}
		return n
	}
	from := 0
	for from < active && width(from) > w-2 {
		from++
	}
	if from == 0 {
		return truncate(m.strip(id, x, y, labels, active), w)
	}
	out := m.stripFrom(id, x+1, y, labels, active, from)
	m.stripAt[0], m.stripAt[1] = m.stripAt[0]+1, m.stripAt[1]+1
	return truncate(sDim.Render("‹")+out, w)
}

func (m *model) stripFrom(id string, x, y int, labels []string, active, from int) string {
	var b strings.Builder
	start := x
	m.stripAt = [2]int{}
	for i, l := range labels {
		if i < from {
			continue
		}
		if i > from {
			b.WriteString(sSubSep)
			x++
		}
		var p string
		switch {
		case i == active:
			p = sSubOn.Render(l)
		case m.hovering(x, y, lipgloss.Width(sSubOff.Render(l)), 1):
			p = sTabHover.Render(l)
		default:
			p = sSubOff.Render(l)
		}
		w := lipgloss.Width(p)
		m.zone(fmt.Sprintf("%s:%d", id, i), x, y, w, 1)
		if i == active {
			m.stripAt = [2]int{x - start, x - start + w}
		}
		x += w
		b.WriteString(p)
	}
	return b.String()
}

// stripRule is the line under the last strip drawn, lead cells in: thin, heavy accent under the
// active label.
func (m *model) stripRule(lead, w int) string {
	a, b := min(lead+m.stripAt[0], w), min(lead+m.stripAt[1], w)
	thin := lipgloss.NewStyle().Foreground(cPanel)
	return thin.Render(strings.Repeat("─", a)) + sKey.Render(strings.Repeat("━", b-a)) + thin.Render(strings.Repeat("─", w-b))
}

// withStrip puts a row of sub-tabs above a screen's body, underlined; the body is drawn h-2 high
// and its zones shift down under the strip and rule.
func (m *model) withStrip(id string, labels []string, active, w, h int, body func(h int) string) string {
	s := " " + m.strip(id, 1, 0, labels, active)
	rule := m.stripRule(1, w)
	m.originY += 2
	b := body(h - 2)
	m.originY -= 2
	return s + "\n" + rule + "\n" + b
}

// stripHit is the label index a click on strip id landed on, false for any other zone.
func stripHit(h hit, id string) (int, bool) {
	rest, ok := strings.CutPrefix(h.id, id+":")
	if !ok {
		return 0, false
	}
	var i int
	_, err := fmt.Sscanf(rest, "%d", &i)
	return i, err == nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	if k.String() == "ctrl+c" && m.prompt == nil && (m.chat == nil || !m.chat.open || !m.chat.focus) {
		if m.working() && m.stopWork != nil {
			m.stopWork()
			m.busy = 0
			for _, t := range m.tabs {
				if d, ok := t.(*dataTab); ok && d.loading {
					d.loading, d.seq = false, d.seq+1
				}
			}
			m.setStatus("stopped what was running; ctrl+c again quits", false)
			return nil
		}
		return m.quit()
	}
	m.sel = nil
	// alt+arrows switch screens from anywhere: they close what is open over the screen first
	if s := k.String(); (s == "alt+left" || s == "alt+right") && len(m.tabs) > 0 {
		if m.confirm != nil && m.confirm.cancel != nil {
			m.confirm.cancel()
		}
		var esc tea.Cmd
		if m.prompt != nil && m.prompt.escape != nil {
			esc = m.prompt.escape()
		}
		m.confirm, m.prompt, m.picker, m.envInfo, m.help, m.showAlerts = nil, nil, nil, nil, false, false
		if m.chat != nil {
			m.chat.focus = false
		}
		step := 1
		if s == "alt+left" {
			step = len(m.tabs) - 1
		}
		return batch(esc, m.openTab((m.active+step)%len(m.tabs)))
	}
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if k.String() == "enter" {
			m.busy++
			m.setStatus(strings.TrimSuffix(c.text, "?")+"…", false)
			return c.run
		}
		if k.String() == "a" && c.always != nil {
			m.busy++
			m.setStatus(strings.TrimSuffix(c.text, "?")+"…", false)
			return c.always
		}
		if c.cancel != nil {
			c.cancel()
		}
		m.setStatus("cancelled", false)
		return nil
	}
	if m.prompt != nil {
		if before := m.prompt.input.Value(); editKey(&m.prompt.input, &m.prompt.sel, k) {
			if s := k.String(); s == "ctrl+c" || s == "ctrl+y" || s == "ctrl+x" {
				m.setStatus("copied", false)
			}
			if m.prompt.input.Value() != before {
				return m.completeLater()
			}
			return nil
		}
		switch k.String() {
		case "esc":
			p := m.prompt
			m.prompt = nil
			if p.escape != nil {
				return p.escape()
			}
			return nil
		case "enter":
			p := m.prompt
			v := p.input.Value()
			if v == "" {
				v = p.template
			}
			if _, _, ok := describing(v); ok && p.hint != "" {
				if p.describedFor != v || p.described == "" {
					m.setStatus("waiting for the AI to write what @? describes (tab takes it)", false)
					return m.completeDue(completeTickMsg{seq: p.seq})
				}
				v = p.described
			}
			m.prompt = nil
			return p.submit(v)
		case "tab":
			if p := m.prompt; p.described != "" && p.describedFor == p.input.Value() {
				p.input.SetValue(p.described)
				p.input.CursorEnd()
				p.described, p.describedFor = "", ""
				return nil
			}
			if p := m.prompt; p.input.Value() == "" && p.template != "" {
				p.input.SetValue(p.template)
				p.input.CursorEnd()
				return nil
			}
		case "ctrl+t":
			m.toggleAutocomplete()
			return nil
		}
		before := m.prompt.input.Value()
		var cmd tea.Cmd
		m.prompt.input, cmd = m.prompt.input.Update(k)
		if m.prompt.input.Value() != before {
			return batch(cmd, m.completeLater())
		}
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
	if box := m.envInfo; box != nil {
		switch k.String() {
		case "up", "k":
			box.sel = max(0, box.sel-1)
			return nil
		case "down", "j":
			box.sel = min(box.sel+1, len(box.lines)-1)
			return nil
		case "e", "enter":
			return m.editEnvVar(box)
		case "y":
			copyText(ansi.Strip(box.plainText()))
			m.setStatus("copied the environment", false)
			return nil
		default:
			m.envInfo = nil
			return nil
		}
	}
	if m.help {
		return m.helpKey(k)
	}
	if m.showAlerts {
		m.showAlerts = false
		return nil
	}
	if c := m.chat; c != nil && c.open && c.focus {
		return m.chatKey(k)
	}
	t := m.tabs[m.active]
	if !t.typing() {
		switch s := k.String(); s {
		case "@":
			if m.chat != nil && m.chat.open {
				m.chat.open, m.chat.focus = false, false
			} else {
				m.chatOpen()
			}
			return nil
		case "q":
			return m.quit()
		case "S":
			id, err := m.saveSession()
			if err != nil {
				m.setStatus("save session: "+err.Error(), true)
			} else {
				m.setStatus("session saved: rig resume "+id+" (quitting with q keeps it current)", false)
			}
			return nil
		case "?", "f1":
			m.help, m.helpScroll, m.helpQuery = true, 0, ""
			return nil
		case "A":
			m.showAlerts = true
			return nil
		case "ctrl+e":
			return m.fetchEnvInfo()
		case "ctrl+w":
			return m.toggleWatch()
		case "E":
			m.pickEnv()
			return nil
		case "T":
			m.pickTask("")
			return nil
		case "N":
			return m.pickNamespace()
		case "V":
			return m.toggleMode()
		case "M":
			m.mouseOff = !m.mouseOff
			if m.mouseOff {
				m.setStatus("mouse off: select and copy with the mouse; M turns it back on", false)
				return tea.DisableMouse
			}
			m.setStatus("mouse on", false)
			return tea.EnableMouseAllMotion
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
		case "`":
			// the eleventh screen has no digit
			if len(m.tabs) > 10 {
				return m.openTab(10)
			}
		}
	}
	return t.update(m, k)
}

// quit leaves at once unless that would cut something short: a test run and operations still
// working stop with rig, while load generators keep sending without it, so it asks first.
func (m *model) quit() tea.Cmd {
	var stops, gens []string
	for _, t := range m.tabs {
		switch t := t.(type) {
		case *testsTab:
			if t.running != "" {
				stops = append(stops, "the tests of "+t.running)
			}
		case *loadTab:
			for _, n := range t.names {
				if t.stats[n].Running {
					gens = append(gens, n)
				}
			}
		}
	}
	if m.busy > 0 {
		stops = append(stops, fmt.Sprintf("%d operations still working", m.busy))
	}
	if m.chat != nil && m.chat.busy {
		stops = append(stops, "the assistant's turn")
	}
	leave := func() tea.Msg {
		if m.session != nil {
			_, _ = m.saveSession()
		}
		return tea.QuitMsg{}
	}
	if len(stops) == 0 && len(gens) == 0 {
		return leave
	}
	var opts, desc []string
	what := "stops " + strings.Join(stops, ", ")
	if len(stops) == 0 {
		what = ""
	}
	if len(gens) > 0 {
		opts, desc = append(opts, "stop the load generators, then quit"), append(desc, strings.Join(gens, ", ")+" stop sending")
		keep := "keeps sending: " + strings.Join(gens, ", ")
		if what != "" {
			keep = what + "; " + keep
		}
		opts, desc = append(opts, "quit, leave them sending"), append(desc, keep)
	} else {
		opts, desc = append(opts, "quit"), append(desc, what)
	}
	opts, desc = append(opts, "stay"), append(desc, "")
	a, ctx := m.app, core.WithConfirmed(m.ctx)
	m.pick("quit rig?", opts, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 || c[0] == "stay" {
			return nil
		}
		if !strings.HasPrefix(c[0], "stop") {
			return leave
		}
		m.busy++
		m.setStatus("stopping "+strings.Join(gens, ", ")+"…", false)
		return func() tea.Msg {
			a.Confirmed = true
			err := each(gens, func(n string) error {
				g, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, n)
				if err == nil {
					err = g.Stop(ctx)
				}
				return err
			})
			if err != nil {
				a.Confirmed = false
				return statusMsg{text: "stop generators: " + err.Error() + " (not quitting)", err: true}
			}
			return leave()
		}
	})
	return nil
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
	var desc []string
	for _, n := range names {
		desc = append(desc, m.app.TaskHelp(n))
	}
	m.pick("task", names, desc, 0, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		name := chosen[0]
		args := m.app.Tasks()[name].Args
		m.askTaskArgs(name, args, make([]string, 0, len(args)))
		return nil
	})
}

// askTaskArgs asks for the task's args one after another (a pick list where it offers choices, else
// a line prefilled with the default), then confirms and runs it; esc anywhere drops the task.
func (m *model) askTaskArgs(name string, args []spec.TaskArg, answers []string) {
	if len(answers) == len(args) {
		m.runTask(name, engine.TaskArgValues(args, answers))
		return
	}
	arg := args[len(answers)]
	label := name + " › " + arg.Name
	if arg.Help != "" {
		label += ": " + arg.Help
	}
	next := func(v string) tea.Cmd {
		m.askTaskArgs(name, args, append(answers, v))
		return nil
	}
	choices := m.app.TaskArgChoices(m.ctx, arg)
	if len(choices) == 0 {
		m.ask(label, arg.Default, next)
		return
	}
	if arg.Multi {
		m.pickMany(label+"  (space marks, enter takes)", choices, nil, strings.Fields(arg.Default), func(c []string) tea.Cmd {
			if len(c) == 0 {
				return nil
			}
			return next(strings.Join(c, " "))
		})
		return
	}
	m.pick(label, choices, nil, max(0, slices.Index(choices, arg.Default)), false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		return next(c[0])
	})
}

// runTask confirms, then runs rig task in the terminal; ctrl+c stops the task, not the shell
// that waits for enter after it.
func (m *model) runTask(name string, args []string) {
	run := func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			return statusMsg{text: err.Error(), err: true}
		}
		script := `trap 'echo; echo "✖ stopped"' INT; "$0" "$@"; rc=$?; echo; [ $rc = 0 ] && echo "✓ done" || echo "✖ failed ($rc)"; printf "press enter "; read _; exit $rc`
		cmd := exec.Command("sh", append([]string{"-c", script, self, "-f", m.app.Spec.File, "-e", m.app.Env.Name, "--yes", "task", name}, args...)...)
		errFile := filepath.Join(os.TempDir(), fmt.Sprintf("rig-task-%d.err", os.Getpid()))
		_ = os.Remove(errFile)
		cmd.Env = append(os.Environ(), "RIG_ERROR_FILE="+errFile)
		return execProcess(cmd, func(err error) tea.Msg {
			if err != nil {
				text := "task " + name + " failed"
				if raw, rerr := os.ReadFile(errFile); rerr == nil {
					text = strings.ReplaceAll(strings.TrimSpace(string(raw)), "\n ", " ·")
				}
				_ = os.Remove(errFile)
				return statusMsg{text: text, err: true}
			}
			return statusMsg{text: "task " + name + " ✓"}
		})()
	}
	what := name
	if len(args) > 0 {
		what += " " + strings.Join(args, " ")
	}
	m.confirm = &confirm{text: "run task " + what + " on " + m.app.Env.Name + "?", run: run}
}

type namespacesMsg struct {
	names []string
	err   error
}

// pickNamespace lists the cluster's namespaces; picking one (or a new one) reopens the environment in it.
func (m *model) pickNamespace() tea.Cmd {
	a := m.app
	if a.Namespace() == "" {
		m.setStatus(a.Env.Name+" is not on Kubernetes", true)
		return nil
	}
	if a.Env.Protected {
		m.setStatus(a.Env.Name+" is protected: its namespace is fixed in "+filepath.Base(a.Spec.File), true)
		return nil
	}
	ctx := m.ctx
	m.setStatus("listing namespaces…", false)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		names, err := a.Namespaces(c)
		return namespacesMsg{names: names, err: err}
	}
}

func (m *model) showNamespaces(msg namespacesMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("namespaces: "+msg.err.Error(), true)
		return nil
	}
	const create = "+ new namespace…"
	names := append([]string{create}, msg.names...)
	sel := 0
	for i, n := range names {
		if n == m.app.Namespace() {
			sel = i
		}
	}
	file, env := m.app.Spec.File, m.app.Env.Name
	use := func(ns string, fresh bool) tea.Cmd {
		a := m.app
		m.setStatus("switching to namespace "+ns+"…", false)
		return func() tea.Msg {
			if fresh {
				if err := a.CreateNamespace(m.ctx, ns); err != nil {
					return envMsg{err: err}
				}
			}
			if err := a.UseNamespace(ns); err != nil {
				return envMsg{err: err}
			}
			b, err := engine.Open(file, env)
			return envMsg{app: b, err: err}
		}
	}
	m.pick("namespace of "+env+" (kept until rig ns --reset)", names, nil, sel, false, func(c []string) tea.Cmd {
		switch {
		case len(c) == 0 || c[0] == m.app.Namespace():
			return nil
		case c[0] == create:
			m.ask("new namespace", "", func(v string) tea.Cmd {
				if v = strings.TrimSpace(v); v == "" {
					return nil
				}
				return use(v, true)
			})
			return nil
		}
		return use(c[0], false)
	})
	return nil
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
		if e.ReadOnly {
			d += sAmber.Render("read-only ")
		} else if e.Protected {
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
	if m.sel != nil && m.sel.dragged {
		return m.sel.paint()
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
		body = m.overlay(m.helpView(bodyH), bodyH)
	case m.showAlerts:
		body = m.overlay(m.alertsView(), bodyH)
	case m.envInfo != nil:
		body = m.overlay(m.envInfo.view(bodyH), bodyH)
	case m.prompt != nil && m.prompt.popup:
		body = m.overlay(m.promptPopup(), bodyH)
	case m.picker != nil:
		body = m.picker.view(m, bodyH)
	case m.chat != nil && m.chat.open:
		cw := m.chatWidth()
		if cw >= m.w {
			body = m.chat.view(m, 0, m.w, bodyH)
			break
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(m.w-cw).MaxWidth(m.w-cw).Render(m.tabs[m.active].view(m, m.w-cw, bodyH)), m.chat.view(m, m.w-cw, cw, bodyH))
	default:
		body = m.tabs[m.active].view(m, m.w, bodyH)
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	m.bodyEnd = m.originY + bodyH
	m.frame = lipgloss.JoinVertical(lipgloss.Left, header, tabs, body, footer)
	return m.frame
}

func (m *model) overlay(box string, h int) string {
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m *model) header() string {
	a := m.app
	left := sAccent.Render(" ◆ rig ") + sTitle.Render(a.Spec.Name) + sDim.Render("  env ") + sTitle.Render(a.Env.Name) +
		sDim.Render(" ("+a.Env.Runtime.Type+")")
	if ns := a.Namespace(); ns != "" {
		left += sDim.Render("  ns ") + sTitle.Render(ns)
	}
	if a.Env.ReadOnly {
		left += " " + lipgloss.NewStyle().Background(cAmber).Foreground(lipgloss.Color("#000000")).Bold(true).Render(" READ-ONLY ")
	} else if a.Env.Protected {
		left += " " + lipgloss.NewStyle().Background(cRed).Foreground(lipgloss.Color("#FFFFFF")).Bold(true).Render(" PROTECTED ")
	}
	if m.simple {
		left += sDim.Render("  simple view")
	}
	if b := m.alertBadge(); b != "" {
		left += " " + b
	}
	if b := m.watchBadge(); b != "" {
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
	// names shrink until every screen fits, so each stays clickable on a narrow terminal
	n := 0
	for _, t := range m.tabs {
		n = max(n, len(t.name()))
	}
	for ; n > 3; n-- {
		w := 0
		for _, t := range m.tabs {
			w += 4 + min(n, len(t.name()))
		}
		if w <= m.w {
			break
		}
	}
	var parts []string
	m.tabSpan = m.tabSpan[:0]
	x := 0
	for i, t := range m.tabs {
		key := fmt.Sprint((i + 1) % 10)
		if i == 10 {
			key = "`"
		}
		name := t.name()
		if len(name) > n {
			name = name[:n]
		}
		var p string
		switch {
		case i == m.active:
			p = sTabOn.Render(key + " " + name)
		case m.hy == 1 && m.hx >= x && m.hx < x+lipgloss.Width(sTabOff.Render(key+" "+name)) && m.picker == nil:
			p = sBand.Render(" ") + sKey.Background(cBar).Render(key) + sTabHover.Background(cBar).Padding(0).Render(" "+name) + sBand.Render(" ")
		default:
			p = sBand.Render(" ") + sKey.Background(cBar).Render(key) + sBand.Foreground(cDim).Render(" "+name+" ")
		}
		w := lipgloss.Width(p)
		m.tabSpan = append(m.tabSpan, [2]int{x, x + w})
		x += w
		parts = append(parts, p)
	}
	bar := truncate(strings.Join(parts, ""), m.w)
	return bar + sBand.Render(strings.Repeat(" ", max(0, m.w-lipgloss.Width(bar))))
}

func (m *model) footer() string {
	var line string
	switch {
	case m.confirm != nil:
		line = sAmber.Bold(true).Render(" "+m.confirm.text) + "   " + m.buttons("confirm (enter)", "cancel (any key)", lipgloss.Width(sAmber.Bold(true).Render(" "+m.confirm.text))+3, m.h-2)
		if m.confirm.always != nil {
			line += sDim.Render("  a: always, this session")
		}
	case m.prompt != nil && m.prompt.popup:
		var hs []string
		for _, h := range [][2]string{{"enter", "run"}, {"esc", "cancel"}, {"ctrl+a shift+←→", "select"}, {"ctrl+c ctrl+x ctrl+v", "copy cut paste"}, {"ctrl+⌫", "word"}, {"tab", "take the AI's / the template"}} {
			hs = append(hs, sKey.Render(h[0])+" "+sDim.Render(h[1]))
		}
		line = " " + strings.Join(hs, "  ")
	case m.prompt != nil:
		line = m.promptView()
	case m.picker != nil:
		line = " " + m.picker.hints()
	case m.chat != nil && m.chat.open && m.chat.focus:
		var hs []string
		for _, h := range [][2]string{{"enter", "send"}, {"tab", "ideas"}, {"↑↓ wheel", "scroll"}, {"ctrl+x", "stop"}, {"/sessions /new /close", ""}, {"esc", "hide"}, {"click left", "back to the screen"}} {
			hs = append(hs, sKey.Render(h[0])+" "+sDim.Render(h[1]))
		}
		line = " " + strings.Join(hs, "  ")
	default:
		// the first few keys of the screen only: ? lists them all, so the footer stays readable
		var hs []string
		n := footerHints
		if m.simple {
			n = 3
		}
		for i, h := range m.tabs[m.active].hints() {
			if i == n {
				break
			}
			hs = append(hs, sKey.Render(h[0])+" "+sDim.Render(h[1]))
		}
		hs = append(hs, sKey.Render("?")+" "+sDim.Render("all keys"), sKey.Render("@")+" "+sDim.Render("AI"))
		if m.simple {
			hs = append(hs, sKey.Render("V")+" "+sDim.Render("detailed view"))
		} else {
			hs = append(hs, sKey.Render("E")+" "+sDim.Render("env"))
			if m.app.Namespace() != "" && !m.app.Env.Protected {
				hs = append(hs, sKey.Render("N")+" "+sDim.Render("namespace"))
			}
			hs = append(hs, sKey.Render("T")+" "+sDim.Render("tasks"))
		}
		hs = append(hs, sKey.Render("q")+" "+sDim.Render("quit"))
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
	return clip(line, m.w, m.h/2) + "\n" + status
}

// promptView is the footer's input: one line while it fits, else the label above a full-width input
// with the whole text wrapped over it, so a long query stays readable while the cursor scrolls.
func (m *model) promptView() string {
	p := m.prompt
	label := p.label
	switch {
	case p.waiting:
		label += sDim.Render(" ⋯ai")
	case p.hint != "" && p.input.ShowSuggestions && len(p.input.MatchedSuggestions()) > 0:
		label += sDim.Render(" (tab accepts)")
	case p.input.Value() == "" && p.template != "":
		label += sDim.Render(" (tab fills, enter runs)")
	}
	if p.hint != "" && m.ai != nil {
		state := "ai off · ctrl+t"
		if m.ai.Setup.AutocompleteOn() {
			state = "ai on · ctrl+t"
			if p.took > 0 {
				state = fmt.Sprintf("ai on %.1fs · ctrl+t", p.took.Seconds())
			}
		}
		label += sDim.Render("  " + state)
	}
	head := sAccent.Render(" " + label + ": ")
	const buttonsW = 28
	text := p.input.Value()
	if text == "" {
		text = p.template
	}
	above := ""
	if p.described != "" && p.describedFor == p.input.Value() {
		above = panel("✦ ai suggests", sTitle.Render(p.described)+sDim.Render("   tab takes it, enter runs it"), m.w, 4, false) + "\n"
	} else if _, _, ok := describing(p.input.Value()); ok && p.hint != "" && !p.waiting {
		above = sDim.Render(" ✦ describe what you want after "+aiTrigger+" — the AI writes it here") + "\n"
	}
	if room := m.w - lipgloss.Width(head) - 3 - buttonsW; lipgloss.Width(text)+2 <= room {
		p.input.Width = room
		m.zones = append(m.zones, zone{id: "prompt:input", x: lipgloss.Width(head), y: m.h - 2, w: room, h: 1})
		head += inputView(p.input, p.sel, room, &p.off)
		return above + head + "   " + m.buttons("ok (enter)", "cancel (esc)", lipgloss.Width(head)+3, m.h-2)
	}
	p.input.Width = m.w - 4
	var preview []string
	if lipgloss.Width(p.input.Value()) > p.input.Width {
		for _, l := range strings.Split(wordWrap(p.input.Value(), m.w-2), "\n") {
			preview = append(preview, " "+sDim.Render(l))
		}
		preview = preview[max(0, len(preview)-8):]
	}
	top := head + "   " + m.buttons("ok (enter)", "cancel (esc)", lipgloss.Width(head)+3, m.h-3-len(preview))
	m.zones = append(m.zones, zone{id: "prompt:input", x: 1, y: m.h - 2, w: p.input.Width, h: 1})
	return above + strings.Join(append(append([]string{top}, preview...), " "+inputView(p.input, p.sel, p.input.Width, &p.off)), "\n")
}

const footerHints = 5

// screenHelp says what each screen is for, at the top of its help.
var screenHelp = map[string]string{
	"Services":  "what runs in this environment: start, stop, restart, scale, deploy, logs, debug",
	"Logs":      "follow the logs of one or more services, filter and search them",
	"Metrics":   "the dashboards of rig.yaml over the environment's Prometheus",
	"Traces":    "find a request's trace (Zipkin/Jaeger) and walk its spans",
	"Queries":   "saved queries (SQL, PromQL, redis, HTTP): run, schedule, chart",
	"Data":      "walk databases, queues and caches; edit or delete rows and keys",
	"KV":        "the config store (Consul): browse, edit and delete keys right in it; F loads the config files",
	"Load":      "load generators: start, stop, change the rate, watch what they send",
	"Hosts":     "the machines the environment runs on: CPU, memory, disk, a shell",
	"Manifests": "Kubernetes manifests of the project: objects, links, issues, apply",
	"Tests":     "the test suites of rig.yaml: run, rerun failures, reports",
}

// helpLines are the help text's rows, split for scrolling and search; the content itself is
// unchanged from before, just no longer rendered as one fixed block.
func (m *model) helpLines() []string {
	rows := [][2]string{
		{"1-9 0 `  tab ⇧tab  alt+←→", "switch screen (or click its name)"}, {"⇧←→", "switch the sub-tab inside a screen"}, {"E", "switch environment"}, {"N", "switch or create a Kubernetes namespace"}, {"T", "run a task (rig task shows what each does)"},
		{"↑↓ / wheel", "move"}, {"enter / dbl-click", "open, run"}, {"ctrl+⇧←→ ctrl+⇧↑↓", "sort column, order (or click a header)"},
		{"esc", "back"}, {"@", "AI chat about this screen (rig ai config sets it up)"}, {"A", "alerts (header badge)"}, {"ctrl+e", "this environment: variables, databases, addresses (↑↓, e edits a variable)"}, {"ctrl+w", "watch: rebuild and restart services as their sources change (errors in A)"}, {"S", "save this session (rig resume <id>)"}, {"M", "mouse on/off (off: select text)"}, {"V", "simple / detailed view"}, {"?", "this help"}, {"q  ctrl+c", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(sKey.Render(padRight(r[0], 18)) + " " + r[1] + "\n")
	}
	name := m.tabs[m.active].name()
	b.WriteString("\n" + sTitle.Render(name) + "  " + sDim.Render(screenHelp[name]) + "\n")
	for _, r := range m.tabs[m.active].hints() {
		b.WriteString(sKey.Render(padRight(r[0], 18)) + " " + r[1] + "\n")
	}
	ask := "nothing asks for confirmation here"
	if m.needsConfirm(true) {
		ask = "dangerous changes (stop, deploy, delete, edits) ask first: enter confirms"
	}
	if m.app.Env.ReadOnly {
		ask = "read-only: every change is refused, reads and queries work"
	} else if m.app.Env.Protected {
		ask = "protected: every change asks first, enter confirms"
	}
	b.WriteString("\n" + sDim.Render(ask) + "\n")
	b.WriteString(sDim.Render("screens load only when opened; services, components and environments come from " + m.app.Spec.File))
	return strings.Split(b.String(), "\n")
}

// helpMatch finds the next line (from, wrapping) containing q, case-insensitively; -1 when q is
// empty or matches nothing.
func helpMatch(lines []string, q string, from int) int {
	if q == "" || len(lines) == 0 {
		return -1
	}
	q = strings.ToLower(q)
	for i := range lines {
		idx := (from + i) % len(lines)
		if strings.Contains(strings.ToLower(ansi.Strip(lines[idx])), q) {
			return idx
		}
	}
	return -1
}

// helpKey drives the open help overlay: scroll it with the usual keys, esc/q/? close it, / opens
// a search (reusing the footer prompt) that jumps to and highlights matching lines, n repeats it.
func (m *model) helpKey(k tea.KeyMsg) tea.Cmd {
	lines := m.helpLines()
	last := max(0, len(lines)-1)
	switch k.String() {
	case "esc", "q", "?", "f1":
		m.help, m.helpQuery = false, ""
	case "up", "k":
		m.helpScroll = max(0, m.helpScroll-1)
	case "down", "j":
		m.helpScroll = min(last, m.helpScroll+1)
	case "pgup":
		m.helpScroll = max(0, m.helpScroll-20)
	case "pgdown":
		m.helpScroll = min(last, m.helpScroll+20)
	case "home", "g":
		m.helpScroll = 0
	case "end", "G":
		m.helpScroll = last
	case "/":
		m.ask("search help", m.helpQuery, func(v string) tea.Cmd {
			m.helpQuery = v
			if i := helpMatch(lines, v, 0); i >= 0 {
				m.helpScroll = i
			}
			return nil
		})
	case "n":
		if i := helpMatch(lines, m.helpQuery, m.helpScroll+1); i >= 0 {
			m.helpScroll = i
		}
	}
	return nil
}

// helpView windows helpLines to h rows from helpScroll, highlighting the line a search landed on.
func (m *model) helpView(h int) string {
	lines := m.helpLines()
	inner := max(1, h-4) // border + padding
	m.helpScroll = min(m.helpScroll, max(0, len(lines)-inner))
	end := min(len(lines), m.helpScroll+inner)
	visible := append([]string{}, lines[m.helpScroll:end]...)
	if m.helpQuery != "" {
		for i := range visible {
			if strings.Contains(strings.ToLower(ansi.Strip(visible[i])), strings.ToLower(m.helpQuery)) {
				visible[i] = highlight(sSelected, visible[i], lipgloss.Width(visible[i]))
			}
		}
	}
	title := "rig — keys"
	if m.helpQuery != "" {
		title += sDim.Render("  /" + m.helpQuery + " (n: next)")
	}
	if len(lines) > inner {
		title += sDim.Render(fmt.Sprintf("  %d-%d/%d", m.helpScroll+1, end, len(lines)))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render(title) + "\n\n" + strings.Join(visible, "\n"))
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

// promptPopup is a popup prompt's box: the label, the whole input wrapped with its cursor, and
// what the AI offers.
func (m *model) promptPopup() string {
	p := m.prompt
	w := min(m.w-6, 110)
	text, style := p.input.Value(), lipgloss.NewStyle()
	if text == "" {
		text, style = p.template, sDim
	}
	runes := []rune(text)
	pos := min(p.input.Position(), len(runes))
	body := selectedView(p.input, p.sel, style)
	if p.input.Value() == "" {
		at := " "
		if pos < len(runes) {
			at = string(runes[pos])
		}
		after := ""
		if pos < len(runes) {
			after = string(runes[pos+1:])
		}
		body = sCursor.Render(at) + style.Render(after)
	}
	body = ansi.Wrap(body, w-4, " ,")
	var notes []string
	if p.waiting {
		notes = append(notes, sDim.Render("⋯ the AI is writing"))
	}
	if s := p.input.MatchedSuggestions(); p.input.ShowSuggestions && len(s) > 0 && s[0] != p.input.Value() {
		notes = append(notes, sAccent.Render("✦ ")+sDim.Render(ansi.Wrap(s[0], w-6, " ,"))+sDim.Render("  (tab takes it)"))
	}
	if p.input.Value() == "" && p.template != "" {
		notes = append(notes, sDim.Render("tab fills in the template, enter runs it as is"))
	}
	content := sTitle.Render(p.label) + "\n\n" + body
	if len(notes) > 0 {
		content += "\n\n" + strings.Join(notes, "\n")
	}
	if p.described != "" && p.describedFor == p.input.Value() {
		content += "\n\n" + panel("✦ ai suggests", ansi.Wrap(p.described, w-6, " ,")+sDim.Render("  (tab takes it)"), w-2, 5, false)
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(w).Render(content)
}
