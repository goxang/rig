// Package tui is rig's terminal control plane: one screen per concern (services, metrics, load,
// logs, traces, data, queries, manifests, hosts), all driven by the same engine as the CLI.
package tui

import (
	"context"
	"fmt"
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
	envPick *envPick
	help    bool

	services []core.Status
	svcAt    time.Time
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

type envPick struct {
	names []string
	sel   int
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
	envMsg struct {
		app *engine.App
		err error
	}
)

func newTabs() []tab {
	return []tab{&overviewTab{}, &servicesTab{}, &metricsTab{}, &loadTab{}, &logsTab{}, &tracesTab{}, &dataTab{}, &queryTab{}, &manifestsTab{}, &hostsTab{}}
}

func Run(ctx context.Context, a *engine.App) error {
	if a.Env == nil {
		return fmt.Errorf("no environment: define one under environments: and set default:")
	}
	m := &model{ctx: ctx, app: a, opened: map[int]bool{}, tabs: newTabs()}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err == tea.ErrProgramKilled {
		return nil
	}
	return err
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.openTab(0), m.fetchServices(), tick())
}

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) openTab(i int) tea.Cmd {
	m.active = i
	if !m.opened[i] {
		m.opened[i] = true
		return m.tabs[i].open(m)
	}
	return m.tabs[i].refresh(m)
}

// do runs a read-only slow operation off the UI loop and reports its outcome in the footer.
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

// mutate asks before a change; the answer also counts as the confirmation a protected environment needs.
func (m *model) mutate(label string, f func(ctx context.Context) error) tea.Cmd {
	text := label + "?"
	if m.app.Env.Protected {
		text = label + " on PROTECTED " + m.app.Env.Name + "?"
	}
	a, ctx := m.app, core.WithConfirmed(m.ctx)
	m.confirm = &confirm{text: text, run: func() tea.Msg {
		a.Confirmed = true
		defer func() { a.Confirmed = false }()
		if err := f(ctx); err != nil {
			return statusMsg{text: label + ": " + err.Error(), err: true}
		}
		return statusMsg{text: label + " ✓"}
	}}
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
	gen, a, ctx := m.gen, m.app, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return servicesMsg{gen: gen, sts: a.StatusAll(c, a.Spec.ServiceNames())}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(tick(), m.fetchServices(), m.tabs[m.active].refresh(m))
	case servicesMsg:
		if msg.gen == m.gen {
			m.services, m.svcAt = msg.sts, time.Now()
		}
		return m, nil
	case statusMsg:
		m.busy = max(0, m.busy-1)
		m.setStatus(msg.text, msg.err)
		return m, tea.Batch(m.fetchServices(), m.tabs[m.active].refresh(m))
	case envMsg:
		if msg.err != nil {
			m.setStatus("switch environment: "+msg.err.Error(), true)
			return m, nil
		}
		old := m.app
		m.app, m.gen = msg.app, m.gen+1
		go old.Close()
		m.services = nil
		m.opened = map[int]bool{}
		m.tabs = newTabs()
		m.setStatus("environment "+m.app.Env.Name, false)
		return m, tea.Batch(m.openTab(m.active), m.fetchServices())
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	// data arriving for a tab must reach it even when another tab is showing
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		cmds = append(cmds, t.update(m, msg))
	}
	return m, tea.Batch(cmds...)
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	if k.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if k.String() == "y" || k.String() == "Y" {
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
	if m.envPick != nil {
		p := m.envPick
		switch k.String() {
		case "esc", "q":
			m.envPick = nil
		case "up", "k":
			p.sel = max(0, p.sel-1)
		case "down", "j":
			p.sel = min(len(p.names)-1, p.sel+1)
		case "enter":
			m.envPick = nil
			name, file := p.names[p.sel], m.app.Spec.File
			m.setStatus("opening "+name+"…", false)
			return func() tea.Msg {
				a, err := engine.Open(file, name)
				return envMsg{app: a, err: err}
			}
		}
		return nil
	}
	if m.help {
		m.help = false
		return nil
	}
	t := m.tabs[m.active]
	if !t.typing() {
		switch s := k.String(); s {
		case "q":
			return tea.Quit
		case "?":
			m.help = true
			return nil
		case "E":
			names := m.app.Spec.EnvironmentNames()
			sel := 0
			for i, n := range names {
				if n == m.app.Env.Name {
					sel = i
				}
			}
			m.envPick = &envPick{names: names, sel: sel}
			return nil
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

func (m *model) View() string {
	if m.w == 0 {
		return "loading…"
	}
	header := m.header()
	tabs := m.tabBar()
	footer := m.footer()
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(tabs) - lipgloss.Height(footer)
	var body string
	switch {
	case m.help:
		body = m.overlay(m.helpView(), bodyH)
	case m.envPick != nil:
		body = m.overlay(m.envView(), bodyH)
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
	up := 0
	for _, s := range m.services {
		if engine.Ready(s) {
			up++
		}
	}
	right := fmt.Sprintf("%s %d/%d up  %s ", sGreen.Render("●"), up, len(m.services), sDim.Render(time.Now().Format("15:04:05")))
	if m.busy > 0 {
		right = sAmber.Render("⟳ working  ") + right
	}
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	return sHeader.Width(m.w).Render(left + strings.Repeat(" ", gap) + right)
}

func (m *model) tabBar() string {
	var parts []string
	for i, t := range m.tabs {
		key := fmt.Sprint((i + 1) % 10)
		if i == m.active {
			parts = append(parts, sTabOn.Render(key+" "+t.name()))
		} else {
			parts = append(parts, sTabOff.Render(key+" "+t.name()))
		}
	}
	return truncate(strings.Join(parts, ""), m.w)
}

func (m *model) footer() string {
	var line string
	switch {
	case m.confirm != nil:
		line = sAmber.Render(" "+m.confirm.text) + sDim.Render("  y to confirm, any key to cancel")
	case m.prompt != nil:
		line = sAccent.Render(" "+m.prompt.label+": ") + m.prompt.input.View() + sDim.Render("   enter ok · esc cancel")
	default:
		var hs []string
		for _, h := range m.tabs[m.active].hints() {
			hs = append(hs, sKey.Render(h[0])+" "+sDim.Render(h[1]))
		}
		hs = append(hs, sKey.Render("E")+" "+sDim.Render("env"), sKey.Render("?")+" "+sDim.Render("help"), sKey.Render("q")+" "+sDim.Render("quit"))
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
		{"1-9 0 / tab", "switch screen"}, {"E", "switch environment"}, {"↑↓ / j k", "move"}, {"enter", "open / act"},
		{"?", "this help"}, {"q / ctrl+c", "quit"},
	}
	rows = append(rows, m.tabs[m.active].hints()...)
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(sKey.Render(padRight(r[0], 14)) + " " + r[1] + "\n")
	}
	b.WriteString("\n" + sDim.Render("services, components and environments come from "+m.app.Spec.File))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render("keys — "+m.tabs[m.active].name()) + "\n\n" + b.String())
}

func (m *model) envView() string {
	var b strings.Builder
	for i, n := range m.envPick.names {
		e := m.app.Spec.Environments[n]
		rt := ""
		if e.Runtime != nil {
			rt = e.Runtime.Type
		}
		line := padRight(n, 14) + sDim.Render(padRight(rt, 12)+e.Description)
		if e.Protected {
			line += " " + sRed.Render("protected")
		}
		if i == m.envPick.sel {
			line = sSelected.Render("▸ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render("environment") + "\n\n" + b.String())
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
