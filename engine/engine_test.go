package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

// fake is an in-memory runtime: the smallest adapter there is, and proof the engine needs nothing more.
type fake struct {
	mu      sync.Mutex
	running map[string]int
	log     []string
}

func (f *fake) Discover(context.Context) ([]core.Workload, error) { return nil, nil }
func (f *fake) Start(_ context.Context, s *spec.Service) error    { return f.set(s.Name, 1, "start") }
func (f *fake) Stop(_ context.Context, s *spec.Service) error     { return f.set(s.Name, 0, "stop") }
func (f *fake) Restart(context.Context, *spec.Service) error      { return nil }
func (f *fake) Scale(_ context.Context, s *spec.Service, n int) error {
	return f.set(s.Name, n, "scale")
}
func (f *fake) Logs(context.Context, *spec.Service, core.LogOptions) (<-chan core.LogLine, error) {
	return nil, core.ErrUnsupported
}
func (f *fake) Exec(context.Context, *spec.Service, core.ExecOptions) error { return nil }
func (f *fake) Deploy(_ context.Context, s *spec.Service, _ core.Release) error {
	return f.set(s.Name, s.CountOr(1), "deploy")
}
func (f *fake) Status(_ context.Context, s *spec.Service) (core.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.running[s.Name]
	st := core.Status{State: core.StateStopped}
	if n > 0 {
		st = core.Status{State: core.StateRunning, Ready: n, Desired: n}
	}
	return st, nil
}

func (f *fake) set(name string, n int, op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[name] = n
	f.log = append(f.log, op+" "+name)
	return nil
}

type fakeLoad struct{ rate float64 }

func (l *fakeLoad) Start(context.Context) error { return nil }
func (l *fakeLoad) Stop(context.Context) error  { return nil }
func (l *fakeLoad) SetRate(_ context.Context, r float64) error {
	l.rate = r
	return nil
}
func (l *fakeLoad) Status(context.Context) (core.LoadStatus, error) {
	return core.LoadStatus{Rate: l.rate}, nil
}

var shared = &fake{running: map[string]int{}}
var other = &fake{running: map[string]int{}}

func init() {
	plugin.Register(core.KindRuntime, "fake", "test runtime", func(core.Env, *spec.Component) (any, error) { return shared, nil })
	plugin.Register(core.KindRuntime, "fake2", "second test runtime", func(core.Env, *spec.Component) (any, error) { return other, nil })
	plugin.Register(core.KindLoad, "fakeload", "test generator", func(core.Env, *spec.Component) (any, error) { return &fakeLoad{}, nil })
}

const file = `
project: t
default: test
services:
  db:  { role: infra }
  api: { depends_on: [db], replicas: 2 }
  web: { depends_on: [api] }
  gen: { role: load, depends_on: [api] }
environments:
  test: { runtime: { type: fake } }
  prod: { runtime: { type: fake }, protected: true }
components:
  load: { type: fakeload, max: 100, step: 25 }
`

func open(t *testing.T, env string) *App {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "rig.yaml")
	if err := os.WriteFile(p, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Open(p, env)
	if err != nil {
		t.Fatal(err)
	}
	shared.running, shared.log = map[string]int{}, nil
	return a
}

