package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// extra are the routes the TUI has beyond the Engine API: its screens (views), a plugin's
// credential, the `:` command line and the assistant.
func (h *Handler) extra(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/views", h.auth(h.withEngine(h.views)))
	mux.HandleFunc("POST /v1/views/render", h.auth(h.withEngine(h.render)))
	mux.HandleFunc("POST /v1/secret", h.auth(h.withEngine(h.secret)))
	mux.HandleFunc("POST /v1/cli", h.auth(h.withEngine(h.cli)))
	mux.HandleFunc("GET /v1/ai", h.auth(h.withEngine(h.aiInfo)))
	mux.HandleFunc("POST /v1/ai/config", h.auth(h.aiConfig))
	mux.HandleFunc("GET /v1/ai/options", h.auth(h.aiOptions))
	mux.HandleFunc("POST /v1/ai/probe", h.auth(h.aiProbe))
	mux.HandleFunc("GET /v1/ai/session", h.auth(h.withEngine(h.aiSession)))
	mux.HandleFunc("POST /v1/ai/close", h.auth(h.withEngine(func(w http.ResponseWriter, r *http.Request, l *engine.Local) {
		var in struct{ ID string }
		if !readJSON(w, r, &in) {
			return
		}
		if err := ai.CloseSession(l.App.AIDir(), in.ID); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{})
	})))
	mux.HandleFunc("POST /v1/ai/chat", h.auth(h.withEngine(h.aiChat)))
	mux.HandleFunc("GET /v1/ai/pending", h.auth(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, h.ai.list()) }))
	mux.HandleFunc("POST /v1/ai/approve", h.auth(h.aiApprove))
	mux.HandleFunc("POST /v1/ai/complete", h.auth(h.withEngine(h.aiComplete)))
}

// aiComplete finishes what a prompt holds, or writes it from a description after "@?" (as the TUI's prompts do).
func (h *Handler) aiComplete(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	var in struct{ Hint, Text string }
	if !readJSON(w, r, &in) {
		return
	}
	run, err := l.App.AI(h.ai.socket())
	if err != nil {
		writeErr(w, err)
		return
	}
	if !run.Setup.Enabled() {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "AI is " + run.Setup.Describe() + ": set it up in Settings"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var text string
	if before, want, ok := strings.Cut(in.Text, "@?"); ok && strings.TrimSpace(want) != "" {
		text, err = run.Describe(ctx, in.Hint, before, strings.TrimSpace(want), nil)
	} else {
		var rest string
		rest, err = run.Complete(ctx, in.Hint, in.Text)
		text = in.Text + rest
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"text": text})
}

func (h *Handler) withEngine(f func(http.ResponseWriter, *http.Request, *engine.Local)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, err := h.Engine(r.Header.Get("X-Rig-Env"))
		if err != nil {
			writeErr(w, err)
			return
		}
		f(w, r, l)
	}
}

