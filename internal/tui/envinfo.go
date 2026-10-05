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
	gen  int
	text string
}

// secretName is a name whose value the ctrl+e box hides.
var secretName = regexp.MustCompile(`(?i)pass|secret|token|login|credential|private|apikey|api_key`)

func maskValue(name, value string) string {
	if value != "" && secretName.MatchString(name) {
		return sDim.Render("•••• (set)")
	}
	return value
}

// fetchEnvInfo builds the ctrl+e box: the environment, the variables tasks and manifests get (with
// `rig vars set` overrides, read from the environment's state), and where each component points.
func (m *model) fetchEnvInfo() tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	m.setStatus("reading "+a.Env.Name+"…", false)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return envInfoMsg{gen: gen, text: envInfoText(c, a)}
	}
}

func envInfoText(ctx context.Context, a *engine.App) string {
	var b strings.Builder
	row := func(k, v, from string) {
		b.WriteString("  " + sKey.Render(padRight(k, 22)) + " " + v)
		if from != "" {
			b.WriteString(sDim.Render("  " + from))
		}
		b.WriteString("\n")
	}
	section := func(s string) { b.WriteString("\n" + sTitle.Render(s) + "\n") }

	env := a.Env
	row("environment", env.Name, env.Description)
	row("runtime", env.Runtime.Type, "")
	if ns := a.Namespace(); ns != "" {
		row("namespace", ns, "")
	}
	if env.Protected {
		row("protected", sRed.Render("yes"), "every change asks")
	}
	vars, err := a.Vars(ctx)
	if len(vars) > 0 || err != nil {
		section("manifest and task variables")
		for _, k := range engine.SortedKeys(vars) {
			row(k, maskValue(k, vars[k].Value), vars[k].From)
		}
		if err != nil {
			b.WriteString("  " + sRed.Render("state: "+err.Error()) + "\n")
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
			row(k, maskValue(k, project[k]), from)
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
			row(n, comp.Type+"  "+strings.Join(parts, " "), "")
		}
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).
		Render(sTitle.Render("rig — "+env.Name) + "\n" + strings.TrimRight(b.String(), "\n") + "\n\n" + sDim.Render("y copies · any key closes"))
}
