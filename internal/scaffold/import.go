package scaffold

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/spec"
)

func init() { spec.RegisterImporter("compose", importCompose) }

// importCompose reads a compose file's services as rig services under their compose names, so a
// docker runtime with compose: finds their containers. ${VARS} fill from the environment, then the
// compose file's .env.
func importCompose(path, dir string) (map[string]*spec.Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	base := filepath.Dir(path)
	rel := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		if r, err := filepath.Rel(dir, p); err == nil {
			return r
		}
		return p
	}
	dotenv, _ := spec.ReadEnvFile(filepath.Join(base, ".env"))
	lookup := func(k string) (string, bool) {
		if v, ok := os.LookupEnv(k); ok {
			return v, true
		}
		v, ok := dotenv[k]
		return v, ok
	}
	expand := func(v string) string { return spec.Expand(v, lookup) }

	out := map[string]*spec.Service{}
	for n, c := range doc.Services {
		x := fromCompose(n, c, filepath.Base(path))
		s := &spec.Service{Name: n, Role: x.Role, Image: expand(x.Image), DependsOn: c.DependsOn.keys,
			Ports: map[string]int{}, Env: map[string]string{}}
		for _, p := range x.Ports {
			s.Ports[p.Name] = p.Port
		}
		for _, kv := range x.Env {
			s.Env[kv[0]] = expand(kv[1])
		}
		for _, f := range c.EnvFile {
			s.EnvFile = append(s.EnvFile, rel(expand(f)))
		}
		if c.Build != nil {
			ctx := filepath.Join(base, c.Build.Context)
			df := c.Build.Dockerfile
			if df == "" {
				df = "Dockerfile"
			}
			s.Build = &spec.Build{Context: rel(ctx), Dockerfile: rel(filepath.Join(ctx, df))}
		}
		sec := map[string]any{}
		if len(x.DockerCommand) > 0 {
			sec["command"] = x.DockerCommand
		}
		if len(x.DockerArgs) > 0 {
			sec["args"] = x.DockerArgs
		}
		for _, v := range x.Volumes {
			host, rest, _ := strings.Cut(v, ":")
			sec["volumes"] = append(sliceOf(sec["volumes"]), "./"+rel(host)+":"+rest)
		}
		if len(sec) > 0 {
			var node yaml.Node
			if err := node.Encode(sec); err != nil {
				return nil, err
			}
			s.Sections = map[string]yaml.Node{"docker": node}
		}
		out[n] = s
	}
	return out, nil
}

func sliceOf(v any) []string {
	l, _ := v.([]string)
	return l
}
