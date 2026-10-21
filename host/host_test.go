package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/rpc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__sandbox":
			if err := Sandbox(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		case "__mcp":
			fakeMCP()
			return
		}
	}
	tmp, _ := os.MkdirTemp("", "rig-host-*")
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		os.Setenv(k, filepath.Join(tmp, k))
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func plugin(t *testing.T, root, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, strings.SplitN(strings.TrimPrefix(manifest, "name: "), "\n", 2)[0])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestManifest(t *testing.T) {
	m, err := ParseManifest([]byte("name: x\nversion: 1.0.0\nfuture: 1\ntasks: {hi: {steps: [echo hi]}}\nactions: {go: {run: echo}}\n"), "/p/rig-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], `"future"`) {
		t.Errorf("warnings %v", m.Warnings)
	}
	if m.Fragment.Get("tasks.hi.steps") == nil {
		t.Error("tasks did not reach the fragment")
	}
	if m.Actions["go"].Effect != "write" || m.Kind() != "script" {
		t.Errorf("effect %q kind %q", m.Actions["go"].Effect, m.Kind())
	}
	if _, err := ParseManifest([]byte("name: Bad Name\n"), "x"); err == nil {
		t.Error("a bad name loaded")
	}
}

func TestParseOutput(t *testing.T) {
	for in, want := range map[string]string{
		`[{"a":1,"b":"x"},{"a":2.5}]`:     "a:number b:string",
		"{\"a\":1}\n{\"a\":2,\"c\":true}": "a:int c:bool",
		"a,b\n1,x\n2,y":                   "a:int b:string",
		"a\tb\n1\tx":                      "a:int b:string",
		"- name: x\n  n: [1]\n":           "name:string n:json",
		"just text\nmore":                 "line:string",
		`{"fields":[{"name":"z","type":"int"}],"columns":[[1]]}`: "z:int",
	} {
		f, err := parseOutput([]byte(in), "")
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		var got []string
		for _, fl := range f.Fields {
			got = append(got, fl.Name+":"+fl.Type)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%q: fields %v, want %s", in, got, want)
		}
	}
}

