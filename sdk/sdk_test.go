package sdk

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// host drives a plugin over pipes, the way rig does.
type host struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
}

func start(t *testing.T, p *Plugin) *host {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = p.ServeIO(inR, outW); outW.Close() }()
	return &host{t: t, in: inW, out: bufio.NewScanner(outR)}
}

func (h *host) send(s string) {
	if _, err := io.WriteString(h.in, s+"\n"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) next() map[string]any {
	got := make(chan map[string]any, 1)
	go func() {
		var m map[string]any
		if h.out.Scan() {
			_ = json.Unmarshal(h.out.Bytes(), &m)
		}
		got <- m
	}()
	select {
	case m := <-got:
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatal("no message from the plugin")
		return nil
	}
}

func TestHostCallsRoundTrip(t *testing.T) {
	p := New("t").Action("count", Write, func(c *Ctx, a Args) (*Result, error) {
		v, err := c.State("n")
		if err != nil {
			return nil, err
		}
		n, _ := v.Value.(float64)
		return &Result{Message: strings.Repeat("x", int(n))}, c.SetState("n", n+1)
	})
	h := start(t, p)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"run","params":{"action":"count"}}`)
	get := h.next()
	if get["method"] != MethodHostStateGet {
		t.Fatalf("want host/state.get, got %v", get)
	}
	h.send(`{"jsonrpc":"2.0","id":` + string(mustJSON(get["id"])) + `,"result":{"value":3,"found":true}}`)
	set := h.next()
	if set["method"] != MethodHostStateSet || set["params"].(map[string]any)["value"] != 4.0 {
		t.Fatalf("want host/state.set 4, got %v", set)
	}
	h.send(`{"jsonrpc":"2.0","id":` + string(mustJSON(set["id"])) + `,"result":{}}`)
	if r := h.next(); r["result"].(map[string]any)["message"] != "xxx" {
		t.Fatalf("got %v", r)
	}
}

func TestStreamWaitsForAcks(t *testing.T) {
	p := New("t").Stream("lines", func(c *Ctx, a Args, out *Out) error {
		for i := 0; ; i++ {
			if err := out.Line("x"); err != nil {
				return err
			}
			if i%flushRows == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	})
	h := start(t, p)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"stream","params":{"id":"s","source":"lines"}}`)
	seq := 0
	for seq < ackWindow {
		m := h.next()
		if m["method"] == MethodNotifyStream {
			seq = int(m["params"].(map[string]any)["seq"].(float64))
		}
	}
	quiet := make(chan map[string]any, 1)
	go func() { quiet <- h.next() }()
	select {
	case m := <-quiet:
		t.Fatalf("batch past the window without an ack: %v", m)
	case <-time.After(200 * time.Millisecond):
	}
	h.send(`{"jsonrpc":"2.0","method":"$/ack","params":{"id":"s","seq":8}}`)
	if m := <-quiet; m["method"] != MethodNotifyStream {
		t.Fatalf("want the next batch after the ack, got %v", m)
	}
	h.send(`{"jsonrpc":"2.0","method":"$/cancel","params":{"id":"s"}}`)
	h.send(`{"jsonrpc":"2.0","method":"$/ack","params":{"id":"s","seq":100}}`)
	for {
		m := h.next()
		if b, _ := m["params"].(map[string]any); b != nil && b["done"] == true {
			return
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestArgsSchemaEmbedded(t *testing.T) {
	type base struct {
		Release string `json:"release" rig:"required"`
	}
	s := ArgsSchema(reflect.TypeOf(struct {
		base
		All bool `json:"all"`
	}{}))
	if fmt.Sprint(s["required"]) != "[release]" || len(s["properties"].(map[string]any)) != 2 {
		t.Fatalf("schema %v", s)
	}
}

func TestFailedToolIsUnavailable(t *testing.T) {
	err := exec.Command("false").Run()
	if e := asError(fmt.Errorf("git log: %w", err)); e.Code != CodeUnavailable {
		t.Errorf("failed tool: %+v", e)
	}
	if e := asError(errors.New("nil map")); e.Code != CodeInternal {
		t.Errorf("bug: %+v", e)
	}
}
