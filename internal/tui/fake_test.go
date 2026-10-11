package tui

import (
	"context"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

// stillRuntime stands in for the docker runtime the examples name; adapters live in rig-plugins.
type stillRuntime struct{}

func init() {
	plugin.Register(core.KindRuntime, "docker", "test runtime that runs nothing", func(core.Env, *spec.Component) (any, error) {
		return stillRuntime{}, nil
	})
}

func (stillRuntime) Discover(context.Context) ([]core.Workload, error) { return nil, nil }
func (stillRuntime) Start(context.Context, *spec.Service) error        { return nil }
func (stillRuntime) Stop(context.Context, *spec.Service) error         { return nil }
func (stillRuntime) Restart(context.Context, *spec.Service) error      { return nil }
func (stillRuntime) Scale(context.Context, *spec.Service, int) error   { return nil }
func (stillRuntime) Exec(context.Context, *spec.Service, core.ExecOptions) error {
	return nil
}
func (stillRuntime) Deploy(context.Context, *spec.Service, core.Release) error { return nil }
func (stillRuntime) Status(_ context.Context, s *spec.Service) (core.Status, error) {
	return core.Status{Service: s.Name, State: core.StateAbsent}, nil
}
func (stillRuntime) Logs(context.Context, *spec.Service, core.LogOptions) (<-chan core.LogLine, error) {
	ch := make(chan core.LogLine)
	close(ch)
	return ch, nil
}
