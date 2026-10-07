package ai

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		argv []string
		want Risk
	}{
		{[]string{"status"}, Read},
		{[]string{"kv", "get", "a/b"}, Read},
		{[]string{"kv", "put", "a/b", "1"}, Change},
		{[]string{"kv", "rm", "a/b"}, Danger},
		{[]string{"down"}, Danger},
		{[]string{"restart", "parsersvc"}, Change},
		{[]string{"scale", "core", "-2"}, Danger},
		{[]string{"scale", "core", "3"}, Change},
		{[]string{"query", "db", "SELECT * FROM t"}, Read},
		{[]string{"query", "db", "@Switch SELECT 1; DELETE FROM t"}, Danger},
		{[]string{"query", "db", "UPDATE t SET a=1 WHERE id=2"}, Change},
		{[]string{"query", "db", "/* x */ drop table t"}, Danger},
		{[]string{"query", "db", "EXEC dbo.p @a = 1"}, Change},
		{[]string{"query", "prom", "sum(rate(http_requests_total[1m]))"}, Read},
		{[]string{"query", "cache", "FLUSHALL"}, Danger},
		{[]string{"query", "k8s", "delete pod x"}, Danger},
		{[]string{"query", "saved-one"}, Read},
		{[]string{"secret", "get", "x"}, Refused},
		{[]string{"task"}, Read},
		{[]string{"task", "reset"}, Danger},
		{[]string{"infra", "down"}, Danger},
	}
	for _, c := range cases {
		if got := Classify(c.argv); got != c.want {
			t.Errorf("%v: got %s, want %s", c.argv, got, c.want)
		}
	}
}

func TestQuoted(t *testing.T) {
	msg := "Please  STOP all services on staging now"
	for q, want := range map[string]bool{
		"stop all services":    true,
		`"stop all  services"`: true,
		"stop":                 false, // too short to prove anything
		"delete the database":  false,
		"":                     false,
	} {
		if got := Quoted(msg, q); got != want {
			t.Errorf("%q: got %v", q, got)
		}
	}
	if !Quoted("yes", "yes") {
		t.Error("a whole short message counts")
	}
}

func TestCleanCompletion(t *testing.T) {
	for _, c := range [][3]string{
		{"SELECT * FR", "OM t", "OM t"},
		{"SELECT * FR", "SELECT * FROM t", "OM t"},
		{"echo hello wor", "```sh\nld\n```", "ld"},
		{"a ", " b", "b"},
	} {
		if got := cleanCompletion(c[0], c[1]); got != c[2] {
			t.Errorf("%q + %q: got %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestResolveProviders(t *testing.T) {
	s := Resolve(Config{Provider: "deepseek", Backend: BackendClaude})
	if s.Enabled() {
		t.Error("deepseek must run through opencode")
	}
	s = Resolve(Config{Provider: "openai", Backend: BackendOpencode, URL: "http://x/v1"})
	if s.Enabled() || s.Why == "" {
		t.Error("openai without a model is not usable")
	}
	s = Resolve(Config{Provider: "ollama", Backend: BackendOpencode, Model: "qwen2.5-coder"})
	if s.URL != "http://localhost:11434/v1" || s.APIKey != "" {
		t.Errorf("ollama: url %q, key %q", s.URL, s.APIKey)
	}
	if s = Resolve(Config{Provider: "lmstudio", Backend: BackendOpencode}); s.Enabled() {
		t.Error("lmstudio without a model is not usable")
	}
}

func TestDeniedPath(t *testing.T) {
	for rel, want := range map[string]bool{
		".env": true, "svc/.env": true, ".env.local": true, "certs/server.pem": true, "a/b/secrets/x.json": true,
		"configs/kube/app.json": true, "configs": true, "main.go": false, "envs/readme.md": false, "pkg/key.go": false,
	} {
		if got := DeniedPath(rel, []string{"configs/**"}); got != want {
			t.Errorf("DeniedPath(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestFlattenKeepsAMultiLineQueryWhole(t *testing.T) {
	got := flatten("```sql\nSELECT *\n  FROM t\nWHERE a = 1\n```")
	if got != "SELECT * FROM t WHERE a = 1" {
		t.Fatalf("flatten = %q", got)
	}
}

func TestSplitNext(t *testing.T) {
	rest, next := SplitNext("The answer.\n\nNEXT: restart parser\n")
	if rest != "The answer." || next != "restart parser" {
		t.Fatalf("%q %q", rest, next)
	}
	if rest, next := SplitNext("no suggestion"); rest != "no suggestion" || next != "" {
		t.Fatalf("%q %q", rest, next)
	}
}
