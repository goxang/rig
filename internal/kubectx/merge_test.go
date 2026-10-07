package kubectx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeReplacesAndKeepsCurrent(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("no kubectl")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config")
	old := `apiVersion: v1
kind: Config
clusters: [{name: a, cluster: {server: "https://a"}}, {name: r, cluster: {server: "https://old"}}]
contexts: [{name: a, context: {cluster: a, user: u}}, {name: r, context: {cluster: r, user: ru}}]
users: [{name: u, user: {token: x}}, {name: ru, user: {token: oldtok}}]
current-context: a
`
	fresh := `apiVersion: v1
kind: Config
clusters: [{name: r, cluster: {server: "https://rancher.x/k8s/clusters/local"}}]
contexts: [{name: r, context: {cluster: r, user: ru}}]
users: [{name: ru, user: {token: newtok}}]
current-context: r
`
	if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", file)
	got, names, err := Merge([]byte(fresh))
	if err != nil || got != file || len(names) != 1 || names[0] != "r" {
		t.Fatalf("Merge = %q %v %v", got, names, err)
	}
	out, _ := os.ReadFile(file)
	s := string(out)
	for _, want := range []string{"newtok", "https://rancher.x/k8s/clusters/local", "current-context: a", "https://a"} {
		if !strings.Contains(s, want) {
			t.Errorf("merged kubeconfig lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "oldtok") {
		t.Errorf("old token kept:\n%s", s)
	}
	if b, _ := os.ReadFile(file + ".rig-backup"); string(b) != old {
		t.Error("no backup of the old kubeconfig")
	}
	if c, err := Resolve("", "https://rancher.x/k8s/clusters/local"); err != nil || c != "r" {
		t.Errorf("Resolve = %q %v", c, err)
	}
}
