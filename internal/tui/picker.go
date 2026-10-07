package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	// del, when set, removes the selected item for good (ctrl+d)
	del func(item string) error
	// zx, zy is where the last frame drew the rows, for hover
	zx, zy int
	// at, when set, opens it as a dropdown under that body cell over the screen, not a centered box
	at *[2]int
	// groups, when set, are tabs over the list (tab, shift+tab, ←→, click); the first holds everything
	groups []pickGroup
	group  int
	// back, when set, is where esc returns: the picker or input this one was opened from
	back func() tea.Cmd
	// preview, when set, shows what the selected item is in full under the list (⇧↑↓ scroll it)
	preview       func(item string) string
	pvOff, pvRows int
	pvFor         string
}

type pickGroup struct {
	name  string
	items map[string]bool
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
	var exact, loose []int
	for i, it := range p.items {
		if p.group > 0 && !p.groups[p.group].items[it] {
			continue
		}
		d := ""
		if i < len(p.desc) {
			d = p.desc[i]
		}
		switch matchTier(it, d, p.filter) {
		case 0:
			exact = append(exact, i)
		case 1:
			loose = append(loose, i)
		}
	}
	return append(exact, loose...)
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
	cmd := p.done(chosen)
	m.chainBack(func() tea.Cmd { m.picker = p; return nil })
	return cmd
}

// chainBack makes esc on the picker or input just opened return to what opened it (restore),
// rather than drop the whole walk.
func (m *model) chainBack(restore func() tea.Cmd) {
	switch {
	case m.prompt != nil && m.prompt.escape == nil:
		m.prompt.escape = restore
	case m.prompt == nil && m.picker != nil && m.picker.back == nil:
		m.picker.back = restore
	}
}

func (p *picker) key(m *model, k tea.KeyMsg) tea.Cmd {
	vis := p.visible()
	switch k.String() {
	case "esc":
		m.picker = nil
		if p.back != nil {
			return p.back()
		}
		return nil
	case "enter":
		return p.finish(m)
	case "tab", "right", "ctrl+right", "shift+tab", "left", "ctrl+left":
		if n := len(p.groups); n > 0 {
			d := map[bool]int{true: 1, false: n - 1}[k.String() == "tab" || k.String() == "right" || k.String() == "ctrl+right"]
			p.group, p.sel = (p.group+d)%n, 0
		}
	case "shift+up":
		p.pvOff = max(0, p.pvOff-3)
	case "shift+down":
		p.pvOff += 3
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
		}
	case "ctrl+d":
		if p.del != nil && p.sel < len(vis) {
			i := vis[p.sel]
			if err := p.del(p.items[i]); err != nil {
				m.setStatus(err.Error(), true)
				return nil
			}
			m.setStatus("closed "+p.items[i], false)
			p.items = append(p.items[:i:i], p.items[i+1:]...)
			if i < len(p.desc) {
				p.desc = append(p.desc[:i:i], p.desc[i+1:]...)
			}
			if len(p.items) == 0 {
				m.picker = nil
				return nil
			}
			p.sel = min(p.sel, len(p.visible())-1)
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
		if r := []rune(p.filter); len(r) > 0 {
			p.filter = string(r[:len(r)-1])
			p.sel = 0
		}
	case "ctrl+h", "ctrl+w", "alt+backspace":
		r := []rune(p.filter)
		p.filter, p.sel = string(r[:wordLeft(r, len(r))]), 0
	case "ctrl+v":
		if v, err := clipboard.ReadAll(); err == nil {
			p.filter, p.sel = p.filter+strings.TrimSpace(v), 0
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
	if g, ok := stripHit(h, "pickgroup"); ok && g < len(p.groups) {
		p.group, p.sel = g, 0
		return nil
	}
	if h.id != "picker" {
		if p.at != nil {
			m.picker = nil // a click beside a dropdown closes it
		}
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
	if p.del != nil {
		h = append(h, sKey.Render("ctrl+d")+sDim.Render(" close"))
	}
	if len(p.groups) > 0 {
		h = append(h, sKey.Render("tab ←→")+sDim.Render(" group"))
	}
	if p.preview != nil {
		h = append(h, sKey.Render("⇧↑↓")+sDim.Render(" scroll details"))
	}
	return strings.Join(append(h, sKey.Render("enter")+sDim.Render(" ok"), sKey.Render("esc")+sDim.Render(" cancel")), "  ")
}

func (p *picker) view(m *model, h int) string {
	vis := p.visible()
	// sized on every item, not the ones the filter or group shows, so the box stays put
	p.pvRows = p.previewRows(h)
	rows := min(len(p.items), max(3, h-8-min(len(p.groups), 1)-p.pvRows))
	off := scroll(p.sel, 0, rows, len(vis))
	if p.sel >= rows {
		off = p.sel - rows + 1
	}
	p.off = off
	w := 30
	for i := range p.items {
		d := ""
		if i < len(p.desc) {
			d = p.desc[i]
		}
		w = max(w, lipgloss.Width(p.items[i])+lipgloss.Width(d)+8)
	}
	strip := 0
	for _, g := range p.groups {
		strip += lipgloss.Width(g.name) + 6 // padding, a count and the bar
	}
	if p.preview != nil {
		w = max(w, 100)
	}
	w = min(max(w, strip), m.w-8)
	top := 4 // rows start below the border, padding, the title line and a blank one
	if len(p.groups) > 0 {
		top++
	}
	// the box is drawn twice when it has groups: once to measure it, then with the strip's zones in place
	z0 := len(m.zones)
	box := p.box(m, w, rows, off, vis, 0, 0)
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	x, y := max(0, (m.w-bw)/2), max(0, (h-bh)/2)
	if len(p.groups) > 0 {
		m.zones = m.zones[:z0]
		box = p.box(m, w, rows, off, vis, x+3, y+3)
	}
	m.zone("picker", x+3, y+top, w, rows)
	p.zx, p.zy = x+3, y+top
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, box)
}

func (p *picker) box(m *model, w, rows, off int, vis []int, sx, sy int) string {
	var b strings.Builder
	title := sTitle.Render(p.title)
	if p.filter != "" {
		title += sDim.Render("  filter ") + sAmber.Render(p.filter)
	}
	b.WriteString(title + "\n")
	if len(p.groups) > 0 {
		labels := make([]string, len(p.groups))
		for i, g := range p.groups {
			n := 0
			for it := range p.marked {
				if p.marked[it] && (i == 0 || g.items[it]) {
					n++
				}
			}
			labels[i] = g.name
			if n > 0 {
				labels[i] += fmt.Sprintf(" %d", n)
			}
		}
		b.WriteString(m.stripFit("pickgroup", sx, sy, labels, p.group, w) + "\n")
	}
	b.WriteString("\n")
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
			nw := p.nameWidth(w)
			line = truncate(padRight(truncate(line, nw+2), nw+4)+sDim.Render(p.desc[i]), w)
		}
		line = padRight(line, w)
		if off+r == p.sel {
			line = highlight(sSelected, line, w)
		} else if m.hovering(p.zx, p.zy+r, w, 1) {
			line = highlight(sHover, line, w)
		}
		b.WriteString(line + "\n")
	}
	if len(vis) == 0 {
		b.WriteString(sDim.Render("nothing matches") + "\n")
	}
	for r := max(1, len(vis)-off); r < rows; r++ {
		b.WriteString(" \n")
	}
	if n := p.pvRows; n > 0 {
		b.WriteString(sDim.Render(strings.Repeat("─", w)) + "\n")
		item := ""
		if p.sel < len(vis) {
			item = p.items[vis[p.sel]]
		}
		if item != p.pvFor {
			p.pvFor, p.pvOff = item, 0
		}
		var lines []string
		if item != "" {
			lines = strings.Split(lipgloss.NewStyle().Width(w).Render(p.preview(item)), "\n")
		}
		p.pvOff = min(p.pvOff, max(0, len(lines)-n))
		for r := 0; r < n; r++ {
			l := ""
			if p.pvOff+r < len(lines) {
				l = truncate(lines[p.pvOff+r], w)
			}
			b.WriteString(l + "\n")
		}
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).Render(strings.TrimRight(b.String(), "\n"))
}

