package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	mdBullet   = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	mdInline   = regexp.MustCompile("`[^`]+`|\\*\\*[^*]+\\*\\*|__[^_]+__|\\[[^\\]]+\\]\\([^)]+\\)")
	mdTableSep = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
)

// renderMarkdown draws an answer in w columns: every line wraps (code too, tabs expanded), so
// nothing runs past the box.
func renderMarkdown(s string, w int) string {
	w = max(w, 10)
	var out []string
	add := func(lines ...string) { out = append(out, lines...) }
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\t", "    ")), "\n")
	codeBar := lipgloss.NewStyle().Foreground(cPanel).Render("│ ")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			lang := strings.TrimPrefix(t, "```")
			if lang != "" {
				add(sDim.Render(lang))
			}
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				for _, part := range strings.Split(ansi.Hardwrap(lines[i], w-2, true), "\n") {
					add(codeBar + sGreen.Render(part))
				}
			}
		case strings.HasPrefix(t, "|") && i+1 < len(lines) && mdTableSep.MatchString(strings.TrimSpace(lines[i+1])):
			var rows [][]string
			rows = append(rows, mdCells(t))
			for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				rows = append(rows, mdCells(strings.TrimSpace(lines[i])))
			}
			i--
			add(mdTable(rows, w)...)
		case strings.HasPrefix(t, "#"):
			level := len(t) - len(strings.TrimLeft(t, "#"))
			text := strings.TrimSpace(strings.TrimLeft(t, "#"))
			st := sTitle
			if level <= 2 {
				st = sAccent
			}
			add(strings.Split(st.Render(ansi.Wrap(mdInlineRender(text), w, "")), "\n")...)
		case t == "---" || t == "***" || t == "___":
			add(sDim.Render(strings.Repeat("─", w)))
		case strings.HasPrefix(t, ">"):
			bar := sDim.Render("▎ ")
			for _, part := range strings.Split(ansi.Wrap(mdInlineRender(strings.TrimSpace(strings.TrimPrefix(t, ">"))), w-2, ""), "\n") {
				add(bar + sDim.Render(part))
			}
		case mdBullet.MatchString(l):
			p := mdBullet.FindStringSubmatch(l)
			indent := strings.Repeat(" ", min(len(p[1]), 8))
			mark := p[2]
			if !strings.ContainsAny(mark[len(mark)-1:], ".)") {
				mark = "•"
			}
			head := indent + mark + " "
			add(hang(head, sAccent.Render(mark), mdInlineRender(p[3]), w)...)
		default:
			add(strings.Split(ansi.Wrap(mdInlineRender(l), w, ""), "\n")...)
		}
	}
	return strings.Join(out, "\n")
}

// hang wraps text after head, the next lines indented under the text.
func hang(head, styledMark, text string, w int) []string {
	hw := lipgloss.Width(head)
	parts := strings.Split(ansi.Wrap(text, max(w-hw, 4), ""), "\n")
	first := strings.Repeat(" ", hw-lipgloss.Width(styledMark)-1) + styledMark + " "
	out := []string{first + parts[0]}
	for _, p := range parts[1:] {
		out = append(out, strings.Repeat(" ", hw)+p)
	}
	return out
}

func mdInlineRender(s string) string {
	code := lipgloss.NewStyle().Foreground(cPurple)
	bold := lipgloss.NewStyle().Bold(true)
	return mdInline.ReplaceAllStringFunc(s, func(m string) string {
		switch {
		case strings.HasPrefix(m, "`"):
			return code.Render(strings.Trim(m, "`"))
		case strings.HasPrefix(m, "["):
			text, url, _ := strings.Cut(m[1:len(m)-1], "](")
			return sUnderline.Render(text) + sDim.Render(" ("+url+")")
		default:
			return bold.Render(m[2 : len(m)-2])
		}
	})
}

func mdCells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	cells := strings.Split(row, "|")
	for i, c := range cells {
		cells[i] = mdInlineRender(strings.TrimSpace(c))
	}
	return cells
}

// mdTable aligns the columns when they fit in w; otherwise each row becomes "header: value" lines.
func mdTable(rows [][]string, w int) []string {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for j, c := range r {
			widths[j] = max(widths[j], lipgloss.Width(c))
		}
	}
	total := 0
	for _, x := range widths {
		total += x + 2
	}
	var out []string
	if total <= w {
		for i, r := range rows {
			var b strings.Builder
			for j := range widths {
				c := ""
				if j < len(r) {
					c = r[j]
				}
				if i == 0 {
					c = sTitle.Render(c)
				}
				b.WriteString(padRight(c, widths[j]) + "  ")
			}
			out = append(out, strings.TrimRight(b.String(), " "))
			if i == 0 {
				out = append(out, sDim.Render(strings.Repeat("─", total-2)))
			}
		}
		return out
	}
	head := rows[0]
	for _, r := range rows[1:] {
		for j, c := range r {
			h := ""
			if j < len(head) {
				h = head[j]
			}
			out = append(out, strings.Split(ansi.Wrap(sDim.Render(h+": ")+c, w, ""), "\n")...)
		}
		out = append(out, "")
	}
	return out
}
