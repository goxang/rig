package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
)

// infraCommand manages infrastructure where it runs: in the environment named by `infra:` when the
// current one borrows it, else in the current environment.
func infraCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "infra",
		Short: "infrastructure (databases, brokers, ...): up once, shared by local, docker and kind",
		Long: `Infrastructure is every service with role: infra. An environment with infra: <env> borrows
the shared ones from that environment, so one SQL Server serves local processes, docker and kind.
rig up never redeploys infrastructure that already runs; these commands are how it changes.`,
	}
	// borrowed is set when the current environment takes its shared services from another one
	var borrowed []string
	where := func(a *engine.App) (*engine.App, error) {
		ia, err := a.InfraApp()
		if err != nil || ia == nil {
			return a, err
		}
		ia.Confirmed = a.Confirmed
		borrowed = nil
		for _, n := range a.Spec.ServiceNames() {
			if a.SharedElsewhere(n) && !a.Spec.Services[n].Manual {
				borrowed = append(borrowed, n)
			}
		}
		return ia, nil
	}
	targets := func(args []string) []string {
		switch {
		case len(args) > 0:
			return args
		case len(borrowed) > 0:
			return borrowed
		}
		return []string{"infra"}
	}
	up := &cobra.Command{Use: "up [service...]", Short: "start infrastructure that is not running", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
		ia, err := where(a)
		if err != nil {
			return err
		}
		fmt.Printf("%s on %s\n", bold("rig infra up"), envLabel(ia))
		return ia.Up(ctx, targets(args), engine.UpOptions{NoDeps: true, Out: os.Stdout})
	})}
	down := &cobra.Command{Use: "down [service...]", Short: "stop infrastructure (data stays in its volumes)", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
		ia, err := where(a)
		if err != nil {
			return err
		}
		return ia.Down(ctx, targets(args), os.Stdout)
	})}
	restart := &cobra.Command{Use: "restart <service...>", Args: cobra.MinimumNArgs(1), Short: "restart infrastructure services", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
		ia, err := where(a)
		if err != nil {
			return err
		}
		if err := ia.Guard(); err != nil {
			return err
		}
		names, err := ia.Targets(args, false)
		if err != nil {
			return err
		}
		return parallelNames(names, func(n string) error { return ia.Restart(ctx, n) })
	})}
	status := &cobra.Command{Use: "status", Aliases: []string{"ls"}, Short: "infrastructure and where it runs", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
		ia, err := where(a)
		if err != nil {
			return err
		}
		names, err := ia.Targets(targets(args), false)
		if err != nil {
			return err
		}
		fmt.Printf("%s  %s\n\n", bold("infrastructure"), envLabel(ia))
		var rows [][]string
		for _, st := range ia.StatusAll(ctx, names) {
			s := ia.Spec.Services[st.Service]
			shared := ""
			if s.Shared {
				shared = "shared"
			}
			rows = append(rows, []string{st.Service, stateText(st.State), fmt.Sprintf("%d/%d", st.Ready, st.Desired), shared, shortImage(st.Image), st.Message})
		}
		printTable(os.Stdout, []string{"SERVICE", "STATE", "READY", "", "IMAGE", "MESSAGE"}, rows)
		return nil
	})}
	cmd.AddCommand(up, down, restart, status)
	return cmd
}