// dropdown draws the picker as a list hanging under p.at over the screen's own view.
func (p *picker) dropdown(m *model, base string, h int) string {
	vis := p.visible()
	x, y := p.at[0], p.at[1]
	rows := min(len(vis), max(3, h-y-4))
	p.off = scroll(p.sel, p.off, rows, len(vis))
	w := 20
	for _, i := range vis {
		w = max(w, lipgloss.Width(p.items[i])+4)
	}
	w = min(w, m.w-x-4)
	var b strings.Builder
	if p.filter != "" {
		b.WriteString(sDim.Render("filter ") + sAmber.Render(p.filter) + "\n")
	}
	for r := 0; r < rows && p.off+r < len(vis); r++ {
		i := vis[p.off+r]
		mark := ""
		if p.multi {
			mark = sDim.Render("○ ")
			if p.marked[p.items[i]] {
				mark = sGreen.Render("● ")
			}
		}
		line := padRight(mark+p.items[i], w)
		if p.off+r == p.sel {
			line = highlight(sSelected, line, w)
		} else if m.hovering(p.zx, p.zy+r, w, 1) {
			line = highlight(sHover, line, w)
		}
		b.WriteString(line + "\n")
	}
	if len(vis) == 0 {
		b.WriteString(sDim.Render("nothing matches") + "\n")
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Render(strings.TrimRight(b.String(), "\n"))
	top := y + 1
	if p.filter != "" {
		top++
	}
	m.zone("picker", x+1, top, w, rows)
	p.zx, p.zy = x+1, top
	return overlayAt(base, box, x, y)
}

// overlayAt draws box over base with its top left corner at cell x, y.
func overlayAt(base, box string, x, y int) string {
	lines := strings.Split(base, "\n")
	for i, bl := range strings.Split(box, "\n") {
		for y+i >= len(lines) {
			lines = append(lines, "")
		}
		under := lines[y+i]
		left := ansi.Truncate(under, x, "")
		left += strings.Repeat(" ", max(0, x-ansi.StringWidth(left)))
		lines[y+i] = left + "\x1b[0m" + bl + "\x1b[0m" + ansi.Cut(under, x+ansi.StringWidth(bl), ansi.StringWidth(under))
	}
	return strings.Join(lines, "\n")
}

// nameWidth is the name column's width when items have descriptions: the widest name, at most
// half the box, so descriptions line up as a second column.
func (p *picker) nameWidth(w int) int {
	n := 0
	for _, it := range p.items {
		n = max(n, lipgloss.Width(it))
	}
	return min(n, w/2)
}

func (p *picker) previewRows(h int) int {
	if p.preview == nil {
		return 0
	}
	return max(6, (h-10)/2)
}
