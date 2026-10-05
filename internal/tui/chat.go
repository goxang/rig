package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

// chat is the assistant's drawer (@): a conversation bound to the environment on screen, sent with
// what the screen shows. Turns run the user's opencode or Claude Code; rig's MCP server is its hands.
type chat struct {
	open, focus bool
	runner      *ai.Runner
	sess        *ai.Session
	// msgs is the transcript as shown; the running turn owns sess until it ends
	msgs    []ai.Message
	input   textinput.Model
	busy    bool
	cancel  context.CancelFunc
	started time.Time
	scroll  int
	starter int
	err     string
}

type (
	chatEventMsg struct {
		s *ai.Session
		e ai.Event
	}
	chatDoneMsg struct {
		s   *ai.Session
		err error
	}
	// bridgeMsg is a request from the assistant's tools: a go-ahead to ask for, or a UI action
	bridgeMsg struct {
		req   ai.Request
		reply chan ai.Reply
	}
)

var slashCommands = []string{"/new", "/sessions", "/close", "/stop", "/model", "/effort", "/fast", "/autocomplete", "/help"}

// screenStarters are offered (tab) in an empty chat, per screen.
var screenStarters = map[string][]string{
	"Services":  {"why is this service failing? check its status and logs", "suggest pprof commands to find where this service spends CPU and memory", "restart this service", "what is unhealthy in this environment?"},
	"Logs":      {"find the issue in these logs and where it comes from in the code", "which errors repeat most, and why?", "show only the errors of these services in the Logs screen"},
	"KV":        {"set this key's value to ", "explain this configuration", "which services read this key? restart them"},
	"Data":      {"write a query for ", "explain this table and how the code uses it", "run this procedure with sample inputs and explain the result"},
	"Queries":   {"add a query that counts failed transactions in the last 5 minutes and schedule it every 30s", "explain this result"},
	"Metrics":   {"explain what these panels show right now", "add a query for the p99 latency of each service"},
	"Traces":    {"why is this trace slow?", "which service fails in this trace?"},
	"Load":      {"what limits the throughput right now?", "raise the rate one step and watch the error rate"},
	"Tests":     {"why do these tests fail?"},
	"Manifests": {"what is broken in these manifests?"},
	"Hosts":     {"which host is under pressure and why?"},
}

// ideas are the project's ai.ideas for this screen and for "all", then rig's own.
func (m *model) ideas() []string {
	name := m.tabs[m.active].name()
	var out []string
	if a := m.app.Spec.AI; a != nil {
		out = append(append(out, a.Ideas[strings.ToLower(name)]...), a.Ideas["all"]...)
	}
	return append(out, screenStarters[name]...)
}

func (m *model) chatOpen() *chat {
	if m.chat == nil {
		in := textinput.New()
		in.Prompt = "› "
		in.Placeholder = "ask anything · tab: ideas · /sessions"
		in.ShowSuggestions = true
		in.SetSuggestions(slashCommands)
		m.chat = &chat{input: in}
	}
	c := m.chat
	if c.runner == nil {
		c.runner = m.aiRunner()
		if c.runner == nil {
			c.err = "could not read the AI setup: rig ai config"
		}
	}
	c.open, c.focus = true, true
	c.input.Focus()
	return c
}

func (c *chat) enabled() bool { return c.runner != nil && c.runner.Setup.Enabled() }

// newSession starts a conversation on the environment on screen.
func (c *chat) newSession(m *model) {
	c.sess, c.msgs, c.scroll, c.starter = nil, nil, 0, 0
	if c.enabled() {
		c.sess = ai.NewSession(m.app.AIDir(), c.runner.Setup.Backend, m.app.Env.Name)
	}
}

func (c *chat) load(m *model, id string) error {
	s, err := ai.LoadSession(m.app.AIDir(), id)
	if err != nil {
		return err
	}
	if s.Env != m.app.Env.Name {
		return fmt.Errorf("conversation %s is on %s: switch there (E) first", s.ID, s.Env)
	}
	c.sess, c.msgs, c.scroll = s, append([]ai.Message{}, s.Messages...), 0
	return nil
}

// reset follows an environment switch: the old conversation stays with its environment.
func (c *chat) reset() {
	if c.cancel != nil {
		c.cancel()
	}
	c.runner, c.sess, c.msgs, c.busy, c.scroll = nil, nil, nil, false, 0
}

