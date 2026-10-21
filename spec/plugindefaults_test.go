package spec

import (
	"path/filepath"
	"testing"

	"github.com/goxang/rig/kernel/config"
)

func TestExtendsTemplateFromPlugin(t *testing.T) {
	old := PluginDefaults
	t.Cleanup(func() { PluginDefaults = old })
	PluginDefaults = func(string, map[string]*Plugin) []*config.Doc {
		d, err := config.Parse([]byte("templates:\n  owner: {instructions: answer the request, tools: [\"*:read\"]}\n"), "pack", "plugin pack")
		if err != nil {
			t.Fatal(err)
		}
		return []*config.Doc{d}
	}
	p, _, err := LoadData([]byte("version: 1\nproject: x\nplugins: {pack: {}}\nagents:\n  po: {extends: owner, on: unit.request}\n"), filepath.Join(t.TempDir(), "rig.yaml"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Agents["po"]; a == nil || a.Instructions != "answer the request" {
		t.Fatalf("agent po = %+v", a)
	}
}
