package docs

import (
	"strings"
	"testing"
)

func TestSections(t *testing.T) {
	doc := "# t\nintro\n## tasks\nrun steps\n### step help\nshown in T\n## alerts\ncpu\n"
	got, heads := Sections(doc, "step")
	if !strings.Contains(got, "## tasks") || !strings.Contains(got, "### step help") || strings.Contains(got, "alerts") {
		t.Fatalf("got %q", got)
	}
	if len(heads) != 3 {
		t.Fatalf("headings %v", heads)
	}
	if got, _ := Sections(doc, "nothing here"); got != "" {
		t.Fatalf("got %q", got)
	}
}
