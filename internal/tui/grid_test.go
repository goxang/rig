package tui

import "testing"

func TestPrintable(t *testing.T) {
	if got := printable("a\tb\r\nc\x07\x1b[1md"); got != "a b  c\x1b[1md" {
		t.Fatalf("%q", got)
	}
}

func TestGlobMatcher(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"", "x", true}, {"sett", "parser.settlement.Request", true}, {"*settle*", "parser.settlement.Request", true},
		{"parser*", "dispatcher.parser", false}, {"*.request.*", "parser.settlement.Request.*.*.*", true},
	}
	for _, c := range cases {
		if got := globMatcher(c.p)(c.s); got != c.want {
			t.Errorf("%q ~ %q = %v", c.p, c.s, got)
		}
	}
}
