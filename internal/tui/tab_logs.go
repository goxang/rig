package tui

import (
	"bytes"
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
// or of the instances picked for a single service.
type logsTab struct {
	log      *logView
	services []string
	// instances narrows a single picked service to the instances chosen here; empty means all.
	instances []string
	grep      string
	// query is grep when it reads as a field query: lines are filtered here, not by the source
	query   logQuery
	restart bool
	stream  int
	cancel  context.CancelFunc
	ch      <-chan core.LogLine
	err     string
	// inspect shows the picked line (or the newest) field by field; inTree says the keys walk its
	// fields rather than the log lines beside it
	inspect *jsonTree
	inTree  bool
	// id names its zones and stream messages: "logs" for the Logs screen, "svc" when a service page
	// embeds it, following that one service (fixed)
	id    string
	fixed bool
}

func (t *logsTab) zone(part string) string {
	if t.id == "" {
		return "logs:" + part
	}
	return t.id + ":" + part
}

func logsUI(m *model) *spec.LogsUI {
	if ui := m.app.Spec.UI; ui != nil {
		return ui.Logs
	}
	return nil
}

type logBatchMsg struct {
	id          string
	gen, stream int
	lines       []core.LogLine
	done        bool
}

type logStartMsg struct {
	id          string
	gen, stream int
	ch          <-chan core.LogLine
	err         error
}

func (t *logsTab) name() string { return "Logs" }
func (t *logsTab) typing() bool { return false }
func (t *logsTab) hints() [][2]string {
	if t.inspect != nil && !t.inTree {
		return [][2]string{{"↑↓ pgup pgdn", "previous/next log line"}, {"enter →", "into the fields"}, {"Y", "copy the line"}, {"esc v", "close"}}
	}
	if t.inspect != nil {
		return [][2]string{{"↑↓", "move"}, {"←→ space", "fold"}, {"+ - z", "expand all, fold all, toggle"}, {"J K  ctrl+↓↑", "next/previous log line"}, {"w", "wrap"}, {"H L  shift+←→", "sideways"},
			{"f", "filter lines by this field"}, {"y", "copy value"}, {"Y", "copy the line"}, {"esc", "back to the lines"}, {"v", "close"}}
	}
	return [][2]string{{"f", "pick services"}, {"/", "filter: text, regex, a.b=value"}, {"↑↓ click", "pick a line"}, {"enter v", "inspect the line"}, {"w", "wrap"}, {"s", "structured/raw"},
		{"h", "fields shown"}, {"i", "pick instances"}, {"p", "pause"}, {"pgup pgdn ←→", "page, sideways"},
		{"drag", "select text: copied (past an edge scrolls)"}, {"y/Y", "copy shown/all"}, {"G", "follow again"}, {"c", "clear"}, {"M", "mouse off: terminal selection"}}
}

func (t *logsTab) interval() time.Duration { return time.Second }

// open streams nothing until services are picked (f): a log of everything is heavy and rarely wanted.
// It does not open the picker itself, which would take the keys that switch screens.
func (t *logsTab) open(m *model) tea.Cmd {
	if t.log == nil {
		t.log = newLogView(t.zone("body"), logCap, logsUI(m))
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
		t.services, t.instances = chosen, nil
		return t.start(m)
	})
}

