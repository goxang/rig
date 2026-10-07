package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

// pickShipServices lets the services to ship be changed first, names marked, one tab per section.
func pickShipServices(m *model, names []string) tea.Cmd {
	sp := m.app.Spec
	all := sp.ServiceNames()
	sections := sp.SectionMap()
	groups := []pickGroup{{name: "all"}}
	for _, n := range append(slices.Clone(sp.SectionOrder), "other", "infra") {
		g := pickGroup{name: n, items: map[string]bool{}}
		for _, svc := range all {
			s := sp.Services[svc]
			sec, ok := sections[svc]
			switch {
			case s.Role == spec.RoleInfra:
				sec = "infra"
			case !ok:
				sec = "other"
			}
			if sec == n {
				g.items[svc] = true
			}
		}
		if len(g.items) > 0 {
			groups = append(groups, g)
		}
	}
	desc := make([]string, len(all))
	for i, n := range all {
		desc[i] = strings.Join(sp.Services[n].Groups, ",")
	}
	m.pickMany("ship: space marks a service, enter goes on", all, desc, names, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		return ship(m, chosen)
	})
	if len(groups) > 2 {
		m.picker.groups = groups
		if len(names) > 0 {
			for i, g := range groups[1:] {
				if g.items[names[0]] {
					m.picker.group = i + 1
				}
			}
		}
	}
	return nil
}

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
			if !slices.Contains(chosen, "deploy") {
				return runShip(m, names, chosen, tag, nil, nil)
			}
			plan := &shipPlan{names: names, steps: chosen, tag: tag, env: map[string]map[string]string{}}
			return plan.env_(m, 0)
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

// shipPlan is a ship being set up: which services, steps and tag, and the env changes per service
// that the deploy takes.
type shipPlan struct {
	names, steps []string
	tag          string
	env          map[string]map[string]string
	vars         map[string]string
}

// env_ shows the env service i of the plan deploys with (on Kubernetes its live workload's), to
// change any before the deploy; the last service's "go on" ships.
func (p *shipPlan) env_(m *model, i int) tea.Cmd {
	if i >= len(p.names) {
		return runShip(m, p.names, p.steps, p.tag, p.vars, p.env)
	}
	a, ctx, name := m.app, m.ctx, p.names[i]
	m.setStatus("reading the env of "+name+"…", false)
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		env, over, err := a.ServiceEnv(c, name)
		return thenMsg(func() tea.Cmd {
			m.setStatus("", false)
			if err != nil {
				m.setStatus("env of "+name+": "+err.Error()+" (shipping without changes)", true)
				return p.env_(m, i+1)
			}
			return p.pickEnv(m, i, env, over)
		})
	}
}

func (p *shipPlan) pickEnv(m *model, i int, env map[string]string, over map[string]bool) tea.Cmd {
	name := p.names[i]
	changed := p.env[name]
	if changed == nil {
		changed = map[string]string{}
		p.env[name] = changed
	}
	goOn := "▶ ship with these"
	if i < len(p.names)-1 {
		goOn = "▶ next: the env of " + p.names[i+1]
	}
	const add, runtimeVars = "+ new variable", "⚙ environment variables of the deploy"
	items, desc := []string{goOn, add}, []string{"or pick a variable to change it for this and later deploys", "NAME=value"}
	if vars, _ := m.app.Vars(m.ctx); len(vars) > 0 {
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		sort.Strings(names)
		items, desc = append(items, runtimeVars), append(desc, truncate(strings.Join(names, ", "), 70))
	}
	keys := make([]string, 0, len(env)+len(changed))
	for k := range env {
		keys = append(keys, k)
	}
	for k := range changed {
		if _, ok := env[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := maskValue(k, env[k])
		switch c, ok := changed[k]; {
		case ok && c == "":
			v = sAmber.Render("removed (back to the manifest's)")
		case ok:
			v = maskValue(k, c) + sAmber.Render("  (new)")
		case over[k]:
			v += sDim.Render("  (set with rig setenv)")
		}
		items, desc = append(items, k), append(desc, truncate(v, 80))
	}
	title := fmt.Sprintf("env of %s on %s", name, m.app.Env.Name)
	if len(p.names) > 1 {
		title += fmt.Sprintf(" (%d of %d)", i+1, len(p.names))
	}
	m.pick(title, items, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		again := func() tea.Cmd { return p.pickEnv(m, i, env, over) }
		switch k := c[0]; k {
		case goOn:
			return p.env_(m, i+1)
		case runtimeVars:
			if p.vars == nil {
				p.vars = map[string]string{}
			}
			return pickDeployVars(m, p.vars, func(map[string]string) tea.Cmd { return again() })
		case add:
			m.ask("new variable of "+name+" (NAME=value)", "", func(v string) tea.Cmd {
				if k, val, ok := strings.Cut(strings.TrimSpace(v), "="); ok && k != "" {
					changed[k] = val
				}
				return again()
			})
		default:
			cur, ok := changed[k]
			if !ok && !secretName.MatchString(k) {
				cur = env[k]
			}
			m.ask(k+" of "+name+" (empty removes it)", cur, func(v string) tea.Cmd {
				if v == env[k] {
					delete(changed, k)
				} else {
					changed[k] = v
				}
				return again()
			})
		}
		return nil
	})
	return nil
}

func runShip(m *model, names, steps []string, tag string, vars map[string]string, env map[string]map[string]string) tea.Cmd {
	a := m.app
	has := func(s string) bool { return slices.Contains(steps, s) }
	return m.act(label(strings.Join(steps, "+"), names)+tagNote(tag), true, func(ctx context.Context) error {
		out := jobOut(ctx)
		if len(vars) > 0 {
			fmt.Fprintf(out, "→ environment variables: %s\n", strings.Join(sortedKeys(vars), ", "))
			b, err := setVars(ctx, a, vars)
			if err != nil {
				return err
			}
			a = b
		}
		for n, kv := range env {
			if len(kv) == 0 {
				continue
			}
			fmt.Fprintf(out, "→ %s: env %s\n", n, strings.Join(sortedKeys(kv), ", "))
			if err := a.KeepEnv(ctx, []string{n}, kv); err != nil {
				return err
			}
		}
		return each(names, func(n string) error {
			rt, s, err := a.Owner(n)
			if err != nil {
				return err
			}
			if s.Build == nil {
				if has("deploy") {
					fmt.Fprintf(out, "→ %s: deploy\n", n)
					return rt.Deploy(ctx, s, core.Release{})
				}
				return nil
			}
			var img string
			switch {
			case has("build") && has("push"):
				fmt.Fprintf(out, "→ %s: build and push %s\n", n, tag)
				img, err = a.Build(ctx, s, tag, out)
			case has("build"):
				fmt.Fprintf(out, "→ %s: build %s\n", n, tag)
				img, err = a.BuildLocal(ctx, s, tag, out)
			case has("push"):
				fmt.Fprintf(out, "→ %s: push %s\n", n, tag)
				err = a.Push(ctx, s, tag, out)
			}
			if err != nil || !has("deploy") {
				return err
			}
			if img == "" && tag != "" {
				img = a.TagImage(s, tag)
			}
			fmt.Fprintf(out, "→ %s: deploy %s\n", n, img)
			if err := rt.Deploy(ctx, s, core.Release{Image: img}); err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ %s\n", n)
			return nil
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
