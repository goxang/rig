package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

// LiveObject is one object of the namespace as JSON (`kubectl get -o json`).
func (r *Runtime) LiveObject(ctx context.Context, kind, name string) ([]byte, error) {
	out, err := r.kubectl("get", kind+"/"+name, "-o", "json").Output(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s/%s on the cluster: %w", kind, name, err)
	}
	return out, nil
}

// InjectedEnv names the env vars a deploy of s adds from rig.yaml, which a manifest synced back
// from the cluster should not take in.
func (r *Runtime) InjectedEnv(s *spec.Service) map[string]bool {
	out := map[string]bool{}
	for k := range r.Opt.Env {
		out[k] = true
	}
	for k := range s.Env {
		out[k] = true
	}
	return out
}

// KubectlArgs point kubectl at this environment's cluster and namespace.
func (r *Runtime) KubectlArgs() []string {
	return []string{"--context", r.Opt.Context, "-n", r.Opt.Namespace}
}

// LiveEnv is the first container's env on the cluster; values from secrets and config maps show
// where they come from.
func (r *Runtime) LiveEnv(ctx context.Context, s *spec.Service) (map[string]string, error) {
	out, err := r.kubectl("get", r.section(s).Workload, "-o", "json").Output(ctx)
	if err != nil {
		return nil, err
	}
	var w struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name      string `json:"name"`
							Value     string `json:"value"`
							ValueFrom *struct {
								SecretKeyRef    *struct{ Name, Key string } `json:"secretKeyRef"`
								ConfigMapKeyRef *struct{ Name, Key string } `json:"configMapKeyRef"`
								FieldRef        *struct {
									FieldPath string `json:"fieldPath"`
								} `json:"fieldRef"`
							} `json:"valueFrom"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, err
	}
	env := map[string]string{}
	cs := w.Spec.Template.Spec.Containers
	if len(cs) == 0 {
		return env, nil
	}
	c := cs[0]
	if want := r.section(s).Container; want != "" {
		for _, x := range cs {
			if x.Name == want {
				c = x
			}
		}
	}
	for _, e := range c.Env {
		v := e.Value
		switch f := e.ValueFrom; {
		case f == nil:
		case f.SecretKeyRef != nil:
			v = "(secret " + f.SecretKeyRef.Name + "/" + f.SecretKeyRef.Key + ")"
		case f.ConfigMapKeyRef != nil:
			v = "(config map " + f.ConfigMapKeyRef.Name + "/" + f.ConfigMapKeyRef.Key + ")"
		case f.FieldRef != nil:
			v = "(field " + f.FieldRef.FieldPath + ")"
		}
		env[e.Name] = v
	}
	return env, nil
}

type containerResources struct {
	Name      string `json:"name"`
	Resources struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

func (r *Runtime) Resources(ctx context.Context, s *spec.Service) ([]core.Resources, error) {
	out, err := r.kubectl("get", r.section(s).Workload, "-o", "json").Output(ctx)
	if err != nil {
		return nil, err
	}
	var w struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []containerResources `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, err
	}
	var res []core.Resources
	for _, c := range w.Spec.Template.Spec.Containers {
		res = append(res, core.Resources{Container: c.Name,
			CPURequest: c.Resources.Requests["cpu"], CPULimit: c.Resources.Limits["cpu"],
			MemRequest: c.Resources.Requests["memory"], MemLimit: c.Resources.Limits["memory"]})
	}
	return res, nil
}

// SetResources runs `kubectl set resources` on one container; empty values stay as they are.
func (r *Runtime) SetResources(ctx context.Context, s *spec.Service, res core.Resources) error {
	pairs := func(cpu, mem string) string {
		var p []string
		if cpu != "" {
			p = append(p, "cpu="+cpu)
		}
		if mem != "" {
			p = append(p, "memory="+mem)
		}
		return strings.Join(p, ",")
	}
	args := []string{"set", "resources", r.section(s).Workload}
	if res.Container != "" {
		args = append(args, "-c", res.Container)
	}
	if p := pairs(res.CPURequest, res.MemRequest); p != "" {
		args = append(args, "--requests="+p)
	}
	if p := pairs(res.CPULimit, res.MemLimit); p != "" {
		args = append(args, "--limits="+p)
	}
	return r.kubectl(args...).Run(ctx)
}
