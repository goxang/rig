package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/spec"
)

var svcRef = regexp.MustCompile(`svc://[A-Za-z0-9_.-]+(:[A-Za-z0-9_-]+)?`)

// Tasks are the project's tasks with this environment's replacing those of the same name.
func (a *App) Tasks() map[string]spec.Task {
	out := map[string]spec.Task{}
	for n, t := range a.Spec.Tasks {
		out[n] = t
	}
	if a.Env != nil {
		for n, t := range a.Env.Tasks {
			out[n] = t
		}
	}
	return out
}

// TaskHelp is a task's help, or its steps' first lines when it has none.
func (a *App) TaskHelp(name string) string {
	t := a.Tasks()[name]
	if t.Help != "" {
		return t.Help
	}
	var steps []string
	for _, st := range t.Steps {
		first, _, _ := strings.Cut(strings.TrimSpace(st), "\n")
		if !strings.HasPrefix(first, `[ -n "${RIG_YES`) {
			steps = append(steps, first)
		}
	}
	return strings.Join(steps, " && ")
}

func (a *App) TaskNames() []string {
	var names []string
	for n := range a.Tasks() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TaskArgChoices is what a task arg offers to pick from: its choices, or the services and groups,
// or the hosts, of this environment.
func (a *App) TaskArgChoices(ctx context.Context, arg spec.TaskArg) []string {
	if len(arg.Choices) > 0 {
		return arg.Choices
	}
	switch arg.From {
	case "services":
		groups, names := map[string]bool{}, []string{}
		for n, svc := range a.Spec.Services {
			names = append(names, n)
			for _, g := range svc.Groups {
				groups[g] = true
			}
		}
		out := []string{"all"}
		for g := range groups {
			if a.Spec.Services[g] == nil && g != "all" {
				out = append(out, g)
			}
		}
		sort.Strings(out[1:])
		sort.Strings(names)
		return append(out, names...)
	case "hosts":
		h, _, err := Get[core.Hosts](a, core.KindHosts, "")
		if err != nil {
			return nil
		}
		list, _ := h.Hosts(ctx)
		var out []string
		for _, x := range list {
			out = append(out, x.Name)
		}
		return out
	}
	return nil
}

// TaskArgValues turns the answers to a task's args, in order, into what RunTask takes: NAME=value for
// an UPPER_CASE arg, the words of the answer for a positional one; empty answers are left out.
func TaskArgValues(args []spec.TaskArg, answers []string) []string {
	var out []string
	for i, arg := range args {
		if i >= len(answers) || strings.TrimSpace(answers[i]) == "" {
			continue
		}
		if arg.Env() {
			out = append(out, arg.Name+"="+strings.TrimSpace(answers[i]))
		} else {
			out = append(out, strings.Fields(answers[i])...)
		}
	}
	return out
}

// PresetTaskArgsDefaults is what a task's args come to when nothing answers them: their defaults,
// except an UPPER_CASE one already set in the environment, which passes through.
func PresetTaskArgsDefaults(args []spec.TaskArg) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if !a.Env() || os.Getenv(a.Name) == "" {
			out[i] = a.Default
		}
	}
	return TaskArgValues(args, out)
}

// PresetTaskArgs maps name=value words that name one of the task's args onto it (whatever its
// case), the args not named taking their defaults, and keeps the other words after them. named is
// false when no word names an arg: words is then not touched.
func PresetTaskArgs(args []spec.TaskArg, words []string) (out []string, named bool) {
	answers := make([]string, len(args))
	set := make([]bool, len(args))
	var rest []string
	for _, w := range words {
		k, v, ok := strings.Cut(w, "=")
		i := slices.IndexFunc(args, func(a spec.TaskArg) bool { return a.Name == k })
		if !ok || i < 0 {
			rest = append(rest, w)
			continue
		}
		answers[i], set[i], named = v, true, true
	}
	if !named {
		return words, false
	}
	for i, a := range args {
		if !set[i] && (!a.Env() || os.Getenv(a.Name) == "") {
			answers[i] = a.Default
		}
	}
	return append(TaskArgValues(args, answers), rest...), true
}

