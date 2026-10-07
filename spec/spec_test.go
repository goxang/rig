package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const project = `
project: shop
default: dev
vars: { ns: shop }
imports:
  - godev: .godev.yaml
services:
  db:    { role: infra, image: "postgres:17", ports: { pg: 5432 } }
  api:   { depends_on: [db], groups: [core], ports: { http: 8080 }, env: { NS: "${ns}", MODE: "${MODE:-plain}" }, k8s: { workload: deployment/api-v2 } }
  web:   { depends_on: [api], groups: [core] }
  load:  { role: load, depends_on: [api] }
environments:
  dev:
    runtime: { type: local }
  kind:
    vars: { ns: shop-kind }
    runtime: { type: kind, namespace: "${ns}" }
    services:
      api: { replicas: 3, env: { EXTRA: "1" } }
    components:
      metrics: { url: "http://kind-prom" }
components:
  metrics: { type: prometheus, url: "http://prom" }
`

const godev = `
services:
  worker: { path: ./cmd/worker, args: [--fast], group: [bg] }
  api:    { path: ./cmd/api, group: [imported] }
`

func write(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rig.yaml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".godev.yaml"), []byte(godev), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "rig.yaml")
}

func TestLoadDefaultEnvironment(t *testing.T) {
	p, e, err := Load(write(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "dev" || e.Runtime.Type != "local" {
		t.Fatalf("env = %s/%s", e.Name, e.Runtime.Type)
	}
	api := p.Services["api"]
	if api.Env["NS"] != "shop" || api.Env["MODE"] != "plain" {
		t.Fatalf("vars not expanded: %v", api.Env)
	}
	if api.Role != RoleApp || p.Services["db"].Role != RoleInfra {
		t.Fatal("roles")
	}
	var k8s struct{ Workload string }
	if ok, err := api.Section("k8s", &k8s); !ok || err != nil || k8s.Workload != "deployment/api-v2" {
		t.Fatalf("section: %v %v %+v", ok, err, k8s)
	}
}

func TestEnvironmentPatchesAndVars(t *testing.T) {
	t.Setenv("MODE", "fast")
	p, e, err := Load(write(t), "kind")
	if err != nil {
		t.Fatal(err)
	}
	var opt struct{ Namespace string }
	if err := e.Runtime.Decode(&opt); err != nil || opt.Namespace != "shop-kind" {
		t.Fatalf("namespace = %q, %v", opt.Namespace, err)
	}
	api := p.Services["api"]
	if api.CountOr(0) != 3 || api.Env["EXTRA"] != "1" || api.Env["NS"] != "shop-kind" || api.Env["MODE"] != "fast" {
		t.Fatalf("patch not merged: %+v", api)
	}
	if len(api.DependsOn) != 1 {
		t.Fatal("patch dropped fields it did not mention")
	}
	m := p.Components["metrics"]
	var mo struct{ URL string }
	_ = m.Decode(&mo)
	if m.Type != "prometheus" || mo.URL != "http://kind-prom" {
		t.Fatalf("component override: %s %s", m.Type, mo.URL)
	}
}

func TestRigYAMLMergesOverImport(t *testing.T) {
	p, _, err := Load(write(t), "")
	if err != nil {
		t.Fatal(err)
	}
	w := p.Services["worker"]
	if w == nil || w.Build.Go != "./cmd/worker" || !reflect.DeepEqual(w.Run.Args, []string{"--fast"}) || !w.InGroup("bg") {
		t.Fatalf("worker = %+v", w)
	}
	api := p.Services["api"]
	if api.Build == nil || api.Build.Go != "./cmd/api" || !reflect.DeepEqual(api.Groups, []string{"core"}) || api.Ports["http"] != 8080 || api.Env["MODE"] != "plain" {
		t.Fatalf("api = %+v: rig.yaml's keys must win over the import's, the rest of the import kept", api)
	}
}

func TestSelectAndOrder(t *testing.T) {
	p, _, err := Load(write(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Select([]string{"core"}); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Fatalf("group: %v", got)
	}
	if got := p.Select(nil); contains(got, "load") {
		t.Fatalf("all must leave load generators out: %v", got)
	}
	layers, err := p.Order(p.WithDeps([]string{"web"}))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"db"}, {"api"}, {"web"}}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("layers = %v", layers)
	}
	if bad := p.Unknown([]string{"core", "nope"}); !reflect.DeepEqual(bad, []string{"nope"}) {
		t.Fatalf("unknown = %v", bad)
	}
}