func (m *model) chatKey(k tea.KeyMsg) tea.Cmd {
	c := m.chat
	switch k.String() {
	case "@":
		if c.input.Value() != "" {
			break
		}
		fallthrough
	case "esc":
		c.open, c.focus = false, false
		return nil
	case "ctrl+x":
		if c.busy && c.cancel != nil {
			c.cancel()
		}
		return nil
	case "pgup", "up":
		if k.String() == "pgup" {
			c.scroll += 10
		} else {
			c.scroll++
		}
		return nil
	case "pgdown", "down":
		if k.String() == "pgdown" {
			c.scroll = max(0, c.scroll-10)
		} else {
			c.scroll = max(0, c.scroll-1)
		}
		return nil
	case "tab":
		v := c.input.Value()
		ideas := m.ideas()
		if len(ideas) > 0 && (v == "" || contains(ideas, v)) {
			c.input.SetValue(ideas[c.starter%len(ideas)])
			c.input.CursorEnd()
			c.starter++
			return nil
		}
	case "enter":
		v := strings.TrimSpace(c.input.Value())
		if v == "" {
			return nil
		}
		c.input.SetValue("")
		if strings.HasPrefix(v, "/") {
			return m.chatCommand(v)
		}
		return m.chatSend(v)
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	return cmd
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (m *model) chatCommand(v string) tea.Cmd {
	c := m.chat
	switch strings.Fields(v)[0] {
	case "/new":
		if c.busy {
			m.setStatus("a turn is running: ctrl+x stops it", true)
			return nil
		}
		c.newSession(m)
	case "/stop":
		if c.cancel != nil {
			c.cancel()
		}
	case "/close":
		if c.busy {
			m.setStatus("a turn is running: ctrl+x stops it", true)
			return nil
		}
		if c.sess != nil {
			_ = ai.CloseSession(m.app.AIDir(), c.sess.ID)
		}
		c.newSession(m)
		m.setStatus("conversation closed", false)
	case "/sessions":
		m.pickChatSession()
	case "/model", "/fast":
		return m.pickModel(strings.Fields(v)[0] == "/fast")
	case "/effort":
		if c.runner == nil {
			return nil
		}
		levels := append([]string{"(default)"}, c.runner.Setup.Efforts()...)
		m.pick("effort of chat turns ("+c.runner.Setup.Backend+")", levels, nil, max(0, indexOf(levels, c.runner.Setup.Effort)), false, func(l []string) tea.Cmd {
			if len(l) > 0 {
				m.setAI("effort", strings.TrimPrefix(l[0], "(default)"))
			}
			return nil
		})
	case "/autocomplete":
		m.toggleAutocomplete()
	default:
		c.msgs = append(c.msgs, ai.Message{Role: "assistant", Text: "/new starts over · /sessions picks a conversation (ctrl+d closes one there) · /close forgets this one · /stop or ctrl+x stops a turn · /model and /effort set the chat's model and reasoning level, /fast the completions' model, /autocomplete turns suggestions while typing on or off · esc hides the chat, @ brings it back. Anything else goes to the assistant with what this screen shows."})
	}
	return nil
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

type aiModelsMsg struct {
	fast   bool
	models []string
	err    error
}

// pickModel lists the models the backend (or, for /fast, the completion endpoint) offers.
func (m *model) pickModel(fast bool) tea.Cmd {
	r := m.aiRunner()
	if r == nil {
		return nil
	}
	m.setStatus("listing models…", false)
	ctx := m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ms, err := r.Models(c, fast)
		return aiModelsMsg{fast: fast, models: ms, err: err}
	}
}

func (m *model) modelsListed(msg aiModelsMsg) {
	if msg.err != nil {
		m.setStatus("models: "+msg.err.Error(), true)
		return
	}
	m.setStatus("", false)
	key, cur, title := "model", m.ai.Setup.Model, "chat model ("+m.ai.Setup.Backend+") · type to filter"
	if msg.fast {
		key, cur, title = "fast_model", m.ai.Setup.FastModel, "completion model · type to filter · the fastest flash/mini one is best"
	}
	items := append([]string{"(default)"}, msg.models...)
	m.pick(title, items, nil, max(0, indexOf(items, cur)), false, func(c []string) tea.Cmd {
		if len(c) > 0 {
			m.setAI(key, strings.TrimPrefix(c[0], "(default)"))
		}
		return nil
	})
}

// setAI saves one AI setting and applies it to the next turn and completion.
func (m *model) setAI(key, value string) {
	if err := ai.ChangeConfig(key, value); err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	m.ai = nil
	if c := m.chat; c != nil {
		c.runner = m.aiRunner()
	}
	shown := value
	if shown == "" {
		shown = "default"
	}
	m.setStatus("AI "+key+": "+shown, false)
}

// pickChatSession lists this environment's conversations: enter continues one, d closes it.
func (m *model) pickChatSession() {
	ss, err := ai.ListSessions(m.app.AIDir())
	if err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	var ids, desc []string
	for _, s := range ss {
		if s.Env == m.app.Env.Name {
			ids = append(ids, s.ID)
			desc = append(desc, fmt.Sprintf("%s · %d msgs · %s", s.Updated.Format("01-02 15:04"), len(s.Messages), s.Title))
		}
	}
	if len(ids) == 0 {
		m.setStatus("no conversations on "+m.app.Env.Name+" yet", false)
		return
	}
	m.pick("conversations on "+m.app.Env.Name+" (ctrl+d closes one)", ids, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		if err := m.chat.load(m, c[0]); err != nil {
			m.setStatus(err.Error(), true)
		}
		m.chatOpen()
		return nil
	})
	m.picker.del = func(id string) error {
		if c := m.chat; c.sess != nil && c.sess.ID == id {
			if c.busy {
				return errors.New("its turn is running: ctrl+x stops it")
			}
			c.newSession(m)
		}
		return ai.CloseSession(m.app.AIDir(), id)
	}
}

func (m *model) chatSend(text string) tea.Cmd {
	c := m.chat
	if !c.enabled() {
		m.setStatus("AI is off: rig ai config", true)
		return nil
	}
	if c.busy {
		m.setStatus("a turn is running: wait, or ctrl+x stops it", true)
		return nil
	}
	if c.sess == nil {
		c.newSession(m)
	}
	screen := m.screenContext()
	c.msgs = append(c.msgs, ai.Message{Role: "user", Text: text, At: time.Now()})
	c.busy, c.started, c.scroll = true, time.Now(), 0
	ctx, cancel := context.WithCancel(m.ctx)
	c.cancel = cancel
	r, s := c.runner, c.sess
	return func() tea.Msg {
		defer cancel()
		err := r.Turn(ctx, s, text, screen, func(e ai.Event) { program.Send(chatEventMsg{s: s, e: e}) })
		return chatDoneMsg{s: s, err: err}
	}
}

func (m *model) chatUpdate(msg tea.Msg) (tea.Cmd, bool) {
	c := m.chat
	switch msg := msg.(type) {
	case chatEventMsg:
		if c != nil && msg.s == c.sess {
			role := msg.e.Kind
			if role == "text" {
				role = "assistant"
			}
			c.msgs = append(c.msgs, ai.Message{Role: role, Text: msg.e.Text, At: time.Now()})
		}
		return nil, true
	case chatDoneMsg:
		if c != nil && msg.s == c.sess {
			c.busy = false
			if msg.err != nil && !errors.Is(msg.err, ai.ErrTurnFailed) {
				c.msgs = append(c.msgs, ai.Message{Role: "error", Text: msg.err.Error()})
			}
		}
		// the assistant may have changed what the screens show
		m.svcAt = time.Time{}
		return m.tabs[m.active].refresh(m), true
	case bridgeMsg:
		return m.bridge(msg), true
	case aiModelsMsg:
		m.modelsListed(msg)
		return nil, true
	}
	return nil, false
}

// bridge answers the assistant's tools: a confirmation the person gives in the footer, or a UI action.
func (m *model) bridge(b bridgeMsg) tea.Cmd {
	if b.req.Op == "approve" {
		if m.confirm != nil || m.prompt != nil {
			b.reply <- ai.Reply{Text: "the user is busy with another prompt; ask them in the chat"}
			return nil
		}
		m.confirm = &confirm{text: b.req.Text + "?", run: func() tea.Msg {
			b.reply <- ai.Reply{OK: true}
			return statusMsg{text: "approved for the assistant"}
		}, cancel: func() { b.reply <- ai.Reply{Text: "declined in rig"} }}
		return nil
	}
	ok, text, cmd := m.uiAction(b.req)
	b.reply <- ai.Reply{OK: ok, Text: text}
	return cmd
}

// uiAction is what rig_ui does: queries on the Queries screen, a screen opened, logs shown.
func (m *model) uiAction(r ai.Request) (bool, string, tea.Cmd) {
	a := r.Args
	switch r.Action {
	case "add_query":
		if a["component"] == "" || a["query"] == "" {
			return false, "add_query needs component and query", nil
		}
		if _, err := m.app.Component(a["component"]); err != nil {
			return false, err.Error(), nil
		}
		name := a["name"]
		if name == "" {
			name = fmt.Sprintf("ai-%d", len(m.sched.extra)+1)
		}
		q := &spec.Query{Name: name, Source: a["component"], Query: a["query"], Group: "ai", Every: 30 * time.Second, Help: a["query"]}
		scheduled := ""
		if a["every"] != "" {
			d, err := time.ParseDuration(a["every"])
			if err != nil || d < time.Second {
				return false, "every: a duration like 30s", nil
			}
			q.Every, scheduled = d, ", runs every "+d.String()
		}
		m.sched.mu.Lock()
		m.sched.extra[name] = q
		if scheduled != "" {
			m.sched.active[name] = true
		}
		m.sched.mu.Unlock()
		return true, "added " + name + " to the Queries screen" + scheduled, m.sched.run(m.ctx, name, q.Query)
	case "schedule", "unschedule":
		q := m.sched.queries()[a["name"]]
		if q == nil {
			return false, "no query " + a["name"], nil
		}
		on := r.Action == "schedule"
		nq := *q
		if on && a["every"] != "" {
			d, err := time.ParseDuration(a["every"])
			if err != nil || d < time.Second {
				return false, "every: a duration like 30s", nil
			}
			nq.Every = d
		}
		if on && nq.Every <= 0 {
			nq.Every = 30 * time.Second
		}
		m.sched.mu.Lock()
		m.sched.extra[a["name"]] = &nq
		m.sched.active[a["name"]] = on
		m.sched.mu.Unlock()
		if on {
			return true, a["name"] + " runs every " + nq.Every.String() + " while rig is open", nil
		}
		return true, a["name"] + " is no longer scheduled", nil
	case "open":
		for i, t := range m.tabs {
			if strings.EqualFold(t.name(), a["screen"]) {
				return true, "opened " + t.name(), m.openTab(i)
			}
		}
		return false, "no screen " + a["screen"], nil
	case "logs":
		for i, t := range m.tabs {
			if lt, ok := t.(*logsTab); ok {
				lt.services, lt.grep, lt.instance = strings.Fields(a["services"]), a["grep"], ""
				if !m.opened[i] {
					return true, "showing logs", m.openTab(i)
				}
				m.active = i
				return true, "showing logs", lt.start(m)
			}
		}
	}
	return false, "unknown action " + r.Action, nil
}

func (m *model) chatWidth() int {
	if m.w < 90 {
		return m.w
	}
	return min(max(46, m.w*2/5), 90)
}

func (c *chat) view(m *model, x, w, h int) string {
	m.zone("chat", x, 0, w, h)
	iw := w - 4
	title := "AI"
	if c.runner != nil {
		title += " · " + c.runner.Setup.Describe()
	}
	title += " · " + m.app.Env.Name
	if m.app.Env.Protected {
		title += " PROTECTED"
	}
	var lines []string
	add := func(s string) { lines = append(lines, strings.Split(s, "\n")...) }
	switch {
	case c.err != "":
		add(sRed.Render(wordWrap(c.err, iw)))
	case !c.enabled():
		why := "no backend"
		if c.runner != nil {
			why = c.runner.Setup.Why
		}
		add(sAmber.Render(wordWrap("AI is off: "+why, iw)))
		add("")
		add(sDim.Render(wordWrap("Install opencode (free models, no key) or Claude Code, or set a provider:\n  rig ai config provider=deepseek api_key=…\n  rig ai config provider=openai url=… api_key=… model=…\n  rig ai config proxy=localhost:10808\nrig ai check tests it.", iw)))
	case len(c.msgs) == 0:
		add(sDim.Render(wordWrap("Ask about what this screen shows, or to do anything you can do in rig. It works on "+m.app.Env.Name+" only; the project directory is its only workspace.", iw)))
		add("")
		add(sTitle.Render("ideas (tab)"))
		for _, s := range m.ideas() {
			add(sDim.Render(wordWrap("· "+s, iw)))
		}
	}
	for _, msg := range c.msgs {
		switch msg.Role {
		case "user":
			add("")
			add(sAccent.Render(wordWrap("› "+msg.Text, iw)))
		case "assistant":
			add(renderMarkdown(msg.Text, iw))
		case "tool":
			add(sDim.Render(wordWrap("  → "+msg.Text, iw)))
		case "error":
			add(sRed.Render(wordWrap("✖ "+msg.Text, iw)))
		}
		for _, t := range msg.Tools {
			add(sDim.Render(wordWrap("  → "+t, iw)))
		}
	}
	status := sDim.Render("enter send · tab ideas · /model /effort /sessions /new · esc hide")
	if c.busy {
		status = sAmber.Render(fmt.Sprintf("⟳ working %s", time.Since(c.started).Round(time.Second))) + sDim.Render(" · ctrl+x stops")
	} else if m.confirm != nil {
		status = sAmber.Render("↓ answer the question below")
	}
	room := max(1, h-4)
	end := max(0, len(lines)-c.scroll)
	c.scroll = len(lines) - end
	start := max(0, end-room)
	body := strings.Join(lines[start:end], "\n")
	if pad := room - (end - start); pad > 0 && len(c.msgs) > 0 {
		body = strings.Repeat("\n", pad) + body
	} else if pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	c.input.Width = iw - 3
	body += "\n" + c.input.View() + "\n" + status
	return panel(title, body, w, h, c.focus)
}

func wordWrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

// renderMarkdown colours what matters in an answer: code, headings, list bullets.
func renderMarkdown(s string, w int) string {
	var out []string
	code := false
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			code = !code
			continue
		case code:
			out = append(out, sGreen.Render(truncate("  "+l, w)))
			continue
		case strings.HasPrefix(t, "#"):
			out = append(out, sTitle.Render(wordWrap(strings.TrimLeft(t, "# "), w)))
			continue
		}
		l = strings.ReplaceAll(l, "**", "")
		out = append(out, wordWrap(l, w))
	}
	return strings.Join(out, "\n")
}

