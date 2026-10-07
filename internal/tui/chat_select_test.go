package tui

import "testing"

func TestChatSelectionSpansLinesAndDropsCodeBar(t *testing.T) {
	c := &chat{convo: &convo{}, lines: []string{"intro text", codeBarText + "go build ./...", "after"}, rows: []int{0, 1, 2}, bw: 40, bh: 3}
	m := &model{}
	c.drag(m, 6, 0, dragPress)
	c.drag(m, 10, 2, dragMove)
	if got := c.selection(); got != "text\ngo build ./...\nafter" {
		t.Fatalf("selection = %q", got)
	}
	c.drag(m, 39, 2, dragMove)
	if c.hoff == 0 {
		t.Fatal("dragging past the right edge did not scroll sideways")
	}
	c.drag(m, 0, -1, dragMove)
	if c.scroll == 0 {
		t.Fatal("dragging above the box did not scroll up")
	}
}