func TestCycle(t *testing.T) {
	p := &Project{Services: map[string]*Service{
		"a": {DependsOn: []string{"b"}}, "b": {DependsOn: []string{"a"}}, "c": {},
	}}
	_, err := p.Order([]string{"a", "b", "c"})
	if err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("cycle not reported: %v", err)
	}
}

func TestExpand(t *testing.T) {
	look := func(n string) (string, bool) {
		v, ok := map[string]string{"A": "1", "E": ""}[n]
		return v, ok
	}
	for in, want := range map[string]string{
		"${A}":        "1",
		"${B:-two}":   "two",
		"${E:-three}": "three",
		"${B}":        "${B}",
		"$${A}":       "${A}",
		"x-${A}-y":    "x-1-y",
	} {
		if got := Expand(in, look); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestDelaySurvivesEnvironmentPatch(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rig.yaml")
	y := "project: x\ndefault: e\nservices:\n  a: { delay: 10s, shared: true }\nenvironments:\n  e: { runtime: { type: local }, services: { a: { replicas: 2 } } }\n"
	if err := os.WriteFile(f, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, err := Load(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Services["a"]; a.Delay != 10*time.Second || !a.Shared || a.CountOr(0) != 2 {
		t.Fatalf("patched service = %+v", a)
	}
}

func TestOnlyByRole(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rig.yaml")
	y := "project: x\ndefault: e\nservices:\n  a: {}\n  db: { role: infra }\nenvironments:\n  e: { runtime: { type: local }, only: [app] }\n"
	if err := os.WriteFile(f, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, err := Load(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Services["a"]; !ok || len(p.Services) != 1 {
		t.Fatalf("only: [app] must keep the app services: %v", p.ServiceNames())
	}
}

func TestSecretsResolve(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("AppData", cfg) // os.UserConfigDir on Windows
	dir := t.TempDir()
	f := filepath.Join(dir, "rig.yaml")
	y := "project: p\nsecrets:\n  PW: { default: dflt }\n  TOKEN: {}\nservices:\n  a: { image: x, env: { P: \"${PW}\", T: \"${TOKEN}\" } }\nenvironments:\n  e: { runtime: { type: local } }\ndefault: e\n"
	if err := os.WriteFile(f, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret("p", "TOKEN", "s3cret"); err != nil {
		t.Fatal(err)
	}
	p, _, err := Load(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if env := p.Services["a"].Env; env["P"] != "dflt" || env["T"] != "s3cret" {
		t.Fatalf("env %v", env)
	}
	// Windows has no mode bits: the user's AppData is what keeps the file private there
	if st, _ := os.Stat(func() string { s, _ := SecretsFile("p"); return s }()); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestSections(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rig.yaml")
	y := `version: 1
project: p
services:
  db: { role: infra }
  api: { groups: [core] }
  worker: { groups: [core] }
  ui: {}
sections:
  web: { services: [ui, api] }
  core: { services: [core] }
`
	if err := os.WriteFile(f, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, err := Load(f, "")
	if err != nil {
		t.Fatal(err)
	}
	got := p.SectionMap()
	want := map[string]string{"ui": "web", "api": "web", "worker": "core"}
	if fmt.Sprint(got) != fmt.Sprint(want) || fmt.Sprint(p.SectionOrder) != "[web core]" {
		t.Fatalf("sections %v order %v", got, p.SectionOrder)
	}
	if err := os.WriteFile(f, []byte(y+"  bad: { services: [nope] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(f, ""); err == nil {
		t.Fatal("a section naming an unknown service loads")
	}
}

func TestTaskForms(t *testing.T) {
	var ts map[string]Task
	if err := yaml.Unmarshal([]byte("a: [x, y]\nb: {help: hi, steps: [z]}\n"), &ts); err != nil {
		t.Fatal(err)
	}
	if len(ts["a"].Steps) != 2 || ts["b"].Help != "hi" || ts["b"].Steps[0] != "z" {
		t.Fatalf("%+v", ts)
	}
}
