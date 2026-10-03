package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/spec"
)

// HelmSection deploys a service with a Helm chart instead of manifests (`helm:` on the service).
// Status, logs, scale and the rest still work through k8s.workload, which names what the chart
// creates (charts often call it <release>-<chart>).
type HelmSection struct {
	Chart   string   `yaml:"chart"` // a path, repo/name or oci:// reference
	Version string   `yaml:"version"`
	Release string   `yaml:"release"` // default: the service name
	Values  []string `yaml:"values"`  // values files, relative to the project
	// Set are --set values; $TAG, $NAMESPACE and the environment's manifest vars are filled in.
	Set map[string]string `yaml:"set"`
	// Where rig puts what it builds and scales, e.g. image.repository, image.tag, replicaCount.
	ImageKey    string `yaml:"image_key"`
	TagKey      string `yaml:"tag_key"`
	ReplicasKey string `yaml:"replicas_key"`
}

func (r *Runtime) helm(s *spec.Service) (HelmSection, bool) {
	var h HelmSection
	ok, _ := s.Section("helm", &h)
	if h.Release == "" {
		h.Release = s.Name
	}
	return h, ok && h.Chart != ""
}

// helmArgs are the arguments shared by upgrade --install and template.
func (r *Runtime) helmArgs(ctx context.Context, s *spec.Service, h HelmSection, rel core.Release) []string {
	chart := h.Chart
	if !strings.Contains(chart, "://") {
		if p := filepath.Join(r.env.Project().Dir, chart); exists(p) {
			chart = p
		}
	}
	args := []string{h.Release, chart, "-n", r.Opt.Namespace}
	if h.Version != "" {
		args = append(args, "--version", h.Version)
	}
	for _, v := range h.Values {
		args = append(args, "-f", filepath.Join(r.env.Project().Dir, v))
	}
	vars := r.vars(ctx)
	keys := make([]string, 0, len(h.Set))
	for k := range h.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := os.Expand(h.Set[k], func(n string) string { x, _ := vars(n); return x })
		args = append(args, "--set", k+"="+v)
	}
	if rel.Image != "" {
		repo, tag := rel.Image, ""
		if i := strings.LastIndex(rel.Image, ":"); i > strings.LastIndex(rel.Image, "/") {
			repo, tag = rel.Image[:i], rel.Image[i+1:]
		}
		switch {
		case h.ImageKey != "" && h.TagKey != "":
			args = append(args, "--set", h.ImageKey+"="+repo, "--set", h.TagKey+"="+tag)
		case h.ImageKey != "":
			args = append(args, "--set", h.ImageKey+"="+rel.Image)
		}
	}
	if n, set := s.Count(); h.ReplicasKey != "" && (set || rel.Replicas > 0) {
		if rel.Replicas > 0 {
			n = rel.Replicas
		}
		args = append(args, "--set", h.ReplicasKey+"="+strconv.Itoa(n))
	}
	return args
}

func (r *Runtime) helmDeploy(ctx context.Context, s *spec.Service, h HelmSection, rel core.Release) error {
	args := append([]string{"upgrade", "--install", "--kube-context", r.Opt.Context}, r.helmArgs(ctx, s, h, rel)...)
	if r.Opt.CreateNamespace {
		args = append(args, "--create-namespace")
	}
	return sh.New("helm", args...).Run(ctx)
}

func (r *Runtime) helmTemplate(ctx context.Context, s *spec.Service, h HelmSection, rel core.Release) ([]byte, error) {
	return sh.New("helm", append([]string{"template"}, r.helmArgs(ctx, s, h, rel)...)...).Output(ctx)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
