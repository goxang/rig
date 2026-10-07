package tui

import (
	"strings"
	"testing"
)

func TestJobKeepsLinesAndProgressBars(t *testing.T) {
	j := &job{}
	j.Write([]byte("→ api: build\npulling 10%\rpulling 50%\rpulling 100%\npart"))
	j.Write([]byte("ial\r\n"))
	if got := strings.Join(j.output(), "|"); got != "→ api: build|pulling 100%|partial" {
		t.Fatalf("output = %q", got)
	}
	if _, last, _ := j.state(); last != "partial" {
		t.Fatalf("last = %q", last)
	}
}

func TestCommandMenuAndNearest(t *testing.T) {
	if m := cmdMenu("/mo"); len(m) != 1 || m[0].name != "/model" {
		t.Fatalf("menu = %v", m)
	}
	if m := cmdMenu("/model x"); m != nil {
		t.Fatalf("a command with words after it has no menu: %v", m)
	}
	if n := nearestCommand("/sesions"); n != "/sessions" {
		t.Fatalf("nearest = %q", n)
	}
	if n := nearestCommand("/deploy-everything"); n != "" {
		t.Fatalf("nothing is near, got %q", n)
	}
}
