package engine

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/spec"
)

// AI is the assistant's runner for this project and environment; sock is the bridge that answers
// its approvals and UI actions ("" when nothing can).
func (a *App) AI(sock string) (*ai.Runner, error) {
	c, err := ai.LoadConfig()
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	sc := ai.Scope{Project: a.Spec.Name, Dir: a.Spec.Dir, File: a.Spec.File, Envs: a.Spec.EnvironmentNames()}
	if a.Env != nil {
		sc.Env, sc.Protected, sc.ReadOnly = a.Env.Name, a.Env.Protected, a.Env.ReadOnly
		if a.Env.Runtime != nil {
			sc.Runtime = a.Env.Runtime.Type
		}
	}
	if p := a.Spec.AI; p != nil {
		sc.Deny, sc.Extra = p.Deny, p.Instructions
	}
	r := &ai.Runner{Setup: ai.Resolve(c), Scope: sc, Self: self, Sock: sock, Kube: sc.Runtime == "kubernetes", AuditFile: a.AuditFile()}
	if c.RedactOn() {
		r.Redactor = a.Redactor()
	}
	return r, nil
}

// AuditFile is the log of what the assistant ran on this environment (ai.AuditEntry lines).
func (a *App) AuditFile() string { return filepath.Join(a.StateDir(), "ai-audit.jsonl") }

// Redactor takes this project's secrets out of text bound for a model (ai.Redactor).
func (a *App) Redactor() *ai.Redactor { return ai.NewRedactor(a.KnownSecrets()) }

// KnownSecrets are the values rig knows to be secret, by name: secrets:, the ${NAME}s with a secret's
// name taken from the environment, and the env vars, variables and component fields named like one.
func (a *App) KnownSecrets() map[string]string {
	out := a.Spec.SecretValues()
	add := func(name, value string) {
		if ai.SecretName(name) && value != "" {
			if _, ok := out[name]; !ok {
				out[name] = value
			}
		}
	}
	for n, v := range a.Spec.FromEnv {
		add(n, v)
	}
	for n, v := range a.Spec.Vars {
		add(n, v)
	}
	for _, s := range a.Spec.Services {
		for k, v := range s.Env {
			add(k, v)
		}
	}
	for name, c := range a.Spec.Components {
		secretFields(name, &c.Node, add)
	}
	if a.Env != nil && a.Env.Runtime != nil {
		secretFields("runtime", &a.Env.Runtime.Node, add)
		for k, v := range a.Env.Vars {
			add(k, v)
		}
	}
	return out
}

// secretFields finds the scalar fields named like a secret in a component's mapping, at any depth.
func secretFields(prefix string, n *yaml.Node, add func(name, value string)) {
	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			secretFields(prefix, c, add)
		}
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind == yaml.ScalarNode {
			name := k.Value
			if !ai.SecretName(name) {
				continue
			}
			if !strings.Contains(strings.ToUpper(name), "_") {
				name = prefix + "." + name
			}
			add(name, v.Value)
			continue
		}
		secretFields(prefix, v, add)
	}
}

// AIDir is where the project's AI sessions live.
func (a *App) AIDir() string {
	dir, err := spec.DataDir(a.Spec.Dir)
	if err != nil {
		return a.StateDir()
	}
	return dir
}
