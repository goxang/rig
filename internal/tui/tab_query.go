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

// queryTab is a console over every component that speaks a query language.
type queryTab struct {
	comps   []string
	langs   map[string]string
	sel     int
	input   textinput.Model
	editing bool
	result  core.Table
	err     string
	took    time.Duration
	row     int
	history []string
	hpos    int
}

type queryMsg struct {
	gen   int
	t     core.Table
	err   error
	took  time.Duration
	comps map[string]string
}

var examples = map[string]string{
	"sql": "SELECT 1", "promql": "up", "logql": `{app=~".+"}`, "redis": "INFO", "kubectl": "get pods -o wide",
	"http": "GET /", "traces": "limit=20", "kv": "", "rabbitmq": "queues",
}

func (t *queryTab) name() string { return "Query" }
func (t *queryTab) typing() bool { return t.editing }
func (t *queryTab) hints() [][2]string {
	if t.editing {
		return [][2]string{{"enter", "run"}, {"esc", "leave"}, {"ctrl+p/n", "history"}}
	}
	return [][2]string{{"←→", "component"}, {"i /", "write query"}, {"↑↓", "rows"}}
}

func (t *queryTab) open(m *model) tea.Cmd {
	t.input = textinput.New()
	t.input.Prompt = "› "
	t.input.Placeholder = "query"
	a, gen := m.app, m.gen
	return func() tea.Msg {
		qs := a.Queriers()
		langs := map[string]string{}
		for n, q := range qs {
			langs[n] = q.QueryLanguage()
		}
		return queryMsg{gen: gen, comps: langs}
	}
}

func (t *queryTab) refresh(m *model) tea.Cmd { return nil }

func (t *queryTab) current() string {
	if t.sel < len(t.comps) {
		return t.comps[t.sel]
	}
	return ""
}

func (t *queryTab) run(m *model, q string) tea.Cmd {
	comp := t.current()
	a, gen, ctx := m.app, m.gen, m.ctx
	if len(t.history) == 0 || t.history[len(t.history)-1] != q {
		t.history = append(t.history, q)
	}
	t.hpos = len(t.history)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		start := time.Now()
		qr, ok := a.Queriers()[comp]
		if !ok {
			return queryMsg{gen: gen, err: fmt.Errorf("no component %s", comp)}
		}
		res, err := qr.RunQuery(c, q)
		return queryMsg{gen: gen, t: res, err: err, took: time.Since(start)}
	}
}

func (t *queryTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case queryMsg:
		if msg.gen != m.gen {
			return nil
		}
		if msg.comps != nil {
			t.langs = msg.comps
			t.comps = engine.SortedKeys(msg.comps)
			t.input.SetValue(examples[t.langs[t.current()]])
			return nil
		}
		t.result, t.took, t.row, t.err = msg.t, msg.took, 0, ""
		if msg.err != nil {
			t.err = msg.err.Error()
		}
	case tea.KeyMsg:
		if t.editing {
			switch msg.String() {
			case "esc":
				t.editing = false
				t.input.Blur()
				return nil
			case "enter":
				return t.run(m, t.input.Value())
			case "ctrl+p":
				if t.hpos > 0 {
					t.hpos--
					t.input.SetValue(t.history[t.hpos])
					t.input.CursorEnd()
				}
				return nil
			case "ctrl+n":
				if t.hpos < len(t.history)-1 {
					t.hpos++
					t.input.SetValue(t.history[t.hpos])
					t.input.CursorEnd()
				}
				return nil
			}
			var cmd tea.Cmd
			t.input, cmd = t.input.Update(msg)
			return cmd
		}
		switch msg.String() {
		case "left", "h":
			t.sel = max(0, t.sel-1)
			t.input.SetValue(examples[t.langs[t.current()]])
		case "right", "l":
			t.sel = min(len(t.comps)-1, t.sel+1)
			t.input.SetValue(examples[t.langs[t.current()]])
		case "i", "/", "enter":
			t.editing = true
			t.input.Focus()
			t.input.CursorEnd()
		default:
			listKeys(msg, &t.row, len(t.result.Rows))
		}
	}
	return nil
}

func (t *queryTab) view(m *model, w, h int) string {
	if len(t.comps) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sDim.Render("no component in this environment answers queries"))
	}
	var chips []string
	for i, c := range t.comps {
		label := c + sDim.Render(" "+t.langs[c])
		if i == t.sel {
			label = sTabOn.Render(c + " · " + t.langs[c])
		} else {
			label = " " + label + " "
		}
		chips = append(chips, label)
	}
	t.input.Width = w - 6
	inputBox := panel("query · "+t.current(), t.input.View(), w, 3, t.editing)
	head := truncate(strings.Join(chips, " "), w)

	resH := h - 4
	var body string
	switch {
	case t.err != "":
		body = sRed.Render(wrap(t.err, w-4))
	case len(t.result.Columns) == 0 && t.result.Note == "":
		body = sDim.Render("press i to write a query, enter to run")
	default:
		widths := colWidths(t.result, w-4)
		off := scroll(t.row, 0, resH-3, len(t.result.Rows))
		body = table(t.result.Columns, widths, t.result.Rows, t.row, off, resH-3)
		if t.result.Note != "" {
			body += "\n" + sDim.Render(t.result.Note)
		}
	}
	title := fmt.Sprintf("result · %d rows · %s", len(t.result.Rows), t.took.Round(time.Millisecond))
	return lipgloss.JoinVertical(lipgloss.Left, head, inputBox, panel(title, body, w, resH, !t.editing))
}

// colWidths sizes columns to their content, shrinking the widest ones to fit w.
func colWidths(t core.Table, w int) []int {
	ws := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		ws[i] = lipgloss.Width(c)
	}
	for _, r := range t.Rows[:min(len(t.Rows), 200)] {
		for i := range ws {
			if i < len(r) {
				ws[i] = max(ws[i], min(lipgloss.Width(r[i]), 60))
			}
		}
	}
	for {
		total := len(ws) - 1
		for _, x := range ws {
			total += x
		}
		if total <= w {
			return ws
		}
		big := 0
		for i := range ws {
			if ws[i] > ws[big] {
				big = i
			}
		}
		if ws[big] <= 4 {
			return ws
		}
		ws[big]--
	}
}
