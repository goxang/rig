// Package local runs services as processes on this machine. Processes are detached into their own
// process group, so they outlive the rig command that started them; pid and log files live under
// .rig/<env>/.
package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/localhost"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindRuntime, "local", "processes on this machine: go build + run, detached, pid and log files under .rig/", New)
}

type Options struct {
	Env map[string]string `yaml:"env"`
	// StopTimeout is how long a process gets after SIGTERM before SIGKILL.
	StopTimeout time.Duration `yaml:"stop_timeout"`
}

type Runtime struct {
	localhost.Host
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	r := &Runtime{env: env}
	if err := c.Decode(&r.opt); err != nil {
		return nil, err
	}
	if r.opt.StopTimeout == 0 {
		r.opt.StopTimeout = 10 * time.Second
	}
	return r, nil
}

type pidFile struct {
	PID     int       `json:"pid"`
	Command []string  `json:"command"`
	Dir     string    `json:"dir"`
	Started time.Time `json:"started"`
}

func (r *Runtime) path(kind, svc, ext string) string {
	return filepath.Join(r.env.StateDir(), kind, svc+ext)
}

func (r *Runtime) read(svc string) (pidFile, bool) {
	var p pidFile
	raw, err := os.ReadFile(r.path("run", svc, ".json"))
	if err != nil || json.Unmarshal(raw, &p) != nil {
		return p, false
	}
	return p, alive(p.PID)
}

func alive(pid int) bool {
	if !sh.Alive(pid) {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true // no procfs (macOS): signal 0 succeeding is all we know
	}
	// state is the field after the parenthesised command name; Z is a zombie
	s := string(raw)
	if i := strings.LastIndex(s, ")"); i > 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

func (r *Runtime) dir(s *spec.Service) string {
	d := r.env.Project().Dir
	if s.Run != nil && s.Run.Dir != "" {
		d = filepath.Join(d, s.Run.Dir)
	}
	return d
}

// command is what to run: run.command, or the binary built from build.go plus run.args.
// A debug build keeps what a debugger needs: no inlining, no optimisation.
func (r *Runtime) command(ctx context.Context, s *spec.Service, debug bool) ([]string, error) {
	if s.Run != nil && len(s.Run.Command) > 0 {
		return append(append([]string{}, s.Run.Command...), s.Run.Args...), nil
	}
	if s.Build == nil || s.Build.Go == "" {
		return nil, fmt.Errorf("%s: nothing to run locally (set run.command or build.go)", s.Name)
	}
	bin := r.path("bin", s.Name, "")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return nil, err
	}
	build := []string{"build", "-o", bin}
	if debug {
		build = append(build, "-gcflags=all=-N -l")
	}
	b := sh.New("go", append(build, s.Build.Go)...)
	b.Dir = r.env.Project().Dir
	if err := b.Run(ctx); err != nil {
		return nil, err
	}
	var args []string
	if s.Run != nil {
		args = s.Run.Args
	}
	return append([]string{bin}, args...), nil
}

func (r *Runtime) Start(ctx context.Context, s *spec.Service) error {
	return r.start(ctx, s, nil)
}

func (r *Runtime) Relaunch(ctx context.Context, s *spec.Service, wrap func([]string) []string) error {
	if err := r.Stop(ctx, s); err != nil {
		return err
	}
	return r.start(ctx, s, wrap)
}

