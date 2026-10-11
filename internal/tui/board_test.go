package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/goxang/rig/protocol"
)

func TestBoardSortsAndScrolls(t *testing.T) {
	f := &protocol.Frame{
		Fields: []protocol.Field{{Name: "key"}, {Name: "title"}, {Name: "status"}},
		Columns: [][]any{
			{"PIN-10", "PIN-9", "PIN-100", "PIN-1"},
			{"ten", "nine", "hundred", "one"},
			{"A", "A", "B", "C"},
		},
	}
	wn := &wnode{n: protocol.Node{Widget: "board"}, frame: f}
	out := boardWidget(wn, 50, 10, true)
	st := wn.st.(*boardState)
	if got := st.cols[0]; got[0] != 0 || got[1] != 1 {
		t.Fatalf("unsorted board keeps frame order, got %v", got)
	}
	if w := lipgloss.Width(out); w > 50 {
		t.Fatalf("board is %d wide in 50", w)
	}
	if !strings.Contains(out, "›") || strings.Contains(out, "C 1") {
		t.Fatalf("three columns in 50 cells should scroll:\n%s", out)
	}
	st.key("alt+up")
	boardWidget(wn, 50, 10, true)
	if got := st.cols[0]; got[0] != 1 || got[1] != 0 {
		t.Fatalf("PIN-9 sorts before PIN-10, got %v", got)
	}
	st.key("right")
	st.key("right")
	if out := boardWidget(wn, 50, 10, true); !strings.Contains(out, "C") || !strings.Contains(out, "‹") {
		t.Fatalf("the cursor's column scrolls into view:\n%s", out)
	}
}
