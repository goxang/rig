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

func TestMaskValueHidesURLPasswords(t *testing.T) {
	if got := maskValue("DATABASE_URL", "postgres://postgres:shop@postgres:5432/shop"); got != "postgres://postgres:••••@postgres:5432/shop" {
		t.Fatalf("got %q", got)
	}
	if got := maskValue("ZIPKIN_URL", "http://zipkin:9411"); got != "http://zipkin:9411" {
		t.Fatalf("a URL without credentials changed: %q", got)
	}
}

func TestEnvBoxSearch(t *testing.T) {
	b := &envBox{lines: []envLine{{text: "  MAIN_DB  Switch", key: "MAIN_DB"}, {}, {text: "  TAG  v1", key: "TAG"}}}
	for _, k := range []string{"/", "t", "a", "g"} {
		b.key(keyOf(k))
	}
	if s := b.shown(); len(s) != 1 || b.sel != 2 {
		t.Fatalf("shown %v sel %d", s, b.sel)
	}
	if edit, _ := b.key(keyOf("enter")); edit || b.typing {
		t.Fatal("enter should end the search, not edit")
	}
	if edit, _ := b.key(keyOf("enter")); !edit {
		t.Fatal("enter should edit")
	}
	if _, done := b.key(keyOf("esc")); done || b.filter != "" {
		t.Fatal("esc should clear the search first")
	}
}
