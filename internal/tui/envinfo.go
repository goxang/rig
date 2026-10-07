package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/engine"
)

type envInfoMsg struct {
	gen int
	box *envBox
}

// secretName is a name whose value the ctrl+e box hides.
var secretName = regexp.MustCompile(`(?i)pass|secret|token|login|credential|private|apikey|api_key`)

func maskValue(name, value string) string {
	if value != "" && secretName.MatchString(name) {
		return sDim.Render("•••• (set)")
	}
	return value
}

// envLine is one line of the ctrl+e box; key is set when the line is a manifest/task variable
// (`rig vars set`'s own write path), which e/enter edits in place.
type envLine struct {
	text string
	key  string
}

// envBox is the ctrl+e box: scrollable, and its variable rows are editable.
type envBox struct {
	env         string
	lines       []envLine
	sel, offset int
}

func (b *envBox) plainText() string {
	var lines []string
	for _, l := range b.lines {
		lines = append(lines, l.text)
	}
	return strings.Join(lines, "\n")
}

// view renders the box, scrolled to keep sel visible within h (the body height available).
func (b *envBox) view(h int) string {
	const frame = 7 // border(2) + padding(2) + title(1) + blank before footer(1) + footer(1)
	inner := max(1, h-frame)
	b.sel = max(0, min(b.sel, len(b.lines)-1))
	b.offset = scroll(b.sel, b.offset, inner, len(b.lines))
	var body strings.Builder
	for i := b.offset; i < len(b.lines) && i-b.offset < inner; i++ {
		l := b.lines[i]
		text := l.text
		if i == b.sel && text != "" {
			text = highlight(sSelected, text, lipgloss.Width(text))
		}
		body.WriteString(text + "\n")
	}
	footer := "y copies · any other key closes"
	if len(b.lines) > 0 && b.lines[b.sel].key != "" {
		footer = "↑↓ select · e/enter edits · y copies · any other key closes"
	} else {
		footer = "↑↓ select · y copies · any other key closes"
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render("rig — "+b.env) + "\n" + strings.TrimRight(body.String(), "\n") + "\n\n" + sDim.Render(footer))
}

// fetchEnvInfo builds the ctrl+e box: the environment, the variables tasks and manifests get (with
// `rig vars set` overrides, read from the environment's state), and where each component points.
func (m *model) fetchEnvInfo() tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	m.setStatus("reading "+a.Env.Name+"…", false)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return envInfoMsg{gen: gen, box: envInfoBox(c, a)}
	}
}

func envInfoBox(ctx context.Context, a *engine.App) *envBox {
	box := &envBox{env: a.Env.Name}
	row := func(k, v, from, key string) {
		text := "  " + sKey.Render(padRight(k, 22)) + " " + v
		if from != "" {
			text += sDim.Render("  " + from)
		}
		box.lines = append(box.lines, envLine{text: text, key: key})
	}
	section := func(s string) {
		box.lines = append(box.lines, envLine{}, envLine{text: sTitle.Render(s)})
	}

	env := a.Env
	row("environment", env.Name, env.Description, "")
	row("runtime", env.Runtime.Type, "", "")
	if ns := a.Namespace(); ns != "" {
		row("namespace", ns, "", "")
	}
	if env.ReadOnly {
		row("read-only", sAmber.Render("yes"), "every change is refused, even confirmed", "")
	}
	if env.Protected {
		row("protected", sRed.Render("yes"), "every change asks", "")
	}
	vars, err := a.Vars(ctx)
	if len(vars) > 0 || err != nil {
		section("manifest and task variables")
		for _, k := range engine.SortedKeys(vars) {
			row(k, maskValue(k, vars[k].Value), vars[k].From, k)
		}
		if err != nil {
			box.lines = append(box.lines, envLine{text: "  " + sRed.Render("state: "+err.Error())})
		}
	}
	project := map[string]string{}
	for k, v := range a.Spec.Vars {
		project[k] = v
	}
	for k, v := range env.Vars {
		project[k] = v
	}
	if len(project) > 0 {
		section("rig.yaml variables (${NAME})")
		for _, k := range engine.SortedKeys(project) {
			from := ""
			if _, ok := env.Vars[k]; ok {
				from = "environments." + env.Name + ".vars"
			}
			row(k, maskValue(k, project[k]), from, "")
		}
	}
	if len(a.Spec.Components) > 0 {
		section("components")
		for _, n := range engine.SortedKeys(a.Spec.Components) {
			comp := a.Spec.Components[n]
			if strings.HasPrefix(n, "default:") || comp == nil {
				continue
			}
			var fields map[string]any
			_ = comp.Node.Decode(&fields)
			var parts []string
			for _, k := range []string{"addr", "url", "database", "db", "namespace", "user", "driver"} {
				if v, ok := fields[k]; ok && fmt.Sprint(v) != "" {
					parts = append(parts, sDim.Render(k+"=")+maskValue(k, fmt.Sprint(v)))
				}
			}
			row(n, comp.Type+"  "+strings.Join(parts, " "), "", "")
		}
	}
	// land the initial selection on the first editable row, when there is one
	for i, l := range box.lines {
		if l.key != "" {
			box.sel = i
			break
		}
	}
	return box
}

// editEnvVar opens the prompt to change the selected variable's value, the same `rig vars set` write
// path the CLI uses (environment state, key "var.<name>").
func (m *model) editEnvVar(box *envBox) tea.Cmd {
	if box.sel < 0 || box.sel >= len(box.lines) {
		return nil
	}
	key := box.lines[box.sel].key
	if key == "" {
		return nil
	}
	vars, _ := m.app.Vars(m.ctx)
	cur := ""
	if v, ok := vars[key]; ok && !secretName.MatchString(key) {
		cur = v.Value
	}
	m.envInfo = nil
	m.ask("value of "+key, cur, func(v string) tea.Cmd {
		if v == cur {
			return nil
		}
		a := m.app
		return m.act("set var "+key, false, func(ctx context.Context) error {
			return a.SetState(ctx, map[string]string{"var." + key: v})
		})
	})
	return nil
}
