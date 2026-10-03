package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

const logCap = 5000

// logsTab streams the logs of the services the user picks (all of them only when asked), merged,
// or of one instance of one service.
type logsTab struct {
	lines    []core.LogLine
	services []string
	instance string
	grep     string
	paused   bool
	scroll   int
	restart  bool
	stream   int
	cancel   context.CancelFunc
	ch       <-chan core.LogLine
	err      string
	shown    int // lines the last render showed
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
	return [][2]string{{"f", "pick services"}, {"i", "pick instance"}, {"/", "grep (regex)"}, {"p", "pause"}, {"↑↓", "scroll"},
		{"y/Y", "copy shown/all"}, {"M", "mouse off: select text"}, {"c", "clear"}}
}

func (t *logsTab) interval() time.Duration { return time.Second }

// open streams nothing until services are picked (f): a log of everything is heavy and rarely wanted.
// It does not open the picker itself, which would take the keys that switch screens.
func (t *logsTab) open(m *model) tea.Cmd {
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
	t.lines, t.scroll, t.err = nil, 0, ""
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
		t.lines = append(t.lines, msg.lines...)
		if len(t.lines) > logCap {
			t.lines = t.lines[len(t.lines)-logCap:]
		}
		if t.paused {
			t.scroll += len(msg.lines)
		}
		if msg.done {
			return nil
		}
		return next(msg.gen, msg.stream, t.ch)
	case tea.KeyMsg:
		switch msg.String() {
		case "f", "enter":
			t.pickServices(m)
		case "i":
			t.pickInstance(m)
		case "/":
			m.ask("grep (text or regex, (?i) ignores case)", t.grep, func(v string) tea.Cmd {
				t.grep = v
				return t.start(m)
			})
		case "p":
			t.paused = !t.paused
			if !t.paused {
				t.scroll = 0
			}
		case "up", "k":
			t.paused = true
			t.scroll = min(t.scroll+1, max(0, len(t.lines)-1))
		case "down", "j":
			t.scroll = max(0, t.scroll-1)
		case "pgup":
			t.paused = true
			t.scroll = min(t.scroll+20, max(0, len(t.lines)-1))
		case "pgdown":
			t.scroll = max(0, t.scroll-20)
		case "G", "end":
			t.scroll, t.paused = 0, false
		case "c":
			t.lines, t.scroll = nil, 0
		case "y":
			end := len(t.lines) - t.scroll
			return t.copy(m, t.lines[max(0, end-t.shown):end])
		case "Y":
			return t.copy(m, t.lines)
		}
	}
	return nil
}

func (t *logsTab) view(m *model, w, h int) string {
	if len(t.services) == 0 {
		return panel("logs", sDim.Render("press f (or enter) to pick the services whose logs to follow"), w, h, true)
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
	state := sGreen.Render("● following")
	if t.paused {
		state = sAmber.Render("❚❚ paused")
	}
	title += fmt.Sprintf(" · %d lines · ", len(t.lines)) + state
	inner := h - 2
	t.shown = inner
	if t.err != "" {
		return panel(title, sRed.Render(t.err), w, h, true)
	}
	end := len(t.lines) - t.scroll
	start := max(0, end-inner)
	nameW := 0
	for _, l := range t.lines[start:end] {
		nameW = max(nameW, len(l.Service))
	}
	nameW = min(nameW, 18)
	var b strings.Builder
	for _, l := range t.lines[start:end] {
		who := l.Service
		if len(t.services) == 1 && l.Instance != "" {
			who = shortInstance(l.Instance)
		}
		svc := lipgloss.NewStyle().Foreground(colorFor(who)).Render(padRight(who, nameW))
		text := pretty(l.Text)
		if re := t.grepRe(); re != nil {
			text = re.ReplaceAllStringFunc(text, func(s string) string { return sAmber.Bold(true).Render(s) })
		}
		b.WriteString(sDim.Render(l.Time.Local().Format("15:04:05.000")) + " " + svc + " " + text + "\n")
	}
	if len(t.lines) == 0 {
		b.WriteString(sDim.Render("waiting for log lines…"))
	}
	return panel(title, strings.TrimRight(b.String(), "\n"), w, h, true)
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

// pretty turns a JSON log line into "LEVEL message key=value ...", coloured by level; other lines pass through.
func pretty(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") {
		return levelColor(s)
	}
	var m map[string]any
	if json.Unmarshal([]byte(t), &m) != nil {
		return levelColor(s)
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				delete(m, k)
				return fmt.Sprint(v)
			}
		}
		return ""
	}
	pick("time", "ts", "timestamp", "@timestamp", "Time")
	level := strings.ToUpper(pick("level", "lvl", "severity", "Level"))
	msg := pick("msg", "message", "Message")
	var kv []string
	for _, k := range engine.SortedKeys(m) {
		kv = append(kv, sDim.Render(k+"=")+fmt.Sprint(m[k]))
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

func (t *logsTab) grepRe() *regexp.Regexp {
	if t.grep == "" {
		return nil
	}
	if re, err := regexp.Compile(t.grep); err == nil {
		return re
	}
	return regexp.MustCompile(regexp.QuoteMeta(t.grep))
}

// copy puts lines on the clipboard (OSC 52, which most terminals and tmux honour) and in a file
// under the environment's state directory, for terminals that ignore it.
func (t *logsTab) copy(m *model, lines []core.LogLine) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Time.Local().Format("15:04:05.000") + " " + l.Service + " " + l.Text + "\n")
	}
	text := b.String()
	dir := filepath.Join(m.app.StateDir(), "logs")
	file := filepath.Join(dir, time.Now().Format("20060102-150405")+".log")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(file, []byte(text), 0o644)
	}
	osc52(text)
	m.setStatus(fmt.Sprintf("copied %d lines (also in %s)", len(lines), file), false)
	return nil
}
