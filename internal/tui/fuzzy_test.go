package tui

import "testing"

func TestFuzzy(t *testing.T) {
	for _, c := range []struct {
		s, q string
		want bool
	}{
		{"god-object/hsm config enable", "godhsmconfig", true},
		{"god-object/hsm config enable", "hsm god", true},
		{"god-object/hsm config enable", "hsmgod", false},
		{"Shaparak", "SHP", true},
		{"abc", "abcd", false},
	} {
		if got := fuzzy(c.s, c.q); got != c.want {
			t.Errorf("fuzzy(%q, %q) = %v", c.s, c.q, got)
		}
	}
	if matchTier("god-object", "hsm", "hsm") != 0 || matchTier("god-object", "x", "gdo") != 1 || matchTier("a", "b", "z") != -1 {
		t.Error("matchTier")
	}
}
