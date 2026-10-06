package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	_ "github.com/goxang/rig/adapters/all"
	"github.com/goxang/rig/engine"
)

func TestEnvInfo(t *testing.T) {
	a, err := engine.Open("../../examples/shop/rig.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(envInfoBox(context.Background(), a).plainText())
	for _, want := range []string{"environment", a.Env.Name, "components", "database=shop"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if maskValue("DB_PASSWORD", "x") == "x" || maskValue("MAIN_DB", "Switch") != "Switch" {
		t.Error("masking")
	}
}

func TestEnvBoxScrollAndSelect(t *testing.T) {
	box := &envBox{env: "test"}
	for i := 0; i < 5; i++ {
		box.lines = append(box.lines, envLine{text: fmt.Sprintf("header %d", i)})
	}
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("VAR_%d", i)
		box.lines = append(box.lines, envLine{text: k + " value", key: k})
	}
	// a short height must not panic, and the selected row must stay visible once scrolled past it
	box.sel = 22
	out := ansi.Strip(box.view(8))
	if !strings.Contains(out, "VAR_17") {
		t.Errorf("scrolled view does not keep the selected row visible:\n%s", out)
	}

	box.sel, box.offset = 0, 0
	box.sel = min(box.sel+30, len(box.lines)-1)
	if box.sel != len(box.lines)-1 {
		t.Errorf("down clamp: got sel %d, want %d", box.sel, len(box.lines)-1)
	}
	box.sel = max(0, box.sel-100)
	if box.sel != 0 {
		t.Errorf("up clamp: got sel %d, want 0", box.sel)
	}
}
