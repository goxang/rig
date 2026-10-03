package viz

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type Rel struct {
	Node    string
	Label   string
	Missing bool
}

var (
	boxStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5794F2")).Bold(true)
	arrow     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#6E6E6E", Dark: "#7A7A7A"})
	nodeStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#222222", Dark: "#E0E0E0"})
	missing   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F2495C"))
	kindStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#B877D9"))
)

// RelationGraph draws one object in a box, what points at it on the left and what it points at on the right.
func RelationGraph(center string, in, out []Rel) string {
	rows := max(len(in), len(out), 1) + 2
	inW, inLab, outLab := 0, 0, 0
	for _, r := range in {
		inW = max(inW, lipgloss.Width(r.Node))
		inLab = max(inLab, lipgloss.Width(r.Label))
	}
	for _, r := range out {
		outLab = max(outLab, lipgloss.Width(r.Label))
	}
	boxW := lipgloss.Width(center) + 4
	// spread the edges over the box's rows, starting at the top
	at := func(list []Rel, row int) (Rel, bool) {
		i := row - (rows-len(list))/2
		if i < 0 || i >= len(list) {
			return Rel{}, false
		}
		return list[i], true
	}
	var b strings.Builder
	for row := 0; row < rows; row++ {
		if r, ok := at(in, row); ok {
			b.WriteString(strings.Repeat(" ", inW-lipgloss.Width(r.Node)) + node(r) + arrow.Render(" ─"+pad(r.Label, inLab, '─')+"─▶ "))
		} else {
			b.WriteString(strings.Repeat(" ", (inW+inLab+5)*min(1, len(in))))
		}
		switch row {
		case 0:
			b.WriteString(boxStyle.Render("┌" + strings.Repeat("─", boxW-2) + "┐"))
		case rows - 1:
			b.WriteString(boxStyle.Render("└" + strings.Repeat("─", boxW-2) + "┘"))
		case rows / 2:
			b.WriteString(boxStyle.Render("│ ") + kindName(center) + boxStyle.Render(" │"))
		default:
			b.WriteString(boxStyle.Render("│" + strings.Repeat(" ", boxW-2) + "│"))
		}
		if r, ok := at(out, row); ok {
			b.WriteString(arrow.Render(" ─"+pad(r.Label, outLab, '─')+"─▶ ") + node(r))
		}
		if row < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func node(r Rel) string {
	if r.Missing {
		return missing.Render(r.Node + " (missing)")
	}
	return kindName(r.Node)
}

func kindName(id string) string {
	k, n, ok := strings.Cut(id, "/")
	if !ok {
		return nodeStyle.Render(id)
	}
	return kindStyle.Render(k+"/") + nodeStyle.Bold(true).Render(n)
}

func pad(s string, w int, fill rune) string {
	return s + strings.Repeat(string(fill), max(0, w-lipgloss.Width(s)))
}
