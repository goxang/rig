package web

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/protocol"
)

// A term is one PTY the page drives: a host shell, a service's exec, a view's terminal or a project shell.
type term struct {
	cmd      *exec.Cmd
	pty      *os.File
	once     sync.Once
	read     sync.Mutex
	attached atomic.Bool
}

func (t *term) close() {
	t.once.Do(func() {
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		_ = t.pty.Close()
		go func() { _ = t.cmd.Wait() }()
	})
}

type terms struct {
	mu sync.Mutex
	m  map[string]*term
}

func (s *terms) get(id string) *term {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

func (s *terms) drop(id string) {
	s.mu.Lock()
	t := s.m[id]
	delete(s.m, id)
	s.mu.Unlock()
	if t != nil {
		t.close()
	}
}

func (h *Handler) termRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/term", h.auth(h.withEngine(h.termOpen)))
	mux.HandleFunc("POST /v1/term/out", h.auth(h.termOut))
	mux.HandleFunc("POST /v1/term/in", h.auth(h.termIn))
	mux.HandleFunc("POST /v1/term/resize", h.auth(h.termResize))
	mux.HandleFunc("POST /v1/term/close", h.auth(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if readJSON(w, r, &in) {
			h.terms.drop(in.ID)
			writeJSON(w, map[string]any{})
		}
	}))
}

type termReq struct {
	Kind     string         `json:"kind"`
	Host     string         `json:"host"`
	Service  string         `json:"service"`
	Instance string         `json:"instance"`
	Command  any            `json:"command"`
	Dir      string         `json:"dir"`
	Env      map[string]any `json:"env"`
	Cols     int            `json:"cols"`
	Rows     int            `json:"rows"`
}

func (h *Handler) termCmd(l *engine.Local, in termReq) (*exec.Cmd, error) {
	a := l.App
	switch in.Kind {
	case "host":
		if err := a.Writable(); err != nil {
			return nil, err
		}
		hs, _, err := engine.Get[core.Hosts](a, core.KindHosts, "")
		if err != nil {
			return nil, err
		}
		var command []string
		if c, _ := in.Command.(string); c != "" {
			// Hosts join argv with spaces and hand it to a shell, so the line goes in as one word.
			command = []string{c + "; echo; printf 'press enter '; read _"}
		}
		// The session outlives this request: the PTY's own close ends it.
		return hs.Shell(context.Background(), in.Host, command)
	case "service":
		if err := a.Writable(); err != nil {
			return nil, err
		}
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		args := []string{"-f", a.Spec.File, "-e", a.Env.Name, "exec", in.Service}
		if in.Instance != "" {
			args = append(args, "-i", in.Instance)
		}
		c := exec.Command(exe, args...)
		c.Dir = a.Spec.Dir
		return c, nil
	}
	var argv []string
	switch c := in.Command.(type) {
	case string:
		argv = []string{"sh", "-c", c}
	case []any:
		for _, v := range c {
			argv = append(argv, fmt.Sprint(v))
		}
	}
	if len(argv) == 0 {
		argv = []string{cmp.Or(os.Getenv("SHELL"), "sh")}
	}
	sc := sh.New(argv[0], argv[1:]...)
	sc.Dir = a.Spec.Dir
	if in.Dir != "" {
		sc.Dir = in.Dir
		if !filepath.IsAbs(in.Dir) {
			sc.Dir = filepath.Join(a.Spec.Dir, in.Dir)
		}
	}
	for k, v := range in.Env {
		sc.Env = append(sc.Env, k+"="+fmt.Sprint(v))
	}
	c := sc.Exec(context.Background())
	return c, nil
}

func (h *Handler) termOpen(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	var in termReq
	if !readJSON(w, r, &in) {
		return
	}
	c, err := h.termCmd(l, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if c.Env == nil {
		c.Env = os.Environ()
	}
	c.Env = append(c.Env, "TERM=xterm-256color", "COLORTERM=truecolor")
	f, err := pty.StartWithSize(c, &pty.Winsize{Cols: uint16(cmp.Or(in.Cols, 80)), Rows: uint16(cmp.Or(in.Rows, 24))})
	if err != nil {
		writeErr(w, err)
		return
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	t := &term{cmd: c, pty: f}
	h.terms.mu.Lock()
	if h.terms.m == nil {
		h.terms.m = map[string]*term{}
	}
	h.terms.m[id] = t
	h.terms.mu.Unlock()
	// A page that never attaches leaves no process behind.
	time.AfterFunc(30*time.Second, func() {
		if !t.attached.Load() {
			h.terms.drop(id)
		}
	})
	writeJSON(w, map[string]any{"id": id})
}

// termOut streams the PTY's bytes until the process ends or the page goes away; either ends the session.
func (h *Handler) termOut(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string }
	if !readJSON(w, r, &in) {
		return
	}
	t := h.terms.get(in.ID)
	if t == nil || !t.read.TryLock() {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "no terminal " + in.ID})
		return
	}
	t.attached.Store(true)
	defer h.terms.drop(in.ID)
	defer t.read.Unlock()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	go func() {
		<-r.Context().Done()
		t.close()
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *Handler) termIn(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID, Data string }
	if !readJSON(w, r, &in) {
		return
	}
	t := h.terms.get(in.ID)
	if t == nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "no terminal " + in.ID})
		return
	}
	if _, err := t.pty.Write([]byte(in.Data)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{})
}

func (h *Handler) termResize(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID         string
		Cols, Rows int
	}
	if !readJSON(w, r, &in) {
		return
	}
	if t := h.terms.get(in.ID); t != nil && in.Cols > 0 && in.Rows > 0 {
		_ = pty.Setsize(t.pty, &pty.Winsize{Cols: uint16(in.Cols), Rows: uint16(in.Rows)})
	}
	writeJSON(w, map[string]any{})
}
