package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/manifest"
)

func TestManifestHits(t *testing.T) {
	dir := t.TempDir()
	doc := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: parser
spec:
  template:
    spec:
      containers:
        - name: parser
          env:
            - name: VERBOSITY
              value: "0"
---
apiVersion: v1
kind: Secret
metadata:
  name: db
stringData:
  password: hunter2
`
	if err := os.WriteFile(filepath.Join(dir, "m.yml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := manifest.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range set.Objects {
		for _, h := range manifestHits(o) {
			if h.value == "hunter2" {
				t.Fatal("a Secret's value is searchable")
			}
			if h.path == "spec.template.spec.containers[0].env[0].value" {
				found = true
				if h.value != "0" || h.line != 11 {
					t.Fatalf("env value hit = %+v, want value 0 on yaml line 11 (0-based)", h)
				}
			}
		}
	}
	if !found {
		t.Fatal("no hit for the env value")
	}
}
