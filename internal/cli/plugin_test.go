package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goxang/rig/spec"
)

func TestSetYAMLKeepsCommentsAndNests(t *testing.T) {
	f := filepath.Join(t.TempDir(), "local.yaml")
	os.WriteFile(f, []byte("# mine\nenv: dev\nplugins:\n"), 0o644)
	if err := spec.SetYAML(f, []string{"plugins", "kube", "off"}, true); err != nil {
		t.Fatal(err)
	}
	if err := spec.SetYAML(f, []string{"plugins", "kube", "off"}, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f)
	got := string(raw)
	if !strings.Contains(got, "# mine") || !strings.Contains(got, "env: dev") || !strings.Contains(got, "kube:\n        off: false") && !strings.Contains(got, "kube:\n    off: false") {
		t.Fatalf("got:\n%s", got)
	}
}
