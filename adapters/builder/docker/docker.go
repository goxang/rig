// Package docker builds images with `docker build` and pushes them when the environment has a registry.
package docker

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/internal/sh"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindBuilder, "docker", "docker build (+ push when the environment has a registry)", New)
}

type Options struct {
	Platform string   `yaml:"platform"`
	Args     []string `yaml:"args"`
}

type Builder struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	b := &Builder{env: env}
	return b, c.Decode(&b.opt)
}

func (b *Builder) Build(ctx context.Context, s *spec.Service, o core.BuildOptions) (string, error) {
	ref := core.ImageRef(s, o.Registry, o.Tag)
	dir := b.env.Project().Dir
	bctx := filepath.Join(dir, s.Build.Context)
	if s.Build.Context == "" {
		bctx = filepath.Join(dir, filepath.Dir(s.Build.Dockerfile))
	}
	args := []string{"build", "-t", ref, "-f", filepath.Join(dir, s.Build.Dockerfile)}
	if b.opt.Platform != "" {
		args = append(args, "--platform", b.opt.Platform)
	}
	keys := make([]string, 0, len(s.Build.Args))
	for k := range s.Build.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+s.Build.Args[k])
	}
	args = append(append(args, b.opt.Args...), bctx)
	cmd := sh.New("docker", args...)
	if err := cmd.Attach(ctx, nil, o.Out, o.Out); err != nil {
		return "", err
	}
	if o.Push {
		return ref, sh.New("docker", "push", ref).Attach(ctx, nil, o.Out, o.Out)
	}
	return ref, nil
}
