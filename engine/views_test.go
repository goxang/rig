package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/kernel/view"
)

func TestHomeFileReloads(t *testing.T) {
	l := openLocal(t, "dev")
	l.App.Spec.ViewOrder = []string{"Access"}
	l.App.Spec.Views = map[string]*view.View{"Access": {Slot: "home", File: "access.yaml"}}
	file := filepath.Join(l.App.Spec.Dir, "access.yaml")
	for _, text := range []string{"prepared", "refreshed"} {
		if err := os.WriteFile(file, []byte("widget: markdown\ntext: "+text), 0o600); err != nil {
			t.Fatal(err)
		}
		got := Views(l.App)[0].View.Root.Children[0].Children[0]
		if got.Widget != "markdown" || got.Props["text"] != text {
			t.Fatalf("home file was not reloaded: %+v", got)
		}
	}
}