func (r *Runtime) start(ctx context.Context, s *spec.Service, wrap func([]string) []string) error {
	if _, ok := r.read(s.Name); ok {
		return nil
	}
	argv, err := r.command(ctx, s, wrap != nil)
	if err != nil {
		return err
	}
	if wrap != nil {
		argv = wrap(argv)
	}
	logPath := r.path("logs", s.Name, ".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = r.dir(s)
	cmd.Stdout, cmd.Stderr = logf, logf
	sh.Detach(cmd)
	cmd.Env = os.Environ()
	for _, m := range []map[string]string{r.opt.Env, s.Env} {
		for k, v := range m {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p := pidFile{PID: cmd.Process.Pid, Command: argv, Dir: cmd.Dir, Started: time.Now()}
	_ = cmd.Process.Release()
	raw, _ := json.Marshal(p)
	if err := os.MkdirAll(filepath.Dir(r.path("run", s.Name, ".json")), 0o755); err != nil {
		return err
	}
	return os.WriteFile(r.path("run", s.Name, ".json"), raw, 0o644)
}

func (r *Runtime) Stop(ctx context.Context, s *spec.Service) error {
	p, ok := r.read(s.Name)
	if !ok {
		_ = os.Remove(r.path("run", s.Name, ".json"))
		return nil
	}
	sh.StopGroup(p.PID, false)
	deadline := time.Now().Add(r.opt.StopTimeout)
	for alive(p.PID) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if alive(p.PID) {
		sh.StopGroup(p.PID, true)
	}
	return os.Remove(r.path("run", s.Name, ".json"))
}

func (r *Runtime) Restart(ctx context.Context, s *spec.Service) error {
	if err := r.Stop(ctx, s); err != nil {
		return err
	}
	return r.Start(ctx, s)
}

func (r *Runtime) Deploy(ctx context.Context, s *spec.Service, _ core.Release) error {
	return r.Restart(ctx, s)
}

func (r *Runtime) Scale(ctx context.Context, s *spec.Service, n int) error {
	switch n {
	case 0:
		return r.Stop(ctx, s)
	case 1:
		return r.Start(ctx, s)
	}
	return fmt.Errorf("local runtime runs one process per service: %w", core.ErrUnsupported)
}

func (r *Runtime) Status(ctx context.Context, s *spec.Service) (core.Status, error) {
	p, ok := r.read(s.Name)
	if !ok {
		return core.Status{Service: s.Name, State: core.StateStopped}, nil
	}
	in := core.Instance{ID: strconv.Itoa(p.PID), Host: "local", IP: "127.0.0.1", State: core.StateRunning, Ready: true, Started: p.Started, Memory: rss(p.PID)}
	if s.Health != nil {
		port := s.PortNumber(s.Health.Port)
		if port == 0 {
			port = s.FirstPort()
		}
		in.Ready = localhost.Probe(ctx, fmt.Sprintf("http://127.0.0.1:%d%s", port, s.Health.Path))
	}
	st := core.Status{Service: s.Name, State: core.StateRunning, Desired: 1, Instances: []core.Instance{in}, Image: strings.Join(p.Command, " ")}
	if in.Ready {
		st.Ready = 1
	} else {
		st.State = core.StateStarting
	}
	return st, nil
}

func rss(pid int) int64 {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(raw))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

func (r *Runtime) Discover(ctx context.Context) ([]core.Workload, error) {
	files, _ := filepath.Glob(filepath.Join(r.env.StateDir(), "run", "*.json"))
	sort.Strings(files)
	var out []core.Workload
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		st := core.Status{Service: name, State: core.StateStopped}
		if s, ok := r.env.Project().Services[name]; ok {
			st, _ = r.Status(ctx, s)
		}
		out = append(out, core.Workload{Name: name, Kind: "process", Service: name, Status: st})
	}
	return out, nil
}

func (r *Runtime) Logs(ctx context.Context, s *spec.Service, o core.LogOptions) (<-chan core.LogLine, error) {
	f, err := os.Open(r.path("logs", s.Name, ".log"))
	if err != nil {
		return nil, fmt.Errorf("%s has no log yet: %w", s.Name, err)
	}
	if o.Tail > 0 {
		seekTail(f, o.Tail)
	}
	out := make(chan core.LogLine, 256)
	go func() {
		defer close(out)
		defer f.Close()
		rd := bufio.NewReader(f)
		var partial string
		for {
			line, err := rd.ReadString('\n')
			if err == nil {
				out <- core.LogLine{Service: s.Name, Time: time.Now(), Text: strings.TrimRight(partial+line, "\n")}
				partial = ""
				continue
			}
			partial += line
			if !errors.Is(err, io.EOF) || !o.Follow {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	return out, nil
}

// seekTail positions f at the start of its last n lines.
func seekTail(f *os.File, n int) {
	st, err := f.Stat()
	if err != nil {
		return
	}
	const chunk = 64 * 1024
	size := st.Size()
	off := size
	lines := 0
	buf := make([]byte, chunk)
	for off > 0 {
		step := int64(chunk)
		if off < step {
			step = off
		}
		off -= step
		if _, err := f.ReadAt(buf[:step], off); err != nil && !errors.Is(err, io.EOF) {
			return
		}
		for i := step - 1; i >= 0; i-- {
			if buf[i] == '\n' && off+i != size-1 {
				lines++
				if lines == n {
					_, _ = f.Seek(off+i+1, io.SeekStart)
					return
				}
			}
		}
	}
	_, _ = f.Seek(0, io.SeekStart)
}

func (r *Runtime) Exec(ctx context.Context, s *spec.Service, o core.ExecOptions) error {
	if len(o.Command) == 0 {
		return errors.New("exec needs a command")
	}
	cmd := sh.New(o.Command[0], o.Command[1:]...)
	cmd.Dir = r.dir(s)
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd.Attach(ctx, o.Stdin, o.Stdout, o.Stderr)
}

// Forward is the port itself, except a service's metrics port when its process does not listen
// there: local configs often give every service its own metrics port while rig.yaml names one for
// all, so the process's own port serving /metrics stands in.
func (r *Runtime) Forward(ctx context.Context, t core.Target) (string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", t.Port)
	s := r.env.Project().Services[t.Service]
	if s == nil || s.Metrics == nil || s.PortNumber(s.Metrics.Port) != t.Port {
		return addr, nil
	}
	p, ok := r.read(t.Service)
	if !ok {
		return addr, nil
	}
	if port, ok := metricsPort(ctx, p.PID, t.Port, s.Metrics.Path); ok {
		return fmt.Sprintf("127.0.0.1:%d", port), nil
	}
	return addr, nil
}

// PID is the process id of a running service, for adapters that attach to it (delve, perf).
func (r *Runtime) PID(svc string) (int, bool) {
	p, ok := r.read(svc)
	return p.PID, ok
}
