package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Terminals send ctrl+backspace as ctrl+h, so it deletes a word, as in an editor; backspace stays one character.
func newInput() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.KeyMap.DeleteWordBackward = key.NewBinding(key.WithKeys("ctrl+h", "alt+backspace", "ctrl+w"))
	in.KeyMap.DeleteCharacterBackward = key.NewBinding(key.WithKeys("backspace"))
	in.KeyMap.DeleteWordForward = key.NewBinding(key.WithKeys("alt+delete", "alt+d"))
	in.KeyMap.LineStart = key.NewBinding(key.WithKeys("home"))
	return in
}

func editorTextarea(in *textarea.Model) {
	in.KeyMap.DeleteWordBackward = key.NewBinding(key.WithKeys("ctrl+h", "alt+backspace", "ctrl+w"))
	in.KeyMap.DeleteCharacterBackward = key.NewBinding(key.WithKeys("backspace"))
	in.KeyMap.LineStart = key.NewBinding(key.WithKeys("home"))
	in.KeyMap.WordForward = key.NewBinding(key.WithKeys("ctrl+right", "alt+f"))
	in.KeyMap.WordBackward = key.NewBinding(key.WithKeys("ctrl+left", "alt+b"))
}

// textSel is a selection in a one-line input: from anchor to the cursor.
type textSel struct {
	on     bool
	anchor int
}

func (s textSel) span(pos int) (int, int, bool) {
	if !s.on || s.anchor == pos {
		return pos, pos, false
	}
	return min(s.anchor, pos), max(s.anchor, pos), true
}

// editKey gives a textinput the editor keys it lacks: ctrl+a, shift+arrows and drags select;
// ctrl+c copies (the selection, else everything), ctrl+x cuts; typing, pasting or deleting
// replaces the selection. It reports whether the key is used up.
func editKey(in *textinput.Model, s *textSel, k tea.KeyMsg) bool {
	pos := in.Position()
	val := []rune(in.Value())
	a, b, has := s.span(pos)
	move := func(to int) {
		if !s.on {
			s.on, s.anchor = true, pos
		}
		in.SetCursor(max(0, min(len(val), to)))
	}
	switch k.String() {
	case "ctrl+a":
		s.on, s.anchor = true, 0
		in.CursorEnd()
		return true
	case "shift+left":
		move(pos - 1)
		return true
	case "shift+right":
		move(pos + 1)
		return true
	case "shift+home":
		move(0)
		return true
	case "shift+end":
		move(len(val))
		return true
	case "ctrl+shift+left":
		move(wordLeft(val, pos))
		return true
	case "ctrl+shift+right":
		move(wordRight(val, pos))
		return true
	case "ctrl+c", "ctrl+y", "ctrl+x":
		text := string(val)
		if has {
			text = string(val[a:b])
		}
		copyText(text)
		if k.String() == "ctrl+x" {
			if !has {
				a, b = 0, len(val)
			}
			in.SetValue(string(val[:a]) + string(val[b:]))
			in.SetCursor(a)
			s.on = false
		}
		return true
	}
	if !has {
		s.on = false
		return false
	}
	s.on = false
	cut := func() {
		in.SetValue(string(val[:a]) + string(val[b:]))
		in.SetCursor(a)
	}
	switch {
	case k.Type == tea.KeyBackspace || k.Type == tea.KeyDelete || k.Type == tea.KeyCtrlH:
		cut()
		return true
	case k.Type == tea.KeyRunes || k.Type == tea.KeySpace || k.String() == "ctrl+v":
		cut()
	case k.Type == tea.KeyLeft:
		in.SetCursor(a)
		return true
	case k.Type == tea.KeyRight:
		in.SetCursor(b)
		return true
	}
	return false
}

func wordLeft(r []rune, pos int) int {
	for pos > 0 && !isWord(r[pos-1]) {
		pos--
	}
	for pos > 0 && isWord(r[pos-1]) {
		pos--
	}
	return pos
}

func wordRight(r []rune, pos int) int {
	for pos < len(r) && !isWord(r[pos]) {
		pos++
	}
	for pos < len(r) && isWord(r[pos]) {
		pos++
	}
	return pos
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// inputView draws a one-line input w cells wide with its cursor and selection; *off is the first
// rune shown, kept so the cursor stays in view and clicks map back to runes.
func inputView(in textinput.Model, s textSel, w int, off *int) string {
	val := []rune(in.Value())
	if in.EchoMode == textinput.EchoPassword {
		val = []rune(strings.Repeat("•", len(val)))
	}
	pos := min(in.Position(), len(val))
	if len(val) == 0 && in.Placeholder != "" {
		ph := []rune(in.Placeholder)
		return sCursor.Render(string(ph[0])) + sDim.Render(truncate(string(ph[1:]), max(0, w-1)))
	}
	w = max(1, w)
	if pos < *off {
		*off = pos
	}
	if pos >= *off+w {
		*off = pos - w + 1
	}
	*off = max(0, min(*off, max(0, len(val)-w+1)))
	a, b, has := s.span(pos)
	var out strings.Builder
	for i := *off; i < len(val) && i < *off+w; i++ {
		c := string(val[i])
		switch {
		case i == pos:
			out.WriteString(sCursor.Render(c))
		case has && i >= a && i < b:
			out.WriteString(sSel.Render(c))
		default:
			out.WriteString(c)
		}
	}
	if pos == len(val) && pos-*off < w {
		ghost := ""
		if m := in.MatchedSuggestions(); in.ShowSuggestions && len(m) > 0 && strings.HasPrefix(m[0], in.Value()) {
			ghost = m[0][len(in.Value()):]
		}
		if ghost == "" {
			out.WriteString(sCursor.Render(" "))
		} else {
			g := []rune(ghost)
			out.WriteString(sCursor.Render(string(g[0])) + sDim.Render(truncate(string(g[1:]), max(0, w-(pos-*off)-1))))
		}
	}
	return out.String()
}

// selectedView draws the whole value with the selection marked, for inputs that wrap (popups).
func selectedView(in textinput.Model, s textSel, style lipgloss.Style) string {
	val := []rune(in.Value())
	pos := min(in.Position(), len(val))
	a, b, has := s.span(pos)
	var out strings.Builder
	for i, r := range val {
		switch {
		case i == pos:
			out.WriteString(sCursor.Render(string(r)))
		case has && i >= a && i < b:
			out.WriteString(sSel.Render(string(r)))
		default:
			out.WriteString(style.Render(string(r)))
		}
	}
	if pos == len(val) {
		out.WriteString(sCursor.Render(" "))
	}
	return out.String()
}
