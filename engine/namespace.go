package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/internal/sh"
)

// NamespaceOverride, when set, replaces the namespace of a Kubernetes environment for this run
// (rig -n); otherwise the one `rig ns` picked, kept in .rig/<env>/namespace, does.
var NamespaceOverride string

func (a *App) namespaceFile() string { return filepath.Join(a.StateDir(), "namespace") }

func (a *App) onKubernetes() bool {
	return a.Env != nil && a.Env.Runtime != nil && (a.Env.Runtime.Type == "kubernetes" || a.Env.Runtime.Type == "kind")
}

// pickNamespace writes the overriding namespace into the runtime's options before it is built.
func (a *App) pickNamespace() error {
	if !a.onKubernetes() {
		return nil
	}
	ns := NamespaceOverride
	if ns != "" && a.Env.Protected {
		return fmt.Errorf("environment %s is protected: its namespace stays the one in %s", a.Env.Name, filepath.Base(a.Spec.File))
	}
	if ns == "" && !a.Env.Protected {
		raw, _ := os.ReadFile(a.namespaceFile())
		ns = strings.TrimSpace(string(raw))
	}
	if ns == "" {
		return nil
	}
	setKey(&a.Env.Runtime.Node, "namespace", ns)
	return nil
}

func setKey(n *yaml.Node, key, value string) {
	if n.Kind != yaml.MappingNode {
		*n = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "type"}, {Kind: yaml.ScalarNode, Value: n.Value}}}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// Namespace is the namespace the environment works in, "" when it is not on Kubernetes.
func (a *App) Namespace() string {
	if !a.onKubernetes() {
		return ""
	}
	var o struct {
		Namespace string `yaml:"namespace"`
	}
	_ = a.Env.Runtime.Decode(&o)
	if o.Namespace == "" {
		return "default"
	}
	return o.Namespace
}

// UseNamespace makes ns this environment's namespace from now on; "" goes back to rig.yaml's.
func (a *App) UseNamespace(ns string) error {
	if !a.onKubernetes() {
		return fmt.Errorf("%s is not on Kubernetes", a.envName())
	}
	if ns == "" {
		err := os.Remove(a.namespaceFile())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if a.Env.Protected {
		return fmt.Errorf("environment %s is protected: its namespace stays the one in %s", a.Env.Name, filepath.Base(a.Spec.File))
	}
	if err := os.MkdirAll(a.StateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(a.namespaceFile(), []byte(ns+"\n"), 0o644)
}

func (a *App) kubeContext() string {
	var o struct {
		Context string `yaml:"context"`
		Cluster string `yaml:"cluster"`
	}
	_ = a.Env.Runtime.Decode(&o)
	if o.Context == "" && a.Env.Runtime.Type == "kind" {
		if o.Cluster == "" {
			o.Cluster = "rig"
		}
		return "kind-" + o.Cluster
	}
	return o.Context
}

func (a *App) kubectl(args ...string) *sh.Cmd {
	if c := a.kubeContext(); c != "" {
		args = append([]string{"--context", c}, args...)
	}
	return sh.New("kubectl", args...)
}

// Namespaces lists the cluster's namespaces.
func (a *App) Namespaces(ctx context.Context) ([]string, error) {
	if !a.onKubernetes() {
		return nil, fmt.Errorf("%s is not on Kubernetes", a.envName())
	}
	out, err := a.kubectl("get", "namespaces", "-o", "jsonpath={.items[*].metadata.name}").Output(ctx)
	if err != nil {
		return nil, err
	}
	names := strings.Fields(string(out))
	sort.Strings(names)
	return names, nil
}

func (a *App) CreateNamespace(ctx context.Context, ns string) error {
	if err := a.Writable(); err != nil {
		return err
	}
	if !a.onKubernetes() {
		return fmt.Errorf("%s is not on Kubernetes", a.envName())
	}
	return a.kubectl("create", "namespace", ns).Run(ctx)
}