func TestUpOrdersAndWaits(t *testing.T) {
	a := open(t, "")
	if err := a.Up(context.Background(), []string{"web"}, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	want := []string{"deploy db", "deploy api", "deploy web"}
	if !reflect.DeepEqual(shared.log, want) {
		t.Fatalf("order = %v", shared.log)
	}
	if err := a.Down(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	want = []string{"stop web", "stop gen", "stop api"}
	if got := shared.log[3:]; got[0] != "stop web" || got[len(got)-1] != "stop api" || len(got) != 3 {
		t.Fatalf("down must stop apps in reverse order and leave infra: %v, want %v", got, want)
	}
	if err := a.Down(context.Background(), []string{"infra"}, nil); err != nil || shared.log[len(shared.log)-1] != "stop db" {
		t.Fatalf("named infra must stop: %v %v", err, shared.log)
	}
}

func TestUpLeavesRunningInfra(t *testing.T) {
	a := open(t, "")
	ctx := context.Background()
	if err := a.Up(ctx, nil, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	shared.log = nil
	if err := a.Up(ctx, nil, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	for _, l := range shared.log {
		if l == "deploy db" {
			t.Fatalf("running infra was redeployed: %v", shared.log)
		}
	}
}

func TestSharedInfraRunsInItsEnvironment(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rig.yaml")
	y := `
project: t
default: dev
services:
  db:  { role: infra, shared: true }
  api: { depends_on: [db] }
environments:
  dev:   { infra: infra, runtime: { type: fake } }
  infra: { runtime: { type: fake2 } }
`
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Open(p, "dev")
	if err != nil {
		t.Fatal(err)
	}
	shared.running, shared.log = map[string]int{}, nil
	other.running, other.log = map[string]int{}, nil
	if err := a.Up(context.Background(), []string{"api"}, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(other.log, []string{"deploy db"}) || !reflect.DeepEqual(shared.log, []string{"deploy api"}) {
		t.Fatalf("db must deploy on the infra runtime: infra %v, dev %v", other.log, shared.log)
	}
	if err := a.Down(context.Background(), nil, nil); err != nil || len(other.log) != 1 {
		t.Fatalf("down must leave shared infra running: %v %v", err, other.log)
	}
}

func TestFillQuery(t *testing.T) {
	q := &spec.Query{Query: "SELECT TOP {{n}} * FROM {{table}}", Params: map[string]string{"n": "10"}}
	got, missing := FillQuery(q, map[string]string{"table": "t"})
	if got != "SELECT TOP 10 * FROM t" || len(missing) != 0 {
		t.Fatalf("fill = %q %v", got, missing)
	}
	if _, missing = FillQuery(q, nil); len(missing) != 1 || missing[0] != "table" {
		t.Fatalf("missing = %v", missing)
	}
}

func TestProtected(t *testing.T) {
	a := open(t, "prod")
	err := a.Up(context.Background(), []string{"db"}, UpOptions{Wait: time.Second})
	if !errors.Is(err, ErrProtected) {
		t.Fatalf("protected env changed without confirmation: %v", err)
	}
	a.Confirmed = true
	if err := a.Up(context.Background(), []string{"db"}, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadLimits(t *testing.T) {
	a := open(t, "")
	ctx := context.Background()
	if err := a.SetRate(ctx, "load", 150, false); !errors.Is(err, ErrOverMax) {
		t.Fatalf("rate above max accepted: %v", err)
	}
	if err := a.SetRate(ctx, "load", 60, false); err != nil {
		t.Fatal(err)
	}
	r, err := a.Nudge(ctx, "load", 1)
	if err != nil || r != 85 {
		t.Fatalf("nudge up = %v %v", r, err)
	}
	if r, _ = a.Nudge(ctx, "load", 1); r != 100 {
		t.Fatalf("nudge must stop at max, got %v", r)
	}
	st, _ := a.LoadState(ctx)
	if st["load.load.rate"] != "100" {
		t.Fatalf("rate not recorded in state: %v", st)
	}
}

func TestResolveNeedsForwarder(t *testing.T) {
	a := open(t, "")
	if got, _ := a.Resolve(context.Background(), "http://x:1"); got != "http://x:1" {
		t.Fatalf("plain address changed: %s", got)
	}
	if _, err := a.Resolve(context.Background(), "svc://api:80"); err == nil {
		t.Fatal("svc:// resolved without a forwarding runtime")
	}
}

func TestUpLeavesRunningDependencies(t *testing.T) {
	a := open(t, "")
	ctx := context.Background()
	if err := a.Up(ctx, []string{"web"}, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	shared.log = nil
	if err := a.Up(ctx, []string{"web"}, UpOptions{Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"deploy web"}; !reflect.DeepEqual(shared.log, want) {
		t.Fatalf("running dependencies were redeployed: %v", shared.log)
	}
}

func TestIgnoreState(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{".git", ".rig"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gi := filepath.Join(dir, ".gitignore")
	_ = os.WriteFile(gi, []byte("bin/"), 0o644)
	ignoreState(dir)
	ignoreState(dir)
	raw, _ := os.ReadFile(gi)
	if got := string(raw); got != "bin/\n# rig: pids, logs, profiles, reports, test runs (rig.yaml)\n.rig/\n" {
		t.Fatalf(".gitignore = %q", got)
	}
}
