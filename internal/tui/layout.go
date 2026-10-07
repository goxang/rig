package tui

import tea "github.com/charmbracelet/bubbletea"

//go:generate sh -c "go run ./layoutgen > layouts.go && gofmt -w layouts.go"

// latinKey turns a key typed on a non-Latin layout into the US key in its place, so q quits on a
// Persian or Russian keyboard too; ok is false for a rune no known layout puts on a key.
func latinKey(k tea.KeyMsg) (tea.KeyMsg, bool) {
	if k.Type != tea.KeyRunes || len(k.Runes) != 1 || k.Runes[0] < 0x80 {
		return k, true
	}
	r, ok := layoutKeys[k.Runes[0]]
	if !ok {
		return k, false
	}
	k.Runes = []rune{r}
	return k, true
}

// typingText is true where a key is text, not a command: an input, a picker's filter, help's search
func (m *model) typingText() bool {
	return m.prompt != nil || m.picker != nil || m.help || m.chat != nil && m.chat.open && m.chat.focus ||
		len(m.tabs) > 0 && m.tabs[m.active].typing()
}
