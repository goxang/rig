// Package cli is rig's command line; every command is a thin layer over engine and the core interfaces.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/server"
	"github.com/goxang/rig/spec"
)

var Version = "dev"

type globals struct {
	file   string
	env    string
	yes    bool
	remote string
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
	// Pinned is the saved session rig opens on, Unpin forgets that choice.
	Pinned func(projectDir string) (id, env string)
	Unpin  func(projectDir string)
)

func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot()
	code := 0
	if err := root.ExecuteContext(ctx); err != nil {
		text := sh.WithHint(err.Error())
		fmt.Fprintln(os.Stderr, red("error: ")+text)
		if f := os.Getenv("RIG_ERROR_FILE"); f != "" {
			_ = os.WriteFile(f, []byte(text), 0o600) // the UI that ran this shows it after the terminal closes
		}
		code = 1
		var pe *protocol.Error
		switch {
		case errors.Is(err, engine.ErrFailed):
			code = 3
		case errors.Is(err, engine.ErrProtected), errors.As(err, &pe) && pe.Code == protocol.CodeDenied:
			code = 4
		case errors.As(err, &pe) && pe.Code == server.CodeFailed:
			code = 3
		}
	}
	if f := os.Getenv("RIG_DONE_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(strconv.Itoa(code)), 0o600) // a rig reopened after this task started reads how it ended
	}
	return code
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
			if UI != nil && Pinned != nil {
				if done, err := openPinned(cmd.Context(), fresh); done {
					return err
				}
			}
			remembered := false
			if g.env == "" && os.Getenv("RIG_ENV") == "" && UI != nil && !fresh {
				g.env = engine.LastEnv(g.file)
				remembered = g.env != ""
			}
			a, err := open()
			if err != nil && remembered {
				// the remembered environment is gone from rig.yaml: fall back to the default
				g.env = ""
				a, err = open()
			}
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
	root.Flags().BoolVar(&fresh, "fresh", false, "open the UI clean, on the project's default environment, instead of on the session S saved last (and stop opening on it)")
	root.PersistentFlags().StringVarP(&g.file, "file", "f", "", "project file (default: rig.yaml found from here up, or $RIG_FILE)")
	root.PersistentFlags().StringVarP(&g.env, "env", "e", "", "environment (default: $RIG_ENV, then the project's default; the UI opens on the one it last showed)")
	root.PersistentFlags().StringVar(&engine.NamespaceOverride, "namespace", "", "Kubernetes namespace for this run, instead of the environment's (rig ns switches it for good)")
	root.PersistentFlags().BoolVar(&brief, "brief", brief, "terse output for agents and scripts: no colour, tab-separated, long cells cut (or $RIG_BRIEF=1)")
	root.PersistentFlags().StringVarP(&g.remote, "remote", "r", os.Getenv("RIG_REMOTE"), "run on a rig server signed in to with rig login (or $RIG_REMOTE)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		setBrief(brief)
		return checkRemote(cmd)
	}
	root.PersistentFlags().BoolVarP(&g.yes, "yes", "y", os.Getenv("RIG_YES") != "", "confirm changes to a protected environment (or $RIG_YES)")

	root.AddGroup(&cobra.Group{ID: "svc", Title: "Services:"}, &cobra.Group{ID: "obs", Title: "Observe:"},
		&cobra.Group{ID: "data", Title: "Data & load:"}, &cobra.Group{ID: "infra", Title: "Project:"}, &cobra.Group{ID: "team", Title: "Server, access & automation:"},
		&cobra.Group{ID: "tool", Title: "The tool itself:"})
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
	for _, c := range append(append(projectCommands(), pluginCommand(), trustCommand(), doctorCommand(), purgeCommand(), kubeconfigCommand(), whyCommand(), inboxCommand(), agentCommand(), serveCommand(), loginCommand(), logoutCommand(), whoamiCommand(), adminCommand(), callCommand(), docsCommand(), schemaCommand(), skillCommand(), themeCommand()), lockCommands()...) {
		c.GroupID = "infra"
		root.AddCommand(c)
	}
	for _, c := range root.Commands() {
		if g, ok := commandGroups[c.Name()]; ok {
			c.GroupID = g
		}
	}
	cobra.AddTemplateFunc("brief", briefShort)
	root.SetUsageTemplate(strings.ReplaceAll(root.UsageTemplate(), "{{.Short}}", "{{brief .Short}}"))
	addCompletions(root)
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		if cmd == root {
			printProjectHelp()
		}
	})
	return root
}

