package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/spec"
)

const logCap = 5000

// logsTab streams the logs of the services the user picks (all of them only when asked), merged,
// or of one instance of one service.
type logsTab struct {
	log      *logView
	services []string
	instance string
	grep     string
	restart  bool
	stream   int
	cancel   context.CancelFunc
	ch       <-chan core.LogLine
	err      string
	// inspect shows the picked line (or the newest) field by field
	inspect *jsonTree
}

func logsUI(m *model) *spec.LogsUI {
	if ui := m.app.Spec.UI; ui != nil {
		return ui.Logs
	}
	return nil
}

type logBatchMsg struct {
	gen, stream int
	lines       []core.LogLine
	done        bool
}

type logStartMsg struct {
	gen, stream int
	ch          <-chan core.LogLine
	err         error
}

func (t *logsTab) name() string { return "Logs" }
func (t *logsTab) typing() bool { return false }
func (t *logsTab) hints() [][2]string {
	if t.inspect != nil {
		return [][2]string{{"↑↓", "move"}, {"←→ space", "fold"}, {"z", "fold/expand all"}, {"J K  ctrl+↓↑", "next/previous log line"}, {"w", "wrap"}, {"H L  shift+←→", "sideways"},
			{"y", "copy value"}, {"Y", "copy the line"}, {"esc v", "close"}}
	}
	return [][2]string{{"f", "pick services"}, {"/", "grep (regex)"}, {"↑↓ click", "pick a line"}, {"enter v", "inspect the line"}, {"w", "wrap"}, {"s", "structured/raw"},
		{"h", "fields shown"}, {"i", "pick instance"}, {"p", "pause"}, {"pgup pgdn ←→", "page, sideways"},
		{"drag", "select text: copied (past an edge scrolls)"}, {"y/Y", "copy shown/all"}, {"G", "follow again"}, {"c", "clear"}, {"M", "mouse off: terminal selection"}}
}

func (t *logsTab) interval() time.Duration { return time.Second }

// open streams nothing until services are picked (f): a log of everything is heavy and rarely wanted.
// It does not open the picker itself, which would take the keys that switch screens.
func (t *logsTab) open(m *model) tea.Cmd {
	if t.log == nil {
		t.log = newLogView("logs:body", logCap, logsUI(m))
	}
	if len(t.services) == 0 {
		return nil
	}
	return t.start(m)
}

func (t *logsTab) pickServices(m *model) {
	names := m.app.Spec.ServiceNames()
	var desc []string
	for _, n := range names {
		desc = append(desc, strings.Join(m.app.Spec.Services[n].Groups, ","))
	}
	m.pickMany("logs of which services? (space marks several)", names, desc, t.services, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		t.services, t.instance = chosen, ""
		return t.start(m)
	})
}

func (t *logsTab) pickInstance(m *model) {
	if len(t.services) != 1 {
		m.setStatus("pick a single service first (f) to choose one of its instances", true)
		return
	}
	svc := t.services[0]
	var ids, desc []string
	for _, st := range m.services {
		if st.Service != svc {
			continue
		}
		ids = append(ids, "all instances")
		desc = append(desc, "")
		for _, in := range st.Instances {
			ids = append(ids, in.ID)
			desc = append(desc, string(in.State)+" "+in.Host)
		}
	}
	m.pick("instance of "+svc, ids, desc, 0, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		t.instance = chosen[0]
		if t.instance == "all instances" {
			t.instance = ""
		}
		return t.start(m)
	})
}

func (t *logsTab) refresh(m *model) tea.Cmd {
	if t.restart {
		t.restart = false
		return t.start(m)
	}
	return nil
}

func (t *logsTab) start(m *model) tea.Cmd {
	if t.cancel != nil {
		t.cancel()
	}
	t.stream++
	if t.log == nil {
		t.log = newLogView("logs:body", logCap, logsUI(m))
	}
	t.log.reset()
	t.err = ""
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	a, gen, stream := m.app, m.gen, t.stream
	q := core.LogQuery{Follow: true, Tail: 200, Match: t.grep, Services: t.services}
	if _, err := regexp.Compile(t.grep); err == nil {
		q.Regex = true
	}
	if len(t.services) == 1 {
		q.Instance = t.instance
	}
	return func() tea.Msg {
		src, _, err := engine.Get[core.LogSource](a, core.KindLogs, "")
		if err != nil {
			return logStartMsg{gen: gen, stream: stream, err: err}
		}
		ch, err := src.Logs(ctx, q)
		return logStartMsg{gen: gen, stream: stream, ch: ch, err: err}
	}
}

