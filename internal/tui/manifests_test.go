package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddService(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rig.yaml")
	in := "project: x\nservices:\n  api: { image: a }\n\n# envs\nenvironments:\n  k: {}\n"
	if err := os.WriteFile(f, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := addService(f, "redis", "infra", "[infra]", "redis:7", "deployment/redis", "k8s/redis.yml"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(f)
	want := "  api: { image: a }\n  redis:\n    role: infra\n    groups: [infra]\n    image: \"redis:7\"\n    k8s: { workload: deployment/redis, manifests: [k8s/redis.yml] }\n\n# envs\nenvironments:"
	if !strings.Contains(string(out), want) {
		t.Fatalf("got\n%s", out)
	}
}