// pickInstance lets the user toggle on any subset of the single picked service's instances;
// none picked means all of them.
func (t *logsTab) pickInstance(m *model) {
	if len(t.services) != 1 {
		m.setStatus("pick a single service first (f) to choose its instances", true)
		return
	}
	svc := t.services[0]
	var ids, desc []string
	for _, st := range m.services {
		if st.Service != svc {
			continue
		}
		for _, in := range st.Instances {
			ids = append(ids, in.ID)
			desc = append(desc, string(in.State)+" "+in.Host)
		}
	}
	m.pickMany("instances of "+svc+" (space toggles; none picked means all)", ids, desc, t.instances, func(chosen []string) tea.Cmd {
		t.instances = chosen
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
		t.log = newLogView(t.zone("body"), logCap, logsUI(m))
	}
	t.log.reset()
	t.err = ""
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	a, gen, stream, id := m.app, m.gen, t.stream, t.id
	q := core.LogQuery{Follow: true, Tail: 200, Match: t.grep, Services: t.services}
	if _, err := regexp.Compile(t.grep); err == nil {
		q.Regex = true
	}
	t.query = nil
	if fq, ok := parseLogQuery(t.grep); ok {
		// the source cannot read fields: take more history and filter it here
		t.query, q.Match, q.Regex, q.Tail = fq, "", false, 2000
	}
	// the source only filters to one instance; picking several is filtered client-side in filter()
	if len(t.services) == 1 && len(t.instances) == 1 {
		q.Instance = t.instances[0]
	}
	return func() tea.Msg {
		src, _, err := engine.Get[core.LogSource](a, core.KindLogs, "")
		if err != nil {
			return logStartMsg{id: id, gen: gen, stream: stream, err: err}
		}
		ch, err := src.Logs(ctx, q)
		return logStartMsg{id: id, gen: gen, stream: stream, ch: ch, err: err}
	}
}

// next waits for log lines and hands them over in batches, so a chatty service does not flood the UI loop.
func next(id string, gen, stream int, ch <-chan core.LogLine) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return logBatchMsg{id: id, gen: gen, stream: stream, done: true}
		}
		batch := []core.LogLine{first}
		deadline := time.After(100 * time.Millisecond)
		for len(batch) < 500 {
			select {
			case l, ok := <-ch:
				if !ok {
					return logBatchMsg{id: id, gen: gen, stream: stream, lines: batch, done: true}
				}
				batch = append(batch, l)
			case <-deadline:
				return logBatchMsg{id: id, gen: gen, stream: stream, lines: batch}
			}
		}
		return logBatchMsg{id: id, gen: gen, stream: stream, lines: batch}
	}
}

