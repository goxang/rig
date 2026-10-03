// Package command wraps any load tool behind shell commands: start, stop, rate (with {rate}) and status,
// whose output may be JSON {"running":..,"rate":..,"sent":..,"failed":..}.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/internal/sh"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindLoad, "command", "any load tool behind start/stop/rate/status shell commands", New)
}

type Options struct {
	Start  string `yaml:"start"`
	Stop   string `yaml:"stop"`
	Rate   string `yaml:"rate"`
	Status string `yaml:"status"`
}

type Gen struct {
	opt  Options
	env  core.Env
	rate float64
}

func New(env core.Env, c *spec.Component) (any, error) {
	g := &Gen{env: env}
	if err := c.Decode(&g.opt); err != nil {
		return nil, err
	}
	if g.opt.Start == "" || g.opt.Stop == "" {
		return nil, fmt.Errorf("command load generator needs start and stop")
	}
	return g, nil
}

func (g *Gen) run(ctx context.Context, script string) ([]byte, error) {
	if script == "" {
		return nil, core.ErrUnsupported
	}
	c := sh.New("sh", "-c", script)
	c.Dir = g.env.Project().Dir
	return c.Output(ctx)
}

func (g *Gen) Start(ctx context.Context) error {
	_, err := g.run(ctx, g.opt.Start)
	return err
}

func (g *Gen) Stop(ctx context.Context) error {
	_, err := g.run(ctx, g.opt.Stop)
	return err
}

func (g *Gen) SetRate(ctx context.Context, rps float64) error {
	_, err := g.run(ctx, strings.ReplaceAll(g.opt.Rate, "{rate}", strconv.FormatFloat(rps, 'f', -1, 64)))
	if err == nil {
		g.rate = rps
	}
	return err
}

func (g *Gen) Status(ctx context.Context) (core.LoadStatus, error) {
	st := core.LoadStatus{Rate: g.rate}
	if g.opt.Status == "" {
		return st, nil
	}
	out, err := g.run(ctx, g.opt.Status)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return st, err
	}
	var j struct {
		Running *bool    `json:"running"`
		Rate    *float64 `json:"rate"`
		Sent    int64    `json:"sent"`
		Failed  int64    `json:"failed"`
	}
	if json.Unmarshal(out, &j) == nil {
		if j.Running != nil {
			st.Running = *j.Running
		}
		if j.Rate != nil {
			st.Rate = *j.Rate
		}
		st.Sent, st.Failed = j.Sent, j.Failed
		return st, nil
	}
	st.Running = err == nil
	st.Extra = map[string]string{"status": strings.TrimSpace(string(out))}
	return st, nil
}
