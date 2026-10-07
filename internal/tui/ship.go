package tui

import (
	"context"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

// ship asks which of build, push and deploy to run for names, the image tag, and (when deploying)
// the environment's variables, such as the database names; then runs the chosen steps in order.
func ship(m *model, names []string) tea.Cmd {
	a := m.app
	if !a.ImageBased() {
		return m.act(label("deploy", names), true, func(ctx context.Context) error {
			return each(names, func(n string) error { return a.Deploy(ctx, n, "") })
		})
	}
	steps := []string{"build", "push", "deploy"}
	desc := []string{"build the images", "", "roll the tag out (env vars next)"}
	if r, ok := a.Runtime().(core.Registrar); ok && r.Registry() != "" {
		desc[1] = "push them to " + r.Registry()
	} else {
		steps, desc = []string{"build", "deploy"}, []string{desc[0], desc[2]}
	}
	m.pickMany(label("ship", names)+": space toggles a step, enter goes on", steps, desc, steps, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		tag, hint := a.DefaultTag(m.ctx, ""), "image tag"
		if !slices.Contains(chosen, "build") {
			st, _ := a.LoadState(m.ctx)
			tag, hint = st["tag"], "image tag (empty: the last built one)"
		}
		m.ask(label(strings.Join(chosen, "+"), names)+": "+hint, tag, func(tag string) tea.Cmd {
			tag = strings.TrimSpace(tag)
			if tag == "" && !slices.Equal(chosen, []string{"deploy"}) {
				m.setStatus("building and pushing need a tag", true)
				return nil
			}
			run := func(vars map[string]string) tea.Cmd { return runShip(m, names, chosen, tag, vars) }
			if !slices.Contains(chosen, "deploy") {
				return run(nil)
			}
			return pickDeployVars(m, map[string]string{}, run)
		})
		return nil
	})
	return nil
}

// pickDeployVars shows the variables a deploy fills in (rig.yaml's, or `rig vars set` ones);
// picking one changes it, the first row deploys with what is shown.
func pickDeployVars(m *model, changed map[string]string, run func(map[string]string) tea.Cmd) tea.Cmd {
	vars, _ := m.app.Vars(m.ctx)
	if len(vars) == 0 {
		return run(nil)
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	const go_ = "▶ go on with these"
	items, desc := []string{go_}, []string{"or pick a variable to change it for this and later deploys"}
	for _, k := range keys {
		v := vars[k].Value
		if c, ok := changed[k]; ok {
			v = c + sAmber.Render("  (new)")
			if c == "" {
				v = sAmber.Render("back to rig.yaml's")
			}
		}
		items, desc = append(items, k), append(desc, maskValue(k, v))
	}
	m.pick("environment variables of "+m.app.Env.Name, items, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		if c[0] == go_ {
			return run(changed)
		}
		k := c[0]
		cur, ok := changed[k]
		if !ok && !secretName.MatchString(k) {
			cur = vars[k].Value
		}
		m.ask("value of "+k+" (empty: rig.yaml's)", cur, func(v string) tea.Cmd {
			if v != vars[k].Value {
				changed[k] = v
			} else {
				delete(changed, k)
			}
			return pickDeployVars(m, changed, run)
		})
		return nil
	})
	return nil
}

func runShip(m *model, names, steps []string, tag string, vars map[string]string) tea.Cmd {
	a := m.app
	has := func(s string) bool { return slices.Contains(steps, s) }
	return m.act(label(strings.Join(steps, "+"), names)+tagNote(tag), true, func(ctx context.Context) error {
		if len(vars) > 0 {
			b, err := setVars(ctx, a, vars)
			if err != nil {
				return err
			}
			a = b
		}
		return each(names, func(n string) error {
			rt, s, err := a.Owner(n)
			if err != nil {
				return err
			}
			if s.Build == nil {
				if has("deploy") {
					return rt.Deploy(ctx, s, core.Release{})
				}
				return nil
			}
			var img string
			switch {
			case has("build") && has("push"):
				img, err = a.Build(ctx, s, tag, nil)
			case has("build"):
				img, err = a.BuildLocal(ctx, s, tag, nil)
			case has("push"):
				err = a.Push(ctx, s, tag, nil)
			}
			if err != nil || !has("deploy") {
				return err
			}
			if img == "" && tag != "" {
				img = a.TagImage(s, tag)
			}
			return rt.Deploy(ctx, s, core.Release{Image: img})
		})
	})
}

// setVars keeps vars in the environment's state and opens the environment again, since rig.yaml's
// ${VARS} are filled in when it is read; the UI switches to the new one.
func setVars(ctx context.Context, a *engine.App, vars map[string]string) (*engine.App, error) {
	kv := map[string]string{}
	for k, v := range vars {
		kv["var."+k] = v
	}
	if err := a.SetState(ctx, kv); err != nil {
		return nil, err
	}
	b, err := engine.Open(a.Spec.File, a.Env.Name)
	if err != nil {
		return nil, err
	}
	b.Confirmed = a.Confirmed
	if program != nil {
		program.Send(envMsg{app: b})
	}
	return b, nil
}
