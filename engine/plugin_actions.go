package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// pluginActions make plugin management ordinary actions, so the AI can find and install a plugin
// with approval like any other write.
func (l *Local) pluginActions() {
	reg := l.runner.Actions
	dir := l.App.Spec.Dir
	names := protocol.Schema{"type": "object", "required": []any{"names"}, "properties": map[string]any{
		"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}
	installer := func(ctx context.Context) *host.Installer {
		// policy already asked whoever had to agree, so the installer asks no one again
		return &host.Installer{Dir: dir, Registry: host.UserRegistry(), Yes: true, Out: jobs.Out(ctx)}
	}
	_ = reg.Add(actions.Action{Name: "plugin.search", Effect: protocol.EffectRead, Help: "plugins in the registry index whose name or description holds text",
		Args: protocol.Schema{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			ix, err := host.UserRegistry().Index(ctx, true)
			if err != nil {
				return nil, err
			}
			q := strings.ToLower(fmt.Sprint(args["text"]))
			if args["text"] == nil {
				q = ""
			}
			var lines []string
			for n, e := range ix.Plugins {
				if strings.Contains(strings.ToLower(n+" "+e.Description), q) {
					lines = append(lines, fmt.Sprintf("%s %s: %s", n, e.Latest(), e.Description))
				}
			}
			sort.Strings(lines)
			return &protocol.RunResult{Message: strings.Join(lines, "\n")}, nil
		}})
	add := func(name, help string, run func(ctx context.Context, names []string) error, plan func(ctx context.Context, names []string) string) {
		a := actions.Action{Name: name, Effect: protocol.EffectWrite, Help: help, Args: names,
			Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
				ns := strs(args["names"])
				if len(ns) == 0 {
					return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "names is required"}
				}
				if err := run(ctx, ns); err != nil {
					return nil, err
				}
				return &protocol.RunResult{Message: name + " " + strings.Join(ns, " ")}, nil
			}}
		if plan != nil {
			a.Plan = func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
				return &protocol.RunResult{Message: plan(ctx, strs(args["names"]))}, nil
			}
		}
		_ = reg.Add(a)
	}
	add("plugin.add", "install plugins and pin them in rig.lock", func(ctx context.Context, ns []string) error {
		return installer(ctx).Add(ctx, ns)
	}, func(ctx context.Context, ns []string) string {
		ix, err := host.UserRegistry().Index(ctx, false)
		if err != nil {
			return "install " + strings.Join(ns, " ")
		}
		var out []string
		for _, n := range ns {
			line := "install " + n
			if e := ix.Plugins[n]; e != nil {
				if v := e.Versions[e.Latest()]; v != nil {
					line += " " + e.Latest() + " with permissions " + v.Permissions.String()
				}
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	})
	add("plugin.rm", "unpin plugins from rig.lock", func(_ context.Context, ns []string) error { return installer(context.Background()).Remove(ns) }, nil)
	add("plugin.up", "upgrade plugins within their major version", func(ctx context.Context, ns []string) error { return installer(ctx).Up(ctx, ns, false) }, nil)
	for _, on := range []bool{true, false} {
		verb := map[bool]string{true: "on", false: "off"}[on]
		add("plugin."+verb, "turn plugins "+verb+" for this user (.rig/local.yaml)", func(_ context.Context, ns []string) error {
			for _, n := range ns {
				if err := spec.SetYAML(filepath.Join(dir, ".rig", "local.yaml"), []string{"plugins", n, "off"}, !on); err != nil {
					return err
				}
			}
			return nil
		}, nil)
	}
}