func TestScriptPlugin(t *testing.T) {
	proj := t.TempDir()
	t.Setenv("RIG_TEST_LEAK", "secret-value")
	dir := plugin(t, filepath.Join(proj, "plugins"), `name: sh
version: 0.1.0
sources:
  pods: { run: "printf '[{\"name\":\"api\",\"ready\":1}]'" }
  env: { run: env, format: lines }
actions:
  say: { run: bin/say, effect: read }
`)
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin/say"), []byte("#!/bin/sh\necho working\necho \"said $RIG_ARGS\"\n"), 0o755)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{Options: Options{Dir: proj}, plugins: map[string]*Plugin{}}
	h.add(m, nil)
	ctx := context.Background()
	f, err := h.Query(ctx, "sh.pods", nil)
	if err != nil || frames.Len(f) != 1 || frames.Row(f, 0)["name"] != "api" {
		t.Fatalf("pods = %+v, %v", f, err)
	}
	f, err = h.Query(ctx, "sh.env", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range frames.Column(f, "line") {
		if strings.Contains(fmt.Sprint(l), "secret-value") {
			t.Fatal("the plugin saw an undeclared variable")
		}
	}
	var jm jobs.Manager
	jctx, j := jm.Start(ctx, "say")
	res, err := h.Run(jctx, "sh.say", protocol.Args{"to": "x"}, false)
	if err != nil || res.Message != `said {"to":"x"}` {
		t.Fatalf("say = %+v, %v", res, err)
	}
	if out := j.Output(); len(out) == 0 || out[0] != "working" {
		t.Errorf("job output %q", out)
	}
	if _, err := h.Query(ctx, "sh.nope", nil); err == nil {
		t.Error("an unknown source answered")
	}
}

func buildHello(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hello")
	if out, err := exec.Command("go", "build", "-C", "../sdk", "-o", bin, "./examples/hello").CombinedOutput(); err != nil {
		t.Fatalf("build hello: %v\n%s", err, out)
	}
	return bin
}

func TestServerPlugin(t *testing.T) {
	bin := buildHello(t)
	proj := t.TempDir()
	dir := plugin(t, filepath.Join(proj, "plugins"), "name: hello\nversion: 0.1.0\nserver: { command: ["+bin+"] }\n")
	m, _ := LoadManifest(dir)
	h := &Host{Options: Options{Dir: proj, Idle: 300 * time.Millisecond}, plugins: map[string]*Plugin{}}
	p := h.add(m, nil)
	defer h.Close()
	ctx := context.Background()

	pr := p.Provides(ctx)
	if pr.Actions["greet"].Effect != protocol.EffectWrite || !pr.Sources["ticks"].Stream {
		t.Fatalf("provides %+v", pr)
	}
	f, err := h.Query(ctx, "hello.greetings", protocol.Args{"name": "rig"})
	if err != nil || frames.Row(f, 0)["name"] != "rig" {
		t.Fatalf("greetings %+v %v", f, err)
	}
	var jm jobs.Manager
	jctx, j := jm.Start(ctx, "greet")
	res, err := h.Run(jctx, "hello.greet", protocol.Args{"name": "x"}, false)
	if err != nil || res.Message != "hello x" {
		t.Fatalf("greet %+v %v", res, err)
	}
	if fr, ok := j.Fraction(); !ok || fr != 1 {
		t.Errorf("progress %v %v", fr, ok)
	}
	if res, err := h.Run(ctx, "hello.greet", protocol.Args{"name": "x"}, true); err != nil || len(res.Changes) != 1 {
		t.Errorf("plan %+v %v", res, err)
	}
	ch, err := h.Stream(ctx, "hello.ticks", protocol.Args{"n": 3})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for b := range ch {
		lines = append(lines, b.Lines...)
	}
	if len(lines) != 3 || lines[2] != "tick 3" {
		t.Errorf("ticks %q", lines)
	}

	pid, ok := p.Running()
	if !ok {
		t.Fatal("not running after calls")
	}
	syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(50 * time.Millisecond)
	if _, err := h.Query(ctx, "hello.greetings", nil); err != nil {
		t.Fatalf("no restart after a crash: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, ok := p.Running(); ok {
		t.Error("still running after the idle timeout")
	}

	// a fresh host lists the plugin from the cached initialize without starting it
	h2 := &Host{Options: Options{Dir: proj}, plugins: map[string]*Plugin{}}
	p2 := h2.add(m, nil)
	if _, ok := p2.Provides(ctx).Sources["greetings"]; !ok {
		t.Error("cached provides lost greetings")
	}
	if _, ok := p2.Running(); ok {
		t.Error("listing started the plugin")
	}
}

func TestHostMethods(t *testing.T) {
	proj := t.TempDir()
	m, _ := ParseManifest([]byte("name: a\npermissions: { secrets: [TOK], uses: [\"b.*\"] }\n"), filepath.Join(proj, "a/rig-plugin.yaml"))
	h := &Host{Options: Options{Dir: proj, Secret: func(n string) (string, bool) { return "v-" + n, true }}, plugins: map[string]*Plugin{}}
	p := h.add(m, nil)
	ctx := context.Background()
	call := func(method string, params any) (any, error) {
		b, _ := json.Marshal(params)
		return p.serve(ctx, method, b)
	}
	if v, err := call(protocol.MethodHostSecret, protocol.SecretParams{Name: "TOK"}); err != nil || v.(protocol.SecretValue).Value != "v-TOK" {
		t.Errorf("declared secret: %v %v", v, err)
	}
	if _, err := call(protocol.MethodHostSecret, protocol.SecretParams{Name: "OTHER"}); err == nil {
		t.Error("an undeclared secret was served")
	}
	if _, err := call(protocol.MethodHostQuery, protocol.QueryParams{Source: "c.x"}); err == nil || !strings.Contains(err.Error(), "may not use") {
		t.Errorf("uses: %v", err)
	}
	if _, err := call(protocol.MethodHostStateSet, protocol.StateSetParams{Key: "k", Value: 3}); err != nil {
		t.Fatal(err)
	}
	if v, _ := call(protocol.MethodHostStateGet, protocol.StateGetParams{Key: "k"}); v.(protocol.StateValue).Value != 3.0 {
		t.Errorf("state %v", v)
	}
	if _, err := call(protocol.MethodHostAI, protocol.AIParams{Prompt: "x"}); err == nil {
		t.Error("AI without permission")
	}
}

func TestSandboxDeniesUndeclaredPath(t *testing.T) {
	if v, err := llsys.LandlockGetABIVersion(); err != nil || v < 1 {
		t.Skip("no Landlock")
	}
	// outside /tmp, which the sandbox allows
	outside, err := os.MkdirTemp(".", ".sandbox-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)
	outside, _ = filepath.Abs(outside)
	os.WriteFile(filepath.Join(outside, "f"), []byte("data"), 0o644)
	SandboxExe = os.Args[0]
	defer func() { SandboxExe = "" }()
	proj := t.TempDir()
	for fs, ok := range map[string]bool{"": false, "permissions: { fs: [\"ro:" + outside + "\"] }\n": true} {
		m, _ := ParseManifest([]byte("name: s\n"+fs+"sources: { f: { run: [cat, "+filepath.Join(outside, "f")+"] } }\n"), filepath.Join(proj, "s/rig-plugin.yaml"))
		h := &Host{Options: Options{Dir: proj}, plugins: map[string]*Plugin{}}
		h.add(m, nil)
		_, err := h.Query(context.Background(), "s.f", nil)
		if ok != (err == nil) {
			t.Errorf("fs %q: err %v", fs, err)
		}
	}
}

func fakeMCP() {
	tools := []map[string]any{
		{"name": "list", "description": "list", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
		{"name": "drop", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"destructiveHint": true}},
		{"name": "echo", "inputSchema": map[string]any{"type": "object"}},
	}
	c := rpc.New(os.Stdin, os.Stdout, rpc.Options{OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": mcpVersion, "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}}}, nil
		case "tools/list":
			return map[string]any{"tools": tools}, nil
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(params, &p)
			return map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprint(p.Name, " ", p.Arguments["x"])}}}, nil
		case "resources/list":
			return map[string]any{"resources": []map[string]any{{"uri": "mem://a", "name": "a"}}}, nil
		case "resources/read":
			return map[string]any{"contents": []map[string]any{{"uri": "mem://a", "text": `[{"k":1}]`}}}, nil
		}
		return nil, errNoMethod
	}})
	<-c.Done()
}

