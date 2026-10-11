package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"

	"github.com/goxang/rig/core"
)

// plain drops colour: NO_COLOR, --brief, or stdout not a terminal.
var plain = os.Getenv("NO_COLOR") != "" || brief || !term.IsTerminal(int(os.Stdout.Fd()))

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

var (
	bold  = paint(lipgloss.NewStyle().Bold(true))
	dim   = paint(lipgloss.NewStyle().Faint(true))
	red   = paint(lipgloss.NewStyle().Foreground(lipgloss.Color("1")))
	green = paint(lipgloss.NewStyle().Foreground(lipgloss.Color("2")))
	amber = paint(lipgloss.NewStyle().Foreground(lipgloss.Color("3")))
)

func stateText(s core.State) string {
	switch s {
	case core.StateRunning:
		return green("● " + string(s))
	case core.StateStarting, core.StateDegraded:
		return amber("◐ " + string(s))
	case core.StateFailed:
		return red("✖ " + string(s))
	case core.StateStopped, core.StateAbsent:
		return dim("○ " + string(s))
	}
	return dim("? " + string(s))
}

// brief is output for agents (--brief, RIG_BRIEF=1): no colour or padding, tab-separated, long
// cells cut, at most briefRows rows; every token is paid for.
var brief = os.Getenv("RIG_BRIEF") != ""

const briefRows = 100

func setBrief(on bool) {
	brief = on
	plain = plain || on
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// printTable writes an aligned table; cells may carry ANSI colour.
func printTable(w io.Writer, cols []string, rows [][]string) {
	if brief {
		fmt.Fprintln(w, strings.Join(cols, "\t"))
		for i, r := range rows {
			if i == briefRows {
				fmt.Fprintf(w, "+%d rows\n", len(rows)-briefRows)
				break
			}
			cells := make([]string, len(r))
			for j, c := range r {
				cells[j] = cut(strings.Join(strings.Fields(ansi.ReplaceAllString(c, "")), " "), 60)
			}
			fmt.Fprintln(w, strings.Join(cells, "\t"))
		}
		return
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(c)
	}
	for _, r := range rows {
		for i := range cols {
			if i < len(r) && lipgloss.Width(r[i]) > widths[i] {
				widths[i] = min(lipgloss.Width(r[i]), 80)
			}
		}
	}
	line := func(cells []string, style func(...string) string) {
		var b strings.Builder
		for i := range cols {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if lipgloss.Width(c) > 80 {
				c = c[:77] + "..."
			}
			b.WriteString(c)
			if i < len(cols)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(c)+2))
			}
		}
		fmt.Fprintln(w, style(strings.TrimRight(b.String(), " ")))
	}
	line(cols, bold)
	for _, r := range rows {
		line(r, func(s ...string) string { return strings.Join(s, "") })
	}
}

func printCoreTable(w io.Writer, t core.Table) {
	if len(t.Columns) > 0 {
		printTable(w, t.Columns, t.Rows)
	}
	if t.Note != "" {
		fmt.Fprintln(w, dim(t.Note))
	}
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMi", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%.0fKi", float64(n)/(1<<10))
	}
	return "-"
}

func paint(st lipgloss.Style) func(...string) string {
	return func(s ...string) string {
		if plain {
			return strings.Join(s, " ")
		}
		return st.Render(s...)
	}
}
