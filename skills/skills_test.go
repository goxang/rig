package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The built-in AI, given the rig-plugin skill, writes a plugin that passes rig plugin test. It runs
// the AI set up with rig ai config, so it is opt-in: RIG_AI_CHECK=1 go test -run Sentry ./skills
func TestAIWritesASentryPlugin(t *testing.T) {
	if os.Getenv("RIG_AI_CHECK") == "" {
		t.Skip("RIG_AI_CHECK unset")
	}
	bin := filepath.Join(t.TempDir(), "rig")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/rig").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	project := "version: 1\nproject: sentrycheck\ndefault: dev\nenvironments:\n  dev: {runtime: {type: local}}\n"
	if err := os.WriteFile(filepath.Join(dir, "rig.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	rig := func(in string, args ...string) {
		t.Helper()
		c := exec.Command(bin, args...)
		c.Dir, c.Env = in, append(os.Environ(), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := c.CombinedOutput()
		t.Logf("rig %v:\n%s", args, out)
		if err != nil {
			t.Fatalf("rig %v: %v", args, err)
		}
	}
	rig(dir, "ai", "write a plugin that shows Sentry issues as a tab")
	found, _ := filepath.Glob(filepath.Join(dir, "*", "rig-plugin.yaml"))
	if len(found) != 1 {
		t.Fatalf("want one plugin folder, found %v", found)
	}
	rig(filepath.Dir(found[0]), "plugin", "test")
}
