package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileAction(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one two two"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	t.Setenv("RIG_AI_ENV", "")
	run := func(args map[string]any) (string, error) { return fileAction(root, []string{"configs/**"}, args) }

	for _, p := range []string{"../x", "/etc/passwd", ".env", "configs/app.json", ".git/config", "escape/new.txt"} {
		if _, err := run(map[string]any{"action": "write", "path": p, "content": "x"}); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("write %s: %v, want refused", p, err)
		}
	}
	if _, err := run(map[string]any{"action": "edit", "path": "a.txt", "old": "two", "new": "2"}); err == nil {
		t.Error("an ambiguous edit went through")
	}
	if _, err := run(map[string]any{"action": "edit", "path": "a.txt", "old": "one", "new": "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(map[string]any{"action": "write", "path": "d/b.txt", "content": "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(map[string]any{"action": "move", "path": "d/b.txt", "to": "c.txt"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := run(map[string]any{"action": "read", "path": "a.txt"}); out != "1 two two" {
		t.Errorf("read %q", out)
	}
	if out, _ := run(map[string]any{"action": "list"}); strings.Contains(out, ".env") || !strings.Contains(out, "c.txt") {
		t.Errorf("list %q", out)
	}
	os.MkdirAll(filepath.Join(root, "p", "secrets"), 0o755)
	os.WriteFile(filepath.Join(root, "p", "secrets", "k"), []byte("x"), 0o600)
	if _, err := run(map[string]any{"action": "delete", "path": "p", "confirm": true}); err == nil {
		t.Error("deleted a directory holding secrets")
	}
	if _, err := run(map[string]any{"action": "delete", "path": "c.txt"}); err == nil {
		t.Error("deleted without a go-ahead")
	}
	if _, err := run(map[string]any{"action": "delete", "path": "c.txt", "confirm": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); !os.IsNotExist(err) {
		t.Error("c.txt still there")
	}
}
