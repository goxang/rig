package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEditKeySelection(t *testing.T) {
	in := newInput()
	in.Focus()
	in.SetValue("select name from users")
	var s textSel
	press := func(k tea.KeyMsg) {
		if !editKey(&in, &s, k) {
			in, _ = in.Update(k)
		}
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlShiftLeft})
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("orders")})
	if got := in.Value(); got != "select name from orders" {
		t.Fatalf("typing over a selected word: %q", got)
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlH})
	if got := in.Value(); got != "select name from " {
		t.Fatalf("ctrl+backspace: %q", got)
	}
	press(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := in.Value(); got != "select name from" {
		t.Fatalf("backspace: %q", got)
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlA})
	press(tea.KeyMsg{Type: tea.KeyBackspace})
	if in.Value() != "" {
		t.Fatalf("ctrl+a then backspace: %q", in.Value())
	}
}
