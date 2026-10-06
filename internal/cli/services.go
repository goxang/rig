package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

func serviceCommands() []*cobra.Command {
	var up engine.UpOptions
	upCmd := &cobra.Command{
		Use:   "up [service|group...]",
		Short: "build (with --build) and deploy services and their dependencies, in dependency order",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			up.Out = os.Stdout
			fmt.Printf("%s %s on %s\n", bold("rig up"), strings.Join(args, " "), envLabel(a))
			return a.Up(ctx, args, up)
		}),
	}
	upCmd.Flags().BoolVarP(&up.Build, "build", "b", false, "build images first (container runtimes)")
	upCmd.Flags().StringVarP(&up.Tag, "tag", "t", "", "image tag: what --build produces (default: <branch>-<date>-<time>), or without --build the tag to deploy")
	upCmd.Flags().StringVar(&up.Ref, "ref", "", "with --build, build from this git branch, tag or commit instead of the working tree")
	upCmd.Flags().BoolVar(&up.NoDeps, "no-deps", false, "do not bring up dependencies")
	upCmd.Flags().DurationVar(&up.Wait, "wait", 3*time.Minute, "how long each phase may take to become ready")
	upCmd.Flags().BoolVar(&up.DryRun, "dry-run", false, "print the phases and what each service would get, change nothing")
	upCmd.Flags().DurationVar(&up.Settle, "settle", 20*time.Second, "after the last phase, how long to watch for services that crash once ready (0 skips)")

	down := &cobra.Command{
		Use:   "down [service|group...]",
		Short: "stop services in reverse dependency order (every app when none given; infra only when named)",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			return a.Down(ctx, args, os.Stdout)
		}),
	}

	each := func(use, short string, f func(ctx context.Context, a *engine.App, s string) error) *cobra.Command {
		return &cobra.Command{
			Use: use + " <service|group...>", Short: short, Args: cobra.MinimumNArgs(1),
			RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
				if err := a.Guard(); err != nil {
					return err
				}
				names, err := a.Targets(args, false)
				if err != nil {
					return err
				}
				return parallelNames(names, func(n string) error {
					if err := f(ctx, a, n); err != nil {
						return fmt.Errorf("%s: %w", n, err)
					}
					fmt.Printf("  %s %s\n", green("✓"), n)
					return nil
				})
			}),
		}
	}
	start := each("start", "start services (deploying what is not there yet)", func(ctx context.Context, a *engine.App, n string) error {
		return a.Start(ctx, n)
	})
	stop := each("stop", "stop services, keeping their definitions", func(ctx context.Context, a *engine.App, n string) error {
		return a.Stop(ctx, n)
	})
	restart := each("restart", "restart services (e.g. to reload their configuration)", func(ctx context.Context, a *engine.App, n string) error {
		return a.Restart(ctx, n)
	})

	var tag, ref string
	var buildFirst bool
	deploy := &cobra.Command{
		Use:   "deploy <service|group...>",
		Short: "roll out services: --tag deploys that image tag from the registry, --build builds a fresh one first",
		Example: `  rig deploy app -t master-20261003     # every app at an existing tag, infra untouched
  rig deploy api --build             # build from the working tree, push, roll out
  rig deploy core --build --ref master  # build branch master, push, roll out`,
		Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			if buildFirst && tag == "" {
				tag = a.DefaultTag(ctx, ref)
			}
			err = parallelNames(names, func(n string) error {
				s := a.Spec.Services[n]
				if buildFirst && s.Build != nil && a.ImageBased() {
					img, err := a.BuildFrom(ctx, s, tag, ref, os.Stdout)
					if err != nil {
						return err
					}
					rt, svc, err := a.Owner(n)
					if err != nil {
						return err
					}
					if err := rt.Deploy(ctx, svc, core.Release{Image: img}); err != nil {
						return fmt.Errorf("%s: %w", n, err)
					}
				} else if err := a.Deploy(ctx, n, tag); err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
				fmt.Printf("  %s %s deployed\n", green("✓"), n)
				return nil
			})
			if err != nil {
				return err
			}
			if tag != "" {
				return a.SetState(ctx, map[string]string{"tag": tag})
			}
			return nil
		}),
	}
	deploy.Flags().BoolVarP(&buildFirst, "build", "b", false, "build the image first")
	deploy.Flags().StringVarP(&tag, "tag", "t", "", "image tag to deploy (or to build, with --build)")
	deploy.Flags().StringVar(&ref, "ref", "", "with --build, build from this git branch, tag or commit")

	build := &cobra.Command{
		Use:   "build <service|group...>",
		Short: "build images and push them to the environment's registry (an existing tag is overwritten only with --yes)",
		Args:  cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			if tag == "" {
				tag = a.DefaultTag(ctx, ref)
			}
			err = parallelNames(names, func(n string) error {
				if a.Spec.Services[n].Build == nil {
					return nil
				}
				_, err := a.BuildFrom(ctx, a.Spec.Services[n], tag, ref, os.Stdout)
				return err
			})
			if err == nil {
				fmt.Printf("tag %s\n", bold(tag))
			}
			return err
		}),
	}
	build.Flags().StringVarP(&tag, "tag", "t", "", "image tag (default: <branch>-<date>-<time>)")
	build.Flags().StringVar(&ref, "ref", "", "build from this git branch, tag or commit instead of the working tree")

	scale := &cobra.Command{
		Use:   "scale <service|group...> <replicas|+n|-n>",
		Short: "set replica counts, or move them up or down: rig scale core +1",
		Args:  cobra.MinimumNArgs(2),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			last := args[len(args)-1]
			names, err := a.Targets(args[:len(args)-1], false)
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(last)
			if err != nil {
				return fmt.Errorf("replicas: %w", err)
			}
			relative := strings.HasPrefix(last, "+") || strings.HasPrefix(last, "-")
			if err := a.ScaleBy(ctx, names, n, !relative); err != nil {
				return err
			}
			for _, st := range a.StatusAll(ctx, names) {
				fmt.Printf("  %s %-28s %d replicas\n", green("✓"), st.Service, st.Desired)
			}
			return nil
		}),
	}

	var watch bool
	status := &cobra.Command{
		Use: "status [service|group...]", Aliases: []string{"ls", "ps"}, Short: "state of services in the environment",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				names = a.Spec.ServiceNames()
			}
			for {
				sts := a.StatusAll(ctx, names)
				if watch {
					fmt.Print("\033[H\033[2J")
				}
				fmt.Printf("%s  %s\n\n", bold(a.Spec.Name), envLabel(a))
				var rows [][]string
				for _, st := range sts {
					s := a.Spec.Services[st.Service]
					rows = append(rows, []string{st.Service, s.Role, stateText(st.State), fmt.Sprintf("%d/%d", st.Ready, st.Desired), restarts(st), shortImage(st.Image), st.Message})
				}
				printTable(os.Stdout, []string{"SERVICE", "ROLE", "STATE", "READY", "RESTARTS", "IMAGE", "MESSAGE"}, rows)
				if !watch {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		}),
	}
	status.Flags().BoolVarP(&watch, "watch", "w", false, "refresh every 2s")

	discover := &cobra.Command{
		Use: "discover", Short: "everything running in the environment, managed by rig or not",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			ws, err := a.Runtime().Discover(ctx)
			if err != nil {
				return err
			}
			var rows [][]string
			for _, w := range ws {
				svc := w.Service
				if svc == "" {
					svc = dim("(unmanaged)")
				}
				rows = append(rows, []string{w.Kind, w.Name, svc, stateText(w.Status.State), fmt.Sprintf("%d/%d", w.Status.Ready, w.Status.Desired)})
			}
			printTable(os.Stdout, []string{"KIND", "NAME", "SERVICE", "STATE", "READY"}, rows)
			return nil
		}),
	}

	var lo core.LogOptions
	var grep, regex string
	logs := &cobra.Command{
		Use: "logs [service|group...]", Short: "logs of services, merged and coloured per service",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			src, _, err := engine.Get[core.LogSource](a, core.KindLogs, "")
			if err != nil {
				return err
			}
			var names []string
			if len(args) > 0 {
				if names, err = a.Targets(args, false); err != nil {
					return err
				}
			}
			q := core.LogQuery{Services: names, Follow: lo.Follow, Tail: lo.Tail, Since: lo.Since, Match: grep, Instance: lo.Instance}
			if regex != "" {
				q.Match, q.Regex = regex, true
			}
			ch, err := src.Logs(ctx, q)
			if err != nil {
				return err
			}
			for l := range ch {
				if brief {
					fmt.Println(l.Service, cut(l.Text, 400))
					continue
				}
				fmt.Printf("%s %s %s\n", dim(l.Time.Local().Format("15:04:05.000")), serviceColor(l.Service)(fmt.Sprintf("%-16s", l.Service)), l.Text)
			}
			return nil
		}),
	}
	logs.Flags().BoolVarP(&lo.Follow, "follow", "F", false, "keep streaming")
	logs.Flags().IntVarP(&lo.Tail, "tail", "n", 100, "lines from the end")
	logs.Flags().DurationVar(&lo.Since, "since", 0, "only newer than this")
	logs.Flags().StringVarP(&grep, "grep", "g", "", "only lines containing this")
	logs.Flags().StringVarP(&regex, "regex", "E", "", `only lines matching this regular expression, e.g. -E '(?i)timeout|refused'`)
	logs.Flags().StringVarP(&lo.Instance, "instance", "i", "", "one instance (pod, container) of a single service")

	var instance string
	exec := &cobra.Command{
		Use: "exec <service> [-- command...]", Short: "run a command (default: a shell) in a service", Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			cmd := args[1:]
			if len(cmd) == 0 {
				cmd = []string{"sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}
			}
			tty := term.IsTerminal(int(os.Stdin.Fd()))
			return a.Exec(ctx, args[0], core.ExecOptions{Command: cmd, Instance: instance, TTY: tty, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
		}),
	}
	exec.Flags().StringVarP(&instance, "instance", "i", "", "a specific instance (pod, container)")

	setenv := &cobra.Command{
		Use:   "setenv <service|group> [K=V... | K=]",
		Short: "env vars of services, kept in the environment's state so every deploy keeps them; running ones redeploy",
		Example: `  rig setenv load-generator              # what is set
  rig -e staging -y setenv api VERBOSITY=2 LOG_MODE=json
  rig -e staging -y setenv api VERBOSITY=             # back to the manifest's`,
		Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			names, err := a.Targets(args[:1], false)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				var rows [][]string
				for _, n := range names {
					o := a.EnvOverrides(n)
					for _, k := range engine.SortedKeys(o) {
						rows = append(rows, []string{n, k, o[k]})
					}
				}
				printTable(os.Stdout, []string{"SERVICE", "VAR", "VALUE"}, rows)
				return nil
			}
			kv := map[string]string{}
			for _, f := range args[1:] {
				k, v, ok := strings.Cut(f, "=")
				if !ok || k == "" {
					return fmt.Errorf("%q: write K=V (K= removes)", f)
				}
				kv[k] = v
			}
			return a.SetEnv(ctx, names, kv)
		}),
	}
	watchCmd := &cobra.Command{
		Use:   "watch [service|group...]",
		Short: "rebuild and restart services as their sources change (every app service when none); ctrl+w in the UI",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				names = nil
			}
			fmt.Printf("%s on %s: ctrl+c stops\n", bold("rig watch"), envLabel(a))
			return a.Watch(ctx, names, func(e engine.WatchEvent) {
				fmt.Print(dim(time.Now().Format("15:04:05 ")))
				switch e.State {
				case "failed":
					fmt.Printf("%s %s: %v\n%s", red("✖"), e.Service, e.Err, e.Output)
				case "ok":
					fmt.Printf("%s %s rebuilt in %s\n", green("✓"), e.Service, e.Took.Round(100*time.Millisecond))
				default:
					fmt.Printf("%s %s %s\n", dim("·"), e.Service, e.State)
				}
			})
		}),
	}
	return []*cobra.Command{upCmd, down, status, discover, start, stop, restart, scale, deploy, build, logs, exec, setenv, watchCmd}
}

func envLabel(a *engine.App) string {
	s := "env " + bold(a.Env.Name) + " (" + a.Env.Runtime.Type + ")"
	if a.Env.Protected {
		s += " " + red("protected")
	}
	return s
}

func restarts(st core.Status) string {
	n := 0
	for _, in := range st.Instances {
		n += in.Restarts
	}
	if n == 0 {
		return dim("0")
	}
	return amber(strconv.Itoa(n))
}

func shortImage(img string) string {
	if i := strings.LastIndex(img, "/"); i >= 0 && len(img) > 48 {
		return "…" + img[i:]
	}
	return img
}

var palette = []string{"6", "5", "4", "3", "2", "14", "13", "12", "11", "10"}

func serviceColor(name string) func(...string) string {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return out.NewStyle().Foreground(lipgloss.Color(palette[h%len(palette)])).Render
}

func parallelNames(names []string, f func(string) error) error {
	errs := make([]error, len(names))
	done := make(chan struct{})
	for i, n := range names {
		go func() {
			errs[i] = f(n)
			done <- struct{}{}
		}()
	}
	for range names {
		<-done
	}
	return errors.Join(errs...)
}
