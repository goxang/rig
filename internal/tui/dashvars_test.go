package tui

import (
	"slices"
	"testing"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

func TestVarsByDependency(t *testing.T) {
	d := &spec.Dashboard{VarOrder: []string{"job", "node", "nodename"}, Vars: map[string]*spec.DashVar{
		"job":      {Query: "node_uname_info"},
		"node":     {Query: `node_uname_info{job="$job", nodename="$nodename"}`},
		"nodename": {Query: `node_uname_info{job="${job}"}`},
	}}
	if got := varsByDependency(d); !slices.Equal(got, []string{"job", "nodename", "node"}) {
		t.Fatalf("order %v", got)
	}
	if !dependsOn(d, "nodename") || dependsOn(d, "node") {
		t.Fatal("dependsOn")
	}
	if got := engine.DefaultVar(&spec.DashVar{Default: "pep"}, []string{"a", "b"}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("stale default kept: %v", got)
	}
}
