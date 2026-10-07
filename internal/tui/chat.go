package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

// chat is the assistant's drawer (@): conversations bound to the environment on screen, sent with
// what the screen shows. Turns run the user's opencode or Claude Code; rig's MCP server is its hands.
// The conversation on screen is the embedded convo; live holds every one this run still follows, so
// several can run at once.
type chat struct {
	open, focus bool
	runner      *ai.Runner
	*convo
	live    []*convo
	input   textarea.Model
	starter int
	err     string
	// all is ctrl+a's select-all of the draft: the next key copies, replaces or drops it
	all bool
	// cmdSel is the highlighted row of the command menu that opens while a / command is typed
	cmdSel int
	// hist are the messages sent, oldest first; histAt walks them with ↑↓ (len(hist): the draft)
	hist      []string
	histAt    int
	histDraft string
	// hoff scrolls the transcript sideways over code wider than the box
	hoff int
	// a drag over the transcript selects by line and column and scrolls past an edge; rows is the
	// line drawn on each body row (-1 for padding), lines what they index, bx..bh the body's box
	anchor, head   lpos
	selecting      bool
	dragged        bool
	rows           []int
	lines          []string
	bx, by, bw, bh int
}

// convo is one conversation as shown: its transcript, its running turn and what waits for it.
type convo struct {
	sess *ai.Session
	// msgs is the transcript as shown; the running turn owns sess until it ends
	msgs    []ai.Message
	busy    bool
	cancel  context.CancelFunc
	started time.Time
	scroll  int
	// queue are messages typed while a turn runs, sent in order after it
	queue []string
	// next is what the last answer suggests asking next (tab takes it)
	next string
	// rendered caches each message's lines at width rw
	rendered [][]string
	rw       int
}

func (c *chat) find(s *ai.Session) *convo {
	if c == nil {
		return nil
	}
	for _, cv := range c.live {
		if cv.sess == s {
			return cv
		}
	}
	return nil
}

// switchTo shows cv, keeping only the conversations still at work besides it.
func (c *chat) switchTo(cv *convo) {
	keep := []*convo{cv}
	for _, o := range c.live {
		if o != cv && (o.busy || len(o.queue) > 0) {
			keep = append(keep, o)
		}
	}
	c.live, c.convo = keep, cv
}

func (c *chat) running() int {
	n := 0
	for _, cv := range c.live {
		if cv.busy {
			n++
		}
	}
	return n
}

type chatCmd struct{ name, args, help string }

var chatCommands = []chatCmd{
	{"/new", "", "start a new conversation (a running one goes on in /sessions)"},
	{"/sessions", "", "pick a conversation (ctrl+d closes one there)"},
	{"/close", "", "forget this conversation"},
	{"/stop", "", "stop the running turn (ctrl+x)"},
	{"/why", "[--fix] <service or symptom>", "gather an incident's logs, traces, metrics and changes, ask for its root cause and a fix; answer fix to apply it"},
	{"/connect", "", "use your installed opencode or Claude Code"},
	{"/model", "", "the chat's model"},
	{"/effort", "", "the chat's reasoning level"},
	{"/fast", "", "the model of completions while typing"},
	{"/autocomplete", "", "suggestions while typing: on or off"},
	{"/redact", "", "masking of secrets in what the model sees: on or off"},
	{"/help", "", "these commands"},
}

// cmdMenu is what the command menu offers for draft v: the commands starting with what is typed,
// then those containing it; nothing once v is more than one word or no command.
func cmdMenu(v string) []chatCmd {
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \n") {
		return nil
	}
	var pre, in []chatCmd
	for _, c := range chatCommands {
		switch {
		case strings.HasPrefix(c.name, v):
			pre = append(pre, c)
		case strings.Contains(c.name, v[1:]):
			in = append(in, c)
		}
	}
	return append(pre, in...)
}

