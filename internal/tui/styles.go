package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/MohammadmahdiAhmadi/rig/core"
)

var (
	cAccent = lipgloss.Color("#5794F2")
	cGreen  = lipgloss.Color("#73BF69")
	cAmber  = lipgloss.Color("#FF9830")
	cRed    = lipgloss.Color("#F2495C")
	cPurple = lipgloss.Color("#B877D9")
	cText   = lipgloss.AdaptiveColor{Light: "#1F1F1F", Dark: "#D8D9DA"}
	cDim    = lipgloss.AdaptiveColor{Light: "#7A7A7A", Dark: "#7B7F85"}
	cPanel  = lipgloss.AdaptiveColor{Light: "#D0D0D0", Dark: "#2C3235"}
	cBar    = lipgloss.AdaptiveColor{Light: "#ECECEC", Dark: "#181B1F"}

	sTitle    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sDim      = lipgloss.NewStyle().Foreground(cDim)
	sAccent   = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sGreen    = lipgloss.NewStyle().Foreground(cGreen)
	sAmber    = lipgloss.NewStyle().Foreground(cAmber)
	sRed      = lipgloss.NewStyle().Foreground(cRed)
	sSelected = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#DCE7FB", Dark: "#22344F"}).Bold(true)
	sTabOn    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent).Bold(true).Padding(0, 1)
	sTabOff   = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sHeader   = lipgloss.NewStyle().Background(cBar).Foreground(cText)
	sKey      = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
)

// panel draws a titled, rounded box exactly w×h.
func panel(title, body string, w, h int, focused bool) string {
	var border lipgloss.TerminalColor = cPanel
	if focused {
		border = cAccent
	}
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Width(w - 2).Height(h - 2).MaxHeight(h)
	box := st.Render(clip(body, w-2, h-2))
	if title == "" {
		return box
	}
	// write the title into the top border
	lines := strings.SplitN(box, "\n", 2)
	t := " " + title + " "
	if lipgloss.Width(t) > w-4 {
		t = truncate(t, w-4)
	}
	tb := lipgloss.NewStyle().Foreground(border).Render("╭─") + sTitle.Render(t) +
		lipgloss.NewStyle().Foreground(border).Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(t)))+"╮")
	if len(lines) == 2 {
		return tb + "\n" + lines[1]
	}
	return tb
}

// clip cuts body to w columns and h lines so a panel never grows.
func clip(body string, w, h int) string {
	lines := strings.Split(body, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = truncate(l, w)
		}
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// ANSI-aware: lipgloss MaxWidth cuts styled text by cells
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return truncate(s, w)
}

func stateDot(s core.State) string {
	switch s {
	case core.StateRunning:
		return sGreen.Render("●")
	case core.StateStarting, core.StateDegraded:
		return sAmber.Render("◐")
	case core.StateFailed:
		return sRed.Render("✖")
	case core.StateStopped, core.StateAbsent:
		return sDim.Render("○")
	}
	return sDim.Render("?")
}

func stateText(s core.State) string {
	switch s {
	case core.StateRunning:
		return sGreen.Render(string(s))
	case core.StateStarting, core.StateDegraded:
		return sAmber.Render(string(s))
	case core.StateFailed:
		return sRed.Render(string(s))
	}
	return sDim.Render(string(s))
}

// table renders rows under a header with fixed column widths; sel highlights one row.
func table(cols []string, widths []int, rows [][]string, sel, offset, h int) string {
	var b strings.Builder
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = padRight(c, widths[i])
	}
	b.WriteString(sDim.Render(strings.Join(head, " ")) + "\n")
	for i := offset; i < len(rows) && i-offset < h-1; i++ {
		cells := make([]string, len(cols))
		for j := range cols {
			v := ""
			if j < len(rows[i]) {
				v = rows[i][j]
			}
			cells[j] = padRight(v, widths[j])
		}
		line := strings.Join(cells, " ")
		if i == sel {
			line = sSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// keep the selection inside the visible window of h rows
func scroll(sel, offset, h, n int) int {
	if h <= 0 {
		return 0
	}
	if sel < offset {
		return sel
	}
	if sel >= offset+h {
		return sel - h + 1
	}
	if offset > max(0, n-h) {
		return max(0, n-h)
	}
	return offset
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return num(float64(n)/(1<<30)) + "Gi"
	case n >= 1<<20:
		return num(float64(n)/(1<<20)) + "Mi"
	case n > 0:
		return num(float64(n)/(1<<10)) + "Ki"
	}
	return "-"
}

func num(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}
