package tui

import "testing"

func TestRowMatcher(t *testing.T) {
	cols := []string{"ID", "Destination", "Status"}
	pos := []string{"1", "Pos", "ok"}
	ipg := []string{"2", "IPG", "failed"}
	for _, c := range []struct {
		filter   string
		pos, ipg bool
	}{
		{"Destination=Pos", true, false},
		{"Dest = Pos", true, false},
		{"dest=pos status!=failed", true, false},
		{"status~fail", false, true},
		{"*pg", false, true},
		{"fld", false, true},
		{"nosuchcol=1", false, false},
		{"", true, true},
	} {
		m := rowMatcher(c.filter, cols)
		if m(pos) != c.pos || m(ipg) != c.ipg {
			t.Errorf("%q: pos %v ipg %v, want %v %v", c.filter, m(pos), m(ipg), c.pos, c.ipg)
		}
	}
}
