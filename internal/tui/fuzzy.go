package tui

import (
	"strings"
	"unicode/utf8"
)

// fuzzy reports whether every space-separated word of q appears in s with its letters in order,
// gaps allowed (fzf style): "godhsmconfig" finds "god-object/hsm config enable". Case is ignored.
func fuzzy(s, q string) bool {
	s = strings.ToLower(s)
	for _, w := range strings.Fields(strings.ToLower(q)) {
		rest := s
		for _, r := range w {
			i := strings.IndexRune(rest, r)
			if i < 0 {
				return false
			}
			rest = rest[i+utf8.RuneLen(r):]
		}
	}
	return true
}

// matchTier ranks a filter hit: 0 the text holds q as typed, 1 only fuzzy on name, -1 no match.
// Values (desc) match only as typed: fuzzy over long free text matches nearly anything.
func matchTier(name, desc, q string) int {
	if q == "" || strings.Contains(strings.ToLower(name+" "+desc), strings.ToLower(q)) {
		return 0
	}
	if fuzzy(name, q) {
		return 1
	}
	return -1
}
