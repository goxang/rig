package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/ide"
)

func ideCommand() *cobra.Command {
	var open bool
	c := &cobra.Command{
		Use:   "ide [service|group...]",
		Short: "put the services into GoLand's Services view, grouped by section: logs, stop and debug from the IDE (o on the Services screen)",
		Long: `Writes, for each service of this environment, GoLand run configurations in one folder per
section of rig.yaml ("rig · <section>"):

  <service>          rig attach: starts it if it is down, shows its live logs; stop stops it
  <service> · debug  Go services: brings up its debugger (rig debug --detach), then attaches GoLand

They run rig on this environment, so rig and GoLand see the same processes: what one starts or
stops, the other shows. VS Code gets "rig: <service>" attach configs in .vscode/launch.json.`,
		RunE: withApp(func(_ context.Context, a *engine.App, args []string) error {
			names := a.Spec.ServiceNames()
			if len(args) > 0 {
				var err error
				if names, err = a.Targets(args, false); err != nil {
					return err
				}
			}
			r, err := ide.Write(a, names)
			if err != nil {
				return err
			}
			fmt.Printf("%s %d services (%d debuggable) in .idea/runConfigurations, folders %s\n", green("✓"), r.Services, r.Debug, strings.Join(r.Folders, ", "))
			if r.VSCode != "" {
				fmt.Println(dim("  VS Code attach configs in " + r.VSCode))
			}
			if !r.Dashboard {
				fmt.Println(amber("  GoLand's Services view: + › Run Configuration Type › Shell Script and Go Remote"))
			} else {
				fmt.Println(dim("  Services view (alt+8) shows them; an open GoLand may need: + › Run Configuration Type › Shell Script"))
			}
			if open {
				return ide.Open(a.Spec.Dir)
			}
			return nil
		}),
	}
	c.Flags().BoolVar(&open, "open", false, "open the project in GoLand afterwards")
	return c
}

func attachCommand() *cobra.Command {
	var keep, debug bool
	c := &cobra.Command{
		Use:   "attach <service>",
		Short: "start a service if it is down and follow its logs; Ctrl-C stops it (--keep leaves it running). GoLand's rig configs run this",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			name := args[0]
			if _, err := a.Service(name); err != nil {
				return err
			}
			if a.Env.Protected && !keep {
				keep = true
				fmt.Println(dim(a.Env.Name + " is protected: leaving " + name + " running when this ends"))
			}
			st, err := a.Status(ctx, name)
			if err != nil {
				return err
			}
			if !up(st.State) {
				if err := a.Guard(); err != nil {
					return err
				}
				fmt.Println(dim("starting " + name + "…"))
				if err := a.Start(ctx, name); err != nil {
					return err
				}
			}
			if debug {
				d, _, err := engine.Get[core.Debugger](a, core.KindDebugger, "")
				if err != nil {
					return err
				}
				s, _ := a.Service(name)
				sess, err := d.Attach(ctx, s, "")
				if err != nil {
					return err
				}
				fmt.Println(green("✓ debugger on ") + bold(sess.Addr))
				if sess.Close != nil {
					defer func() { _ = sess.Close() }()
				}
			}
			src, _, err := engine.Get[core.LogSource](a, core.KindLogs, "")
			if err != nil {
				return err
			}
			follow, stopFollow := context.WithCancel(ctx)
			defer stopFollow()
			ch, err := src.Logs(follow, core.LogQuery{Services: []string{name}, Follow: true, Tail: 200})
			if err != nil {
				return err
			}
			gone := make(chan struct{})
			go watchStopped(follow, a, name, gone)
			for {
				select {
				case l, ok := <-ch:
					if !ok {
						ch = nil
						continue
					}
					fmt.Println(dim(l.Time.Local().Format("15:04:05.000")) + " " + l.Text)
					continue
				case <-gone:
					fmt.Println(dim(name + " was stopped elsewhere"))
					return nil
				case <-ctx.Done():
				}
				break
			}
			if keep {
				return nil
			}
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := a.Stop(c, name); err != nil {
				return fmt.Errorf("stop %s: %w", name, err)
			}
			fmt.Println(green("✓ stopped " + name))
			return nil
		}),
	}
	c.Flags().BoolVar(&keep, "keep", false, "leave the service running when this ends")
	c.Flags().BoolVar(&debug, "debug", false, "also bring up its debugger (on its rig ide port) for as long as this runs")
	return c
}

func up(s core.State) bool {
	return s == core.StateRunning || s == core.StateStarting || s == core.StateDegraded
}

// watchStopped closes gone once the service has been down for two checks in a row.
func watchStopped(ctx context.Context, a *engine.App, name string, gone chan struct{}) {
	down := 0
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := a.Status(ctx, name)
		if err != nil || up(st.State) {
			down = 0
			continue
		}
		if down++; down == 2 {
			close(gone)
			return
		}
	}
}

// debuggerUp is whether something already listens on the service's stable debug port.
func debuggerUp(svc string) (string, bool) {
	addr := "127.0.0.1:" + strconv.Itoa(core.DebugPort(svc))
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return addr, false
	}
	c.Close()
	return addr, true
}

var errDetachRemote = errors.New("--detach keeps a debugger only for local processes; for this runtime run rig attach --debug (or rig debug) and keep it open")
