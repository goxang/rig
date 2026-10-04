package engine

import (
	"os"

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
		sc.Env, sc.Protected = a.Env.Name, a.Env.Protected
		if a.Env.Runtime != nil {
			sc.Runtime = a.Env.Runtime.Type
		}
	}
	if p := a.Spec.AI; p != nil {
		sc.Deny, sc.Extra = p.Deny, p.Instructions
	}
	return &ai.Runner{Setup: ai.Resolve(c), Scope: sc, Self: self, Sock: sock, Kube: sc.Runtime == "kubernetes"}, nil
}

// AIDir is where the project's AI sessions live.
func (a *App) AIDir() string {
	dir, err := spec.DataDir(a.Spec.Dir)
	if err != nil {
		return a.StateDir()
	}
	return dir
}
