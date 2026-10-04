// Package cli is rig's command line; every command is a thin layer over engine and the core interfaces.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

var Version = "dev"

type globals struct {
	file string
	env  string
	yes  bool
}

var g globals

// UI opens the terminal UI; set by main so the cli package does not import the TUI.
var UI func(ctx context.Context, a *engine.App) error

// Resume opens a saved UI session (S in the UI); Sessions lists them. Set by main, like UI.
var (
	Resume   func(ctx context.Context, open func(env string) (*engine.App, error), projectDir, id string) error
	Sessions func(projectDir string) ([][]string, error)
	// CloseUISession forgets a saved UI session.
	CloseUISession func(projectDir, id string) error
)

func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
		if errors.Is(err, engine.ErrProtected) {
			return 3
		}
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "rig",
		Short: "one control plane for your services and their infrastructure: local, docker, kubernetes",
		Long: `rig runs, watches and tunes a project's services in any environment from one rig.yaml:
start/stop/scale/deploy, logs, metrics, traces, profiles, debuggers, databases, caches, queues,
load generators, manifests and hosts. Every part is an adapter you can swap or add.

Run rig with no arguments for the terminal UI.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := open()
			if err != nil {
				return err
			}
			defer a.Close()
			if UI == nil {
				return cmd.Help()
			}
			return UI(cmd.Context(), a)
		},
	}
	root.PersistentFlags().StringVarP(&g.file, "file", "f", "", "project file (default: rig.yaml found from here up, or $RIG_FILE)")
	root.PersistentFlags().StringVarP(&g.env, "env", "e", "", "environment (default: $RIG_ENV, then the project's default)")
	root.PersistentFlags().StringVar(&engine.NamespaceOverride, "namespace", "", "Kubernetes namespace for this run, instead of the environment's (rig ns switches it for good)")
	root.PersistentFlags().BoolVar(&brief, "brief", brief, "terse output for agents and scripts: no colour, tab-separated, long cells cut (or $RIG_BRIEF=1)")
	root.PersistentPreRun = func(*cobra.Command, []string) { setBrief(brief) }
	root.PersistentFlags().BoolVarP(&g.yes, "yes", "y", os.Getenv("RIG_YES") != "", "confirm changes to a protected environment (or $RIG_YES)")

	root.AddGroup(&cobra.Group{ID: "svc", Title: "Services:"}, &cobra.Group{ID: "obs", Title: "Observe:"},
		&cobra.Group{ID: "data", Title: "Data & load:"}, &cobra.Group{ID: "infra", Title: "Project & infrastructure:"})
	for _, c := range serviceCommands() {
		c.GroupID = "svc"
		root.AddCommand(c)
	}
	for _, c := range observeCommands() {
		c.GroupID = "obs"
		root.AddCommand(c)
	}
	for _, c := range dataCommands() {
		c.GroupID = "data"
		root.AddCommand(c)
	}
	t := testCommand()
	t.GroupID = "obs"
	r := reportCommand()
	r.GroupID = "obs"
	root.AddCommand(t, r)
	root.AddCommand(resumeCommand())
	ac := aiCommand()
	ac.GroupID = "obs"
	root.AddCommand(ac)
	for _, c := range projectCommands() {
		c.GroupID = "infra"
		root.AddCommand(c)
	}
	return root
}

func open() (*engine.App, error) {
	a, err := engine.Open(g.file, g.env)
	if err != nil {
		if errors.Is(err, spec.ErrNotFound) {
			return nil, err
		}
		return nil, err
	}
	a.Confirmed = g.yes
	return a, nil
}

// withApp wraps a command body that needs the project and environment.
func withApp(f func(ctx context.Context, a *engine.App, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a, err := open()
		if err != nil {
			return err
		}
		defer a.Close()
		if a.Env == nil {
			return errors.New("no environment: define one under environments: and set default:")
		}
		ctx := cmd.Context()
		if g.yes {
			ctx = core.WithConfirmed(ctx)
		}
		return f(ctx, a, args)
	}
}
