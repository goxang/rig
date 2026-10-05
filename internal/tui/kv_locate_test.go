package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateKey(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	domain := write("domainsvc.json", "{\n  \"Zipkin\": {\n    \"SamplingRate\": 1\n  },\n  \"SaveTransactionFields\": true\n}\n")
	loadDomain := write("loadtestdomainsvc.json", "{\n  \"SaveTransactionFields\": true\n}\n")
	parser := write("parsersvc.json", "{\n  \"Zipkin\": {\n    \"SamplingRate\": 1\n  }\n}\n")
	files := []string{domain, loadDomain, parser}

	if got, line := locateKey(files, "switch_v2/domainsvc/domainsvc", "$.SaveTransactionFields"); len(got) != 1 || got[0] != domain || line != 5 {
		t.Fatalf("named file: %v line %d", got, line)
	}
	if got, line := locateKey(files, "switch_v2/domainsvc/loadtestv2svc", "$.SaveTransactionFields"); len(got) != 2 || line != 1 {
		t.Fatalf("by field: %v line %d, want both files holding it", got, line)
	}
	if got, line := locateKey(files, "switch_v2/parsersvc/parsersvc", ""); len(got) != 1 || got[0] != parser || line != 1 {
		t.Fatalf("key only: %v line %d", got, line)
	}
}
