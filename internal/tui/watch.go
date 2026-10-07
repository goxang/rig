package tui

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/engine"
)

// watching is ctrl+w's live rebuild: the services' last events, while engine.Watch runs.
type watching struct {
	cancel context.CancelFunc
	events chan engine.WatchEvent
	last   map[string]engine.WatchEvent
}

type watchMsg struct {
	e      engine.WatchEvent
	events chan engine.WatchEvent
	done   bool
}

func (m *model) toggleWatch() tea.Cmd {
	if m.watch != nil {
		m.watch.cancel()
		m.watch = nil
		m.setStatus("watch off", false)
		return nil
	}
	if m.app.Env.Protected {
		m.setStatus(m.app.Env.Name+" is protected: watch rebuilds services only where nobody else runs them", true)
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	w := &watching{cancel: cancel, events: make(chan engine.WatchEvent, 16), last: map[string]engine.WatchEvent{}}
	m.watch = w
	a := m.app
	go func() {
		if err := a.Watch(ctx, nil, func(e engine.WatchEvent) { w.events <- e }); err != nil {
			w.events <- engine.WatchEvent{State: "failed", Err: err}
		}
		close(w.events)
	}()
	m.setStatus("watch on: services rebuild and restart as their sources change (ctrl+w stops)", false)
	return m.nextWatch()
}

func (m *model) nextWatch() tea.Cmd {
	ch := m.watch.events
	return func() tea.Msg {
		e, ok := <-ch
		return watchMsg{e: e, events: ch, done: !ok}
	}
}

func (m *model) onWatch(msg watchMsg) tea.Cmd {
	w := m.watch
	if w == nil || w.events != msg.events {
		return nil
	}
	if msg.done {
		m.watch = nil
		return nil
	}
	e := msg.e
	w.last[e.Service] = e
	switch e.State {
	case "failed":
		what := e.Service
		if what == "" {
			what = "watch"
		}
		m.setStatus(fmt.Sprintf("%s: %s — still running what it ran (A shows the output)", what, buildError(e)), true)
	case "ok":
		m.setStatus(fmt.Sprintf("%s rebuilt and restarted in %s", e.Service, e.Took.Round(100*time.Millisecond)), false)
		return batch(m.nextWatch(), m.fetchServices())
	default:
		m.setStatus(e.Service+" "+e.State+"…", false)
	}
	return m.nextWatch()
}

// watchBadge is the header's watch state: building, a failed build, or on.
func (m *model) watchBadge() string {
	if m.watch == nil {
		return ""
	}
	var busy, failed []string
	for n, e := range m.watch.last {
		switch e.State {
		case "changed", "building", "restarting":
			busy = append(busy, n)
		case "failed":
			failed = append(failed, n)
		}
	}
	sort.Strings(busy)
	sort.Strings(failed)
	switch {
	case len(busy) > 0:
		return badge(cAmber).Render(" ⟳ " + strings.Join(busy, " ") + " ")
	case len(failed) > 0:
		return badge(cRed).Render(" ✖ build " + strings.Join(failed, " ") + " (A) ")
	}
	return badge(cGreen).Render(" ◉ watch ")
}

// watchFailures are the failed builds' output, for the alerts box.
func (m *model) watchFailures() string {
	if m.watch == nil {
		return ""
	}
	var b strings.Builder
	for _, n := range sortedKeys(m.watch.last) {
		e := m.watch.last[n]
		if e.State != "failed" {
			continue
		}
		b.WriteString(sRed.Render("✖ build "+firstNonEmpty(n, "watch")+": "+buildError(e)) + "\n")
		lines := strings.Split(strings.TrimSpace(e.Err.Error()+"\n"+e.Output), "\n")
		if len(lines) > 15 {
			lines = lines[len(lines)-15:]
		}
		for _, l := range lines {
			if l != "" {
				b.WriteString(sDim.Render("  "+l) + "\n")
			}
		}
	}
	return b.String()
}

var compileError = regexp.MustCompile(`[^\s:]+\.\w+:\d+(:\d+)?: .*`)

// buildError is the line of a failed build that says what is wrong: the first compiler error, else
// the error's first line.
func buildError(e engine.WatchEvent) string {
	if m := compileError.FindString(e.Err.Error() + "\n" + e.Output); m != "" {
		return m
	}
	return firstLine(e.Err.Error())
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}
