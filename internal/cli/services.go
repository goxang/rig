package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/engine"
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
	upCmd.Flags().StringVarP(&up.Tag, "tag", "t", "", "image tag (default: a timestamp)")
	upCmd.Flags().BoolVar(&up.NoDeps, "no-deps", false, "do not bring up dependencies")
	upCmd.Flags().DurationVar(&up.Wait, "wait", 3*time.Minute, "how long each phase may take to become ready")

	down := &cobra.Command{
		Use:   "down [service|group...]",
		Short: "stop services in reverse dependency order (everything when none given)",
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
				for _, n := range names {
					if err := f(ctx, a, n); err != nil {
						return fmt.Errorf("%s: %w", n, err)
					}
					fmt.Printf("  %s %s\n", green("✓"), n)
				}
				return nil
			}),
		}
	}
	start := each("start", "start services (deploying what is not there yet)", func(ctx context.Context, a *engine.App, n string) error {
		return a.Runtime().Start(ctx, a.Spec.Services[n])
	})
	stop := each("stop", "stop services, keeping their definitions", func(ctx context.Context, a *engine.App, n string) error {
		return a.Runtime().Stop(ctx, a.Spec.Services[n])
	})
	restart := each("restart", "restart services", func(ctx context.Context, a *engine.App, n string) error {
		return a.Runtime().Restart(ctx, a.Spec.Services[n])
	})

	var tag string
	var buildFirst bool
	deploy := &cobra.Command{
		Use: "deploy <service|group...>", Short: "roll out services (with --build, a fresh image first)", Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			if tag == "" {
				tag = time.Now().Format("20060102-150405")
			}
			for _, n := range names {
				s := a.Spec.Services[n]
				rel := core.Release{}
				if buildFirst && s.Build != nil && a.ImageBased() {
					if rel.Image, err = a.Build(ctx, s, tag, os.Stdout); err != nil {
						return err
					}
				}
				if err := a.Runtime().Deploy(ctx, s, rel); err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
				fmt.Printf("  %s %s deployed\n", green("✓"), n)
			}
			if buildFirst {
				return a.SetState(ctx, map[string]string{"tag": tag})
			}
			return nil
		}),
	}
	deploy.Flags().BoolVarP(&buildFirst, "build", "b", false, "build the image first")
	deploy.Flags().StringVarP(&tag, "tag", "t", "", "image tag")

	build := &cobra.Command{
		Use: "build <service|group...>", Short: "build images and push them to the environment's registry", Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			names, err := a.Targets(args, false)
			if err != nil {
				return err
			}
			if tag == "" {
				tag = time.Now().Format("20060102-150405")
			}
			for _, n := range names {
				if a.Spec.Services[n].Build == nil {
					continue
				}
				if _, err := a.Build(ctx, a.Spec.Services[n], tag, os.Stdout); err != nil {
					return err
				}
			}
			return nil
		}),
	}
	build.Flags().StringVarP(&tag, "tag", "t", "", "image tag")

	scale := &cobra.Command{
		Use: "scale <service> <replicas>", Short: "set a service's replica count", Args: cobra.ExactArgs(2),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			s, err := a.Service(args[0])
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("replicas: %w", err)
			}
			return a.Runtime().Scale(ctx, s, n)
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
	var grep string
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
			ch, err := src.Logs(ctx, core.LogQuery{Services: names, Follow: lo.Follow, Tail: lo.Tail, Since: lo.Since, Match: grep})
			if err != nil {
				return err
			}
			for l := range ch {
				fmt.Printf("%s %s %s\n", dim(l.Time.Local().Format("15:04:05.000")), serviceColor(l.Service)(fmt.Sprintf("%-16s", l.Service)), l.Text)
			}
			return nil
		}),
	}
	logs.Flags().BoolVarP(&lo.Follow, "follow", "F", false, "keep streaming")
	logs.Flags().IntVarP(&lo.Tail, "tail", "n", 100, "lines from the end")
	logs.Flags().DurationVar(&lo.Since, "since", 0, "only newer than this")
	logs.Flags().StringVarP(&grep, "grep", "g", "", "only lines containing this")

	var instance string
	exec := &cobra.Command{
		Use: "exec <service> [-- command...]", Short: "run a command (default: a shell) in a service", Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			s, err := a.Service(args[0])
			if err != nil {
				return err
			}
			cmd := args[1:]
			if len(cmd) == 0 {
				cmd = []string{"sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}
			}
			tty := term.IsTerminal(int(os.Stdin.Fd()))
			return a.Runtime().Exec(ctx, s, core.ExecOptions{Command: cmd, Instance: instance, TTY: tty, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
		}),
	}
	exec.Flags().StringVarP(&instance, "instance", "i", "", "a specific instance (pod, container)")

	return []*cobra.Command{upCmd, down, status, discover, start, stop, restart, scale, deploy, build, logs, exec}
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
