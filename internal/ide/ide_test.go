package ide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShowInServices(t *testing.T) {
	for name, ws := range map[string]string{
		"none":    "<project version=\"4\">\n</project>\n",
		"partial": "<project>\n  <component name=\"RunDashboard\">\n    <option name=\"configurationTypes\">\n      <set>\n        <option value=\"GoRemoteDebugConfigurationType\" />\n      </set>\n    </option>\n  </component>\n</project>\n",
		"empty":   "<project>\n  <component name=\"RunDashboard\">\n  </component>\n</project>\n",
	} {
		f := filepath.Join(t.TempDir(), "workspace.xml")
		os.WriteFile(f, []byte(ws), 0o644)
		if err := showInServices(f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := showInServices(f); err != nil {
			t.Fatalf("%s twice: %v", name, err)
		}
		raw, _ := os.ReadFile(f)
		for _, typ := range dashboardTypes {
			if n := strings.Count(string(raw), `<option value="`+typ+`" />`); n != 1 {
				t.Errorf("%s: %s %d times:\n%s", name, typ, n, raw)
			}
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"parser": "parser", "/a b/rig": "'/a b/rig'", "it's": `'it'\''s'`} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
