package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/viz"
)

var (
	cAccent, cGreen, cAmber, cRed, cPurple lipgloss.TerminalColor
	cText, cDim                            lipgloss.TerminalColor
	// cPlaceholder reads on both the panel and the cursor line's background
	cPlaceholder, cPanel, cBar lipgloss.TerminalColor

	sTitle, sDim, sAccent, sGreen, sAmber, sRed, sCursor, sSelected, sHover, sUnderline lipgloss.Style
	sTabHover, sTabOn, sTabOff, sHeader, sBand, sSubOn, sSubOff, sKey                   lipgloss.Style
	sSubSep                                                                             string
)

func init() { applyTheme(themes["default"]) }

// applyTheme sets every colour and style of the UI from t.
// theme is the one in use, for code that needs a plain colour (charts).
var theme Theme

// fitted is a theme's colours for one terminal background, each made readable where it is drawn.
type fitted struct {
	sh                                       themeShades
	term                                     string
	text, dim, ph, accent, green, amber, red string
	purple, onAccent                         string
}

// fitTheme keeps each colour readable on the terminal and on the bars and cursor lines it sits on;
// a theme with no light shades takes the default's on light terminals.
func fitTheme(t Theme) (dark, light fitted) {
	fit := func(sh themeShades, term string) fitted {
		bgs := []string{term, sh.Bar, sh.Cursor, sh.Selected, sh.Panel}
		hue := func(c string) string { return readable(c, 3, bgs...) }
		f := fitted{sh: sh, term: term, text: readable(sh.Text, 4.5, bgs...), dim: readable(sh.Dim, 2.5, bgs...), ph: readable(sh.Placeholder, 3, bgs...),
			accent: hue(t.Accent), green: hue(t.Green), amber: hue(t.Amber), red: hue(t.Red), purple: hue(t.Purple)}
		f.onAccent = readable(t.OnAccent, 4.5, f.accent)
		return f
	}
	ls := t.Light
	if ls == (themeShades{}) {
		ls = themes["default"].Light
	}
	return fit(t.themeShades, darkTerm), fit(ls, lightTerm)
}

func applyTheme(t Theme) {
	theme = t
	d, l := fitTheme(t)
	dark, light := d.sh, l.sh
	c := func(dark, light string) lipgloss.TerminalColor {
		return lipgloss.AdaptiveColor{Light: light, Dark: dark}
	}
	cAccent, cGreen, cAmber, cRed, cPurple = c(d.accent, l.accent), c(d.green, l.green), c(d.amber, l.amber), c(d.red, l.red), c(d.purple, l.purple)
	cText, cDim, cPlaceholder = c(d.text, l.text), c(d.dim, l.dim), c(d.ph, l.ph)
	cPanel, cBar = c(dark.Panel, light.Panel), c(dark.Bar, light.Bar)
	cOnAccent := c(d.onAccent, l.onAccent)

	sTitle = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sDim = lipgloss.NewStyle().Foreground(cDim)
	sAccent = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sGreen = lipgloss.NewStyle().Foreground(cGreen)
	sAmber = lipgloss.NewStyle().Foreground(cAmber)
	sRed = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	sCursor = lipgloss.NewStyle().Background(c(dark.Cursor, light.Cursor)).Bold(true)
	sSelected = lipgloss.NewStyle().Background(c(dark.Selected, light.Selected)).Bold(true)
	sHover = lipgloss.NewStyle().Background(c(dark.Hover, light.Hover))
	sUnderline = lipgloss.NewStyle().Underline(true)
	sTabHover = lipgloss.NewStyle().Foreground(cText).Underline(true).Padding(0, 1)
	sTabOn = lipgloss.NewStyle().Foreground(cOnAccent).Background(cAccent).Bold(true).Padding(0, 1)
	sTabOff = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sHeader = lipgloss.NewStyle().Background(cBar).Foreground(cText)
	sBand = lipgloss.NewStyle().Background(cBar)
	sSubOn = lipgloss.NewStyle().Foreground(cAccent).Bold(true).Padding(0, 1)
	sSubOff = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sSubSep = lipgloss.NewStyle().Foreground(cPanel).Render("│")
	sKey = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	if len(t.Series) > 0 {
		term := l
		if lipgloss.HasDarkBackground() {
			term = d
		}
		viz.Palette = make([]lipgloss.Color, len(t.Series))
		for i, s := range t.Series {
			viz.Palette[i] = lipgloss.Color(readable(s, 2.5, term.term))
		}
	}
}

// toolbar is a row of buttons in named sections, as on the Tests and metric panel screens.
type toolbar struct {
	m    *model
	b    strings.Builder
	x, y int
}

func (tb *toolbar) add(s string) {
	tb.b.WriteString(s)
	tb.x += lipgloss.Width(s)
}

func (tb *toolbar) section(name string) { tb.add(sDim.Render("  │ " + name + " ")) }

func (tb *toolbar) button(id, text string, on bool) {
	st := lipgloss.NewStyle().Foreground(cText).Background(cPanel).Padding(0, 1)
	if on {
		st = sTabOn
	}
	s := st.Render(text)
	tb.m.zone(id, tb.x, tb.y, lipgloss.Width(s), 1)
	tb.add(s + " ")
}

func (tb *toolbar) String() string { return tb.b.String() }

// badge is bold text on bg, in black or white, whichever reads better on it.
func badge(bg lipgloss.TerminalColor) lipgloss.Style {
	ink := func(c string) string {
		if contrast("#000000", c) >= contrast("#FFFFFF", c) {
			return "#000000"
		}
		return "#FFFFFF"
	}
	st := lipgloss.NewStyle().Background(bg).Bold(true)
	switch c := bg.(type) {
	case lipgloss.AdaptiveColor:
		return st.Foreground(lipgloss.AdaptiveColor{Light: ink(c.Light), Dark: ink(c.Dark)})
	case lipgloss.Color:
		return st.Foreground(lipgloss.Color(ink(string(c))))
	}
	return st
}

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

// stateRank is how active a state is: descending puts running services on top.
func stateRank(s core.State) float64 {
	switch s {
	case core.StateRunning:
		return 5
	case core.StateStarting, core.StateDegraded:
		return 4
	case core.StateFailed:
		return 3
	case core.StateStopped:
		return 2
	case core.StateAbsent:
		return 1
	}
	return 0
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
			line = highlight(sCursor, line, lipgloss.Width(line))
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