// aiContexter says what a screen shows, for the assistant: selection first, then a sample of the data.
type aiContexter interface {
	aiContext(m *model) string
}

func (m *model) screenContext() string {
	t := m.tabs[m.active]
	ctx := t.name()
	if c, ok := t.(aiContexter); ok {
		if s := strings.TrimSpace(c.aiContext(m)); s != "" {
			ctx += "\n" + s
		}
	}
	if len(ctx) > 12000 {
		ctx = ctx[:12000] + "\n…"
	}
	return ctx
}

func tableSample(t core.Table, rows int) string {
	var b strings.Builder
	b.WriteString(strings.Join(t.Columns, " | ") + "\n")
	for i, r := range t.Rows {
		if i == rows {
			fmt.Fprintf(&b, "… %d rows in all\n", len(t.Rows))
			break
		}
		cells := make([]string, len(r))
		for j, c := range r {
			cells[j] = truncate(c, 80)
		}
		b.WriteString(strings.Join(cells, " | ") + "\n")
	}
	return b.String()
}

func (t *servicesTab) aiContext(m *model) string {
	names := t.targets()
	var b strings.Builder
	fmt.Fprintf(&b, "selected services: %s\n", strings.Join(names, ", "))
	for _, n := range names {
		st := t.status(m, n)
		fmt.Fprintf(&b, "%s: %s ready %d/%d image %s %s\n", n, st.State, st.Ready, st.Desired, st.Image, st.Message)
	}
	var bad []string
	for _, s := range m.services {
		if s.State == core.StateFailed || s.State == core.StateDegraded {
			bad = append(bad, s.Service+" "+string(s.State))
		}
	}
	if len(bad) > 0 {
		b.WriteString("failing in the environment: " + strings.Join(bad, ", ") + "\n")
	}
	if t.log != nil && t.open_ != "" {
		b.WriteString("its log, last lines:\n" + logSample(t.log.lines, 60))
	}
	return b.String()
}

