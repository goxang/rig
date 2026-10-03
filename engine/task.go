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
// inherits this project, environment and confirmation.
func (a *App) RunTask(ctx context.Context, name string, out io.Writer) error {
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
		cmd := sh.New("sh", "-c", line)
		cmd.Dir = a.Spec.Dir
		cmd.Env = env
		if err := cmd.Attach(ctx, os.Stdin, out, out); err != nil {
			return fmt.Errorf("task %s, step %d (%s): %w", name, i+1, title, err)
		}
	}
	return nil
}
