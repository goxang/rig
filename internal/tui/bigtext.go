package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// a three-row digit font for stat tiles, Grafana's "big value" in a terminal
var glyphs = map[rune][3]string{
	'0': {"┏━┓", "┃ ┃", "┗━┛"},
	'1': {" ┓ ", " ┃ ", " ┻ "},
	'2': {"╺━┓", "┏━┛", "┗━╸"},
	'3': {"╺━┓", " ━┫", "╺━┛"},
	'4': {"╻ ╻", "┗━┫", "  ╹"},
	'5': {"┏━╸", "┗━┓", "╺━┛"},
	'6': {"┏━╸", "┣━┓", "┗━┛"},
	'7': {"╺━┓", "  ┃", "  ╹"},
	'8': {"┏━┓", "┣━┫", "┗━┛"},
	'9': {"┏━┓", "┗━┫", "╺━┛"},
	'.': {" ", " ", "╻"},
	'/': {"  ╱", " ╱ ", "╱  "},
	'%': {"o ╱", " ╱ ", "╱ o"},
	'-': {"   ", "╺━╸", "   "},
	' ': {" ", " ", " "},
}

// big renders s in the tile font; characters without a glyph sit small on the baseline.
func big(s string, color lipgloss.TerminalColor) string {
	var rows [3]strings.Builder
	for _, r := range s {
		g, ok := glyphs[r]
		if !ok {
			g = [3]string{" ", " ", string(r)}
		}
		for i := range rows {
			rows[i].WriteString(g[i])
		}
	}
	st := lipgloss.NewStyle().Foreground(color).Bold(true)
	return st.Render(rows[0].String()) + "\n" + st.Render(rows[1].String()) + "\n" + st.Render(rows[2].String())
}

// tile is a stat panel: title, a big value, and a small line under it.
func tile(title, value, sub string, color lipgloss.TerminalColor, w int) string {
	body := big(value, color)
	if lipgloss.Width(body) > w-4 {
		body = "\n" + lipgloss.NewStyle().Foreground(color).Bold(true).Render(value) + "\n"
	}
	body = lipgloss.PlaceHorizontal(w-2, lipgloss.Center, body)
	return panel(title, body+"\n"+lipgloss.PlaceHorizontal(w-2, lipgloss.Center, sDim.Render(sub)), w, 7, false)
}
