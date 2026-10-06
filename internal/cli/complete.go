package cli

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

// completeProject loads rig.yaml for shell completion; nil when there is none. It never opens the
// runtime, so a tab press stays instant.
func completeProject() *spec.Project {
	file := g.file
	if file == "" {
		f, err := spec.Find(".")
		if err != nil {
			return nil
		}
		file = f
	}
	p, _, err := spec.Load(file, g.env)
	if err != nil {
		return nil
	}
	return p
}

const noFiles = cobra.ShellCompDirectiveNoFileComp

func completeEnvs(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	p := completeProject()
	if p == nil {
		return nil, noFiles
	}
	var out []string
	for _, n := range p.EnvironmentNames() {
		out = append(out, n+"\t"+p.Environments[n].Description)
	}
	return out, noFiles
}

// servicesAndGroups are what a [service|group...] argument takes.
func servicesAndGroups(p *spec.Project) []string {
	groups := map[string]bool{"all": true}
	var out []string
	for _, n := range p.ServiceNames() {
		s := p.Services[n]
		out = append(out, n+"\t"+s.Role)
		for _, g := range s.Groups {
			groups[g] = true
		}
	}
	var gs []string
	for g := range groups {
		if p.Services[g] == nil {
			gs = append(gs, g+"\tgroup")
		}
	}
	sort.Strings(gs)
	return append(gs, out...)
}

func completeServices(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	p := completeProject()
	if p == nil {
		return nil, noFiles
	}
	return servicesAndGroups(p), noFiles
}

func completeComponents(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	p := completeProject()
	if p == nil || len(args) > 0 {
		return nil, noFiles
	}
	var out []string
	for n, c := range p.Components {
		out = append(out, n+"\t"+c.Kind+" "+c.Type)
	}
	if e := p.Environments[g.env]; e != nil {
		for n, c := range e.Components {
			out = append(out, n+"\t"+c.Kind+" "+c.Type)
		}
	}
	sort.Strings(out)
	return out, noFiles
}

// completeTask offers the task names, then the task's args: name= for each declared one not given
// yet, and the choices of the positional one being typed.
func completeTask(_ *cobra.Command, args []string, typed string) ([]string, cobra.ShellCompDirective) {
	p := completeProject()
	if p == nil {
		return nil, noFiles
	}
	a := &engine.App{Spec: p, Env: p.Environments[g.env]}
	if len(args) == 0 {
		var out []string
		for _, n := range a.TaskNames() {
			help, _, _ := strings.Cut(a.TaskHelp(n), "\n")
			out = append(out, n+"\t"+help)
		}
		return out, noFiles
	}
	t, ok := a.Tasks()[args[0]]
	if !ok {
		return nil, noFiles
	}
	given := map[string]bool{}
	positional := 0
	for _, w := range args[1:] {
		if k, _, ok := strings.Cut(w, "="); ok {
			given[k] = true
		} else {
			positional++
		}
	}
	var out []string
	if k, v, ok := strings.Cut(typed, "="); ok {
		for _, arg := range t.Args {
			if arg.Name == k {
				for _, c := range taskArgChoices(a, arg) {
					if strings.HasPrefix(c, v) {
						out = append(out, k+"="+c)
					}
				}
			}
		}
		return out, noFiles
	}
	for _, arg := range t.Args {
		if !given[arg.Name] {
			out = append(out, arg.Name+"=\t"+arg.Help)
		}
	}
	n := 0
	for _, arg := range t.Args {
		if arg.Env() {
			continue
		}
		if n == positional || (arg.Multi && n <= positional) {
			for _, c := range taskArgChoices(a, arg) {
				if c != "" {
					out = append(out, c)
				}
			}
			break
		}
		n++
	}
	// name= wants its value right after it: no space, when only such names are left to complete
	var match []string
	names := true
	for _, o := range out {
		if strings.HasPrefix(o, typed) {
			match = append(match, o)
			v, _, _ := strings.Cut(o, "\t")
			names = names && strings.HasSuffix(v, "=")
		}
	}
	if names && len(match) > 0 {
		return match, noFiles | cobra.ShellCompDirectiveNoSpace
	}
	return match, noFiles
}

// taskArgChoices are an arg's choices without reaching the environment (hosts need its runtime).
func taskArgChoices(a *engine.App, arg spec.TaskArg) []string {
	if arg.From == "hosts" {
		return nil
	}
	return a.TaskArgChoices(context.Background(), arg)
}

// addCompletions gives the commands that take services, components or tasks their shell completion.
func addCompletions(root *cobra.Command) {
	_ = root.RegisterFlagCompletionFunc("env", completeEnvs)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.ValidArgsFunction != nil {
			return
		}
		use := c.Use
		switch {
		case c.Name() == "task" && c.Parent() == root:
			c.ValidArgsFunction = completeTask
		case strings.Contains(use, "<service") || strings.Contains(use, "[service"):
			c.ValidArgsFunction = completeServices
		case strings.Contains(use, "[component") || strings.Contains(use, "<component"):
			c.ValidArgsFunction = completeComponents
		}
	}
	walk(root)
}