// nearestCommand is the command closest to a mistyped one, by edit distance, if any is close.
func nearestCommand(v string) string {
	best, dist := "", 3
	for _, c := range chatCommands {
		if d := editDistance(v, c.name); d < dist {
			best, dist = c.name, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func commandsHelp() string {
	var b strings.Builder
	b.WriteString("Commands (type / for the menu):\n")
	for _, c := range chatCommands {
		b.WriteString("- " + strings.TrimSpace(c.name+" "+c.args) + " — " + c.help + "\n")
	}
	b.WriteString("\nAnything else goes to the assistant with what this screen shows. esc hides the chat, @ brings it back.")
	return b.String()
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

// screenStarters are offered (tab) in an empty chat, per screen.
var screenStarters = map[string][]string{
	"Services":  {"why is this service failing? check its status and logs", "suggest pprof commands to find where this service spends CPU and memory", "restart this service", "what is unhealthy in this environment?"},
	"Logs":      {"find the issue in these logs and where it comes from in the code", "which errors repeat most, and why?", "show only the errors of these services in the Logs screen"},
	"KV":        {"set this key's value to ", "explain this configuration", "which services read this key? restart them"},
	"Data":      {"write a query for ", "explain this table and how the code uses it", "run this procedure with sample inputs and explain the result"},
	"Queries":   {"add a query that counts failed transactions in the last 5 minutes and schedule it every 30s", "explain this result"},
	"Metrics":   {"explain what these panels show right now", "add a query for the p99 latency of each service"},
	"Traces":    {"why is this trace slow?", "which service fails in this trace?"},
	"Flow":      {"where is the bottleneck right now?", "why is the picked node hot?"},
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

// chatInputHeight is the chat's input box in rows; longer messages scroll inside it (↑↓).
const chatInputHeight = 3

func (m *model) chatOpen() *chat {
	if m.chat == nil {
		in := textarea.New()
		in.SetPromptFunc(2, func(line int) string {
			if line == 0 {
				return "› "
			}
			return "  "
		})
		in.ShowLineNumbers = false
		in.Placeholder = chatPlaceholder
		in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter"))
		editorTextarea(&in)
		for _, st := range []*textarea.Style{&in.FocusedStyle, &in.BlurredStyle} {
			st.Placeholder = lipgloss.NewStyle().Foreground(cPlaceholder)
			st.CursorLine = lipgloss.NewStyle()
		}
		in.SetHeight(chatInputHeight)
		m.chat = &chat{input: in, convo: &convo{}}
		m.chat.live = []*convo{m.chat.convo}
		m.chat.hist = loadChatHistory(m.app.AIDir())
		m.chat.histAt = len(m.chat.hist)
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

// newSession starts a conversation on the environment on screen; one still running goes on.
func (c *chat) newSession(m *model) {
	cv := &convo{}
	if c.enabled() {
		cv.sess = ai.NewSession(m.app.AIDir(), c.runner.Setup.Backend, m.app.Env.Name)
	}
	c.starter = 0
	c.switchTo(cv)
}

func (c *chat) load(m *model, id string) error {
	for _, cv := range c.live {
		if cv.sess != nil && cv.sess.ID == id {
			c.switchTo(cv)
			return nil
		}
	}
	s, err := ai.LoadSession(m.app.AIDir(), id)
	if err != nil {
		return err
	}
	if s.Env != m.app.Env.Name {
		return fmt.Errorf("conversation %s is on %s: switch there (E) first", s.ID, s.Env)
	}
	c.switchTo(&convo{sess: s, msgs: append([]ai.Message{}, s.Messages...), next: s.Next})
	return nil
}

// reset follows an environment switch: the old conversations stay with their environment.
func (c *chat) reset() {
	for _, cv := range c.live {
		if cv.cancel != nil {
			cv.cancel()
		}
	}
	c.runner, c.convo = nil, &convo{}
	c.live = []*convo{c.convo}
}

const chatPlaceholder = "ask anything · enter send · alt+enter newline · ↑ history · tab ideas · / commands"

// remember adds a sent message to the ↑ history, kept across runs.
func (c *chat) remember(m *model, text string) {
	if n := len(c.hist); n == 0 || c.hist[n-1] != text {
		c.hist = append(c.hist, text)
	}
	if len(c.hist) > 200 {
		c.hist = c.hist[len(c.hist)-200:]
	}
	c.histAt, c.histDraft = len(c.hist), ""
	saveChatHistory(m.app.AIDir(), c.hist)
}

func chatHistoryFile(dir string) string { return filepath.Join(dir, "ai", "history.json") }

func loadChatHistory(dir string) []string {
	var h []string
	if raw, err := os.ReadFile(chatHistoryFile(dir)); err == nil {
		_ = json.Unmarshal(raw, &h)
	}
	return h
}

func saveChatHistory(dir string, h []string) {
	raw, _ := json.Marshal(h)
	_ = os.MkdirAll(filepath.Dir(chatHistoryFile(dir)), 0o700)
	_ = os.WriteFile(chatHistoryFile(dir), raw, 0o600)
}

// recall walks the history: older (up) or newer; past the newest it gives the draft back.
func (c *chat) recall(older bool) {
	if c.histAt == len(c.hist) {
		c.histDraft = c.input.Value()
	}
	switch {
	case older && c.histAt > 0:
		c.histAt--
	case !older && c.histAt < len(c.hist):
		c.histAt++
	default:
		return
	}
	v := c.histDraft
	if c.histAt < len(c.hist) {
		v = c.hist[c.histAt]
	}
	c.input.SetValue(v)
	if older {
		c.input.CursorStart()
	}
}

func (m *model) chatKey(k tea.KeyMsg) tea.Cmd {
	c := m.chat
	all := c.all
	c.all = false
	switch k.String() {
	case "ctrl+a":
		c.all = c.input.Value() != ""
		return nil
	case "ctrl+x":
		if c.busy {
			m.chatStop(c.convo)
			return nil
		}
		fallthrough
	case "ctrl+c", "ctrl+y":
		text := c.input.Value()
		if text == "" {
			text = c.lastAnswer()
		}
		copyText(text)
		if k.String() == "ctrl+x" {
			c.input.SetValue("")
		}
		m.setStatus("copied", false)
		return nil
	}
	if all {
		switch k.Type {
		case tea.KeyBackspace, tea.KeyDelete, tea.KeyCtrlH:
			c.input.SetValue("")
			return nil
		case tea.KeyRunes, tea.KeySpace, tea.KeyCtrlV:
			c.input.SetValue("")
		}
	}
	if menu := cmdMenu(c.input.Value()); len(menu) > 0 {
		c.cmdSel = min(c.cmdSel, len(menu)-1)
		pick := menu[c.cmdSel]
		switch k.String() {
		case "up":
			c.cmdSel = (c.cmdSel + len(menu) - 1) % len(menu)
			return nil
		case "down":
			c.cmdSel = (c.cmdSel + 1) % len(menu)
			return nil
		case "tab":
			c.input.SetValue(pick.name + map[bool]string{true: " ", false: ""}[pick.args != ""])
			c.input.CursorEnd()
			return nil
		case "enter":
			if v := c.input.Value(); v != pick.name && pick.args != "" {
				c.input.SetValue(pick.name + " ")
				c.input.CursorEnd()
				return nil
			}
			c.input.SetValue(pick.name)
		case "esc":
			c.input.SetValue("")
			return nil
		}
	} else {
		c.cmdSel = 0
	}
	switch k.String() {
	case "@":
		if c.input.Value() != "" {
			break
		}
		fallthrough
	case "esc":
		c.open, c.focus = false, false
		return nil
	case "pgup":
		c.scroll += 10
		return nil
	case "pgdown":
		c.scroll = max(0, c.scroll-10)
		return nil
	case "shift+up", "ctrl+up":
		c.scroll++
		return nil
	case "shift+down", "ctrl+down":
		c.scroll = max(0, c.scroll-1)
		return nil
	case "shift+left":
		c.hoff = max(0, c.hoff-8)
		return nil
	case "shift+right":
		c.hoff += 8
		return nil
	case "up":
		// in a multi-line draft up moves between its lines until the first one
		if c.input.Line() == 0 {
			c.recall(true)
			return nil
		}
	case "down":
		if c.input.Line() == c.input.LineCount()-1 {
			c.recall(false)
			return nil
		}
	case "tab":
		v := c.input.Value()
		if v == "" && c.next != "" {
			c.input.SetValue(c.next)
			c.input.CursorEnd()
			return nil
		}
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
		if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "/why") {
			c.remember(m, v)
		}
		if f := strings.Fields(v); f[0] == "/why" {
			return m.chatWhy(f[1:])
		}
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
			m.setStatus("the last conversation goes on: /sessions brings it back", false)
		}
		c.newSession(m)
	case "/stop":
		m.chatStop(c.convo)
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
	case "/connect":
		m.pickConnect()
	case "/autocomplete":
		m.toggleAutocomplete()
	case "/redact":
		m.toggleRedact()
	case "/help", "/?":
		c.msgs = append(c.msgs, ai.Message{Role: "assistant", Text: commandsHelp()})
	default:
		text := "Unknown command " + strings.Fields(v)[0] + "."
		if near := nearestCommand(strings.Fields(v)[0]); near != "" {
			text += " Did you mean " + near + "?"
		}
		c.msgs = append(c.msgs, ai.Message{Role: "assistant", Text: text + " Type / for the menu, /help for the list."})
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

// aiAllowMsg is a command the user always allows the assistant, from now until rig quits.
type aiAllowMsg string

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

// pickConnect offers the installed backends; the picked one runs with its own login and config.
func (m *model) pickConnect() {
	found := ai.Detect()
	if len(found) == 0 {
		m.setStatus("neither opencode nor claude is installed: https://opencode.ai, https://claude.com/claude-code", true)
		return
	}
	var names, desc []string
	for _, f := range found {
		names, desc = append(names, f.Backend), append(desc, f.Describe())
	}
	m.pick("connect the AI to", names, desc, 0, false, func(l []string) tea.Cmd {
		if len(l) == 0 {
			return nil
		}
		if err := ai.Connect(found[slices.Index(names, l[0])]); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		m.ai = nil
		if c := m.chat; c != nil {
			if c.runner, c.err = m.aiRunner(), ""; c.runner != nil {
				m.setStatus("AI: "+c.runner.Setup.Describe(), !c.enabled())
			}
		}
		return nil
	})
}

// pickChatSession lists this environment's conversations: enter continues one, d closes it.
func (m *model) pickChatSession() {
	ss, err := ai.ListSessions(m.app.AIDir())
	if err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	var ids, desc []string
	sel := 0
	for _, s := range ss {
		if s.Env == m.app.Env.Name {
			d := fmt.Sprintf("%s · %d msgs · %s", s.Updated.Format("01-02 15:04"), len(s.Messages), s.Title)
			for _, cv := range m.chat.live {
				if cv.sess != nil && cv.sess.ID == s.ID && cv.busy {
					d = sAmber.Render("⟳ running ") + d
				}
			}
			if m.chat.sess != nil && m.chat.sess.ID == s.ID {
				d = sAccent.Render("● ") + d
				sel = len(ids)
			}
			ids = append(ids, s.ID)
			desc = append(desc, d)
		}
	}
	if len(ids) == 0 {
		m.setStatus("no conversations on "+m.app.Env.Name+" yet", false)
		return
	}
	m.pick("conversations on "+m.app.Env.Name+" (ctrl+d closes one)", ids, desc, sel, false, func(c []string) tea.Cmd {
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
		c := m.chat
		for _, cv := range c.live {
			if cv.sess != nil && cv.sess.ID == id && cv.busy {
				return errors.New("its turn is running: open it and ctrl+x stops it")
			}
		}
		if c.sess != nil && c.sess.ID == id {
			c.newSession(m)
		}
		return ai.CloseSession(m.app.AIDir(), id)
	}
}

// chatWhy is rig why in the chat: rig gathers the evidence, then the assistant (with its tools, so
// it can dig further) explains it.
func (m *model) chatWhy(args []string) tea.Cmd {
	c := m.chat
	if len(args) == 0 {
		m.pick("/why: which service misbehaves?", engine.SortedKeys(m.app.Spec.Services), nil, 0, false, func(l []string) tea.Cmd {
			if len(l) == 0 {
				return nil
			}
			m.chatOpen()
			return m.chatWhy(l)
		})
		return nil
	}
	if !c.enabled() || c.busy {
		return m.chatSend("/why " + strings.Join(args, " "))
	}
	if c.sess == nil {
		c.newSession(m)
	}
	cv := c.convo
	fix := args[0] == "--fix"
	if fix {
		args = args[1:]
	}
	if len(args) == 0 {
		m.setStatus("/why --fix needs a service or a symptom", true)
		return nil
	}
	what := strings.Join(args, " ")
	cv.msgs = append(cv.msgs, ai.Message{Role: "user", Text: "/why " + what, At: time.Now()})
	cv.busy, cv.started, cv.scroll, cv.next = true, time.Now(), 0, ""
	ctx, cancel := context.WithCancel(m.ctx)
	cv.cancel = cancel
	r, s, a := c.runner, cv.sess, m.app
	return func() tea.Msg {
		defer cancel()
		inc, err := a.InvestigateSymptom(ctx, args, 15*time.Minute)
		if err != nil {
			return chatDoneMsg{s: s, err: err}
		}
		program.Send(chatEventMsg{s: s, e: ai.Event{Kind: "tool", Text: "rig why " + what + ": " + inc.Brief()}})
		prompt := inc.Prompt()
		if fix {
			prompt += "\nThe user already answered: " + engine.FixPrompt
		}
		err = r.Turn(ctx, s, prompt, "", func(e ai.Event) { program.Send(chatEventMsg{s: s, e: e}) })
		return chatDoneMsg{s: s, err: err}
	}
}

// chatSend sends text in the conversation on screen, or queues it while a turn runs there.
func (m *model) chatSend(text string) tea.Cmd {
	c := m.chat
	if !c.enabled() {
		m.setStatus("AI is off: rig ai config", true)
		return nil
	}
	if c.sess == nil {
		c.newSession(m)
	}
	if c.busy {
		c.queue = append(c.queue, text)
		c.scroll = 0
		return nil
	}
	return m.sendOn(c.convo, text)
}

func (m *model) sendOn(cv *convo, text string) tea.Cmd {
	screen := m.screenContext()
	cv.msgs = append(cv.msgs, ai.Message{Role: "user", Text: text, At: time.Now()})
	cv.busy, cv.started, cv.scroll, cv.next = true, time.Now(), 0, ""
	ctx, cancel := context.WithCancel(m.ctx)
	cv.cancel = cancel
	r, s := m.chat.runner, cv.sess
	return func() tea.Msg {
		defer cancel()
		err := r.Turn(ctx, s, text, screen, func(e ai.Event) { program.Send(chatEventMsg{s: s, e: e}) })
		return chatDoneMsg{s: s, err: err}
	}
}

// chatStop stops cv's turn; what was queued behind it comes back to the draft.
func (m *model) chatStop(cv *convo) {
	if cv.cancel != nil {
		cv.cancel()
	}
	c := m.chat
	if len(cv.queue) > 0 && cv == c.convo && c.input.Value() == "" {
		c.input.SetValue(strings.Join(cv.queue, "\n"))
		c.input.CursorEnd()
	}
	cv.queue = nil
}

func (m *model) chatUpdate(msg tea.Msg) (tea.Cmd, bool) {
	c := m.chat
	switch msg := msg.(type) {
	case chatEventMsg:
		if cv := c.find(msg.s); cv != nil {
			switch msg.e.Kind {
			case "next":
				cv.next = msg.e.Text
			case "text":
				cv.msgs = append(cv.msgs, ai.Message{Role: "assistant", Text: msg.e.Text, At: time.Now()})
			default:
				cv.msgs = append(cv.msgs, ai.Message{Role: msg.e.Kind, Text: msg.e.Text, At: time.Now()})
			}
		}
		return nil, true
	case chatDoneMsg:
		var cmd tea.Cmd
		if cv := c.find(msg.s); cv != nil {
			cv.busy = false
			if msg.err != nil && !errors.Is(msg.err, ai.ErrTurnFailed) {
				cv.msgs = append(cv.msgs, ai.Message{Role: "error", Text: msg.err.Error()})
			}
			took := time.Since(cv.started).Round(100 * time.Millisecond)
			cv.msgs = append(cv.msgs, ai.Message{Role: "took", Text: "took " + took.String(), At: time.Now()})
			if len(cv.queue) > 0 && c.enabled() {
				next := cv.queue[0]
				cv.queue = cv.queue[1:]
				cmd = m.sendOn(cv, next)
			} else if cv != c.convo {
				m.setStatus("the assistant answered in "+cv.sess.Title+": /sessions opens it", false)
			}
		}
		// the assistant may have changed what the screens show
		m.svcAt = time.Time{}
		return batch(cmd, m.tabs[m.active].refresh(m)), true
	case bridgeMsg:
		return m.bridge(msg), true
	case aiAllowMsg:
		if m.aiAllowed == nil {
			m.aiAllowed = map[string]bool{}
		}
		m.aiAllowed[string(msg)] = true
		m.busy = max(0, m.busy-1)
		m.setStatus("always allowed for the assistant this session: "+string(msg), false)
		return nil, true
	case aiModelsMsg:
		m.modelsListed(msg)
		return nil, true
	}
	return nil, false
}

// bridge answers the assistant's tools: a confirmation the person gives in the footer, or a UI action.
func (m *model) bridge(b bridgeMsg) tea.Cmd {
	switch b.req.Op {
	case "approve":
		if cmd := b.req.Command; cmd != "" && m.aiAllowed[cmd] {
			b.reply <- ai.Reply{OK: true, Text: "allowed for this session"}
			return nil
		}
		if m.confirm != nil || m.prompt != nil {
			b.reply <- ai.Reply{Text: "the user is busy with another prompt; ask them in the chat"}
			return nil
		}
		m.confirm = &confirm{text: b.req.Text + "?", run: func() tea.Msg {
			b.reply <- ai.Reply{OK: true, Text: "the user, in rig"}
			return statusMsg{text: "approved for the assistant"}
		}, cancel: func() { b.reply <- ai.Reply{Text: "declined in rig"} }}
		if cmd := b.req.Command; cmd != "" {
			m.confirm.always = func() tea.Msg {
				b.reply <- ai.Reply{OK: true, Text: "the user, in rig (always this session)"}
				return aiAllowMsg(cmd)
			}
		}
		return nil
	case "audit":
		b.reply <- ai.Reply{OK: true}
		if m.chat != nil {
			m.chat.msgs = append(m.chat.msgs, ai.Message{Role: "audit", Text: b.req.Text, At: time.Now()})
		}
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
				lt.services, lt.grep, lt.instances = strings.Fields(a["services"]), a["grep"], nil
				if !m.opened[i] {
					return true, "showing logs", m.openTab(i)
				}
				m.active = i
				return true, "showing logs", lt.start(m)
			}
		}
	case "state":
		return true, m.uiState(), nil
	case "keys":
		return m.uiKeys(strings.Split(a["keys"], "\n"))
	case "panels":
		return m.uiPanels(a["dashboard"], a["panels"])
	}
	return false, "unknown action " + r.Action, nil
}

// uiState is what rig_ui "state" answers: the screens, the open one's settings and its text.
func (m *model) uiState() string {
	var b strings.Builder
	names := make([]string, len(m.tabs))
	for i, t := range m.tabs {
		names[i] = fmt.Sprintf("%d %s", i+1, t.name())
	}
	t := m.tabs[m.active]
	fmt.Fprintf(&b, "screens: %s\nopen: %s\n", strings.Join(names, ", "), t.name())
	if k, ok := t.(keeper); ok {
		fmt.Fprintf(&b, "settings: %v\n", k.keep())
	}
	if mt, ok := t.(*metricsTab); ok {
		var titles []string
		for _, p := range mt.dashboard(m).Panels {
			titles = append(titles, p.Title)
		}
		fmt.Fprintf(&b, "dashboards: %s\ndashboard %s panels: %s\n", strings.Join(mt.dashNames(m), ", "), mt.dashName(m), strings.Join(titles, " | "))
	}
	if m.confirm != nil {
		b.WriteString("a confirmation is waiting for the user\n")
	}
	chatOpen := m.chat != nil && m.chat.open
	if chatOpen {
		m.chat.open = false
	}
	b.WriteString("screen:\n" + ansi.Strip(m.View()))
	if chatOpen {
		m.chat.open = true
	}
	return b.String()
}

// uiKeys presses keys on the open screen as the user would; it stops at a confirmation, which
// stays the user's to answer.
func (m *model) uiKeys(keys []string) (bool, string, tea.Cmd) {
	focus := m.chat != nil && m.chat.focus
	if focus {
		m.chat.focus = false
	}
	m.driving = true
	defer func() {
		m.driving = false
		if m.chat != nil && m.chat.open {
			m.chat.focus = focus
		}
	}()
	var cmds []tea.Cmd
	for i, k := range keys {
		if m.confirm != nil {
			return false, fmt.Sprintf("stopped before %q: a confirmation is waiting for the user", strings.Join(keys[i:], " ")), batch(cmds...)
		}
		_, c := m.Update(keyOf(k))
		cmds = append(cmds, c)
	}
	return true, "pressed; rig_ui state shows the screen once it loads", batch(cmds...)
}

// keyOf reads a key the way bubbletea names it (enter, esc, ctrl+r, shift+tab, up, f5, space);
// anything else is typed as text.
func keyOf(s string) tea.KeyMsg {
	if s == "space" {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	for t := tea.KeyType(-100); t < 128; t++ {
		if t != tea.KeyRunes && t.String() == s {
			return tea.KeyMsg{Type: t}
		}
	}
	alt, rest := false, s
	if r, ok := strings.CutPrefix(s, "alt+"); ok && r != "" {
		alt, rest = true, r
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(rest), Alt: alt}
}

// uiPanels shows only the named panels of a dashboard (every panel when titles is empty).
func (m *model) uiPanels(dash, titles string) (bool, string, tea.Cmd) {
	for i, tab := range m.tabs {
		t, ok := tab.(*metricsTab)
		if !ok {
			continue
		}
		t.init()
		if dash != "" {
			found := false
			for di, n := range t.dashNames(m) {
				if strings.EqualFold(n, dash) {
					t.dash, t.focus, t.scroll, t.zoom, found = di, 0, 0, false, true
				}
			}
			if !found {
				return false, "no dashboard " + dash + "; have " + strings.Join(t.dashNames(m), ", "), nil
			}
		}
		have := map[string]bool{}
		for _, p := range t.dashboard(m).Panels {
			have[strings.ToLower(p.Title)] = true
		}
		only := map[string]bool{}
		for _, ti := range strings.Split(titles, "\n") {
			if ti = strings.ToLower(strings.TrimSpace(ti)); ti == "" {
				continue
			}
			if !have[ti] {
				return false, fmt.Sprintf("no panel %q on %s; rig_ui state lists them", ti, t.dashName(m)), nil
			}
			only[ti] = true
		}
		t.only[t.dashName(m)] = only
		t.focus = 0
		var cmd tea.Cmd
		if m.active != i || !m.opened[i] {
			cmd = m.openTab(i)
		}
		text := "showing every panel of " + t.dashName(m)
		if len(only) > 0 {
			text = fmt.Sprintf("showing %d panels of %s; O on the screen shows all", len(only), t.dashName(m))
		}
		return true, text, batch(cmd, t.reload(m))
	}
	return false, "this rig has no Metrics screen", nil
}

func (m *model) chatWidth(h int) int {
	if m.w < 90 {
		return m.w
	}
	return m.paneSize("chat", splitGeo{total: m.w, minA: 36, minB: 30, fromEnd: true}, min(max(46, m.w*2/5), 90), 0, 0, h)
}

// transcript renders the conversation's messages in iw columns, once per message.
func (cv *convo) transcript(iw int) []string {
	if cv.rw != iw || len(cv.rendered) > len(cv.msgs) {
		cv.rendered, cv.rw = nil, iw
	}
	bar := sAccent.Render("▌ ")
	for _, msg := range cv.msgs[len(cv.rendered):] {
		var lines []string
		add := func(s string) { lines = append(lines, strings.Split(s, "\n")...) }
		switch msg.Role {
		case "user":
			add("")
			for _, l := range strings.Split(wordWrap(msg.Text, iw-2), "\n") {
				add(bar + sTitle.Render(l))
			}
			add("")
		case "assistant":
			add(renderMarkdown(msg.Text, iw))
			add("")
		case "tool":
			add(sDim.Render(wordWrap("  ⎿ "+msg.Text, iw)))
		case "error":
			add(sRed.Render(wordWrap("✖ "+msg.Text, iw)))
		case "audit":
			add(sAmber.Render(wordWrap("  "+msg.Text, iw)))
		case "took":
			add(sDim.Render("  ⏱ " + msg.Text))
		}
		for _, t := range msg.Tools {
			add(sDim.Render(wordWrap("  ⎿ "+t, iw)))
		}
		cv.rendered = append(cv.rendered, lines)
	}
	var out []string
	for _, l := range cv.rendered {
		out = append(out, l...)
	}
	return out
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
	if c.sess != nil && c.sess.Title != "" {
		title += " · " + c.sess.Title
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
		add(sDim.Render(wordWrap("/connect picks an installed opencode or Claude Code with its own setup. Else install one (opencode has free models, no key), or set a provider:\n  rig ai config provider=deepseek api_key=…\n  rig ai config provider=openai url=… api_key=… model=…\n  rig ai config proxy=localhost:10808\nrig ai check tests it.", iw)))
	case len(c.msgs) == 0:
		add(sDim.Render(wordWrap("Ask about what this screen shows, or to do anything you can do in rig. It works on "+m.app.Env.Name+" only; the project directory is its only workspace.", iw)))
		add("")
		add(sTitle.Render("ideas (tab)"))
		for _, s := range m.ideas() {
			add(sDim.Render(wordWrap("· "+s, iw)))
		}
	}
	lines = append(lines, c.transcript(iw)...)
	if c.busy {
		lines = append(lines, sAmber.Render(fmt.Sprintf("⟳ working %s", time.Since(c.started).Round(time.Second)))+sDim.Render(" · ctrl+x stops"))
	}
	for _, q := range c.queue {
		add(sDim.Render(wordWrap("  ⧗ queued: "+q, iw)))
	}
	keys := "enter send · alt+enter newline · ↑ history · / commands · esc hide"
	switch {
	case m.confirm != nil:
		keys = sAmber.Render("↓ answer the question below")
	case c.busy:
		keys = "enter queues · ctrl+x stops · /new starts another · pgup scrolls"
	}
	status := sDim.Render(keys)
	if n := c.running(); n > 0 && (!c.busy || n > 1) {
		status = sAmber.Render(fmt.Sprintf("%d running · ", n)) + status
	}
	var menuLines []string
	if menu := cmdMenu(c.input.Value()); len(menu) > 0 && c.focus {
		c.cmdSel = min(c.cmdSel, len(menu)-1)
		from := max(0, c.cmdSel-5)
		for i := from; i < len(menu) && i < from+6; i++ {
			cm := menu[i]
			name := cm.name
			if cm.args != "" {
				name += " " + cm.args
			}
			row := fmt.Sprintf(" %-22s %s", name, cm.help)
			if i == c.cmdSel {
				row = sCursor.Render(truncate(row, iw) + strings.Repeat(" ", max(0, iw-lipgloss.Width(row))))
			} else {
				row = sAccent.Render(fmt.Sprintf(" %-22s", name)) + " " + sDim.Render(truncate(cm.help, max(0, iw-24)))
			}
			menuLines = append(menuLines, row)
		}
		menuLines = append(menuLines, sDim.Render(" ↑↓ pick · tab completes · enter runs"))
	}
	// the transcript, then the input in a box of its own, then the keys
	room := max(1, h-2-(chatInputHeight+2)-1-len(menuLines))
	c.scroll = min(c.scroll, max(0, len(lines)-room))
	end := len(lines) - c.scroll
	start := max(0, end-room)
	bw := w - 2
	widest := 0
	for _, l := range lines[start:end] {
		widest = max(widest, ansi.StringWidth(l))
	}
	c.hoff = min(c.hoff, max(0, widest-bw))
	c.lines, c.rows = lines, c.rows[:0]
	var rows []string
	blank := func() { rows, c.rows = append(rows, ""), append(c.rows, -1) }
	if pad := room - (end - start); pad > 0 && len(c.msgs) > 0 {
		for range pad {
			blank()
		}
	}
	for i := start; i < end; i++ {
		l := lines[i]
		if a, z, ok := c.span(i); ok {
			l = paintSpan(l, a, z)
		}
		if c.hoff > 0 {
			l = ansi.TruncateLeft(l, c.hoff, "")
		}
		rows, c.rows = append(rows, ansi.Truncate(l, bw, "")), append(c.rows, i)
	}
	for len(rows) < room {
		blank()
	}
	if c.scroll > 0 {
		rows[len(rows)-1] = sAmber.Render(fmt.Sprintf("↓ %d more lines · pgdown", c.scroll))
	}
	if c.hoff > 0 || widest > bw {
		title += fmt.Sprintf(" · ⇢%d shift+←→", c.hoff)
	}
	body := strings.Join(rows, "\n")
	c.bx, c.by, c.bw, c.bh = x+1, m.originY+1, bw, room
	m.zone("chat:body", x+1, 1, bw, room)
	c.input.Placeholder = chatPlaceholder
	if c.next != "" {
		c.input.Placeholder = c.next + "  (tab)"
	}
	c.input.SetWidth(iw - 2)
	input := c.input.View()
	if c.all {
		input = sSel.Render(ansi.Wrap(c.input.Value(), iw-2, " "))
		input += strings.Repeat("\n", max(0, chatInputHeight-1-strings.Count(input, "\n")))
	}
	border := cPanel
	if c.focus {
		border = cAccent
	}
	input = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(iw).Render(input)
	if len(menuLines) > 0 {
		body += "\n" + strings.Join(menuLines, "\n")
	}
	body += "\n" + input + "\n" + status
	return panel(title, body, w, h, c.focus)
}

func (c *chat) span(i int) (int, int, bool) {
	if !c.dragged {
		return 0, 0, false
	}
	return selSpan(c.anchor, c.head, i)
}

// drag selects transcript text from the press to the mouse and copies it on release; past an edge
// it scrolls that way, so a selection can run longer and wider than the box.
func (c *chat) drag(m *model, x, y int, phase dragPhase) {
	if phase == dragMove && c.selecting {
		switch {
		case x >= c.bw-1:
			c.hoff += 4
		case x <= 0 && c.hoff > 0:
			c.hoff = max(0, c.hoff-4)
		}
		switch {
		case y < 0:
			c.scroll++
		case y >= c.bh && c.scroll > 0:
			c.scroll--
		}
	}
	line := -1
	for r := min(max(y, 0), len(c.rows)-1); r >= 0 && r < len(c.rows); r++ {
		if line = c.rows[r]; line >= 0 {
			break
		}
	}
	if line < 0 {
		c.selecting = false
		return
	}
	at := lpos{line, min(max(x, 0), c.bw-1) + c.hoff}
	switch phase {
	case dragPress:
		c.anchor, c.head, c.selecting, c.dragged = at, at, true, false
	case dragMove:
		if c.selecting && at != c.anchor {
			c.head, c.dragged = at, true
		}
	case dragRelease:
		if c.selecting && c.dragged {
			if text := c.selection(); text != "" {
				copyText(text)
				m.setStatus("copied", false)
			}
		}
		c.selecting, c.dragged = false, false
	}
}

func (c *chat) selection() string {
	var out []string
	for i := min(c.anchor.line, c.head.line); i <= max(c.anchor.line, c.head.line) && i < len(c.lines); i++ {
		a, z, _ := c.span(i)
		l := strings.TrimRight(ansi.Cut(ansi.Strip(c.lines[i]), a, z), " ")
		if a == 0 {
			l = strings.TrimPrefix(strings.TrimPrefix(l, codeBarText), "▌ ")
		}
		out = append(out, l)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

func (c *chat) lastAnswer() string {
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if c.msgs[i].Role == "assistant" {
			return c.msgs[i].Text
		}
	}
	return ""
}

func wordWrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
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
	for i, n := range names {
		if i == 3 {
			break
		}
		b.WriteString(serviceFacts(m, n))
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
	if t.lt != nil && t.open_ != "" {
		b.WriteString("its log, last lines:\n" + logSample(t.lt.log.lines, 60))
	}
	return b.String()
}

// serviceFacts is what rig knows of a service: its definition and, on Kubernetes, the manifest a
// deploy applies or where one would go, so the assistant need not search the repository for it.
func serviceFacts(m *model, n string) string {
	s := m.app.Spec.Services[n]
	if s == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s in rig.yaml: role %s", n, s.Role)
	if s.Image != "" {
		b.WriteString(", image " + s.Image)
	}
	if s.Build != nil {
		switch {
		case s.Build.Go != "":
			b.WriteString(", build go " + s.Build.Go)
		case s.Build.Dockerfile != "":
			b.WriteString(", build dockerfile " + s.Build.Dockerfile)
		}
	}
	if len(s.Ports) > 0 {
		fmt.Fprintf(&b, ", ports %v", s.Ports)
	}
	if len(s.Groups) > 0 {
		b.WriteString(", groups " + strings.Join(s.Groups, ","))
	}
	if len(s.DependsOn) > 0 {
		b.WriteString(", depends on " + strings.Join(s.DependsOn, ","))
	}
	b.WriteString("\n")
	if k, ok := m.k8s(); ok {
		objs, w, err := k.Objects(s)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "%s manifests: %v\n", n, err)
		case w != nil:
			var files []string
			for _, o := range objs {
				if rel, err := filepath.Rel(m.app.Spec.Dir, o.File); err == nil && !slices.Contains(files, rel) {
					files = append(files, rel)
				}
			}
			fmt.Fprintf(&b, "%s manifests: %s (%d objects); a deploy applies them with $TAG and the env filled in\n", n, strings.Join(files, ", "), len(objs))
		default:
			dirs := m.app.ManifestDirs()
			for i, d := range dirs {
				if rel, err := filepath.Rel(m.app.Spec.Dir, d); err == nil {
					dirs[i] = rel
				}
			}
			fmt.Fprintf(&b, "%s manifests: none, so a deploy generates a plain Deployment. Its own manifest goes in %s, named after the workload (metadata.name %s, image $REGISTRY/<image>:$TAG); copy a sibling service's file there as the start\n", n, strings.Join(dirs, " or "), n)
		}
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
		m.prompt.label += sDim.Render("  · " + aiTrigger + " then words: the AI writes it")
	}
}

// askChecked is askAI whose @? answers must pass check (compile, or run where safe) to be offered.
func (m *model) askChecked(label, value, hint string, check func(context.Context, string) error, submit func(string) tea.Cmd) {
	m.askAI(label, value, hint, submit)
	m.prompt.check = check
}

// regexCheck accepts what compiles as a Go (RE2) regexp, any case.
func regexCheck(_ context.Context, v string) error {
	_, err := regexp.Compile("(?i)" + v)
	return err
}

const fuzzyHint = "space-separated words, each matching when its letters appear in order (gaps allowed, any case); every word must match"

// matchesSome accepts a filter that keeps at least one of items.
func matchesSome(items []string, match func(item, filter string) bool) func(context.Context, string) error {
	return func(_ context.Context, v string) error {
		for _, it := range items {
			if match(it, v) {
				return nil
			}
		}
		return errors.New("it keeps none of the rows")
	}
}

// within names up to 60 of the names a filter runs over, for the AI to match against.
func within(what string, names []string) string {
	if len(names) > 60 {
		names = append(names[:60:60], "…")
	}
	return "; the " + what + ": " + strings.Join(names, ", ")
}

// askTemplate is askAI on an empty input with template behind it: typing starts fresh with the AI
// completing, tab takes the template to edit, enter runs it as is.
func (m *model) askTemplate(label, template, hint string, check func(context.Context, string) error, submit func(string) tea.Cmd) {
	m.askChecked(label, "", hint, check, submit)
	m.prompt.template = template
	m.prompt.input.Placeholder = template
}

// asPopup shows the open prompt as a box over the screen: for queries, which outgrow the footer.
func (m *model) asPopup() {
	if m.prompt != nil {
		m.prompt.popup = true
	}
}

// toggleAutocomplete switches AI suggestions while typing, for good (ai.json autocomplete).
// toggleRedact turns the masking of secrets (goxang/scrub) in what reaches the model on or off.
func (m *model) toggleRedact() {
	c, err := ai.LoadConfig()
	if err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	on := !c.RedactOn()
	if err := ai.ChangeConfig("redact", fmt.Sprint(on)); err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	if r := m.aiRunner(); r != nil {
		r.Redactor = nil
		if on {
			r.Redactor = m.app.Redactor()
		}
	}
	if on {
		m.setStatus("secrets are masked before they reach the model", false)
	} else {
		m.setStatus("secrets are NOT masked: the model sees them as they are (/redact masks them again)", true)
	}
}

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

// aiTrigger is typed inline in any AI-assisted input to ask for the rest in plain language; it
// reuses "@", already the chat's own sigil, so the two AI entry points read as one convention.
const aiTrigger = "@?"

// describing splits an input at "@?": the input before it and the words after it, which say what
// the AI should write in their place.
func describing(v string) (before, want string, ok bool) {
	before, want, ok = strings.Cut(v, aiTrigger)
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
	r, hint, seq, check := m.aiRunner(), p.hint, p.seq, p.check
	if p.template != "" {
		hint += "; the screen offered this as a starting point: " + p.template
	}
	if describe {
		p.input.SetSuggestions(nil)
		return func() tea.Msg {
			defer cancel()
			start := time.Now()
			text, err := r.Describe(ctx, hint, before, want, check)
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
		if msg.text == "" {
			return
		}
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