// next waits for log lines and hands them over in batches, so a chatty service does not flood the UI loop.
func next(gen, stream int, ch <-chan core.LogLine) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return logBatchMsg{gen: gen, stream: stream, done: true}
		}
		batch := []core.LogLine{first}
		deadline := time.After(100 * time.Millisecond)
		for len(batch) < 500 {
			select {
			case l, ok := <-ch:
				if !ok {
					return logBatchMsg{gen: gen, stream: stream, lines: batch, done: true}
				}
				batch = append(batch, l)
			case <-deadline:
				return logBatchMsg{gen: gen, stream: stream, lines: batch}
			}
		}
		return logBatchMsg{gen: gen, stream: stream, lines: batch}
	}
}

func (t *logsTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case logStartMsg:
		if msg.gen != m.gen || msg.stream != t.stream {
			return nil
		}
		if msg.err != nil {
			t.err = msg.err.Error()
			return nil
		}
		t.ch = msg.ch
		return next(msg.gen, msg.stream, msg.ch)
	case logBatchMsg:
		if msg.gen != m.gen || msg.stream != t.stream {
			return nil
		}
		t.log.add(msg.lines)
		if msg.done {
			return nil
		}
		return next(msg.gen, msg.stream, t.ch)
	case tea.KeyMsg:
		if t.inspect != nil {
			switch msg.String() {
			case "esc", "v", "q":
				t.inspect = nil
			case "y":
				copyText(t.inspect.current().text())
				m.setStatus("copied "+t.inspect.current().path(), false)
			case "Y":
				if l, ok := t.log.picked(); ok {
					copyText(l.Text)
					m.setStatus("copied the line", false)
				}
			case "J", "ctrl+down":
				t.log.moveCursor("down")
				t.openInspect(m)
			case "K", "ctrl+up":
				t.log.moveCursor("up")
				t.openInspect(m)
			default:
				t.inspect.key(msg)
			}
			return nil
		}
		switch msg.String() {
		case "w":
			t.log.wrap = !t.log.wrap
		case "s":
			t.log.fmt.raw = !t.log.fmt.raw
		case "h":
			t.pickFields(m)
		case "v":
			t.openInspect(m)
		case "enter":
			if _, ok := t.log.picked(); ok {
				t.openInspect(m)
			} else {
				t.pickServices(m)
			}
		case "f":
			t.pickServices(m)
		case "i":
			t.pickInstance(m)
		case "/":
			hint := "text or an RE2 regex ((?i) ignores case) that keeps the log lines of " + strings.Join(t.services, ", ") + " that matter; recent lines:\n" + logSample(t.log.lines, 8)
			m.askAI("grep (text or regex, (?i) ignores case)", t.grep, hint, func(v string) tea.Cmd {
				t.grep = v
				return t.start(m)
			})
		default:
			if t.log != nil {
				t.log.key(m, msg, true)
			}
		}
	}
	return nil
}

func (t *logsTab) view(m *model, w, h int) string {
	if len(t.services) == 0 {
		m.zone("logs:body", 1, 1, w-2, h-2)
		return panel("logs", sDim.Render("press f (or enter, or click here) to pick the services whose logs to follow"), w, h, true)
	}
	title := strings.Join(t.services, ", ")
	if len(t.services) > 3 {
		title = fmt.Sprintf("%d services", len(t.services))
	}
	if t.instance != "" {
		title += " · " + t.instance
	}
	if t.grep != "" {
		title += " · grep " + t.grep
	}
	title += fmt.Sprintf(" · %d lines · ", len(t.log.lines)) + t.log.state()
	if t.err != "" {
		return panel(title, sRed.Render(t.err), w, h, true)
	}
	if len(t.log.lines) == 0 {
		m.zone("logs:body", 1, 1, w-2, h-2)
		return panel(title, sDim.Render("waiting for log lines…"), w, h, true)
	}
	nameW := 0
	for _, l := range t.log.lines[max(0, len(t.log.lines)-t.log.scroll-h):max(0, len(t.log.lines)-t.log.scroll)] {
		nameW = max(nameW, len(l.Service))
	}
	nameW = min(nameW, 18)
	re := t.grepRe()
	if t.inspect != nil {
		iw := min(max(w*3/5, 40), w-20)
		lw := w - iw
		left := t.logPanel(m, title, nameW, re, lw, h)
		it := "line · " + t.inspect.current().path()
		if t.inspect.wrap {
			it += sDim.Render(" ⏎wrap")
		} else if t.inspect.hoff > 0 {
			it += sDim.Render(fmt.Sprintf(" ⇢%d", t.inspect.hoff))
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, left, panel(it, t.inspect.view(m, lw+1, 1, iw-2, h-2), iw, h, true))
	}
	return t.logPanel(m, title, nameW, re, w, h)
}