var fresh bool

// openPinned opens the UI on the session S saved last, when there is one for this environment;
// --fresh forgets it instead. done says the UI ran (or failed to start).
func openPinned(ctx context.Context, fresh bool) (done bool, err error) {
	file := g.file
	if file == "" {
		if file, err = spec.Find("."); err != nil {
			return false, nil
		}
	}
	dir := filepath.Dir(file)
	if fresh {
		Unpin(dir)
		return false, nil
	}
	id, env := Pinned(dir)
	if id == "" || g.env != "" && g.env != env || os.Getenv("RIG_ENV") != "" && os.Getenv("RIG_ENV") != env {
		return false, nil
	}
	fmt.Fprintln(os.Stderr, dim("opening session "+id+" (rig --fresh starts clean)"))
	return true, Resume(ctx, func(e string) (*engine.App, error) {
		if g.env == "" {
			g.env = e
		}
		return open()
	}, dir, id)
}

// printProjectHelp shows the current project's own rig.yaml `help:` text, if it set one —
// a project's quick-start for its main CLI workflows, right under the generic command list.
func printProjectHelp() {
	file, err := spec.Find(".")
	if g.file != "" {
		file = g.file
		err = nil
	}
	if err != nil {
		return
	}
	proj, _, err := spec.Load(file, g.env)
	if err != nil || proj.Help == "" {
		return
	}
	fmt.Printf("\n%s:\n%s\n", bold(proj.Name), proj.Help)
}

func open() (*engine.App, error) {
	ensureOfficial()
	a, err := engine.Open(g.file, g.env)
	if err != nil {
		return nil, err
	}
	if changed, err := ensurePlugins(a.Spec.Dir); err != nil || changed {
		a.Close()
		if err != nil {
			return nil, err
		}
		if a, err = engine.Open(g.file, g.env); err != nil {
			return nil, err
		}
	}
	a.Confirmed = g.yes
	if a.Inferred {
		fmt.Fprintln(os.Stderr, dim("no rig.yaml: running config inferred from this directory (rig init writes it, rig init --dry-run shows it)"))
	}
	// a person at a terminal answers a protected environment's question instead of needing --yes;
	// a task's nested rig inherits the answer as RIG_YES
	if !g.yes && os.Getenv("RIG_TASK") == "" && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) {
		a.Ask = askYes
	}
	return a, nil
}

// runEcho runs action through the Engine, its output on stdout as it comes.
func runEcho(ctx context.Context, a *engine.App, action string, args protocol.Args) error {
	var jm jobs.Manager
	ctx, j := jm.Start(ctx, action)
	j.Echo = os.Stdout
	e := engine.NewLocal(a, &jm)
	defer e.Close()
	_, _, err := e.Run(ctx, action, args, api.RunOpts{})
	j.Finish(err)
	return err
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
			return errors.New("no environment: define one under environments: and set default")
		}
		ctx := cmd.Context()
		if g.yes {
			ctx = core.WithConfirmed(ctx)
		}
		return f(ctx, a, args)
	}
}

var commandGroups = map[string]string{
	"serve": "team", "login": "team", "logout": "team", "whoami": "team", "admin": "team", "inbox": "team",
	"agent": "team", "lock": "team", "unlock": "team", "call": "team", "mcp": "team",
	"plugin": "tool", "trust": "tool", "purge": "tool", "uninstall": "tool", "upgrade": "tool", "version": "tool",
	"theme": "tool", "skill": "tool", "docs": "tool", "schema": "tool", "migrate": "tool",
}

// briefShort is a command's description cut to its first clause, so the list stays one screen;
// `rig <command> -h` has the rest.
func briefShort(s string) string {
	if i := strings.Index(s, "; "); i >= 20 {
		s = s[:i]
	}
	if len(s) > 64 {
		s = s[:strings.LastIndex(s[:64], " ")]
		if i := strings.LastIndex(s, " ("); i > 0 && !strings.Contains(s[i:], ")") {
			s = s[:i]
		}
		s = strings.TrimRight(s, ",:") + "…"
	}
	return s
}
