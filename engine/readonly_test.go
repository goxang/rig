package engine

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/goxang/rig/core"
)

func TestReadOnlyRefusesEveryChange(t *testing.T) {
	a := open(t, "ro")
	a.Confirmed = true // --yes does not lift readonly
	ctx := context.Background()
	s := a.Spec.Services["api"]
	ops := map[string]func() error{
		"up":             func() error { return a.Up(ctx, []string{"api"}, UpOptions{Wait: time.Second}) },
		"up --dry-run":   func() error { return a.Up(ctx, []string{"api"}, UpOptions{DryRun: true}) },
		"down":           func() error { return a.Down(ctx, nil, io.Discard) },
		"start":          func() error { return a.Start(ctx, "api") },
		"stop":           func() error { return a.Stop(ctx, "api") },
		"restart":        func() error { return a.Restart(ctx, "api") },
		"scale":          func() error { return a.Scale(ctx, "api", 3) },
		"scale by":       func() error { return a.ScaleBy(ctx, []string{"api"}, 1, false) },
		"start in order": func() error { return a.StartInOrder(ctx, []string{"api"}, time.Second) },
		"deploy":         func() error { return a.Deploy(ctx, "api", "v1") },
		"exec":           func() error { return a.Exec(ctx, "api", core.ExecOptions{Command: []string{"sh"}}) },
		"build":          func() error { _, err := a.BuildFrom(ctx, s, "v1", "", io.Discard); return err },
		"setenv":         func() error { return a.SetEnv(ctx, []string{"api"}, map[string]string{"A": "1"}) },
		"autoscale":      func() error { return a.SetAutoscale(ctx, "api", core.Bounds{Min: 1, Max: 2}) },
		"resources":      func() error { return a.SetResources(ctx, "api", core.Resources{CPULimit: "1"}) },
		"load rate":      func() error { return a.SetRate(ctx, "load", 10, false) },
		"load nudge":     func() error { _, err := a.Nudge(ctx, "load", 1); return err },
		"task":           func() error { return a.RunTask(ctx, "touch", nil, io.Discard) },
		"state":          func() error { return a.SetState(ctx, map[string]string{"tag": "v1"}) },
		"namespace":      func() error { return a.CreateNamespace(ctx, "x") },
		"watch":          func() error { return a.Watch(ctx, []string{"api"}, func(WatchEvent) {}) },
		"guard":          a.Guard,
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, core.ErrReadOnly) {
			t.Errorf("%s on a read-only environment: %v, want ErrReadOnly", name, err)
		}
	}
	if len(shared.log) > 0 {
		t.Fatalf("the runtime was changed: %v", shared.log)
	}
	if err := a.RunTask(ctx, "look", nil, io.Discard); err != nil {
		t.Fatalf("a readonly task is refused: %v", err)
	}
	if _, err := a.Status(ctx, "api"); err != nil {
		t.Fatalf("reads refused: %v", err)
	}
}

func TestWritesQuery(t *testing.T) {
	for _, c := range []struct {
		lang, q string
		want    bool
	}{
		{"sql", "SELECT * FROM t", false},
		{"sql", "@app DELETE FROM t WHERE id = 1", true},
		{"redis", "GET k", false},
		{"redis", "SET k v", true},
		{"http", "/health", false},
		{"http", "POST /reset", true},
		{"promql", "rate(x[1m])", false},
	} {
		if got := writes(c.lang, c.q); got != c.want {
			t.Errorf("writes(%s, %q) = %v", c.lang, c.q, got)
		}
	}
}
