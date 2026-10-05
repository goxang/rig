package tui

import (
	"context"
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
	text := ansi.Strip(envInfoText(context.Background(), a))
	for _, want := range []string{"environment", a.Env.Name, "components", "database=shop"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if maskValue("DB_PASSWORD", "x") == "x" || maskValue("MAIN_DB", "Switch") != "Switch" {
		t.Error("masking")
	}
}
