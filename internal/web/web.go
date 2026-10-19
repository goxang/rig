// Package web is `rig web`: the control plane in a browser, served on this machine. The page is
// static; everything it shows comes from the Engine API (the calls `rig serve` answers), behind a
// token the printed link carries.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/server"
	"github.com/goxang/rig/spec"
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
	engines map[string]*opening
	ai      aiState
	terms   terms
}

type opening struct {
	done chan struct{}
	l    *engine.Local
	err  error
}

// Engine is the engine for env, opened once; opening one environment never waits on another.
func (h *Handler) Engine(env string) (*engine.Local, error) {
	h.mu.Lock()
	o, ok := h.engines[env]
	if !ok {
		o = &opening{done: make(chan struct{})}
		if h.engines == nil {
			h.engines = map[string]*opening{}
		}
		h.engines[env] = o
		go func() {
			defer close(o.done)
			o.l, o.err = h.open(env)
		}()
	}
	h.mu.Unlock()
	<-o.done
	if o.err != nil {
		h.mu.Lock()
		if h.engines[env] == o {
			delete(h.engines, env)
		}
		h.mu.Unlock()
	}
	return o.l, o.err
}

func (h *Handler) open(env string) (*engine.Local, error) {
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
	return engine.NewLocal(a, &jobs.Manager{}), nil
}

func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, o := range h.engines {
		<-o.done
		if o.l != nil {
			o.l.Close()
			o.l.App.Close()
		}
	}
	h.engines = nil
	h.ai.close()
	h.terms.mu.Lock()
	ids := slices.Collect(maps.Keys(h.terms.m))
	h.terms.mu.Unlock()
	for _, id := range ids {
		h.terms.drop(id)
	}
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
	h.extra(mux)
	h.termRoutes(mux)
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
	var sections []map[string]string
	for _, n := range l.App.Spec.SectionOrder {
		sections = append(sections, map[string]string{"name": n, "help": l.App.Spec.Sections[n].Help})
	}
	var screens []string
	for _, n := range spec.Screens {
		if l.App.Configured(n) {
			screens = append(screens, n)
		}
	}
	out := map[string]any{"project": l.App.Spec.Name, "env": l.App.Env.Name, "default": l.App.Spec.Default, "sections": sections, "screens": screens,
		"runtime": l.App.Env.Runtime.Type}
	if ui := l.App.Spec.UI; ui != nil {
		out["tabs"] = ui.Tabs
		out["logs"] = ui.Logs
	}
	out["used"] = used(l.App.Spec)
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
	status := http.StatusInternalServerError
	switch pe.Code {
	case protocol.CodeInvalidParams:
		status = http.StatusBadRequest
	case protocol.CodeNotFound:
		status = http.StatusNotFound
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": pe})
}

// used are the plugins and component types the project wires up: the API screen hides the rest.
// used names the plugins rig.yaml needs in any environment: those it names, the importers, component
// and runtime types, test runners, and the builders, debugger and IDE its services' builds imply.
func used(p *spec.Project) []string {
	set := map[string]bool{}
	for n, pl := range p.Plugins {
		if pl == nil || !pl.Off {
			set[n] = true
		}
	}
	for _, imp := range p.Imports {
		for k := range imp {
			set[k] = true
		}
	}
	add := func(cs map[string]*spec.Component) {
		for _, c := range cs {
			if c != nil {
				set[c.Type] = true
			}
		}
	}
	add(p.Components)
	for _, env := range p.Environments {
		if env == nil {
			continue
		}
		add(env.Components)
		if env.Runtime != nil {
			set[env.Runtime.Type] = true
		}
	}
	for _, t := range p.Tests {
		if t != nil && t.Runner != "" {
			set[t.Runner] = true
		}
	}
	for _, s := range p.Services {
		if s == nil || s.Build == nil {
			continue
		}
		switch {
		case s.Build.Go != "":
			set["go"], set["delve"], set["goland"] = true, true, true
		case s.Build.Dockerfile != "":
			set["docker"] = true
		case len(s.Build.Command) > 0:
			set["command"] = true
		}
	}
	if _, err := os.Stat(filepath.Join(p.Dir, ".git")); err == nil {
		set["git"] = true
	}
	delete(set, "")
	return slices.Sorted(maps.Keys(set))
}
