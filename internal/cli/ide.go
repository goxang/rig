package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/ide"
	"github.com/goxang/rig/internal/sh"
)

func ideCommand() *cobra.Command {
	var open bool
	c := &cobra.Command{
		Use:   "ide [service|group...]",
		Short: "put the services into GoLand's Services view, grouped by section: logs, stop and debug from the IDE (o on the Services screen)",
		Long: `Writes, for each service of this environment, GoLand run configurations in one folder per
section of rig.yaml ("rig · <section>"):

  <service>          rig attach: starts it if it is down, shows its live logs; stop stops it
  <service> · debug  Go services: starts it when it is down, brings up its debugger in the
                     background (rig debug --detach --start), then attaches GoLand; breakpoints work
                     from there. "<service> · stop debugger" ends the background debugger

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

func startIfDown(ctx context.Context, a *engine.App, name string) error {
	st, err := a.Status(ctx, name)
	if err != nil || up(st.State) {
		return err
	}
	if err := a.Guard(); err != nil {
		return err
	}
	fmt.Println(dim("starting " + name + "…"))
	return a.Start(ctx, name)
}

func debugPIDFile(a *engine.App, svc string) string {
	return filepath.Join(a.StateDir(), "debug", svc+".pid")
}

// debugInBackground runs `rig debug <svc>` detached from this process (the debugger, its port
// forward and relay live in it) and returns once the service's stable debug port answers.
func debugInBackground(ctx context.Context, a *engine.App, svc, instance string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(debugPIDFile(a, svc))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	logFile := filepath.Join(dir, svc+".log")
	out, err := os.Create(logFile)
	if err != nil {
		return err
	}
	defer out.Close()
	args := []string{"-f", a.Spec.File, "-e", a.Env.Name, "--yes", "debug", svc}
	if instance != "" {
		args = append(args, "-i", instance)
	}
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = out, out
	sh.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = os.WriteFile(debugPIDFile(a, svc), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(2 * time.Minute)
	for {
		if addr, ok := debuggerUp(svc); ok {
			fmt.Println(green("✓ debugger on ") + bold(addr) + dim("  (rig debug --stop "+svc+" ends it; log "+logFile+")"))
			return nil
		}
		select {
		case <-exited:
			_ = os.Remove(debugPIDFile(a, svc))
			raw, _ := os.ReadFile(logFile)
			return fmt.Errorf("the debugger of %s ended: %s", svc, strings.TrimSpace(string(raw)))
		case <-deadline:
			_ = cmd.Process.Signal(os.Interrupt)
			return fmt.Errorf("the debugger of %s did not listen on its port in 2 minutes (log %s)", svc, logFile)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func stopDebugger(a *engine.App, svc string) error {
	raw, err := os.ReadFile(debugPIDFile(a, svc))
	if err != nil {
		return fmt.Errorf("no background debugger of %s (rig debug --detach starts one)", svc)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	_ = os.Remove(debugPIDFile(a, svc))
	// a stale file may name a pid the system gave to something else since
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && !strings.Contains(string(cmdline), "debug\x00"+svc) {
		return fmt.Errorf("the debugger of %s was not running", svc)
	}
	if pid <= 0 || sh.Interrupt(pid) != nil {
		return fmt.Errorf("the debugger of %s was not running", svc)
	}
	for i := 0; i < 50 && sh.Alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println(green("✓ stopped the debugger of " + svc))
	return nil
}