func TestMCPBridge(t *testing.T) {
	proj := t.TempDir()
	m, _ := ParseManifest([]byte("name: m\nmcp: { command: [\""+os.Args[0]+"\", __mcp] }\n"), filepath.Join(proj, "m/rig-plugin.yaml"))
	h := &Host{Options: Options{Dir: proj}, plugins: map[string]*Plugin{}}
	p := h.add(m, nil)
	defer h.Close()
	ctx := context.Background()
	pr := p.Provides(ctx)
	if pr.Actions["list"].Effect != "read" || pr.Actions["drop"].Effect != "danger" || pr.Actions["echo"].Effect != "write" {
		t.Errorf("effects %+v", pr.Actions)
	}
	res, err := h.Run(ctx, "m.echo", protocol.Args{"x": 7}, false)
	if err != nil || res.Message != "echo 7" {
		t.Fatalf("echo %+v %v", res, err)
	}
	f, err := h.Query(ctx, "m.resource", protocol.Args{"uri": "mem://a"})
	if err != nil || frames.Row(f, 0)["k"] != 1 {
		t.Fatalf("resource %+v %v", f, err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); h.Query(ctx, "m.resources", nil) }()
	}
	wg.Wait()
}

func TestNetPort(t *testing.T) {
	for in, want := range map[string]uint16{"https://gitlab.com": 443, "http://x": 80, "socks5://127.0.0.1:10808": 10808, "db:5432": 5432, "api.example.com": 443} {
		if got := netPort(in); got != want {
			t.Errorf("%s: %d, want %d", in, got, want)
		}
	}
}

// Planted secrets, declared or not, never show in a script plugin's environment.
func TestSecretsNeverReachPluginEnv(t *testing.T) {
	t.Setenv("CANARY_SECRET", "canary-7f3a")
	t.Setenv("OTHER_TOKEN", "canary-91c2")
	t.Setenv("VISIBLE", "shown")
	proj := t.TempDir()
	m, _ := ParseManifest([]byte("name: s\npermissions: { env: [VISIBLE], secrets: [CANARY_SECRET] }\nsources: { env: { run: [env], format: lines } }\n"), filepath.Join(proj, "s/rig-plugin.yaml"))
	h := &Host{Options: Options{Dir: proj}, plugins: map[string]*Plugin{}}
	h.add(m, nil)
	f, err := h.Query(context.Background(), "s.env", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(f.Columns)
	if strings.Contains(got, "canary-") || !strings.Contains(got, "VISIBLE=shown") {
		t.Errorf("plugin environment: %s", got)
	}
}

// Budgets of docs/plan.md section 17, run by CI's bench job.
func TestBudgetRoundTrip(t *testing.T) {
	if os.Getenv("RIG_BENCH") == "" {
		t.Skip("RIG_BENCH unset")
	}
	bin := buildHello(t)
	proj := t.TempDir()
	dir := plugin(t, filepath.Join(proj, "plugins"), "name: hello\nversion: 0.1.0\nserver: { command: ["+bin+"] }\n")
	m, _ := LoadManifest(dir)
	h := &Host{Options: Options{Dir: proj}, plugins: map[string]*Plugin{}}
	p := h.add(m, nil)
	defer h.Close()
	ctx := context.Background()
	const n = 500
	took := make([]time.Duration, n)
	for i := range n {
		start := time.Now()
		if _, err := h.Query(ctx, "hello.greetings", protocol.Args{"name": fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
		took[i] = time.Since(start)
	}
	slices.Sort(took[1:])
	med := took[1+n/2]
	t.Logf("plugin round trip: median %v", med)
	if med > time.Millisecond {
		t.Errorf("plugin round trip: median %v, budget 1ms", med)
	}
	pid, _ := p.Running()
	status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for l := range strings.Lines(string(status)) {
		var kb int
		if _, err := fmt.Sscanf(l, "VmRSS: %d kB", &kb); err != nil {
			continue
		}
		t.Logf("plugin RSS %d kB", kb)
		if kb > 15<<10 {
			t.Errorf("plugin RSS %d kB, budget 15 MB", kb)
		}
	}
}

func TestContextCarriesCaller(t *testing.T) {
	h := &Host{Options: Options{Dir: t.TempDir()}}
	if c := h.context(context.Background()); c.User != os.Getenv("USER") || c.Roles != nil {
		t.Errorf("local call context = %+v", c)
	}
	ctx := identity.With(context.Background(), identity.Subject{Kind: identity.KindToken, Name: "product/l2", Roles: []string{"parent"}})
	if c := h.context(ctx); c.User != "product/l2" || len(c.Roles) != 1 || c.Roles[0] != "parent" {
		t.Errorf("remote call context = %+v", c)
	}
}
