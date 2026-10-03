// Package pprof captures Go profiles from a service's net/http/pprof endpoint, through the runtime's forwarding.
package pprof

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/httpx"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindProfiler, "pprof", "Go net/http/pprof endpoints (cpu, heap, allocs, goroutine, block, mutex, trace)", New)
}

type Options struct {
	// Port is used for services without a pprof probe; default their metrics port.
	Port string `yaml:"port"`
	Path string `yaml:"path"`
	// Summary runs `go tool pprof -top` on each profile when Go is installed.
	Summary *bool `yaml:"summary"`
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
	if p.opt.Path == "" {
		p.opt.Path = "/debug/pprof"
	}
	return p, nil
}

var kinds = map[string]string{
	"cpu": "profile", "heap": "heap", "allocs": "allocs", "goroutine": "goroutine",
	"block": "block", "mutex": "mutex", "threadcreate": "threadcreate", "trace": "trace",
}

func (p *Profiler) Kinds() []string {
	return []string{"cpu", "heap", "allocs", "goroutine", "block", "mutex", "trace"}
}

func (p *Profiler) port(s *spec.Service) int {
	for _, probe := range []*spec.Probe{s.Pprof, s.Metrics} {
		if probe != nil {
			if n := s.PortNumber(probe.Port); n > 0 {
				return n
			}
		}
	}
	if n := s.PortNumber(p.opt.Port); n > 0 {
		return n
	}
	return s.FirstPort()
}

func (p *Profiler) Capture(ctx context.Context, r core.ProfileRequest) (core.Profile, error) {
	ep, ok := kinds[r.Kind]
	if !ok {
		return core.Profile{}, fmt.Errorf("pprof has no %q profile; have %v", r.Kind, p.Kinds())
	}
	f, ok := p.env.Runtime().(core.Forwarder)
	if !ok {
		return core.Profile{}, fmt.Errorf("runtime cannot reach services: %w", core.ErrUnsupported)
	}
	addr, err := f.Forward(ctx, core.Target{Service: r.Service.Name, Instance: r.Instance, Port: p.port(r.Service)})
	if err != nil {
		return core.Profile{}, err
	}
	path := p.opt.Path
	if r.Service.Pprof != nil && r.Service.Pprof.Path != "" {
		path = r.Service.Pprof.Path
	}
	url := "http://" + addr + strings.TrimRight(path, "/") + "/" + ep
	if r.Kind == "cpu" || r.Kind == "trace" {
		if r.Duration == 0 {
			r.Duration = 15 * time.Second
		}
		url += "?seconds=" + strconv.Itoa(int(r.Duration.Seconds()))
	}
	ctx, cancel := context.WithTimeout(ctx, r.Duration+30*time.Second)
	defer cancel()
	var raw []byte
	if err := (httpx.Endpoint{}).DoURL(ctx, "GET", url, nil, &raw); err != nil {
		return core.Profile{}, err
	}
	if r.OutDir == "" {
		r.OutDir = filepath.Join(p.env.StateDir(), "profiles")
	}
	if err := os.MkdirAll(r.OutDir, 0o755); err != nil {
		return core.Profile{}, err
	}
	ext := ".pb.gz"
	format := "pprof"
	if r.Kind == "trace" {
		ext, format = ".trace", "go-trace"
	}
	file := filepath.Join(r.OutDir, fmt.Sprintf("%s-%s-%s%s", r.Service.Name, r.Kind, time.Now().Format("150405"), ext))
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		return core.Profile{}, err
	}
	prof := core.Profile{Kind: r.Kind, Format: format, File: file}
	if format == "pprof" && (p.opt.Summary == nil || *p.opt.Summary) {
		prof.Summary = Top(ctx, file)
	}
	return prof, nil
}

// Top is `go tool pprof -top` for a profile file, "" when Go is not installed.
func Top(ctx context.Context, file string) string {
	if _, err := exec.LookPath("go"); err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", "-nodecount=20", file).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