type viewOut struct {
	Key     string         `json:"key"`
	Title   string         `json:"title"`
	Command string         `json:"command,omitempty"`
	Plugin  string         `json:"plugin,omitempty"`
	Remote  string         `json:"remote,omitempty"`
	Secret  string         `json:"secret,omitempty"`
	Group   string         `json:"group"`
	Root    *protocol.Node `json:"root,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func (h *Handler) views(w http.ResponseWriter, _ *http.Request, l *engine.Local) {
	out := []viewOut{}
	for _, d := range engine.Views(l.App) {
		v := viewOut{Key: d.Key, Title: d.Title, Plugin: d.Plugin, Remote: d.Remote, Group: "project"}
		switch {
		case d.Key == "view:Home":
			v.Group = "home"
		case d.Rank < 20:
			v.Group = "dev"
		case d.Rank >= 30:
			v.Group = "user"
		}
		if d.View.Slot == "command" {
			v.Command = d.View.Command
		}
		if len(d.Secrets) > 0 && !l.App.CredentialSet(d.Plugin, d.Secrets) {
			v.Secret = d.Secrets[0]
		}
		root := d.View.Root
		if d.View.File != "" && root.Widget == "" {
			raw, err := os.ReadFile(filepath.Join(d.Dir, d.View.File))
			if err == nil {
				vv := *d.View
				if err = vv.Load(raw); err == nil {
					root = vv.Root
				}
			}
			if err != nil {
				v.Error = fmt.Sprintf("%s: %v", d.View.File, err)
			}
		}
		if root.Widget != "" {
			v.Root = &root
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	var in struct{ Remote string }
	if !readJSON(w, r, &in) {
		return
	}
	n, err := l.Host.View(r.Context(), in.Remote, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, n)
}

// secret keeps a plugin's credential as `rig secret set` does; only a name a view asks for is taken.
func (h *Handler) secret(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	var in struct{ Name, Value string }
	if !readJSON(w, r, &in) {
		return
	}
	asked := false
	for _, d := range engine.Views(l.App) {
		asked = asked || slices.Contains(d.Secrets, in.Name)
	}
	if !asked || strings.TrimSpace(in.Value) == "" {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "no plugin asks for " + in.Name})
		return
	}
	if err := spec.SetSecret(l.App.Spec.Name, in.Name, strings.TrimSpace(in.Value)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// cli runs a `:` line as `rig <line>` in this project and environment, streaming its output as
// NDJSON {"out": ...} lines and a last {"exit": code}.
func (h *Handler) cli(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	var in struct{ Line string }
	if !readJSON(w, r, &in) {
		return
	}
	args := strings.Fields(in.Line)
	if len(args) == 0 {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "empty command"})
		return
	}
	if args[0] == "rig" {
		args = args[1:]
	}
	exe, err := os.Executable()
	if err != nil {
		writeErr(w, err)
		return
	}
	a := l.App
	c := exec.CommandContext(r.Context(), exe, append([]string{"-f", a.Spec.File, "-e", a.Env.Name}, args...)...)
	c.Dir = a.Spec.Dir
	c.Env = append(os.Environ(), "RIG_BRIEF=1", "NO_COLOR=1")
	nd := newNDJSON(w)
	out := writerFunc(func(p []byte) (int, error) { nd.send(map[string]any{"out": string(p)}); return len(p), nil })
	c.Stdout, c.Stderr = out, out
	code := 0
	if err := c.Run(); err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			nd.send(map[string]any{"out": err.Error() + "\n"})
		}
	}
	nd.send(map[string]any{"exit": code})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type ndjson struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func newNDJSON(w http.ResponseWriter) *ndjson {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	f, _ := w.(http.Flusher)
	return &ndjson{w: w, f: f}
}

func (n *ndjson) send(v any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	_ = json.NewEncoder(n.w).Encode(v)
	if n.f != nil {
		n.f.Flush()
	}
}

// aiState is the bridge the assistant's tools ask for approval on; the page answers.
type aiState struct {
	once    sync.Once
	sock    string
	stop    func()
	mu      sync.Mutex
	seq     int
	pending map[string]*approval
	always  map[string]bool
}

type approval struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	reply chan ai.Reply
}

func (s *aiState) socket() string {
	s.once.Do(func() {
		s.pending, s.always = map[string]*approval{}, map[string]bool{}
		sock := ai.SocketPath()
		stop, err := ai.Serve(sock, s.handle)
		if err == nil {
			s.sock, s.stop = sock, stop
		}
	})
	return s.sock
}

func (s *aiState) handle(r ai.Request) ai.Reply {
	switch r.Op {
	case "audit":
		return ai.Reply{OK: true}
	case "approve":
	default:
		return ai.Reply{Text: "the browser shows no screen on request"}
	}
	s.mu.Lock()
	if s.always[r.Command] {
		s.mu.Unlock()
		return ai.Reply{OK: true, Text: "allowed for this session"}
	}
	s.seq++
	p := &approval{ID: strconv.Itoa(s.seq), Text: r.Text, reply: make(chan ai.Reply, 1)}
	s.pending[p.ID] = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, p.ID)
		s.mu.Unlock()
	}()
	select {
	case rep := <-p.reply:
		if rep.OK && rep.Text == "always" {
			s.mu.Lock()
			s.always[r.Command] = true
			s.mu.Unlock()
			rep.Text = "the user, in the browser (always this session)"
		}
		return rep
	case <-time.After(10 * time.Minute):
		return ai.Reply{Text: "no answer in the browser"}
	}
}

func (s *aiState) list() []*approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*approval{}
	for _, p := range s.pending {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *approval) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (s *aiState) close() {
	if s.stop != nil {
		s.stop()
	}
}

func (h *Handler) aiApprove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string
		OK     bool
		Always bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	h.ai.mu.Lock()
	p := h.ai.pending[in.ID]
	h.ai.mu.Unlock()
	if p == nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeNotFound, Message: "that question is gone"})
		return
	}
	rep := ai.Reply{OK: in.OK, Text: "the user, in the browser"}
	if !in.OK {
		rep.Text = ""
	} else if in.Always {
		rep.Text = "always"
	}
	select {
	case p.reply <- rep:
	default:
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (h *Handler) aiInfo(w http.ResponseWriter, _ *http.Request, l *engine.Local) {
	c, err := ai.LoadConfig()
	if err != nil {
		writeErr(w, err)
		return
	}
	conf := map[string]string{}
	for _, k := range ai.Keys {
		conf[k] = c.Get(k)
	}
	s := ai.Resolve(c)
	var sessions []map[string]any
	if ss, err := ai.ListSessions(l.App.AIDir()); err == nil {
		for _, x := range ss {
			if x.Env == l.App.Env.Name {
				sessions = append(sessions, map[string]any{"id": x.ID, "title": x.Title, "updated": x.Updated, "messages": len(x.Messages)})
			}
		}
	}
	file, _ := ai.ConfigFile()
	writeJSON(w, map[string]any{"config": conf, "keys": ai.Keys, "providers": ai.Providers, "describe": s.Describe(),
		"enabled": s.Enabled(), "file": file, "sessions": sessions})
}

// aiOptions lists what this machine can run the assistant with: installed backends (with their
// models) and local model servers.
func (h *Handler) aiOptions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	type backend struct {
		Backend string   `json:"backend"`
		Model   string   `json:"model"`
		Ready   bool     `json:"ready"`
		Models  []string `json:"models"`
	}
	var backends []backend
	for _, f := range ai.Detect() {
		b := backend{Backend: f.Backend, Model: f.Model, Ready: f.Ready, Models: []string{}}
		if f.Backend == ai.BackendClaude {
			b.Models = []string{"opus", "sonnet", "haiku"}
		} else if out, err := exec.CommandContext(ctx, f.Bin, "models").Output(); err == nil {
			b.Models = strings.Fields(string(out))
		}
		backends = append(backends, b)
	}
	local := []map[string]any{}
	for _, s := range ai.DetectLocal(ctx) {
		local = append(local, map[string]any{"provider": s.Provider, "url": s.URL, "models": s.Models})
	}
	writeJSON(w, map[string]any{"backends": backends, "local": local})
}

// aiProbe lists the models of an OpenAI-compatible endpoint.
func (h *Handler) aiProbe(w http.ResponseWriter, r *http.Request) {
	var in struct{ URL, APIKey string }
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(in.URL, "/")+"/models", nil)
	if err != nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
		return
	}
	if in.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+in.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
		return
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if resp.StatusCode/100 != 2 || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list) != nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "no model list at " + in.URL + " (" + resp.Status + ")"})
		return
	}
	models := []string{}
	for _, m := range list.Data {
		models = append(models, m.ID)
	}
	writeJSON(w, map[string]any{"models": models})
}

func (h *Handler) aiConfig(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if !readJSON(w, r, &in) {
		return
	}
	c, err := ai.LoadConfig()
	if err != nil {
		writeErr(w, err)
		return
	}
	for k, v := range in {
		if err := c.Set(k, v); err != nil {
			writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
			return
		}
	}
	if err := ai.SaveConfig(c); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "describe": ai.Resolve(c).Describe()})
}

func (h *Handler) aiSession(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	s, err := ai.LoadSession(l.App.AIDir(), r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeNotFound, Message: err.Error()})
		return
	}
	writeJSON(w, s)
}

// aiChat runs one turn and streams its events as NDJSON: the session first, then text, tool and
// error events, and a last done.
func (h *Handler) aiChat(w http.ResponseWriter, r *http.Request, l *engine.Local) {
	var in struct{ Text, Session, Screen string }
	if !readJSON(w, r, &in) {
		return
	}
	a := l.App
	run, err := a.AI(h.ai.socket())
	if err != nil {
		writeErr(w, err)
		return
	}
	if !run.Setup.Enabled() {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "AI is " + run.Setup.Describe() + ": set it up in Settings"})
		return
	}
	var s *ai.Session
	if in.Session != "" {
		if s, err = ai.LoadSession(a.AIDir(), in.Session); err != nil {
			writeErr(w, &protocol.Error{Code: protocol.CodeNotFound, Message: err.Error()})
			return
		}
	} else {
		s = ai.NewSession(a.AIDir(), run.Setup.Backend, a.Env.Name)
	}
	nd := newNDJSON(w)
	nd.send(map[string]any{"kind": "session", "text": s.ID, "describe": run.Setup.Describe()})
	err = run.Turn(r.Context(), s, in.Text, in.Screen, func(e ai.Event) { nd.send(map[string]any{"kind": e.Kind, "text": e.Text}) })
	if err != nil {
		nd.send(map[string]any{"kind": "error", "text": err.Error()})
	}
	nd.send(map[string]any{"kind": "done", "text": s.Next})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(v); err != nil {
		writeErr(w, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
		return false
	}
	// net/http notices a page going away only once the body is read to its end: streams rely on that.
	_, _ = io.Copy(io.Discard, body)
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