func (t *logsTab) logPanel(m *model, title string, nameW int, re *regexp.Regexp, w, h int) string {
	body := t.log.render(m, 1, 1, w-2, h-2, func(l core.LogLine) string {
		who := l.Service
		if len(t.services) == 1 && l.Instance != "" {
			who = shortInstance(l.Instance)
		}
		svc := lipgloss.NewStyle().Foreground(colorFor(who)).Render(padRight(who, nameW))
		text := t.log.fmt.render(l.Text)
		if re != nil {
			text = re.ReplaceAllStringFunc(text, func(s string) string { return sAmber.Bold(true).Render(s) })
		}
		return sDim.Render(l.Time.Local().Format("15:04:05.000")) + " " + svc + " " + text
	})
	return panel(title, body, w, h, true)
}

func colorFor(name string) lipgloss.Color {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return viz.Palette[h%len(viz.Palette)]
}

// levelColor tints lines that announce an error or warning, as JSON or plain text.
func levelColor(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, `"level":"error"`) || strings.Contains(l, "level=error") || strings.Contains(l, " error ") || strings.HasPrefix(l, "error"), strings.Contains(l, "panic"):
		return sRed.Render(s)
	case strings.Contains(l, `"level":"warn`) || strings.Contains(l, "level=warn") || strings.HasPrefix(l, "warn"):
		return sAmber.Render(s)
	}
	return s
}

// logFormat is how a JSON log line reads: "LEVEL message key=value ...", coloured by level, with the
// keys of ui.logs; raw shows lines as written.
type logFormat struct {
	raw                  bool
	time, level, message []string
	fields               []string
	hidden               map[string]bool
}

func newLogFormat(ui *spec.LogsUI) logFormat {
	f := logFormat{time: []string{"time", "ts", "timestamp", "@timestamp", "Time"}, level: []string{"level", "lvl", "severity", "Level"},
		message: []string{"msg", "message", "Message"}, hidden: map[string]bool{}}
	if ui == nil {
		return f
	}
	f.raw, f.fields = ui.Raw, ui.Fields
	for _, p := range []struct {
		dst *[]string
		src []string
	}{{&f.time, ui.Time}, {&f.level, ui.Level}, {&f.message, ui.Message}} {
		if len(p.src) > 0 {
			*p.dst = p.src
		}
	}
	for _, k := range ui.Hide {
		f.hidden[k] = true
	}
	return f
}

func parseLogJSON(s string) (map[string]any, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") {
		return nil, false
	}
	var m map[string]any
	return m, json.Unmarshal([]byte(t), &m) == nil
}

// pretty is the format of the Logs screen and service pages before ui.logs; tests and other callers keep it.
func pretty(s string) string { return newLogFormat(nil).render(s) }

func (f logFormat) render(s string) string {
	m, ok := parseLogJSON(s)
	if f.raw || !ok {
		return levelColor(s)
	}
	pick := func(keys []string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				delete(m, k)
				return fmt.Sprint(v)
			}
		}
		return ""
	}
	pick(f.time)
	level := strings.ToUpper(pick(f.level))
	msg := pick(f.message)
	keys := f.fields
	if len(keys) == 0 {
		keys = engine.SortedKeys(m)
	}
	var kv []string
	for _, k := range keys {
		v, ok := m[k]
		if !ok || f.hidden[k] {
			continue
		}
		text := fmt.Sprint(v)
		if _, scalar := v.(string); !scalar {
			if raw, err := json.Marshal(v); err == nil {
				text = string(raw)
			}
		}
		if k == "error" || k == "err" {
			text = sRed.Render(text)
		}
		kv = append(kv, sDim.Render(k+"=")+text)
	}
	lv := sDim
	switch {
	case strings.HasPrefix(level, "ERR"), strings.HasPrefix(level, "FATAL"), strings.HasPrefix(level, "PANIC"):
		lv = sRed.Bold(true)
	case strings.HasPrefix(level, "WARN"):
		lv = sAmber.Bold(true)
	case strings.HasPrefix(level, "INFO"):
		lv = sGreen
	}
	return lv.Render(padRight(level, 5)) + " " + msg + "  " + strings.Join(kv, " ")
}

