package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/ai/agents"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
)

func agentCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "agent",
		Short: "the rig.yaml agents: list them, run one now, or watch their events and schedules",
	}
	c.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "list the agents with their triggers and tools",
		Args:  cobra.NoArgs,
		RunE: withApp(func(_ context.Context, a *engine.App, _ []string) error {
			var rows [][]string
			for _, n := range sortedAgents(a) {
				ag := a.Spec.Agents[n]
				on := ag.On.Event
				if ag.On.Schedule != "" {
					on = strings.TrimSpace(on + " cron(" + ag.On.Schedule + ")")
				}
				rows = append(rows, []string{n, on, strings.Join(ag.Tools, ","), strings.Join(ag.Approve, ",")})
			}
			if len(rows) == 0 {
				fmt.Println(dim("no agents: add an agents: section to rig.yaml"))
				return nil
			}
			printTable(os.Stdout, []string{"AGENT", "ON", "TOOLS", "APPROVE"}, rows)
			return nil
		}),
	})
	c.AddCommand(&cobra.Command{
		Use:   "run <name>",
		Short: "run an agent once now",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			return runAgent(ctx, a, args[0], nil)
		}),
	})
	c.AddCommand(&cobra.Command{
		Use:   "watch",
		Short: "run each agent whenever its event arrives or its schedule comes due, until stopped",
		Args:  cobra.NoArgs,
		RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
			e := engine.NewLocal(a, &jobs.Manager{})
			defer e.Close()
			fmt.Println(dim(fmt.Sprintf("watching %d agents; writes they need approved wait in rig inbox", len(a.Spec.Agents))))
			return watchAgents(ctx, a, e)
		}),
	})
	return c
}

// watchAgents runs each agent whenever its event arrives on e or its schedule comes due.
func watchAgents(ctx context.Context, a *engine.App, e *engine.Local) error {
	var busy sync.Map
	return agents.Watch(ctx, e, a.Spec.Agents, func(name string, ev *protocol.Event) {
		// ponytail: a firing while the agent still runs is dropped, queue them if agents miss events
		if _, running := busy.LoadOrStore(name, true); running {
			return
		}
		go func() {
			defer busy.Delete(name)
			if err := runAgent(ctx, a, name, ev); err != nil {
				fmt.Fprintln(os.Stderr, red("✗")+" agent "+name+": "+err.Error())
			}
		}()
	})
}

func runAgent(ctx context.Context, a *engine.App, name string, ev *protocol.Event) error {
	r, ag, err := a.Agent(name)
	if err != nil {
		return err
	}
	s := ai.NewSession(a.AIDir(), r.Setup.Backend, a.Env.Name)
	return r.Turn(ctx, s, agents.Prompt(ag, ev), "", func(e ai.Event) {
		switch e.Kind {
		case "text":
			fmt.Println(bold(name) + ": " + e.Text)
		case "tool":
			fmt.Println(dim(name + " → " + e.Text))
		}
	})
}

func sortedAgents(a *engine.App) []string {
	var out []string
	for n := range a.Spec.Agents {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
