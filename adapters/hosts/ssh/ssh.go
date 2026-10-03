// Package ssh manages servers over the system ssh client, so ~/.ssh/config, agents, keys and jump
// hosts work exactly as they do in a terminal.
package ssh

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindHosts, "ssh", "servers over the system ssh client: stats, shells, commands", New)
}

type Host struct {
	Name   string            `yaml:"name"`
	Addr   string            `yaml:"addr"`
	User   string            `yaml:"user"`
	Port   int               `yaml:"port"`
	Key    string            `yaml:"key"`
	Jump   string            `yaml:"jump"`
	Roles  []string          `yaml:"roles"`
	Labels map[string]string `yaml:"labels"`
}

type Options struct {
	Defaults Host   `yaml:"defaults"`
	Hosts    []Host `yaml:"hosts"`
}

type SSH struct {
	opt Options
}

func New(_ core.Env, c *spec.Component) (any, error) {
	s := &SSH{}
	if err := c.Decode(&s.opt); err != nil {
		return nil, err
	}
	for i := range s.opt.Hosts {
		h := &s.opt.Hosts[i]
		if h.Name == "" {
			h.Name = h.Addr
		}
		if h.User == "" {
			h.User = s.opt.Defaults.User
		}
		if h.Key == "" {
			h.Key = s.opt.Defaults.Key
		}
		if h.Jump == "" {
			h.Jump = s.opt.Defaults.Jump
		}
		if h.Port == 0 {
			h.Port = s.opt.Defaults.Port
		}
	}
	return s, nil
}

func (s *SSH) host(name string) (Host, error) {
	for _, h := range s.opt.Hosts {
		if h.Name == name || h.Addr == name {
			return h, nil
		}
	}
	return Host{}, fmt.Errorf("no host %q", name)
}

func (h Host) args(tty bool) []string {
	a := []string{"-o", "ConnectTimeout=5"}
	if tty {
		a = append(a, "-t")
	} else {
		a = append(a, "-o", "BatchMode=yes")
	}
	if h.Port > 0 {
		a = append(a, "-p", strconv.Itoa(h.Port))
	}
	if h.Key != "" {
		a = append(a, "-i", h.Key)
	}
	if h.Jump != "" {
		a = append(a, "-J", h.Jump)
	}
	target := h.Addr
	if h.User != "" {
		target = h.User + "@" + h.Addr
	}
	return append(a, target)
}

const probe = `cat /proc/loadavg; nproc; grep -E '^(MemTotal|MemAvailable):' /proc/meminfo; uname -r; (. /etc/os-release && echo "$PRETTY_NAME"); head -1 /proc/stat; sleep 0.3; head -1 /proc/stat; df -kP / | tail -1`

func (s *SSH) Hosts(ctx context.Context) ([]core.Host, error) {
	out := make([]core.Host, len(s.opt.Hosts))
	var wg sync.WaitGroup
	for i, h := range s.opt.Hosts {
		out[i] = core.Host{Name: h.Name, Addr: h.Addr, Roles: h.Roles, Labels: h.Labels}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			raw, err := sh.New("ssh", append(h.args(false), probe)...).Output(c)
			if err != nil {
				return
			}
			parse(string(raw), &out[i])
		}()
	}
	wg.Wait()
	return out, nil
}

func parse(raw string, h *core.Host) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) < 8 {
		return
	}
	h.Ready = true
	h.Load1, _ = strconv.ParseFloat(strings.Fields(lines[0])[0], 64)
	h.CPUs, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
	kb := func(l string) int64 {
		f := strings.Fields(l)
		if len(f) < 2 {
			return 0
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		return n * 1024
	}
	h.MemTotal = kb(lines[2])
	h.MemUsed = h.MemTotal - kb(lines[3])
	h.Kernel, h.OS = lines[4], lines[5]
	a, b := cpu(lines[6]), cpu(lines[7])
	if total := b[0] - a[0]; total > 0 {
		h.CPUUsed = 1 - (b[1]-a[1])/total
	}
	if len(lines) > 8 {
		if f := strings.Fields(lines[8]); len(f) >= 4 {
			h.DiskTotal, h.DiskUsed = kb("x "+f[1]), kb("x "+f[2])
		}
	}
}

func cpu(l string) [2]float64 {
	var total, idle float64
	for i, f := range strings.Fields(l)[1:] {
		n, _ := strconv.ParseFloat(f, 64)
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return [2]float64{total, idle}
}

func (s *SSH) Shell(ctx context.Context, name string, command []string) (*exec.Cmd, error) {
	h, err := s.host(name)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, "ssh", append(h.args(true), command...)...), nil
}

func (s *SSH) Actions() []core.Action {
	return []core.Action{{Name: "run", Mutate: true, Help: "run a command on hosts: run <host|all> <command...>", Run: func(ctx context.Context, args []string, out io.Writer) error {
		if len(args) < 2 {
			return fmt.Errorf("run <host|all> <command...>")
		}
		targets := s.opt.Hosts
		if args[0] != "all" {
			h, err := s.host(args[0])
			if err != nil {
				return err
			}
			targets = []Host{h}
		}
		cmd := strings.Join(args[1:], " ")
		for _, h := range targets {
			raw, err := sh.New("ssh", append(h.args(false), cmd)...).Output(ctx)
			fmt.Fprintf(out, "── %s\n%s", h.Name, raw)
			if err != nil {
				fmt.Fprintf(out, "error: %v\n", err)
			}
		}
		return nil
	}}}
}