// logKeys are the JSON keys of lines, in first-seen order, minus the time, level and message.
func (f logFormat) keys(lines []core.LogLine) []string {
	seen := map[string]bool{}
	for _, k := range append(append(append([]string{}, f.time...), f.level...), f.message...) {
		seen[k] = true
	}
	var out []string
	for _, k := range f.fields {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, l := range lines {
		m, ok := parseLogJSON(l.Text)
		if !ok {
			continue
		}
		for _, k := range engine.SortedKeys(m) {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	for k := range f.hidden {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// pickFields chooses which JSON keys the lines show; it lasts until rig quits (ui.logs.hide keeps it).
func (t *logsTab) pickFields(m *model) {
	keys := t.log.fmt.keys(t.log.lines)
	if len(keys) == 0 {
		m.setStatus("no JSON lines yet: fields come from structured (JSON) logs", true)
		return
	}
	var shown []string
	for _, k := range keys {
		if !t.log.fmt.hidden[k] {
			shown = append(shown, k)
		}
	}
	m.pickMany("fields shown (space toggles; ui.logs.hide sets the default)", keys, nil, shown, func(c []string) tea.Cmd {
		hidden := map[string]bool{}
		for _, k := range keys {
			hidden[k] = !contains(c, k)
		}
		t.log.fmt.hidden = hidden
		return nil
	})
}

// openInspect shows the picked line, or the newest on screen, as a tree: JSON, console and logfmt
// lines, with JSON and Go values inside strings opened up. The stack starts folded.
func (t *logsTab) openInspect(m *model) {
	l, ok := t.log.picked()
	if !ok {
		if len(t.log.visible) == 0 {
			m.setStatus("no line to inspect yet", true)
			return
		}
		l = t.log.visible[len(t.log.visible)-1]
	}
	tree := &jsonTree{zone: "logs:inspect", root: logTree(l.Text)}
	for _, k := range tree.root.kids {
		k.closed = k.container() && (k.key == "stack" || k.key == "caller")
	}
	tree.flatten()
	if old := t.inspect; old != nil {
		tree.wrap, tree.hoff = old.wrap, old.hoff
		if path := old.current().path(); path != "$" {
			for i, r := range tree.rows {
				if r.n.path() == path {
					tree.sel = i
				}
			}
		}
	} else {
		tree.wrap = true
	}
	t.inspect = tree
}

func (t *logsTab) grepRe() *regexp.Regexp {
	if t.grep == "" {
		return nil
	}
	if re, err := regexp.Compile(t.grep); err == nil {
		return re
	}
	return regexp.MustCompile(regexp.QuoteMeta(t.grep))
}

// click on the empty screen picks services; a double-click on a line follows only its service (or,
// following one service, only its instance).
func (t *logsTab) click(m *model, h hit) tea.Cmd {
	if t.inspect != nil && t.inspect.click(h) {
		return nil
	}
	if h.id != "logs:body" {
		return nil
	}
	if len(t.services) == 0 {
		t.pickServices(m)
		return nil
	}
	if h.y >= len(t.log.visible) {
		return nil
	}
	l := t.log.visible[h.y]
	t.log.cur = t.log.rowLine[h.y]
	if t.inspect != nil {
		t.openInspect(m)
		return nil
	}
	if !h.double {
		return nil
	}
	switch {
	case len(t.services) > 1:
		t.services, t.instance = []string{l.Service}, ""
	case l.Instance != "" && t.instance == "":
		t.instance = l.Instance
	default:
		return nil
	}
	return t.start(m)
}

func (t *logsTab) drag(m *model, h hit, phase dragPhase) bool {
	if h.id != "logs:body" || t.log == nil || len(t.services) == 0 {
		return false
	}
	t.log.drag(m, h, phase)
	return true
}

func (t *logsTab) wheel(m *model, h hit, up bool) (tea.Cmd, bool) {
	if t.inspect != nil && h.id == t.inspect.zone {
		t.inspect.wheel(up)
		return nil, true
	}
	if h.id != "logs:body" || t.log == nil {
		return nil, false
	}
	t.log.wheel(up)
	return nil, true
}
