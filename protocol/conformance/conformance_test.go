package conformance_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/conformance"
	"github.com/goxang/rig/protocol/rpc"
	"github.com/goxang/rig/protocol/transcript"
)

var record = flag.Bool("record", false, "record testdata/hello.jsonl from the Go example")

// hellos are the example plugin in every SDK; each must pass the same checks and transcript.
func hellos(t *testing.T) map[string]func() *exec.Cmd {
	sdk, _ := filepath.Abs("../../sdk")
	bin := filepath.Join(t.TempDir(), "hello")
	build := exec.Command("go", "build", "-o", bin, "./examples/hello")
	build.Dir = sdk
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the Go example: %v\n%s", err, out)
	}
	out := map[string]func() *exec.Cmd{"go": func() *exec.Cmd { return exec.Command(bin) }}
	if py, err := exec.LookPath("python3"); err == nil {
		out["python"] = func() *exec.Cmd { return exec.Command(py, filepath.Join(sdk, "python/examples/hello.py")) }
	}
	if node, err := exec.LookPath("node"); err == nil {
		out["typescript"] = func() *exec.Cmd { return exec.Command(node, filepath.Join(sdk, "typescript/examples/hello.js")) }
	}
	return out
}

func TestSDKsConform(t *testing.T) {
	for lang, cmd := range hellos(t) {
		t.Run(lang, func(t *testing.T) {
			c := cmd()
			c.Stderr = os.Stderr
			results, err := conformance.Run(c, conformance.Options{})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"initialize", "configure", "unknown method", "unknown source", "invalid params",
				"cancel of an unknown request", "query greetings", "stream ticks", "doctor", "search", "shutdown"}
			if len(results) != len(want) {
				t.Errorf("ran %d checks, want %d: %v", len(results), len(want), results)
			}
			for i, r := range results {
				if i < len(want) && r.Check != want[i] {
					t.Errorf("check %d is %q, want %q", i, r.Check, want[i])
				}
				if r.Err != nil {
					t.Errorf("%s: %v", r.Check, r.Err)
				}
			}
		})
	}
}

// session is a deterministic conversation with hello: what the golden transcript holds.
func session(t *testing.T, p *rpc.Process) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	calls := []struct {
		method string
		params any
	}{
		{protocol.MethodInitialize, protocol.InitializeParams{Host: protocol.HostInfo{Name: "rig", Version: "1.0.0"}, Protocols: []string{protocol.Version}}},
		{protocol.MethodConfigure, protocol.ConfigureParams{Config: protocol.Args{"greeting": "hi"}}},
		{protocol.MethodQuery, protocol.QueryParams{Source: "greetings", Args: protocol.Args{"name": "rig"}}},
		{protocol.MethodRun, protocol.RunParams{Action: "greet", Job: "j1", Args: protocol.Args{"name": "rig"}}},
		{protocol.MethodPlan, protocol.RunParams{Action: "greet", Args: protocol.Args{"name": "rig"}}},
		{protocol.MethodResolve, protocol.ResolveParams{Ref: "hello://rig"}},
		{protocol.MethodResolve, protocol.ResolveParams{Ref: "svc://api"}},
		{protocol.MethodSearch, protocol.SearchParams{Query: "wor"}},
		{protocol.MethodDoctor, protocol.DoctorParams{}},
		{"x.unknown", protocol.Empty{}},
	}
	for _, c := range calls {
		_ = p.Call(ctx, c.method, c.params, nil)
	}
	if err := p.Close(5 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptReplays(t *testing.T) {
	plugins := hellos(t)
	golden := "../testdata/hello.jsonl"
	if *record {
		var buf bytes.Buffer
		p, err := rpc.Start(plugins["go"](), rpc.Options{Tap: transcript.NewRecorder(&buf).Tap})
		if err != nil {
			t.Fatal(err)
		}
		session(t, p)
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(golden)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines, err := transcript.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	for lang, cmd := range plugins {
		t.Run(lang, func(t *testing.T) {
			c := cmd()
			c.Stderr = os.Stderr
			if err := transcript.Replay(context.Background(), c, lines, 5*time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}
