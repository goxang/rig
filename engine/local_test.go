package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/protocol"
)

func openLocal(t *testing.T, env string) *Local {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "rig.yaml")
	os.WriteFile(file, []byte(`project: l
default: dev
environments:
  dev: { runtime: fake }
  shared: { runtime: fake, protected: true }
services:
  api: { run: { command: [sleep, "60"] }, depends_on: [db] }
  db: { role: infra, run: { command: [sleep, "60"] } }
tasks:
  hi: [echo hello]
`), 0o644)
	a, err := Open(file, env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return NewLocal(a, nil)
}

func TestLocalRunsTasksThroughPolicy(t *testing.T) {
	ctx := context.Background()
	l := openLocal(t, "dev")
	j, _, err := l.Run(ctx, "task.hi", nil, api.RunOpts{})
	if err != nil || !strings.Contains(strings.Join(j.Output(), "\n"), "hello") {
		t.Fatalf("task: %v %v", err, j.Output())
	}
	ai := identity.AI(l.Session(ctx))
	var pe *protocol.Error
	if _, _, err := l.Run(ctx, "task.hi", nil, api.RunOpts{As: &ai}); !errors.As(err, &pe) || pe.Code != protocol.CodeNeedsInput || len(l.Inbox(ctx)) != 1 {
		t.Fatalf("an AI write ran: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(l.App.StateDir(), "audit.jsonl"))
	if strings.Count(string(b), "\n") != 2 {
		t.Fatalf("audit:\n%s", b)
	}
	var changes []string
	l.App.changeEvidence(time.Hour, func(e Evidence) { changes = append(changes, e.Summary) })
	if len(changes) != 1 || !strings.Contains(changes[0], "ran task.hi") {
		t.Fatalf("why sees the run but not the held write once: %q", changes)
	}

	p := openLocal(t, "shared")
	if _, _, err := p.Run(ctx, "task.hi", nil, api.RunOpts{}); !errors.Is(err, ErrProtected) {
		t.Fatalf("protected without a yes: %v", err)
	}
	p.App.Confirmed = true
	if _, _, err := p.Run(ctx, "task.hi", nil, api.RunOpts{}); err != nil {
		t.Fatalf("protected with a yes: %v", err)
	}
}

func TestLocalCatalogSearchResolve(t *testing.T) {
	ctx := context.Background()
	l := openLocal(t, "dev")
	hits, _ := l.Search(ctx, "AP")
	if len(hits) != 1 || hits[0].Ref != "svc://api" {
		t.Fatalf("hits %v", hits)
	}
	r, err := l.Resolve(ctx, "svc://api")
	if err != nil || r.Kind != "service" || len(r.Links) != 1 || r.Links[0].Ref != "svc://db" {
		t.Fatalf("resolve %+v %v", r, err)
	}
	f, err := l.Query(ctx, "services.status", nil)
	if err != nil || f.Columns[0][0] != "api" {
		t.Fatalf("status %v %v", f, err)
	}
}

func TestLocalPlugins(t *testing.T) {
	ctx := context.Background()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".rig/plugins/demo"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rig/plugins/demo/rig-plugin.yaml"), []byte(`name: demo
version: 0.1.0
tasks:
  plug: [echo from-plugin]
  hi: [echo plugin-hi]
sources:
  nums: { run: "printf '[{\"n\":1},{\"n\":2}]'" }
actions:
  poke: { run: "echo poked", effect: write }
`), 0o644)
	file := filepath.Join(dir, "rig.yaml")
	os.WriteFile(file, []byte("project: p\ndefault: dev\nenvironments: { dev: { runtime: fake } }\ntasks: { hi: [echo mine] }\n"), 0o644)

	a, err := Open(file, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Tasks()["plug"]; ok {
		t.Fatal("an untrusted project plugin loaded")
	}
	a.Close()

	t.Setenv("RIG_TRUST", "project")
	a, err = Open(file, "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	l := NewLocal(a, nil)
	defer l.Close()
	if _, ok := a.Tasks()["plug"]; !ok {
		t.Fatal("the plugin's task is missing")
	}
	j, _, err := l.Run(ctx, "task.hi", nil, api.RunOpts{})
	if err != nil || !strings.Contains(strings.Join(j.Output(), "\n"), "mine") {
		t.Fatalf("rig.yaml should win over a plugin: %v %v", err, j.Output())
	}
	f, err := l.Query(ctx, "demo.nums", nil)
	if err != nil || len(f.Columns) != 1 || len(f.Columns[0]) != 2 {
		t.Fatalf("nums %+v %v", f, err)
	}
	if _, res, err := l.Run(ctx, "demo.poke", nil, api.RunOpts{}); err != nil || res.Message != "poked" {
		t.Fatalf("poke %+v %v", res, err)
	}
	ai := identity.AI(l.Session(ctx))
	if _, _, err := l.Run(ctx, "demo.poke", nil, api.RunOpts{As: &ai}); err == nil {
		t.Fatal("the AI ran a plugin write without approval")
	}
	b, _ := os.ReadFile(filepath.Join(a.StateDir(), "audit.jsonl"))
	if !strings.Contains(string(b), `"demo.poke"`) {
		t.Errorf("plugin action not audited:\n%s", b)
	}
}
