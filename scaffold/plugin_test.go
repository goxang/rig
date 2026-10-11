package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/host"
)

func TestNewPlugin(t *testing.T) {
	for _, lang := range Langs {
		dir := filepath.Join(t.TempDir(), "p")
		files, err := NewPlugin(dir, "sentry-issues", lang)
		if err != nil {
			t.Fatal(lang, err)
		}
		m, err := host.LoadManifest(dir)
		if err != nil || len(m.Warnings) > 0 {
			t.Fatalf("%s: %v %v (%v)", lang, err, m.Warnings, files)
		}
		if m.Name != "sentry-issues" || len(m.Views) != 1 || len(m.Skills) != 1 {
			t.Errorf("%s: %+v", lang, m)
		}
		if _, err := os.Stat(filepath.Join(dir, "skills", "sentry-issues", "SKILL.md")); err != nil {
			t.Error(lang, err)
		}
	}
	if _, err := NewPlugin(t.TempDir(), "Bad_Name", "go"); err == nil {
		t.Error("a bad name passed")
	}
}

// TestVendoredSDKs fails when an SDK changed and go generate ./scaffold has not copied it.
func TestVendoredSDKs(t *testing.T) {
	for vendored, src := range map[string]string{
		"plugin/python/rig/__init__.py": "../sdk/python/rig/__init__.py", "plugin/python/rig/protocol.py": "../sdk/python/rig/protocol.py",
		"plugin/ts/rig/index.js": "../sdk/typescript/index.js", "plugin/ts/rig/index.d.ts": "../sdk/typescript/index.d.ts",
		"plugin/ts/rig/protocol.js": "../sdk/typescript/protocol.js", "plugin/ts/rig/protocol.d.ts": "../sdk/typescript/protocol.d.ts",
	} {
		a, _ := pluginFS.ReadFile(vendored)
		b, err := os.ReadFile(src)
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs from %s: go generate ./scaffold", vendored, src)
		}
	}
}