func (t *logsTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case logStartMsg:
		if msg.id != t.id || msg.gen != m.gen || msg.stream != t.stream {
			return nil
		}
		if msg.err != nil {
			t.err = msg.err.Error()
			return nil
		}
		t.ch = msg.ch
		return next(t.id, msg.gen, msg.stream, msg.ch)
	case logBatchMsg:
		if msg.id != t.id || msg.gen != m.gen || msg.stream != t.stream {
			return nil
		}
		t.log.add(t.filter(msg.lines))
		if msg.done {
			return nil
		}
		return next(t.id, msg.gen, msg.stream, t.ch)
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

// key handles a key on the log; false (nil cmd) also when the key is not a log key, see handled.
func (t *logsTab) key(m *model, msg tea.KeyMsg) tea.Cmd {
	cmd, _ := t.keyHandled(m, msg)
	return cmd
}

// keyHandled is key, saying whether the key was the log's: a service page embedding the log
// offers the rest to its own keys.
func (t *logsTab) keyHandled(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
	{
		if t.inspect != nil && !t.inTree {
			switch s := msg.String(); s {
			case "esc", "v", "q":
				t.inspect = nil
			case "enter", "right", "l", "tab":
				t.inTree = true
			case "Y":
				if l, ok := t.log.picked(); ok {
					copyText(l.Text)
					m.setStatus("copied the line", false)
				}
			case "up", "down", "k", "j", "pgup", "pgdown", "home", "J", "K", "ctrl+up", "ctrl+down":
				step := map[string]string{"J": "down", "K": "up", "ctrl+down": "down", "ctrl+up": "up"}[s]
				if step == "" {
					step = s
				}
				t.log.moveCursor(step)
				t.inspect = openInspect(m, t.log, t.zone("inspect"), t.inspect)
			default:
				return nil, false
			}
			return nil, true
		}
		if t.inspect != nil {
			switch msg.String() {
			case "esc":
				t.inTree = false
			case "v", "q":
				t.inspect = nil
			case "y":
				copyText(t.inspect.current().text())
				m.setStatus("copied "+t.inspect.current().path(), false)
			case "Y":
				if l, ok := t.log.picked(); ok {
					copyText(l.Text)
					m.setStatus("copied the line", false)
				}
			case "f":
				term := queryFor(t.inspect.current())
				if _, ok := parseLogQuery(t.grep); ok {
					term = t.grep + " " + term
				}
				t.grep, t.inspect = term, nil
				m.setStatus("filter: "+term+" (/ edits it)", false)
				return t.start(m), true
			case "J", "ctrl+down":
				t.log.moveCursor("down")
				t.inspect = openInspect(m, t.log, t.zone("inspect"), t.inspect)
			case "K", "ctrl+up":
				t.log.moveCursor("up")
				t.inspect = openInspect(m, t.log, t.zone("inspect"), t.inspect)
			default:
				t.inspect.key(msg)
			}
			return nil, true
		}
		switch msg.String() {
		case "w":
			t.log.wrap = !t.log.wrap
		case "s":
			t.log.fmt.raw = !t.log.fmt.raw
		case "h":
			pickFields(m, t.log)
		case "v":
			t.openInspect(m)
		case "enter":
			if _, ok := t.log.picked(); ok {
				t.openInspect(m)
			} else if !t.fixed {
				t.pickServices(m)
			} else {
				return nil, false
			}
		case "f":
			if t.fixed {
				return nil, false
			}
			t.pickServices(m)
		case "i":
			t.pickInstance(m)
		case "/":
			hint := "text or an RE2 regex ((?i) ignores case), or a field query (path=value, path!=value, path~text, ANDed by spaces, e.g. output.Transaction.ID=202604), that keeps the log lines of " + strings.Join(t.services, ", ") + " that matter; recent lines:\n" + logSample(t.log.lines, 8)
			check := func(_ context.Context, v string) error {
				if _, ok := parseLogQuery(v); ok {
					return nil
				}
				_, err := regexp.Compile(v)
				return err
			}
			m.askChecked("filter: text, regex, or fields like output.Transaction.ID=202604 level!=debug", t.grep, hint, check, func(v string) tea.Cmd {
				t.grep = v
				return t.start(m)
			})
		default:
			if t.log == nil || !t.log.key(m, msg, true) {
				return nil, false
			}
		}
	}
	return nil, true
}

func (t *logsTab) openInspect(m *model) {
	t.inspect, t.inTree = openInspect(m, t.log, t.zone("inspect"), t.inspect), false
}

func (t *logsTab) view(m *model, w, h int) string {
	if len(t.services) == 0 {
		m.zone(t.zone("body"), 1, 1, w-2, h-2)
		return panel("logs", sDim.Render("press f (or enter, or click here) to pick the services whose logs to follow"), w, h, true)
	}
	title := strings.Join(t.services, ", ")
	if len(t.services) > 3 {
		title = fmt.Sprintf("%d services", len(t.services))
	}
	if len(t.instances) > 0 {
		title += " · " + strings.Join(t.instances, ",")
	}
	switch {
	case t.query != nil:
		title += " · where " + t.grep
	case t.grep != "":
		title += " · grep " + t.grep
	}
	title += fmt.Sprintf(" · %d lines · ", len(t.log.lines)) + t.log.state()
	if t.err != "" {
		return panel(title, sRed.Render(t.err), w, h, true)
	}
	if len(t.log.lines) == 0 {
		m.zone(t.zone("body"), 1, 1, w-2, h-2)
		wait := "waiting for log lines…"
		if t.query != nil {
			wait = "no line matches " + t.grep + " yet (/ edits the filter)"
		}
		return panel(title, sDim.Render(wait), w, h, true)
	}
	nameW := 0
	for _, l := range t.log.lines[max(0, len(t.log.lines)-t.log.scroll-h):max(0, len(t.log.lines)-t.log.scroll)] {
		nameW = max(nameW, len(l.Service))
	}
	nameW = min(nameW, 18)
	re := t.grepRe()
	if t.inspect != nil {
		iw := m.paneSize("logs.inspect", splitGeo{total: w, minA: 30, minB: 20, fromEnd: true}, min(max(w*3/5, 40), w-20), 0, 0, h)
		lw := w - iw
		left := t.logPanel(m, title, nameW, re, lw, h, !t.inTree)
		it := "line · " + t.inspect.current().path()
		if t.inspect.wrap {
			it += sDim.Render(" ⏎wrap")
		} else if t.inspect.hoff > 0 {
			it += sDim.Render(fmt.Sprintf(" ⇢%d", t.inspect.hoff))
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, left, panel(it, t.inspect.view(m, lw+1, 1, iw-2, h-2), iw, h, t.inTree))
	}
	return t.logPanel(m, title, nameW, re, w, h, true)
}

func (t *logsTab) logPanel(m *model, title string, nameW int, re *regexp.Regexp, w, h int, focused bool) string {
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
	return panel(title, body, w, h, focused)
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

// logFormat is how a log line reads: "LEVEL message key=value ...", coloured by level, with the
// keys of ui.logs. JSON, console and logfmt lines all go through logTree, so a value holding JSON, a
// protobuf or a Go struct shows as compact JSON; raw shows lines as written.
type logFormat struct {
	raw                  bool
	time, level, message []string
	fields               []string
	hidden               map[string]bool
	trees                map[string]*jnode
}

func newLogFormat(ui *spec.LogsUI) logFormat {
	f := logFormat{time: []string{"time", "ts", "timestamp", "@timestamp", "Time"}, level: []string{"level", "lvl", "severity", "Level"},
		message: []string{"msg", "message", "Message"}, hidden: map[string]bool{}, trees: map[string]*jnode{}}
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

// tree is line s parsed, cached: the screen redraws the same lines every second.
func (f logFormat) tree(s string) *jnode {
	if n, ok := f.trees[s]; ok {
		return n
	}
	// ponytail: whole-cache reset, an LRU if redraws after a reset ever show up in profiles
	if len(f.trees) > 2*logCap {
		clear(f.trees)
	}
	n := logTree(s)
	f.trees[s] = n
	return n
}

// structured says whether s parsed into fields, not one text value.
func structured(n *jnode) bool {
	return n.kind == 'o' && !(len(n.kids) == 1 && n.kids[0].key == "text" && !n.kids[0].container())
}

func (f logFormat) render(s string) string {
	if f.raw {
		return levelColor(s)
	}
	root := f.tree(s)
	if !structured(root) {
		return levelColor(s)
	}
	byKey := map[string]*jnode{}
	for _, k := range root.kids {
		byKey[k.key] = k
	}
	used := map[string]bool{}
	pick := func(keys []string) string {
		for _, k := range keys {
			if n, ok := byKey[k]; ok {
				used[k] = true
				return n.text()
			}
		}
		return ""
	}
	pick(f.time)
	level := strings.ToUpper(pick(f.level))
	msg := pick(f.message)
	keys := f.fields
	if len(keys) == 0 {
		for _, k := range root.kids {
			keys = append(keys, k.key)
		}
	}
	var kv []string
	for _, k := range keys {
		n, ok := byKey[k]
		if !ok || used[k] || f.hidden[k] {
			continue
		}
		text := n.text()
		if n.container() {
			text = compactJSON(n)
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
	case strings.HasPrefix(level, "WARN"), level == "WRN":
		lv = sAmber.Bold(true)
	case strings.HasPrefix(level, "INF"):
		lv = sGreen
	}
	return lv.Render(padRight(level, 5)) + " " + msg + "  " + strings.Join(kv, " ")
}

func compactJSON(n *jnode) string {
	var b bytes.Buffer
	if json.Compact(&b, n.bytes()) != nil {
		return string(n.bytes())
	}
	return b.String()
}

// keys are the top-level fields of lines, in first-seen order, minus the time, level and message.
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
		root := f.tree(l.Text)
		if !structured(root) {
			continue
		}
		for _, k := range root.kids {
			if !seen[k.key] {
				seen[k.key] = true
				out = append(out, k.key)
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

// pickFields chooses which JSON keys lv's lines show; it lasts until rig quits (ui.logs.hide keeps
// it as the default). Shared by the Logs tab and a service's own log page.
func pickFields(m *model, lv *logView) {
	keys := lv.fmt.keys(lv.lines)
	if len(keys) == 0 {
		m.setStatus("no structured lines yet: fields come from JSON, console or key=value logs", true)
		return
	}
	var shown []string
	for _, k := range keys {
		if !lv.fmt.hidden[k] {
			shown = append(shown, k)
		}
	}
	m.pickMany("fields shown (space toggles; ui.logs.hide sets the default)", keys, nil, shown, func(c []string) tea.Cmd {
		hidden := map[string]bool{}
		for _, k := range keys {
			hidden[k] = !contains(c, k)
		}
		lv.fmt.hidden = hidden
		return nil
	})
}

// openInspect builds a tree for the picked line, or the newest on screen, as a tree: JSON, console
// and logfmt lines, with JSON and Go values inside strings opened up. The stack starts folded. old
// is the previously open inspector, if any, whose scroll/fold state carries over. Shared by the Logs
// tab and a service's own log page.
func openInspect(m *model, lv *logView, zone string, old *jsonTree) *jsonTree {
	l, ok := lv.picked()
	if !ok {
		if len(lv.visible) == 0 {
			m.setStatus("no line to inspect yet", true)
			return old
		}
		l = lv.visible[len(lv.visible)-1]
	}
	tree := &jsonTree{zone: zone, root: logTree(l.Text)}
	for _, k := range tree.root.kids {
		k.closed = k.container() && (k.key == "stack" || k.key == "caller")
	}
	tree.flatten()
	if old != nil {
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
	return tree
}

// filter keeps the lines the field query matches, and, when several (not all) instances are
// picked, the ones from a chosen instance: the source only knows how to filter to a single one.
func (t *logsTab) filter(lines []core.LogLine) []core.LogLine {
	if t.query == nil && len(t.instances) <= 1 {
		return lines
	}
	var out []core.LogLine
	for _, l := range lines {
		if len(t.instances) > 1 && !contains(t.instances, l.Instance) {
			continue
		}
		if t.query != nil && !t.query.match(t.log.fmt.tree(l.Text)) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// grepRe is what to highlight in a shown line: the grep/regex term, or, for a field query, the
// literal values its terms look for.
func (t *logsTab) grepRe() *regexp.Regexp {
	if t.query != nil {
		return t.query.highlightRe()
	}
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
		t.inTree = true
		return nil
	}
	if h.id != t.zone("body") {
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
	t.log.paused = true
	if t.inspect != nil {
		t.openInspect(m)
		return nil
	}
	if !h.double {
		return nil
	}
	if t.fixed {
		t.openInspect(m)
		return nil
	}
	switch {
	case len(t.services) > 1:
		t.services, t.instances = []string{l.Service}, nil
	case l.Instance != "" && len(t.instances) == 0:
		t.instances = []string{l.Instance}
	default:
		return nil
	}
	return t.start(m)
}

func (t *logsTab) drag(m *model, h hit, phase dragPhase) bool {
	if h.id != t.zone("body") || t.log == nil || len(t.services) == 0 {
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
	if h.id != t.zone("body") || t.log == nil {
		return nil, false
	}
	t.log.wheel(up)
	return nil, true
}

func (t *logsTab) atRoot() bool { return t.inspect == nil }
