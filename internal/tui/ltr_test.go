package tui

import (
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/goxang/rig/kernel/view"
)

func TestLTRKeepsWidth(t *testing.T) {
	s := padRight("بررسی مپ خطا ها", 20)
	if s[:3] != "\u200e" || lipgloss.Width(s) != 20 {
		t.Fatalf("%q width %d", s, lipgloss.Width(s))
	}
	if padRight("abc", 4) != "abc " {
		t.Fatal("latin text changed")
	}
}

func TestLTRTruncatedStaysFenced(t *testing.T) {
	s := padRight("یکپارچه سازی load Config ها و envها", 10)
	if lipgloss.Width(s) != 10 || s[len(s)-3:] != "\u200e" {
		t.Fatalf("%q", s)
	}
}

func TestShowViewByMode(t *testing.T) {
	jira := &viewTab{title: "Jira", v: &view.View{Modes: []string{"software", "product"}}}
	games := &viewTab{title: "Games", v: &view.View{}}
	for _, c := range []struct {
		mode string
		tab  *viewTab
		want bool
	}{
		{"product", jira, true}, {"product", games, false}, {"software", games, true}, {"detailed", games, true},
	} {
		if got := showView(c.mode, c.tab); got != c.want {
			t.Errorf("%s %s = %v", c.mode, c.tab.title, got)
		}
	}
	if showScreen("product", "logs") || !showScreen("software", "load") || showScreen("simple", "load") {
		t.Error("showScreen")
	}
}
