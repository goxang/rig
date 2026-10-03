package kv

import "testing"

func TestFieldParent(t *testing.T) {
	doc := map[string]any{"A": map[string]any{"Rate": 5.0}}
	p, leaf := fieldParent(doc, "A.Rate", false)
	if p[leaf] != 5.0 {
		t.Fatalf("read nested: %v", p[leaf])
	}
	p, leaf = fieldParent(doc, "B.C.Rate", true)
	p[leaf] = 7.0
	if doc["B"].(map[string]any)["C"].(map[string]any)["Rate"] != 7.0 {
		t.Fatalf("create nested: %v", doc)
	}
	if p, leaf := fieldParent(doc, "X.Rate", false); p[leaf] != nil {
		t.Fatal("missing path must read as zero")
	}
}
