package tui

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// terminal runs a program on a PTY inside a pane, drawn through a VT emulator: shells, ssh,
// lazygit, k9s, vim, a game. With input on, every key but esc and ctrl+] goes to the program.
type terminal struct {
	argv []string
	dir  string
	env  []string
	// keep leaves the program running while the view is hidden
	keep  bool
	input bool
	// shell takes the keys whenever it has the focus, until ctrl+] leaves it
	shell, left bool

	emu  *vt.SafeEmulator
	pty  *os.File
	cmd  *exec.Cmd
	w, h int
	// x, y is the pane's top-left cell on screen, for mouse reports
	x, y   int
	mouse  atomic.Bool
	exited atomic.Bool
	err    error
	// scroll is lines up into the scrollback
	scroll  int
	pending atomic.Bool
	hidden  atomic.Bool
	// read and copied close when the goroutines between the PTY and emu stop
	read, copied chan struct{}
}

// termMsg asks for a redraw after output; one is in flight at a time.
type termMsg struct{}

const scrollback = 10000

// newTerminal starts in project; a relative dir prop resolves against base, the view's own dir.
func newTerminal(n protocol.Node, project, base string) (*terminal, error) {
	var argv []string
	switch c := n.Props["command"].(type) {
	case string:
		argv = []string{"sh", "-c", c}
	case []any:
		for _, a := range c {
			argv = append(argv, fmt.Sprint(a))
		}
	case nil:
		argv = []string{cmp.Or(os.Getenv("SHELL"), "sh")}
	}
	if len(argv) == 0 {
		return nil, errors.New("terminal: command is empty")
	}
	t := &terminal{argv: argv, dir: project, shell: n.Props["command"] == nil}
	if d := view.Prop(n, "dir"); d != "" {
		if !filepath.IsAbs(d) {
			d = filepath.Join(base, d)
		}
		t.dir = d
	}
	t.keep, _ = n.Props["keep"].(bool)
	if env, ok := n.Props["env"].(map[string]any); ok {
		for k, v := range env {
			t.env = append(t.env, k+"="+fmt.Sprint(v))
		}
	}
	return t, nil
}

func (t *terminal) start(w, h int) error {
	t.w, t.h = w, h
	t.emu = vt.NewSafeEmulator(w, h)
	t.emu.SetScrollbackSize(scrollback)
	t.emu.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(v bool) { t.hidden.Store(!v) },
		EnableMode: func(m ansi.Mode) {
			if mouseMode(m) {
				t.mouse.Store(true)
			}
		},
		DisableMode: func(m ansi.Mode) {
			if mouseMode(m) {
				t.mouse.Store(false)
			}
		},
	})
	t.cmd = exec.Command(t.argv[0], t.argv[1:]...)
	t.cmd.Dir = t.dir
	t.cmd.Env = append(append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor"), t.env...)
	f, err := pty.StartWithSize(t.cmd, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	if err != nil {
		return err
	}
	t.pty = f
	emu, read := t.emu, make(chan struct{})
	t.read = read
	go func() {
		defer close(read)
		buf := make([]byte, 32<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = emu.Write(buf[:n])
				t.redraw()
			}
			if err != nil {
				return
			}
		}
	}()
	// the emulator's replies (cursor reports, device attributes) and encoded keys go to the program
	t.copied = make(chan struct{})
	go func() { defer close(t.copied); _, _ = io.Copy(f, emu) }()
	go func() {
		t.err = t.cmd.Wait()
		t.exited.Store(true)
		t.redraw()
	}()
	return nil
}

func (t *terminal) redraw() {
	if program != nil && t.pending.CompareAndSwap(false, true) {
		program.Send(termMsg{})
	}
}

