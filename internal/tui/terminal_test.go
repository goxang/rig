package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/protocol"
)

func TestTerminalRunsAProgram(t *testing.T) {
	term, err := newTerminal(protocol.Node{Widget: "terminal", Props: protocol.Args{"command": []any{"sh"}}}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := term.start(40, 6); err != nil {
		t.Fatal(err)
	}
	defer term.close()
	term.input = true
	for _, r := range "echo hi-$((40+2))" {
		term.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	term.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(term.lines(), "\n"), "hi-42") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	lines := term.lines()
	t.Logf("%q", lines)
	if !strings.Contains(strings.Join(lines, "\n"), "hi-42") || len(lines) != 6 {
		t.Fatalf("screen: %q", lines)
	}
}

func TestTerminalReportsTheMouse(t *testing.T) {
	cmd := `printf '\033[?1000h\033[?1006hready\n'; stty raw -echo; head -c 9 | od -c | head -1`
	term, err := newTerminal(protocol.Node{Widget: "terminal", Props: protocol.Args{"command": cmd}}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := term.start(40, 6); err != nil {
		t.Fatal(err)
	}
	defer term.close()
	term.x, term.y = 10, 5
	screen := func(want string) string {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if s := strings.Join(term.lines(), "\n"); strings.Contains(s, want) {
				return s
			}
		}
		return strings.Join(term.lines(), "\n")
	}
	screen("ready")
	if term.sendMouse(mouseEvent{Mouse: tea.Mouse{X: 9, Y: 5}}) {
		t.Error("a click left of the pane went in")
	}
	if !term.sendMouse(mouseEvent{Mouse: tea.Mouse{X: 12, Y: 8, Button: tea.MouseLeft}}) {
		t.Fatal("a click in the pane did not go in")
	}
	// od's bytes can wrap across lines when the tty's echo lands first: compare without whitespace
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	for deadline := time.Now().Add(3 * time.Second); !strings.Contains(squash(screen("4")), "033[<0;3;4"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no SGR report: %q", screen("4"))
		}
	}
}
