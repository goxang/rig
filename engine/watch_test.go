package engine

import "testing"

func TestIgnored(t *testing.T) {
	for _, c := range []struct {
		file string
		want bool
	}{
		{"app/main.go", false},
		{"app/main_test.go", true},
		{"node_modules/x/y.js", true},
		{"docs/a/b.go", true},
		{"app/README.md", true},
	} {
		if got := ignored(c.file, append(defaultIgnore, "docs/**")); got != c.want {
			t.Errorf("%s: got %v", c.file, got)
		}
	}
}