func logSample(lines []core.LogLine, n int) string {
	var b strings.Builder
	for _, l := range lines[max(0, len(lines)-n):] {
		b.WriteString(l.Service + " " + truncate(l.Text, 400) + "\n")
	}
	return b.String()
}

func (t *logsTab) aiContext(m *model) string {
	if len(t.services) == 0 || t.log == nil {
		return "no services picked (f picks them)"
	}
	s := "services: " + strings.Join(t.services, ", ")
	if t.grep != "" {
		s += "  grep: " + t.grep
	}
	lines := t.log.lines
	if re := t.grepRe(); re != nil {
		var kept []core.LogLine
		for _, l := range lines {
			if re.MatchString(l.Text) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	return s + "\nlast lines:\n" + logSample(lines, 150)
}

func (t *kvTab) aiContext(m *model) string {
	s := fmt.Sprintf("store %s, prefix %q", t.comp, t.prefix)
	if r, ok := t.list.current(); ok && t.list != nil {
		s += "\nselected key: " + r.id
	}
	if t.valueFor != "" {
		v := string(t.value)
		if len(v) > 4000 {
			v = v[:4000] + "…"
		}
		s += "\nvalue of " + t.valueFor + ":\n" + v
	}
	return s
}

func (t *dataTab) aiContext(m *model) string {
	c := t.current()
	s := fmt.Sprintf("component %s (%s %s)", c.name, c.kind, c.adapter)
	if p := t.paths[c.name]; len(p) > 0 {
		s += ", at " + strings.Join(p, " › ")
	}
	if r, ok := t.right.current(); ok && t.right != nil {
		_, child, _ := strings.Cut(r.id, "\x00")
		if child != "" {
			s += "\nselected: " + child
		}
	}
	if t.query != "" {
		s += "\nquery: " + t.query
	}
	if len(t.table.Columns) > 0 {
		s += "\n" + tableSample(t.table, 20)
	}
	return s
}

func (t *queriesTab) aiContext(m *model) string {
	name, q := t.selected(m)
	if q == nil {
		return ""
	}
	s := fmt.Sprintf("selected query %s on %s:\n%s", name, q.Source, q.Query)
	if r := m.sched.result(name); r != nil {
		if r.err != nil {
			s += "\nlast run failed: " + r.err.Error()
		} else {
			s += "\nlast result:\n" + tableSample(r.table, 20)
		}
	}
	var names []string
	for n := range t.langs {
		names = append(names, n+" ("+t.langs[n]+")")
	}
	sort.Strings(names)
	return s + "\nqueryable components: " + strings.Join(names, ", ")
}

func (t *metricsTab) aiContext(m *model) string {
	if len(t.dashNames(m)) == 0 {
		return ""
	}
	s := "dashboard " + t.dashName(m)
	for i, d := range t.data {
		if i == 12 {
			break
		}
		last := ""
		for j, se := range d.series {
			if j == 4 || len(se.Points) == 0 {
				break
			}
			name := ""
			if j < len(d.names) {
				name = d.names[j]
			}
			last += fmt.Sprintf(" %s=%.4g", name, se.Points[len(se.Points)-1].V)
		}
		s += fmt.Sprintf("\npanel %d: %s →%s", i+1, d.query, last)
	}
	return s
}

func (t *tracesTab) aiContext(m *model) string {
	s := ""
	if r, ok := t.list.current(); ok && t.list != nil {
		s = "selected trace " + r.id
	}
	for i, sp := range t.spans {
		if i == 40 {
			break
		}
		e := ""
		if sp.Error {
			e = " ERROR"
		}
		s += fmt.Sprintf("\n%s %s %s%s", sp.Service, sp.Name, sp.Duration, e)
	}
	return s
}

type (
	completeTickMsg struct{ seq int }
	completeMsg     struct {
		seq         int
		value, text string
		err         error
		took        time.Duration
	}
)

// aiRunner is the assistant for this environment, made on first use.
func (m *model) aiRunner() *ai.Runner {
	if m.ai == nil {
		r, err := m.app.AI(m.sock)
		if err != nil {
			return nil
		}
		m.ai = r
	}
	return m.ai
}

// askAI is ask with the rest of the input suggested by the AI while typing (tab takes it); hint says
// what is typed: the language, where it runs, names in reach.
func (m *model) askAI(label, value, hint string, submit func(string) tea.Cmd) {
	m.ask(label, value, submit)
	if r := m.aiRunner(); r != nil && r.Setup.Enabled() {
		m.prompt.hint = hint
		m.prompt.input.ShowSuggestions = r.Setup.AutocompleteOn()
	}
}

// askTemplate is askAI on an empty input with template behind it: typing starts fresh with the AI
// completing, tab takes the template to edit, enter runs it as is.
func (m *model) askTemplate(label, template, hint string, submit func(string) tea.Cmd) {
	m.askAI(label, "", hint, submit)
	m.prompt.template = template
	m.prompt.input.Placeholder = template
}

// toggleAutocomplete switches AI suggestions while typing, for good (ai.json autocomplete).
func (m *model) toggleAutocomplete() {
	r := m.aiRunner()
	if r == nil || !r.Setup.Enabled() {
		m.setStatus("AI is off: rig ai config", true)
		return
	}
	on := !r.Setup.AutocompleteOn()
	if err := ai.ChangeConfig("autocomplete", fmt.Sprint(on)); err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	r.Setup.Autocomplete = &on
	if p := m.prompt; p != nil {
		p.input.ShowSuggestions = on
		if !on {
			p.input.SetSuggestions(nil)
			p.waiting = false
			if m.completeCancel != nil {
				m.completeCancel()
			}
		}
	}
	if on {
		m.setStatus("AI autocomplete on", false)
	} else {
		m.setStatus("AI autocomplete off (ctrl+t or /autocomplete turns it back on)", false)
	}
}

// describing splits an input at ":?": the input before it and the words after it, which say what
// the AI should write in their place.
func describing(v string) (before, want string, ok bool) {
	before, want, ok = strings.Cut(v, ":?")
	return before, strings.TrimSpace(want), ok && strings.TrimSpace(want) != ""
}

type describeMsg struct {
	seq         int
	value, text string
	err         error
	took        time.Duration
}

// completeLater asks for a completion, or for what ":?" describes, once typing pauses.
func (m *model) completeLater() tea.Cmd {
	p := m.prompt
	if p == nil || p.hint == "" || m.ai == nil {
		return nil
	}
	if _, _, ok := describing(p.input.Value()); !ok && !m.ai.Setup.AutocompleteOn() {
		return nil
	}
	p.seq++
	seq := p.seq
	return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return completeTickMsg{seq: seq} })
}

