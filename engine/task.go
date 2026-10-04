package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goxang/rig/internal/sh"
)

var svcRef = regexp.MustCompile(`svc://[A-Za-z0-9_.-]+(:[A-Za-z0-9_-]+)?`)

// Tasks are the project's tasks with this environment's replacing those of the same name.
func (a *App) Tasks() map[string][]string {
	out := map[string][]string{}
	for n, steps := range a.Spec.Tasks {
		out[n] = steps
	}
	if a.Env != nil {
		for n, steps := range a.Env.Tasks {
			out[n] = steps
		}
	}
	return out
}

func (a *App) TaskNames() []string {
	var names []string
	for n := range a.Tasks() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RunTask runs a task's steps in order with sh -c from the project directory, stopping at the first
// failure. svc:// addresses in a step become a host:port reachable from here, and a nested `rig`
// inherits this project, environment and confirmation. args reach every step as $1... and $RIG_ARGS.
func (a *App) RunTask(ctx context.Context, name string, args []string, out io.Writer) error {
	steps, ok := a.Tasks()[name]
	if !ok {
		return fmt.Errorf("no task %q in %s (have %v)", name, a.envName(), a.TaskNames())
	}
	if err := a.Guard(); err != nil {
		return err
	}
	env := []string{"RIG_FILE=" + a.Spec.File}
	// a nested `rig` is this binary, on PATH or not
	if exe, err := os.Executable(); err == nil {
		env = append(env, "PATH="+filepath.Dir(exe)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if a.Env != nil {
		env = append(env, "RIG_ENV="+a.Env.Name)
	}
	if a.Confirmed {
		env = append(env, "RIG_YES=1")
	}
	env = append(env, "RIG_TASK="+name, "RIG_ARGS="+strings.Join(args, " "))
	vars, _ := a.Vars(ctx)
	for k, v := range vars {
		env = append(env, k+"="+v.Value)
	}
	for i, step := range steps {
		var resolveErr error
		line := svcRef.ReplaceAllStringFunc(step, func(ref string) string {
			addr, err := a.Resolve(ctx, ref)
			if err != nil {
				resolveErr = err
			}
			return addr
		})
		if resolveErr != nil {
			return fmt.Errorf("task %s, step %d: %w", name, i+1, resolveErr)
		}
		title, _, more := strings.Cut(strings.TrimSpace(step), "\n")
		if more {
			title += " …"
		}
		fmt.Fprintf(out, "▸ [%d/%d] %s\n", i+1, len(steps), title)
		cmd := sh.New("sh", append([]string{"-c", line, name}, args...)...)
		cmd.Dir = a.Spec.Dir
		cmd.Env = env
		if err := cmd.Attach(ctx, os.Stdin, out, out); err != nil {
			return fmt.Errorf("task %s, step %d (%s): %w", name, i+1, title, err)
		}
	}
	return nil
}

// Var is a manifest variable and where its value comes from.
type Var struct {
	Value, From string
}

// Vars are the runtime's manifest variables: rig.yaml's, with `rig vars set` overrides from the
// environment's state on top.
func (a *App) Vars(ctx context.Context) (map[string]Var, error) {
	out := map[string]Var{}
	if a.Env == nil || a.Env.Runtime == nil {
		return out, nil
	}
	var file struct {
		Vars map[string]string `yaml:"vars"`
	}
	_ = a.Env.Runtime.Node.Decode(&file)
	for k, v := range file.Vars {
		out[k] = Var{Value: v, From: "rig.yaml"}
	}
	state, err := a.LoadState(ctx)
	if err != nil {
		return out, err
	}
	for k, v := range state {
		if n, ok := strings.CutPrefix(k, "var."); ok {
			out[n] = Var{Value: v, From: "set (rig.yaml: " + file.Vars[n] + ")"}
		}
	}
	return out, nil
}
