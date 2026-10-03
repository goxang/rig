package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

const logCap = 5000

type logsTab struct {
	lines   []core.LogLine
	only    string
	grep    string
	paused  bool
	scroll  int
	restart bool
	stream  int
	cancel  context.CancelFunc
	ch      <-chan core.LogLine
	err     string
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
	return [][2]string{{"f", "service"}, {"/", "grep"}, {"p", "pause"}, {"↑↓", "scroll"}, {"c", "clear"}}
}

func (t *logsTab) open(m *model) tea.Cmd { return t.start(m) }

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
	q := core.LogQuery{Follow: true, Tail: 200, Match: t.grep}
	if t.only != "" {
		q.Services = []string{t.only}
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
		case "f":
			names := append([]string{""}, m.app.Spec.ServiceNames()...)
			for i, n := range names {
				if n == t.only {
					t.only = names[(i+1)%len(names)]
					break
				}
			}
			return t.start(m)
		case "/":
			m.ask("grep", t.grep, func(v string) tea.Cmd {
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
		}
	}
	return nil
}

func (t *logsTab) view(m *model, w, h int) string {
	title := "all services"
	if t.only != "" {
		title = t.only
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
		svc := lipgloss.NewStyle().Foreground(colorFor(l.Service)).Render(padRight(l.Service, nameW))
		text := pretty(l.Text)
		if t.grep != "" {
			text = strings.ReplaceAll(text, t.grep, sAmber.Bold(true).Render(t.grep))
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
