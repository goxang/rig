package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginChecksAskForCredential(t *testing.T) {
	t.Setenv("RIG_TRUST", "project")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TK_TOKEN", "")
	os.Unsetenv("TK_TOKEN")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "tk"), 0o755)
	os.WriteFile(filepath.Join(dir, "tk/rig-plugin.yaml"), []byte("name: tk\nversion: 0.1.0\npermissions: {secrets: [TK_TOKEN]}\n"), 0o644)
	p := filepath.Join(dir, "rig.yaml")
	os.WriteFile(p, []byte("project: t\nenvironments:\n  test: { runtime: { type: fake } }\nplugins:\n  tk: { path: ./tk }\n"), 0o644)
	a, err := Open(p, "test")
	if err != nil {
		t.Fatal(err)
	}
	c := a.PluginChecks()
	if len(c) != 1 || c[0].Status != CheckWarn || !strings.Contains(c[0].Fix, "rig secret set TK_TOKEN") {
		t.Fatalf("%+v", c)
	}
	t.Setenv("TK_TOKEN", "x")
	if c := a.PluginChecks(); c[0].Status != CheckOK {
		t.Fatalf("%+v", c)
	}
}
