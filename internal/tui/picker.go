package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// picker chooses one or several items from a list; typing filters it.
type picker struct {
	title  string
	items  []string
	desc   []string
	sel    int
	multi  bool
	marked map[string]bool
	filter string
	off    int
	done   func(chosen []string) tea.Cmd
}

func (m *model) pick(title string, items, desc []string, sel int, multi bool, done func([]string) tea.Cmd) {
	m.picker = &picker{title: title, items: items, desc: desc, sel: sel, multi: multi, marked: map[string]bool{}, done: done}
}

// pickMany preselects the items in chosen.
func (m *model) pickMany(title string, items, desc, chosen []string, done func([]string) tea.Cmd) {
	m.pick(title, items, desc, 0, true, done)
	for _, c := range chosen {
		m.picker.marked[c] = true
	}
}

func (p *picker) visible() []int {
	var out []int
	f := strings.ToLower(p.filter)
	for i, it := range p.items {
		d := ""
		if i < len(p.desc) {
			d = p.desc[i]
		}
		if f == "" || strings.Contains(strings.ToLower(it+" "+d), f) {
			out = append(out, i)
		}
	}
	return out
}

func (p *picker) finish(m *model) tea.Cmd {
	m.picker = nil
	var chosen []string
	if p.multi {
		for _, it := range p.items {
			if p.marked[it] {
				chosen = append(chosen, it)
			}
		}
	}
	vis := p.visible()
	if len(chosen) == 0 && p.sel < len(vis) {
		chosen = []string{p.items[vis[p.sel]]}
	}
	return p.done(chosen)
}

func (p *picker) key(m *model, k tea.KeyMsg) tea.Cmd {
	vis := p.visible()
	switch k.String() {
	case "esc":
		m.picker = nil
		return nil
	case "enter":
		return p.finish(m)
	case "up":
		p.sel = max(0, p.sel-1)
	case "down":
		p.sel = min(max(len(vis)-1, 0), p.sel+1)
	case "pgup":
		p.sel = max(0, p.sel-10)
	case "pgdown":
		p.sel = min(max(len(vis)-1, 0), p.sel+10)
	case " ":
		if p.multi && p.sel < len(vis) {
			it := p.items[vis[p.sel]]
			p.marked[it] = !p.marked[it]
			p.sel = min(max(len(vis)-1, 0), p.sel+1)
		}
	case "ctrl+a":
		if p.multi {
			all := len(vis) > 0
			for _, i := range vis {
				all = all && p.marked[p.items[i]]
			}
			for _, i := range vis {
				p.marked[p.items[i]] = !all
			}
		}
	case "backspace":
		if p.filter != "" {
			p.filter = p.filter[:len(p.filter)-1]
			p.sel = 0
		}
	default:
		if k.Type == tea.KeyRunes {
			p.filter += string(k.Runes)
			p.sel = 0
		}
	}
	return nil
}

func (p *picker) click(m *model, h hit) tea.Cmd {
	if h.id != "picker" {
		return nil
	}
	vis := p.visible()
	i := p.off + h.y
	if i >= len(vis) {
		return nil
	}
	p.sel = i
	if p.multi {
		it := p.items[vis[i]]
		p.marked[it] = !p.marked[it]
		return nil
	}
	return p.finish(m)
}

func (p *picker) hints() string {
	h := []string{sKey.Render("↑↓") + sDim.Render(" move"), sKey.Render("type") + sDim.Render(" filter")}
	if p.multi {
		h = append(h, sKey.Render("space")+sDim.Render(" mark"), sKey.Render("ctrl+a")+sDim.Render(" all"))
	}
	return strings.Join(append(h, sKey.Render("enter")+sDim.Render(" ok"), sKey.Render("esc")+sDim.Render(" cancel")), "  ")
}

func (p *picker) view(m *model, h int) string {
	vis := p.visible()
	rows := min(len(vis), max(3, h-8))
	off := scroll(p.sel, 0, rows, len(vis))
	if p.sel >= rows {
		off = p.sel - rows + 1
	}
	p.off = off
	w := 30
	for _, i := range vis {
		d := ""
		if i < len(p.desc) {
			d = p.desc[i]
		}
		w = max(w, lipgloss.Width(p.items[i])+lipgloss.Width(d)+8)
	}
	w = min(w, m.w-8)
	var b strings.Builder
	title := sTitle.Render(p.title)
	if p.filter != "" {
		title += sDim.Render("  filter ") + sAmber.Render(p.filter)
	}
	b.WriteString(title + "\n\n")
	for r := 0; r < rows && off+r < len(vis); r++ {
		i := vis[off+r]
		mark := "  "
		if p.multi {
			mark = sDim.Render("○ ")
			if p.marked[p.items[i]] {
				mark = sGreen.Render("● ")
			}
		}
		line := mark + p.items[i]
		if i < len(p.desc) && p.desc[i] != "" {
			line += "  " + sDim.Render(p.desc[i])
		}
		line = padRight(line, w)
		if off+r == p.sel {
			line = sSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(vis) == 0 {
		b.WriteString(sDim.Render("nothing matches") + "\n")
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).Render(strings.TrimRight(b.String(), "\n"))
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	x, y := max(0, (m.w-bw)/2), max(0, (h-bh)/2)
	// rows start below the border, padding and the title line
	m.zone("picker", x+3, y+4, w, rows)
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, box)
}
