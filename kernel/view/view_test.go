package view

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

func TestParseViews(t *testing.T) {
	var vs map[string]*View
	err := yaml.Unmarshal([]byte(`
Payments:
  slot: tab
  layout:
    grid:
      - - { widget: stat, title: TPS, source: metrics, query: 'sum(rate(x[1m]))', unit: /s }
        - { widget: stat, title: failed, source: db, query: "SELECT 1" }
      - - widget: table
          source: db
          query: SELECT * FROM t
          refresh: 10s
          transform: [ { filter: status=FAILED }, { limit: 5 } ]
Games:
  slot: { tab: { key: G } }
  tabs:
    Snake: { widget: terminal, command: go run ./snake }
    "2048": { widget: terminal, command: ./2048.sh, keep: true }
`), &vs)
	if err != nil {
		t.Fatal(err)
	}
	p := vs["Payments"].Root
	if p.Widget != "grid" || len(p.Children) != 2 || p.Children[0].Widget != "row" || len(p.Children[0].Children) != 2 {
		t.Fatalf("grid: %+v", p)
	}
	stat := p.Children[0].Children[0]
	if stat.Bind == nil || stat.Bind.Source != "metrics" || stat.Bind.Args["query"] != "sum(rate(x[1m]))" || Prop(stat, "unit") != "/s" {
		t.Fatalf("stat: %+v", stat)
	}
	table := p.Children[1].Children[0]
	if table.Bind.Every != "10s" || table.Props["transform"] == nil {
		t.Fatalf("table: %+v", table)
	}
	g := vs["Games"]
	if g.Slot != "tab" || g.Key != "G" || g.Root.Widget != "tabs" || Prop(g.Root.Children[1], "title") != "2048" || g.Root.Children[1].Props["keep"] != true {
		t.Fatalf("games: %+v", g)
	}
}

func frame() *protocol.Frame {
	return frames.FromRows([]protocol.Field{{Name: "svc", Type: "string"}, {Name: "status", Type: "string"}, {Name: "ms", Type: "number"}},
		[]map[string]any{
			{"svc": "api", "status": "OK", "ms": 10.0},
			{"svc": "api", "status": "FAILED", "ms": 30.0},
			{"svc": "worker", "status": "FAILED", "ms": 5.0},
		})
}

func TestTransforms(t *testing.T) {
	var ops any
	_ = yaml.Unmarshal([]byte(`
- filter: status=FAILED ms>=5
- join: { source: rt, on: { svc: name } }
- group: { by: svc, agg: { n: count, total: sum(ms), state: first(state) } }
- sort: -total
- rename: { svc: service }
- select: service,n,total,state
- limit: 1
`), &ops)
	other := func(b protocol.Binding) (*protocol.Frame, error) {
		return frames.FromRows([]protocol.Field{{Name: "name"}, {Name: "state"}}, []map[string]any{{"name": "api", "state": "up"}}), nil
	}
	f, err := Apply(frame(), ops, other)
	if err != nil {
		t.Fatal(err)
	}
	rows := frames.Rows(f)
	if len(rows) != 1 || rows[0]["service"] != "api" || rows[0]["n"] != 1.0 || rows[0]["total"] != 30.0 || rows[0]["state"] != "up" || len(f.Fields) != 4 {
		t.Fatalf("rows %v fields %v", rows, f.Fields)
	}
}

func TestFetcherSharesCachesAndBacksOff(t *testing.T) {
	var calls atomic.Int32
	fail := atomic.Bool{}
	gate := make(chan struct{})
	now := time.Unix(0, 0)
	f := &Fetcher{now: func() time.Time { return now }, Query: func(ctx context.Context, s string, a protocol.Args) (*protocol.Frame, error) {
		calls.Add(1)
		<-gate
		if fail.Load() {
			return nil, errors.New("down")
		}
		return frame(), nil
	}}
	b := protocol.Binding{Source: "db", Args: protocol.Args{"query": "q"}, Every: "5s"}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { _, _ = f.Get(context.Background(), b) })
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("5 widgets made %d fetches", calls.Load())
	}
	_, _ = f.Get(context.Background(), b)
	if calls.Load() != 1 {
		t.Fatal("fetched again inside the TTL")
	}
	now = now.Add(6 * time.Second)
	fail.Store(true)
	if fr, err := f.Get(context.Background(), b); err == nil || calls.Load() != 2 {
		t.Fatalf("after the TTL: err %v calls %d", err, calls.Load())
	} else if fr != nil {
		t.Fatal("a failed fetch returned a frame")
	}
	_, err := f.Get(context.Background(), b)
	if err == nil || calls.Load() != 2 {
		t.Fatal("retried inside the backoff")
	}
	now = now.Add(3 * time.Second)
	fail.Store(false)
	if _, err := f.Get(context.Background(), b); err != nil || calls.Load() != 3 {
		t.Fatalf("after backoff: %v %d", err, calls.Load())
	}
}

func TestSupported(t *testing.T) {
	for w, want := range map[string]bool{"calendar": true, "calendar@1": true, "calendar@2": false, "table@x": false, "agenda": false} {
		if got := Supported(protocol.Node{Widget: w}); got != want {
			t.Errorf("%s: %v", w, got)
		}
	}
}
