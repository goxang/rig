package contracts

import (
	"testing"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/protocol"
)

func TestContractsLoadAndCheck(t *testing.T) {
	if len(IDs()) != 24 || Get("metrics").Kind != "metrics" || ForKind("messaging").ID != "queue/1" {
		t.Fatalf("contracts: %v", IDs())
	}
	for _, id := range IDs() {
		if c := Get(id); c.View != nil {
			var v view.View
			if err := v.Load(c.View); err != nil {
				t.Fatalf("%s view: %v", id, err)
			}
		}
	}
	c := Get("queue/1")
	p := &protocol.Provides{
		Sources: map[string]protocol.SourceSpec{"queue.list": {}, "queue.info": {}},
		Actions: map[string]protocol.ActionSpec{"queue.purge": {Effect: "danger"}, "queue.publish": {Effect: "read"}},
	}
	got := c.Check(p)
	if len(got) != 2 || got[0].Name != "queue.publish" || got[1].Name != "inspect" {
		t.Fatalf("problems: %v", got)
	}
	f := &protocol.Frame{Fields: []protocol.Field{{Name: "name", Type: "string"}, {Name: "messages", Type: "string"}}}
	if ps := fieldProblems("queue.list", map[string]string{"name": "string", "messages": "int", "ready": "int"}, f); len(ps) != 2 {
		t.Fatalf("field problems: %v", ps)
	}
}

func TestBridgesImplementTheirKinds(t *testing.T) {
	for _, id := range IDs() {
		c := Get(id)
		if c.Kind == "" {
			continue
		}
		v, err := Bridge(core.Kind(c.Kind), nil, "p", "c", nil)
		if err != nil || !plugin.Implements(core.Kind(c.Kind), v) {
			t.Errorf("%s: bridge %T does not implement %s (%v)", id, v, c.Kind, err)
		}
	}
	for _, o := range optional {
		found := false
		for _, id := range IDs() {
			if _, ok := Get(id).Caps[o.cap]; ok {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no contract has capability %s", o.iface.Name(), o.cap)
		}
	}
}

func TestCapabilitiesGateOptionalInterfaces(t *testing.T) {
	db, _ := Bridge(core.KindDatabase, nil, "p", "c", nil)
	if _, ok := core.As[core.Browser](db); ok {
		t.Fatal("browse served without the capability")
	}
	if _, ok := core.As[core.Querier](db); !ok {
		t.Fatal("a database always answers queries")
	}
	ct := ForKind("database")
	pr := &protocol.Provides{Sources: map[string]protocol.SourceSpec{}, Actions: map[string]protocol.ActionSpec{}}
	for _, s := range ct.Caps["browse"].Sources {
		pr.Sources[s] = protocol.SourceSpec{}
	}
	db, _ = Bridge(core.KindDatabase, nil, "p", "c", pr)
	if _, ok := core.As[core.Browser](db); !ok {
		t.Fatal("browse capability not admitted")
	}
}

func TestArgsSchema(t *testing.T) {
	s := Get("kv/1").ArgsSchema("kv.get")
	if r, _ := s["required"].([]string); len(r) != 1 || r[0] != "key" {
		t.Errorf("kv.get %v", s)
	}
	if s := Get("kv/1").ArgsSchema("kv.put"); len(s["properties"].(map[string]any)) != 2 {
		t.Errorf("kv.put %v", s)
	}
}
