package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdownFits(t *testing.T) {
	md := "# Title\n\nSome **bold** and `code` with a [link](https://example.com/a/very/long/path/that/goes/on).\n\n" +
		"- a bullet that is long enough to wrap around the narrow width of the box\n  - nested\n1. first\n\n" +
		"```go\nfunc main() {\n\tfmt.Println(\"a line of code far longer than the box is wide, which must wrap\")\n}\n```\n\n" +
		"| name | value |\n|---|---|\n| a | 1 |\n| b | a value too wide for the table to fit side by side here |\n\n> quoted text"
	out := renderMarkdown(md, 30)
	for _, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > 30 && !strings.HasPrefix(ansi.Strip(l), codeBarText) {
			t.Fatalf("line %d wide: %q", w, l)
		}
		if strings.Contains(l, "\t") {
			t.Fatalf("tab left in %q", l)
		}
	}
	for _, want := range []string{"Title", "•", "first", "name: ", "quoted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in\n%s", want, out)
		}
	}
}

func TestRenderMarkdownKeepsCodeLines(t *testing.T) {
	code := "fmt.Println(\"a line of code far longer than the box is wide\")"
	out := ansi.Strip(renderMarkdown("```go\n"+code+"\n```", 30))
	if !strings.Contains(out, codeBarText+code) {
		t.Fatalf("code line split:\n%s", out)
	}
}
