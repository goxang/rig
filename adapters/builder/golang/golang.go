// Package golang builds a Go main package on this machine and puts the binary on a base image as one
// layer, with no Docker daemon and no in-container module download: the local build cache makes
// rebuilds take seconds. The image is pushed to the environment's registry, or loaded into the local
// docker daemon when there is none.
package golang

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindBuilder, "go", "go build + one layer on a base image, no Docker daemon (push, or docker load)", New)
}

type Options struct {
	// Base is a registry image, "docker://<image>" to take it from the local docker daemon, or "scratch".
	Base     string `yaml:"base"`
	Platform string `yaml:"platform"`
	Workdir  string `yaml:"workdir"`
	// Ldflags may use {tag} {commit} {branch} {date}.
	Ldflags  string   `yaml:"ldflags"`
	Tags     []string `yaml:"tags"`
	Insecure bool     `yaml:"insecure"`
}

type Builder struct {
	opt Options
	env core.Env
}

func New(env core.Env, c *spec.Component) (any, error) {
	b := &Builder{env: env}
	if err := c.Decode(&b.opt); err != nil {
		return nil, err
	}
	if b.opt.Base == "" {
		b.opt.Base = "gcr.io/distroless/static-debian12:nonroot"
	}
	if b.opt.Platform == "" {
		b.opt.Platform = "linux/amd64"
	}
	if b.opt.Workdir == "" {
		b.opt.Workdir = "/app"
	}
	return b, nil
}

func (b *Builder) Build(ctx context.Context, s *spec.Service, o core.BuildOptions) (string, error) {
	ref := core.ImageRef(s, o.Registry, o.Tag)
	tmp, err := os.MkdirTemp("", "rig-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	goos, goarch, _ := strings.Cut(b.opt.Platform, "/")
	bin := filepath.Join(tmp, s.Name)
	args := []string{"build", "-trimpath", "-o", bin}
	tags := append([]string{"timetzdata"}, b.opt.Tags...)
	args = append(args, "-tags", strings.Join(tags, ","))
	if lf := b.ldflags(ctx, o.Tag); lf != "" {
		args = append(args, "-ldflags", lf)
	}
	cmd := sh.New("go", append(args, s.Build.Go)...)
	cmd.Dir = b.env.Project().Dir
	if o.Dir != "" {
		cmd.Dir = o.Dir
	}
	cmd.Env = []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
	if err := cmd.Run(ctx); err != nil {
		return "", err
	}

	base, err := b.base(ctx, tmp)
	if err != nil {
		return "", fmt.Errorf("base image %s: %w", b.opt.Base, err)
	}
	entry := path.Join(b.opt.Workdir, s.Name)
	layer, err := binaryLayer(bin, strings.TrimPrefix(entry, "/"))
	if err != nil {
		return "", err
	}
	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return "", err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return "", err
	}
	cfg = cfg.DeepCopy()
	cfg.Config.Entrypoint = []string{entry}
	cfg.Config.Cmd = nil
	cfg.Config.WorkingDir = b.opt.Workdir
	cfg.Created = v1.Time{Time: time.Now()}
	cfg.OS, cfg.Architecture = goos, goarch
	if img, err = mutate.ConfigFile(img, cfg); err != nil {
		return "", err
	}

	tagRef, err := name.NewTag(ref, b.nameOpts(ref)...)
	if err != nil {
		return "", err
	}
	if o.Push {
		return ref, remote.Write(tagRef, img, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	}
	file := filepath.Join(tmp, "image.tar")
	if err := tarball.WriteToFile(file, tagRef, img); err != nil {
		return "", err
	}
	return ref, sh.New("docker", "load", "-i", file).Run(ctx)
}

func (b *Builder) nameOpts(ref string) []name.Option {
	if b.opt.Insecure || strings.HasPrefix(ref, "localhost") || strings.HasPrefix(ref, "127.0.0.1") {
		return []name.Option{name.Insecure}
	}
	return nil
}

// base loads the base image; a docker:// base is saved into dir, which must outlive the build.
func (b *Builder) base(ctx context.Context, dir string) (v1.Image, error) {
	if b.opt.Base == "scratch" {
		return empty.Image, nil
	}
	if local, ok := strings.CutPrefix(b.opt.Base, "docker://"); ok {
		file := filepath.Join(dir, "base.tar")
		if err := sh.New("docker", "save", "-o", file, local).Run(ctx); err != nil {
			return nil, err
		}
		tag, err := name.NewTag(local)
		if err != nil {
			return nil, err
		}
		return tarball.ImageFromPath(file, &tag)
	}
	ref, err := name.ParseReference(b.opt.Base, b.nameOpts(b.opt.Base)...)
	if err != nil {
		return nil, err
	}
	goos, goarch, _ := strings.Cut(b.opt.Platform, "/")
	return remote.Image(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithPlatform(v1.Platform{OS: goos, Architecture: goarch}))
}

func (b *Builder) ldflags(ctx context.Context, tag string) string {
	if b.opt.Ldflags == "" {
		return "-s -w"
	}
	git := func(args ...string) string {
		c := sh.New("git", args...)
		c.Dir = b.env.Project().Dir
		out, _ := c.Output(ctx)
		return strings.TrimSpace(string(out))
	}
	r := strings.NewReplacer("{tag}", tag, "{commit}", git("rev-parse", "--short", "HEAD"),
		"{branch}", git("rev-parse", "--abbrev-ref", "HEAD"), "{date}", time.Now().UTC().Format(time.RFC3339))
	return r.Replace(b.opt.Ldflags)
}

// binaryLayer is a one-file layer holding the binary at entry, plus a CA bundle when this machine has one.
func binaryLayer(bin, entry string) (v1.Layer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(src, dst string, mode int64) error {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: dst, Mode: mode, Size: st.Size(), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	}
	dir := path.Dir(entry)
	if dir != "." {
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Unix(0, 0)}); err != nil {
			return nil, err
		}
	}
	if err := add(bin, entry, 0o755); err != nil {
		return nil, err
	}
	const ca = "/etc/ssl/certs/ca-certificates.crt"
	if _, err := os.Stat(ca); err == nil {
		_ = add(ca, strings.TrimPrefix(ca, "/"), 0o644)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	raw := buf.Bytes()
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil })
}
