package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

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

func TestHighlightKeepsCellColours(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	open, _, _ := strings.Cut(sHover.Render("\x00"), "\x00")
	got := highlight(sHover, sGreen.Render("ok")+" x", 6)
	if !strings.HasPrefix(got, open) || !strings.Contains(got, "\x1b[0m"+open+" x  ") {
		t.Fatalf("%q", got)
	}
	if stripANSI(got) != "ok x" {
		t.Fatalf("text changed: %q", stripANSI(got))
	}
}
