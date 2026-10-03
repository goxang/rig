// Package command profiles with any tool you can run: async-profiler for the JVM, perf, py-spy, rbspy,
// dotnet-trace, ... Each profile kind is a command run inside the service (through the runtime's exec)
// or on this machine against the service's pid; what it prints, or the file it writes, is the profile.
package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindProfiler, "command", "any profiler CLI (async-profiler, perf, py-spy, ...) run in the service or against its pid", New)
}

type Kind struct {
	// Command runs with {seconds}, {duration}, {pid} and {out} replaced.
	Command []string `yaml:"command"`
	// Where is "service" (exec inside the instance, the default) or "host" (this machine, for local runtimes).
	Where string `yaml:"where"`
	// Fetch, when set, runs after Command and its output is the profile (e.g. cat the file Command wrote).
	Fetch  []string `yaml:"fetch"`
	Format string   `yaml:"format"`
	Ext    string   `yaml:"ext"`
}

type Options struct {
	Kinds map[string]Kind `yaml:"kinds"`
	// PID is the target pid inside a container; most images run the service as pid 1.
	PID int `yaml:"pid"`
}

type Profiler struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	p := &Profiler{env: env}
	if err := c.Decode(&p.opt); err != nil {
		return nil, err
	}
	if len(p.opt.Kinds) == 0 {
		return nil, fmt.Errorf("command profiler needs kinds, e.g. cpu: {command: [asprof, -d, '{seconds}', -o, flat, '{pid}']}")
	}
	if p.opt.PID == 0 {
		p.opt.PID = 1
	}
	return p, nil
}

func (p *Profiler) Kinds() []string {
	out := make([]string, 0, len(p.opt.Kinds))
	for k := range p.opt.Kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *Profiler) Capture(ctx context.Context, r core.ProfileRequest) (core.Profile, error) {
	k, ok := p.opt.Kinds[r.Kind]
	if !ok {
		return core.Profile{}, fmt.Errorf("no %q profile; have %v", r.Kind, p.Kinds())
	}
	if r.Duration == 0 {
		r.Duration = 15 * time.Second
	}
	if r.OutDir == "" {
		r.OutDir = filepath.Join(p.env.StateDir(), "profiles")
	}
	if err := os.MkdirAll(r.OutDir, 0o755); err != nil {
		return core.Profile{}, err
	}
	ext := k.Ext
	if ext == "" {
		ext = ".txt"
	}
	file := filepath.Join(r.OutDir, fmt.Sprintf("%s-%s-%s%s", r.Service.Name, r.Kind, time.Now().Format("150405"), ext))

	pid := p.opt.PID
	if k.Where == "host" {
		l, ok := p.env.Runtime().(core.ProcessLocator)
		if !ok {
			return core.Profile{}, fmt.Errorf("where: host needs a runtime with local processes")
		}
		if pid, ok = l.PID(r.Service.Name); !ok {
			return core.Profile{}, fmt.Errorf("%s is not running", r.Service.Name)
		}
	}
	vars := map[string]string{
		"{seconds}":  strconv.Itoa(int(r.Duration.Seconds())),
		"{duration}": r.Duration.String(),
		"{pid}":      strconv.Itoa(pid),
		"{out}":      "/tmp/rig-profile" + ext,
	}
	if k.Where == "host" {
		vars["{out}"] = file
	}
	expand := func(args []string) []string {
		out := make([]string, len(args))
		for i, a := range args {
			for k, v := range vars {
				a = strings.ReplaceAll(a, k, v)
			}
			out[i] = a
		}
		return out
	}
	run := func(args []string) ([]byte, error) {
		var out, errb bytes.Buffer
		var err error
		if k.Where == "host" {
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Stdout, cmd.Stderr = &out, &errb
			err = cmd.Run()
		} else {
			err = p.env.Runtime().Exec(ctx, r.Service, core.ExecOptions{Command: args, Instance: r.Instance, Stdout: &out, Stderr: &errb})
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
	out, err := run(expand(k.Command))
	if err != nil {
		return core.Profile{}, err
	}
	if len(k.Fetch) > 0 {
		if out, err = run(expand(k.Fetch)); err != nil {
			return core.Profile{}, err
		}
	}
	if k.Where != "host" || len(out) > 0 {
		if err := os.WriteFile(file, out, 0o644); err != nil {
			return core.Profile{}, err
		}
	}
	prof := core.Profile{Kind: r.Kind, Format: k.Format, File: file}
	if prof.Format == "" || prof.Format == "text" {
		s := string(out)
		if len(s) > 4000 {
			s = s[:4000]
		}
		prof.Summary = s
	}
	return prof, nil
}
