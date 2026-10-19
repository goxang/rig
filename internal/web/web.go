// Package web is `rig web`: the control plane in a browser, served on this machine. The page is
// static; everything it shows comes from the Engine API (the calls `rig serve` answers), behind a
// token the printed link carries.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/server"
)

//go:embed static
var static embed.FS

type Handler struct {
	File  string
	Token string
	// Open opens the project on an environment ("" is its default).
	Open func(file, env string) (*engine.App, error)
	// Info is merged into GET /v1/web: themes, the chosen theme, the version.
	Info map[string]any

	mu      sync.Mutex
	engines map[string]*engine.Local
}

// Engine is the engine for env, opened once.
func (h *Handler) Engine(env string) (*engine.Local, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.engines[env]; ok {
		return l, nil
	}
	a, err := h.Open(h.File, env)
	if err != nil {
		return nil, err
	}
	if a.Env == nil {
		a.Close()
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no environment " + env}
	}
	// each request settles confirmation in the browser, so the app's own terminal prompt stands aside
	a.Confirmed, a.Ask = true, nil
	l := engine.NewLocal(a, &jobs.Manager{})
	if h.engines == nil {
		h.engines = map[string]*engine.Local{}
	}
	h.engines[env] = l
	return l, nil
}

func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.engines {
		l.Close()
		l.App.Close()
	}
	h.engines = nil
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(static, "static")
	assets := http.FileServerFS(files)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		assets.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /v1/web", h.auth(h.info))
	mux.HandleFunc("POST /v1/engine/{method}", h.auth(func(w http.ResponseWriter, r *http.Request) {
		l, err := h.Engine(r.Header.Get("X-Rig-Env"))
		if err != nil {
			writeErr(w, err)
			return
		}
		server.Call(w, r, l, l.Session(r.Context()), nil)
	}))
	return mux
}

// auth wants the token as a bearer: a page on another site can send a request here but cannot
// read the token, and no cookie rides along.
func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.Token)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": protocol.Error{Code: protocol.CodeDenied, Message: "open the link rig web printed"}})
			return
		}
		next(w, r)
	}
}

func (h *Handler) info(w http.ResponseWriter, r *http.Request) {
	l, err := h.Engine(r.Header.Get("X-Rig-Env"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]any{"project": l.App.Spec.Name, "env": l.App.Env.Name, "default": l.App.Spec.Default}
	for k, v := range h.Info {
		out[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func writeErr(w http.ResponseWriter, err error) {
	pe, ok := err.(*protocol.Error)
	if !ok {
		pe = &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": pe})
}
