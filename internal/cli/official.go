package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/goxang/rig/host"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

// ensureOfficial installs, for this user, the official plugins that the project's component types and
// imports name and nothing installed provides, so a fresh machine needs no go install. Failures are
// warnings: the load that follows says what is still missing.
func ensureOfficial() {
	file := g.file
	if file == "" {
		f, err := spec.Find(".")
		if err != nil {
			return
		}
		file = f
	}
	p, _, err := spec.Load(file, g.env)
	if err != nil {
		return
	}
	types := map[string]*spec.Component{}
	add := func(c *spec.Component) {
		if c != nil && c.Type != "" {
			types[c.Type] = c
		}
	}
	for _, c := range p.Components {
		add(c)
	}
	for _, e := range p.Environments {
		add(e.Runtime)
		for _, c := range e.Components {
			add(c)
		}
	}
	installed := map[string]bool{}
	ms, _ := host.Discover(p.Dir, p.Plugins)
	for _, m := range ms {
		installed[m.Name] = true
	}
	var need []string
	for t, c := range types {
		if _, err := plugin.Resolve(c); err != nil && !installed[t] {
			need = append(need, t)
		}
	}
	if len(need) == 0 && len(p.NoImporter) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ix, err := host.UserRegistry().Index(ctx, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, amber("official plugins: ")+err.Error())
		return
	}
	var pick []string
	official := func(n string) bool { e := ix.Plugins[n]; return e != nil && e.Publisher == "goxang" && !installed[n] }
	for _, n := range need {
		if official(n) {
			pick = append(pick, n)
		}
	}
	for _, kind := range p.NoImporter {
		for n, e := range ix.Plugins {
			if ims, _ := e.Provides["importers"].([]any); slices.Contains(ims, any(kind)) && official(n) {
				pick = append(pick, n)
			}
		}
	}
	if len(pick) == 0 {
		return
	}
	slices.Sort(pick)
	pick = slices.Compact(pick)
	fmt.Fprintln(os.Stderr, dim("rig: installing official plugins: "+strings.Join(pick, ", ")))
	in := &host.Installer{Dir: host.ConfigDir(), Registry: host.UserRegistry(), Yes: true, Out: os.Stderr}
	if err := in.Add(ctx, pick); err != nil {
		fmt.Fprintln(os.Stderr, amber("official plugins: ")+err.Error())
	}
}