var taskParam = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// RunTask runs a task's steps in order with sh -c from the project directory, stopping at the first
// failure. svc:// addresses in a step become a host:port reachable from here, and a nested `rig`
// inherits this project, environment and confirmation. A NAME=value arg sets that env var for the
// steps (overriding any manifest var or pre-set shell env of the same name) instead of requiring it
// pre-set; every other arg reaches every step positionally as $1... and $RIG_ARGS.
func (a *App) RunTask(ctx context.Context, name string, args []string, out io.Writer) error {
	task, ok := a.Tasks()[name]
	steps := task.Steps
	if !ok {
		return fmt.Errorf("no task %q in %s (have %v)", name, a.envName(), a.TaskNames())
	}
	if !task.ReadOnly {
		if err := a.Guard(); err != nil {
			return err
		}
	}
	var positional, params []string
	for _, arg := range args {
		if taskParam.MatchString(arg) {
			params = append(params, arg)
		} else {
			positional = append(positional, arg)
		}
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
	env = append(env, "RIG_TASK="+name, "RIG_ARGS="+strings.Join(positional, " "))
	vars, _ := a.Vars(ctx)
	for k, v := range vars {
		env = append(env, k+"="+v.Value)
	}
	// params come last so a CLI-passed NAME=value wins over a manifest var of the same name
	env = append(env, params...)
	args = positional
	secrets := a.Spec.SecretValues()
	start := time.Now()
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
		title := withArgs(stepTitle(step, secrets), args)
		fmt.Fprintf(out, "▸ [%d/%d] %s\n", i+1, len(steps), title)
		cmd := sh.New(sh.Shell(), append([]string{"-c", line, name}, args...)...)
		cmd.Dir = a.Spec.Dir
		cmd.Env = env
		began := time.Now()
		if err := cmd.Attach(ctx, os.Stdin, out, out); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("task %s stopped at step %d of %d (%s): interrupted", name, i+1, len(steps), title)
			}
			return fmt.Errorf("task %s failed at step %d of %d: %s\n  %s; the step's output is above", name, i+1, len(steps), title, exitText(err))
		}
		if d := time.Since(began); d >= time.Second {
			fmt.Fprintf(out, "  ✓ %s\n", d.Round(100*time.Millisecond))
		}
	}
	if d := time.Since(start); len(steps) > 1 {
		fmt.Fprintf(out, "✓ %s done in %s\n", name, d.Round(100*time.Millisecond))
	}
	return nil
}

var argRef = regexp.MustCompile(`\$\{?([1-9])(:-[^}]*)?\}?`)

// withArgs shows $1, ${1} and ${1:-x} in a step title as the values they had.
func withArgs(title string, args []string) string {
	return argRef.ReplaceAllStringFunc(title, func(ref string) string {
		m := argRef.FindStringSubmatch(ref)
		if n := int(m[1][0] - '0'); n <= len(args) {
			return args[n-1]
		}
		return ref
	})
}

// exitText says why a step's shell ended in words, not "exit status 1".
func exitText(err error) string {
	var x *exec.ExitError
	if !errors.As(err, &x) {
		return err.Error()
	}
	switch code := x.ExitCode(); code {
	case -1:
		return "it was killed (" + x.String() + ")"
	case 126:
		return "a command in it is not executable (exit code 126)"
	case 127:
		return "a command in it was not found (exit code 127)"
	default:
		return fmt.Sprintf("it ended with exit code %d", code)
	}
}

// stepTitle is how a step shows while it runs: its first line, with secret values put back as
// ${NAME}; the old "type yes" step of a task shows as what it does.
func stepTitle(step string, secrets map[string]string) string {
	title, _, more := strings.Cut(strings.TrimSpace(step), "\n")
	if strings.HasPrefix(title, `[ -n "${RIG_YES`) {
		return "confirm"
	}
	if more {
		title += " …"
	}
	for n, v := range secrets {
		if len(v) >= 3 {
			title = strings.ReplaceAll(title, v, "${"+n+"}")
		}
	}
	return title
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
