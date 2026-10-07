package engine

import (
	"fmt"

	"github.com/goxang/rig/spec"
)

// KubeTarget is the kubeconfig context and server env's runtime names, read from rig.yaml alone so
// it works while the cluster cannot be reached.
func KubeTarget(file, env string) (name, context, server string, err error) {
	if file == "" {
		if file, err = spec.Find("."); err != nil {
			return "", "", "", err
		}
	}
	_, e, err := spec.Load(file, env)
	if err != nil {
		return "", "", "", err
	}
	var opt struct {
		Context string `yaml:"context"`
		Server  string `yaml:"server"`
	}
	if e.Runtime != nil {
		_ = e.Runtime.Decode(&opt)
	}
	if opt.Context == "" && opt.Server == "" {
		return e.Name, "", "", fmt.Errorf("environment %s names no Kubernetes context or server", e.Name)
	}
	return e.Name, opt.Context, opt.Server, nil
}
