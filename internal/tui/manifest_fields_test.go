package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goxang/rig/manifest"
)

func TestManifestFieldEdit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "m.yml")
	doc := `# the api
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  labels: { app: api, tier: web }
spec:
  replicas: 2 # keep two
  template:
    spec:
      containers:
        - name: api
          env:
            - name: PORT
              value: "8080"
---
apiVersion: v1
kind: Secret
metadata:
  name: db
stringData:
  password: hunter2
`
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := manifest.Scan(file)
	if err != nil {
		t.Fatal(err)
	}
	dep, sec := set.Objects[0], set.Objects[1]

	if strings.Contains(string(yamlTree(sec.Node, true).bytes()), "hunter2") {
		t.Fatal("a Secret's value shows in its fields")
	}

	tree := &jsonTree{root: yamlTree(dep.Node, false)}
	tree.flatten()
	edit := func(path, v string) {
		tree.selectPath(path)
		n := tree.current()
		j := &jnode{kind: n.kind, scalar: n.scalar}
		j.set(v)
		y := yamlAt(dep.Node, n)
		replaceYAML(y, j)
		if ok, err := manifest.PatchScalar(dep, y); !ok || err != nil {
			t.Fatalf("%s: patched %v, %v", path, ok, err)
		}
	}
	edit("$.spec.replicas", "5")
	edit("$.spec.template.spec.containers[0].env[0].value", "9090")
	edit("$.metadata.labels.tier", "a, b")

	raw, _ := os.ReadFile(file)
	want := strings.NewReplacer("replicas: 2 #", "replicas: 5 #", `value: "8080"`, `value: "9090"`, "tier: web }", `tier: "a, b" }`).Replace(doc)
	if string(raw) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", raw, want)
	}
}
