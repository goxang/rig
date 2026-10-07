package tui

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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
	// a connection string's password: postgres://app:pw@db, sqlserver://sa:pw@host
	return urlPassword.ReplaceAllString(value, "${1}••••@")
}

var urlPassword = regexp.MustCompile(`(://[^:/@\s]+:)[^@\s]+@`)

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
	// filter keeps the rows that contain it; typing says / was pressed and keys go to it
	filter string
	typing bool
}

// shown are the indexes of the rows the filter keeps (all without one; blank rows drop).
func (b *envBox) shown() []int {
	var out []int
	f := strings.ToLower(b.filter)
	for i, l := range b.lines {
		if f == "" || l.text != "" && strings.Contains(strings.ToLower(ansi.Strip(l.text)), f) {
			out = append(out, i)
		}
	}
	return out
}

// key handles a key of the open box; done says it closes.
func (b *envBox) key(k tea.KeyMsg) (edit, done bool) {
	shown := b.shown()
	pos := max(0, slices.Index(shown, b.sel))
	move := func(d int) {
		if len(shown) > 0 {
			b.sel = shown[max(0, min(len(shown)-1, pos+d))]
		}
	}
	if b.typing {
		switch k.Type {
		case tea.KeyEnter, tea.KeyDown, tea.KeyUp:
			b.typing = false
			if k.Type != tea.KeyEnter {
				return b.key(k)
			}
		case tea.KeyEsc:
			b.typing, b.filter = false, ""
		case tea.KeyBackspace:
			if r := []rune(b.filter); len(r) > 0 {
				b.filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			b.filter += string(k.Runes)
		}
		if s := b.shown(); len(s) > 0 && !slices.Contains(s, b.sel) {
			b.sel = s[0]
		}
		return false, false
	}
	switch k.String() {
	case "up", "k":
		move(-1)
	case "down", "j":
		move(1)
	case "pgup":
		move(-10)
	case "pgdown":
		move(10)
	case "/":
		b.typing = true
	case "e", "enter":
		return true, false
	case "esc":
		if b.filter != "" {
			b.filter = ""
			return false, false
		}
		return false, true
	default:
		return false, k.String() != "y"
	}
	return false, false
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
	shown := b.shown()
	pos := max(0, slices.Index(shown, b.sel))
	b.offset = scroll(pos, b.offset, inner, len(shown))
	var body strings.Builder
	w := 40
	for _, l := range b.lines {
		w = max(w, lipgloss.Width(l.text))
	}
	for i := b.offset; i < b.offset+min(inner, len(shown)); i++ {
		if i >= len(shown) {
			body.WriteString(" \n")
			continue
		}
		text := b.lines[shown[i]].text
		if shown[i] == b.sel && text != "" {
			text = highlight(sSelected, text, lipgloss.Width(text))
		}
		body.WriteString(text + "\n")
	}
	if len(shown) == 0 {
		body.WriteString(sDim.Render("  nothing matches") + "\n")
	}
	footer := "↑↓ select · / search · y copies · esc closes"
	if len(b.lines) > 0 && b.lines[b.sel].key != "" {
		footer = "↑↓ select · e/enter edits · / search · y copies · esc closes"
	}
	title := sTitle.Render("rig — " + b.env)
	switch {
	case b.typing:
		title += sDim.Render("  search ") + sAmber.Render(b.filter+"▏")
	case b.filter != "":
		title += sDim.Render("  search ") + sAmber.Render(b.filter) + sDim.Render(" (esc clears)")
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).Width(w + 6).
		Render(title + "\n" + strings.TrimRight(body.String(), "\n") + "\n\n" + sDim.Render(footer))
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
			if _, ok := vars[k]; !ok {
				row(k, maskValue(k, project[k]), from, k)
			}
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
	cur := vars[key].Value
	if _, ok := vars[key]; !ok {
		cur = cmp.Or(m.app.Env.Vars[key], m.app.Spec.Vars[key])
	}
	if secretName.MatchString(key) {
		cur = ""
	}
	m.envInfo = nil
	m.ask("value of "+key, cur, func(v string) tea.Cmd {
		if v == cur {
			return nil
		}
		a := m.app
		return m.act("set var "+key, false, func(ctx context.Context) error {
			_, err := setVars(ctx, a, map[string]string{key: v})
			return err
		})
	})
	m.prompt.escape = func() tea.Cmd { m.envInfo = box; return nil }
	return nil
}