func (t *terminal) resize(w, h int) {
	if t.emu == nil || w == t.w && h == t.h || w <= 0 || h <= 0 {
		return
	}
	t.w, t.h = w, h
	t.emu.Resize(w, h)
	if t.pty != nil {
		_ = pty.Setsize(t.pty, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	}
}

func (t *terminal) close() {
	if t.cmd != nil && t.cmd.Process != nil && !t.exited.Load() {
		_ = t.cmd.Process.Kill()
	}
	if t.pty != nil {
		_ = t.pty.Close()
		select {
		case <-t.read:
		case <-time.After(time.Second):
		}
	}
	if t.emu != nil {
		// vt's Read and Close race on a plain bool: wake the copier with a key so it fails on the
		// closed PTY and exits first
		go t.emu.SendKey(uv.KeyPressEvent{Code: uv.KeyEnter})
		select {
		case <-t.copied:
		case <-time.After(time.Second):
		}
		_ = t.emu.Close()
	}
	t.input = false
}

func mouseMode(m ansi.Mode) bool {
	switch m {
	case ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseHighlight, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent:
		return true
	}
	return false
}

// sendMouse reports e to a program that asked for the mouse, when it falls inside the pane.
func (t *terminal) sendMouse(e mouseEvent) bool {
	x, y := e.X-t.x, e.Y-t.y
	if t.emu == nil || t.exited.Load() || !t.mouse.Load() || x < 0 || y < 0 || x >= t.w || y >= t.h {
		return false
	}
	m := uv.Mouse{X: x, Y: y, Button: e.Button, Mod: e.Mod}
	switch e.Action {
	case mouseRelease:
		t.emu.SendMouse(uv.MouseReleaseEvent(m))
	case mouseMotion:
		t.emu.SendMouse(uv.MouseMotionEvent(m))
	default:
		t.emu.SendMouse(uv.MouseClickEvent(m))
	}
	return true
}

func (t *terminal) key(k tea.KeyPressMsg) {
	if t.emu == nil || t.exited.Load() {
		return
	}
	t.scroll = 0
	t.emu.SendKey(uv.KeyPressEvent(k))
}

func (t *terminal) paste(s string) {
	if t.emu != nil && !t.exited.Load() {
		t.emu.Paste(s)
	}
}

func (t *terminal) scrollBy(d int) {
	if t.emu == nil {
		return
	}
	t.scroll = max(0, min(t.scroll+d, t.emu.ScrollbackLen()))
}

func (t *terminal) scrollKey(k string) {
	switch k {
	case "up", "k":
		t.scrollBy(1)
	case "down", "j":
		t.scrollBy(-1)
	case "pgup":
		t.scrollBy(t.h)
	case "pgdown":
		t.scrollBy(-t.h)
	case "G", "end":
		t.scroll = 0
	}
}

// lines is the screen, scrolled up into the scrollback by t.scroll, with the cursor drawn.
func (t *terminal) lines() []string {
	screen := strings.Split(t.emu.Render(), "\n")
	if t.scroll > 0 {
		n := t.emu.ScrollbackLen()
		var back []string
		for y := n - t.scroll; y < n; y++ {
			line := make(uv.Line, t.w)
			for x := range t.w {
				// ponytail: the cell is read after vt unlocks; scrollback lines are not rewritten,
				// only evicted past 10000, so a copy of the lines under one lock if that ever shows
				if c := t.emu.ScrollbackCellAt(x, y); c != nil {
					line[x] = *c
				} else {
					line[x] = uv.EmptyCell
				}
			}
			back = append(back, line.Render())
		}
		screen = append(back, screen...)
		return screen[:min(len(screen), t.h)]
	}
	if t.input && !t.hidden.Load() {
		p := t.emu.CursorPosition()
		if p.Y >= 0 && p.Y < len(screen) {
			l := screen[p.Y]
			ch := cmp.Or(ansi.Strip(ansi.Cut(l, p.X, p.X+1)), " ")
			screen[p.Y] = ansi.Cut(l, 0, p.X) + "\x1b[7m" + ch + "\x1b[27m" + ansi.TruncateLeft(l, p.X+1, "")
		}
	}
	return screen
}

func (t *viewTab) terminalBody(m *model, wn *wnode, x, y, w, h int, focused bool) string {
	term, _ := wn.st.(*terminal)
	if term == nil {
		nt, err := newTerminal(wn.n, m.app.Spec.Dir, t.dir)
		if err != nil {
			return sRed.Render(err.Error())
		}
		if err := nt.start(w, h); err != nil {
			return sRed.Render(err.Error())
		}
		term = nt
		wn.st = term
	}
	if term.emu == nil {
		if err := term.start(w, h); err != nil {
			return sRed.Render(err.Error())
		}
	}
	term.pending.Store(false)
	term.x, term.y = x, y+m.originY
	term.resize(w, h)
	if !focused {
		term.input, term.left = false, false
	} else if term.shell && !term.left && !term.exited.Load() {
		term.input = true
	}
	out := term.lines()
	if term.exited.Load() {
		msg := "exited"
		if term.err != nil {
			msg = term.err.Error()
		}
		out = append(out[:max(0, min(len(out), h-1))], sDim.Render(msg+" · R restarts"))
	}
	return clip(strings.Join(out, "\n"), w, h)
}
