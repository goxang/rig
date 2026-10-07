package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestLatinKey(t *testing.T) {
	for in, want := range map[rune]string{'ض': "q", 'й': "q", 'Й': "Q", 'ی': "d", 'ש': "a", 'σ': "s", 'Σ': "S", '؟': "?", '۱': "1", 'q': "q"} {
		k, ok := latinKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{in}})
		if !ok || k.String() != want {
			t.Errorf("%q: got %q %v, want %q", in, k.String(), ok, want)
		}
	}
	if _, ok := latinKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'中'}}); ok {
		t.Error("a rune on no layout's key should ask for the English layout")
	}
}
