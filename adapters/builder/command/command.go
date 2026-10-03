// Package command builds with the service's own build.command (ko, bazel, buildpacks, a Makefile, ...).
// The command gets IMAGE, TAG and REGISTRY in its environment and must produce IMAGE.
package command

import (
	"context"
	"fmt"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindBuilder, "command", "the service's build.command, given IMAGE, TAG and REGISTRY", New)
}

type Builder struct{ env core.Env }

func New(env core.Env, _ *spec.Component) (any, error) { return &Builder{env: env}, nil }

func (b *Builder) Build(ctx context.Context, s *spec.Service, o core.BuildOptions) (string, error) {
	if len(s.Build.Command) == 0 {
		return "", fmt.Errorf("%s: build needs go, dockerfile or command", s.Name)
	}
	ref := core.ImageRef(s, o.Registry, o.Tag)
	cmd := sh.New(s.Build.Command[0], s.Build.Command[1:]...)
	cmd.Dir = b.env.Project().Dir
	if o.Dir != "" {
		cmd.Dir = o.Dir
	}
	cmd.Env = []string{"IMAGE=" + ref, "TAG=" + o.Tag, "REGISTRY=" + o.Registry, fmt.Sprintf("PUSH=%t", o.Push)}
	return ref, cmd.Attach(ctx, nil, o.Out, o.Out)
}
