// Package docs carries rig's reference into the binary: rig docs, and the rig_docs MCP tool.
package docs

import (
	"embed"
	"strings"
)

//go:embed *.md
var FS embed.FS

// Names are the docs FS holds, the default first.
var Names = []string{"config", "guide", "design", "manifests"}

// Sections returns the ## / ### sections of a doc holding every word of query (case ignored),
// each under its heading, and the doc's headings for when none does.
func Sections(doc, query string) (found string, headings []string) {
	words := strings.Fields(strings.ToLower(query))
	var b strings.Builder
	flush := func(sec string) {
		low := strings.ToLower(sec)
		for _, w := range words {
			if !strings.Contains(low, w) {
				return
			}
		}
		b.WriteString(sec)
	}
	var sec strings.Builder
	for _, line := range strings.SplitAfter(doc, "\n") {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			flush(sec.String())
			sec.Reset()
			headings = append(headings, strings.TrimSpace(line))
		}
		sec.WriteString(line)
	}
	flush(sec.String())
	return b.String(), headings
}
