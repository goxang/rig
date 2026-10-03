// Package delve attaches a headless Delve server to a running Go service: to the local process
// directly, or inside the container (the image needs dlv and the pod SYS_PTRACE) with the port forwarded.
package delve

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindDebugger, "delve", "headless dlv attach, locally or inside the container, port forwarded", New)
}

type Options struct {
	Dlv  string `yaml:"dlv"`
	Port int    `yaml:"port"`
	PID  int    `yaml:"pid"`
}

type Debugger struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	d := &Debugger{env: env}
	if err := c.Decode(&d.opt); err != nil {
		return nil, err
	}
	if d.opt.Dlv == "" {
		d.opt.Dlv = "dlv"
	}
	if d.opt.Port == 0 {
		d.opt.Port = 2345
	}
	if d.opt.PID == 0 {
		d.opt.PID = 1
	}
	return d, nil
}

func (d *Debugger) Attach(ctx context.Context, s *spec.Service, instance string) (core.DebugSession, error) {
	rt := d.env.Runtime()
	if l, ok := rt.(core.ProcessLocator); ok {
		pid, running := l.PID(s.Name)
		if !running {
			return core.DebugSession{}, fmt.Errorf("%s is not running", s.Name)
		}
		sess, err := d.local(pid)
		if err == nil {
			return sess, nil
		}
		// attaching needs ptrace rights over a non-child; running the service as dlv's child does not
		if rl, ok := rt.(core.Relauncher); ok {
			return d.relaunch(ctx, rl, s)
		}
		return core.DebugSession{}, err
	}
	f, ok := rt.(core.Forwarder)
	if !ok {
		return core.DebugSession{}, fmt.Errorf("runtime cannot forward ports: %w", core.ErrUnsupported)
	}
	probe := core.ExecOptions{Command: []string{"sh", "-c", "command -v " + d.opt.Dlv}, Instance: instance, Stdout: io.Discard, Stderr: io.Discard}
	if err := rt.Exec(ctx, s, probe); err != nil {
		return core.DebugSession{}, fmt.Errorf("%s has no %s in its image: build a debug image (go install github.com/go-delve/delve/cmd/dlv@latest into it) and give the container SYS_PTRACE", s.Name, d.opt.Dlv)
	}
	listen := ":" + strconv.Itoa(d.opt.Port)
	start := fmt.Sprintf("nohup %s attach %d --headless --listen=%s --api-version=2 --accept-multiclient --continue >/tmp/dlv.log 2>&1 &",
		d.opt.Dlv, d.opt.PID, listen)
	if err := rt.Exec(ctx, s, core.ExecOptions{Command: []string{"sh", "-c", start}, Instance: instance, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		return core.DebugSession{}, fmt.Errorf("start dlv in %s (the image needs dlv and SYS_PTRACE): %w", s.Name, err)
	}
	time.Sleep(time.Second)
	target := core.Target{Service: s.Name, Instance: instance, Port: d.opt.Port}
	addr, err := f.Forward(ctx, target)
	if err != nil {
		return core.DebugSession{}, err
	}
	stop := func() error {
		return rt.Exec(context.Background(), s, core.ExecOptions{Command: []string{"sh", "-c", "pkill dlv || killall dlv"}, Instance: instance, Stdout: io.Discard, Stderr: io.Discard})
	}
	return core.DebugSession{Addr: addr, Hint: hint(addr), Close: stop}, nil
}

func (d *Debugger) local(pid int) (core.DebugSession, error) {
	port, err := freePort()
	if err != nil {
		return core.DebugSession{}, err
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	cmd := exec.Command(d.opt.Dlv, "attach", strconv.Itoa(pid), "--headless", "--listen="+addr, "--api-version=2", "--accept-multiclient", "--continue")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return core.DebugSession{}, err
	}
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			return core.DebugSession{Addr: addr, Hint: hint(addr), Close: func() error { return cmd.Process.Signal(syscall.SIGINT) }}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return core.DebugSession{}, fmt.Errorf("dlv did not listen on %s (ptrace allowed? see /proc/sys/kernel/yama/ptrace_scope)", addr)
}

func (d *Debugger) relaunch(ctx context.Context, rl core.Relauncher, s *spec.Service) (core.DebugSession, error) {
	port, err := freePort()
	if err != nil {
		return core.DebugSession{}, err
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	wrap := func(argv []string) []string {
		return append([]string{d.opt.Dlv, "exec", argv[0], "--headless", "--listen=" + addr, "--api-version=2", "--accept-multiclient", "--continue", "--"}, argv[1:]...)
	}
	if err := rl.Relaunch(ctx, s, wrap); err != nil {
		return core.DebugSession{}, err
	}
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			restore := func() error { return rl.Relaunch(context.Background(), s, nil) }
			return core.DebugSession{Addr: addr, Hint: hint(addr) + "\n(" + s.Name + " was restarted under dlv; detaching restarts it normally)", Close: restore}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return core.DebugSession{}, fmt.Errorf("%s restarted under dlv but %s never listened", s.Name, addr)
}

func hint(addr string) string {
	return fmt.Sprintf("dlv connect %s   (or an IDE \"Go Remote\" config on %s)", addr, addr)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
