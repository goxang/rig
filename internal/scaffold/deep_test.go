package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeepFindsEntryPoints(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n")
	write("a/b/c/cmd/api/main.go", "package main\n\nfunc main() {}\n")
	write("a/lib/lib.go", "package lib\n")
	write("a/lib/lib_test.go", "package lib\n")
	write("tools/py/worker.py", "if __name__ == '__main__':\n    pass\n")
	write("deploy/web/Dockerfile", "FROM scratch\n")
	write("node_modules/x/main.go", "package main\nfunc main() {}\n")
	write("k8s/app.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: shop}\nspec: {template: {spec: {containers: [{image: shop:1}]}}}\n")
	write("charts/shop/Chart.yaml", "name: shop\n")

	cs, err := Deep(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cs {
		got[c.Kind+":"+c.Name] = c.Where
	}
	for _, want := range []string{"go:api", "python:worker", "dockerfile:web", "manifest:shop", "helm:shop", "tests:unit"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if _, ok := got["go:x"]; ok {
		t.Error("node_modules should be skipped")
	}
}