func (m *model) completeDue(msg completeTickMsg) tea.Cmd {
	p := m.prompt
	if p == nil || p.seq != msg.seq || p.hint == "" {
		return nil
	}
	v := p.input.Value()
	before, want, describe := describing(v)
	if strings.TrimSpace(v) == "" || !describe && len(p.input.MatchedSuggestions()) > 0 {
		return nil
	}
	if m.completeCancel != nil {
		m.completeCancel()
	}
	ctx, cancel := context.WithTimeout(m.ctx, 90*time.Second)
	m.completeCancel = cancel
	p.waiting = true
	r, hint, seq := m.aiRunner(), p.hint, p.seq
	if p.template != "" {
		hint += "; the screen offered this as a starting point: " + p.template
	}
	if describe {
		p.input.SetSuggestions(nil)
		return func() tea.Msg {
			defer cancel()
			start := time.Now()
			text, err := r.Describe(ctx, hint, before, want)
			return describeMsg{seq: seq, value: v, text: text, err: err, took: time.Since(start)}
		}
	}
	return func() tea.Msg {
		defer cancel()
		start := time.Now()
		text, err := r.Complete(ctx, hint, v)
		return completeMsg{seq: seq, value: v, text: text, err: err, took: time.Since(start)}
	}
}

func (m *model) describedMsg(msg describeMsg) {
	p := m.prompt
	if p == nil {
		return
	}
	if p.seq == msg.seq {
		p.waiting = false
	}
	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) && p.seq == msg.seq {
			m.setStatus("AI: "+msg.err.Error(), true)
		}
		return
	}
	p.took = msg.took
	if msg.value == p.input.Value() && msg.text != "" {
		p.described, p.describedFor = msg.text, msg.value
	}
}

func (m *model) completed(msg completeMsg) {
	p := m.prompt
	if p == nil {
		return
	}
	if p.seq == msg.seq {
		p.waiting = false
	}
	if msg.err == nil {
		p.took = msg.took
	}
	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) && p.seq == msg.seq {
			m.setStatus("AI completion: "+msg.err.Error(), true)
		}
		return
	}
	if msg.text != "" && strings.HasPrefix(strings.ToLower(msg.value+msg.text), strings.ToLower(p.input.Value())) {
		p.input.SetSuggestions([]string{msg.value + msg.text})
	}
}
