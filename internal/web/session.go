package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/goxang/rig/spec"
)

// webSession is what the browser saves: its stored choices (filters, sorts, pane sizes, queries)
// and the page it was on. The browser reopens on it when the link carries no page.
type webSession struct {
	Saved time.Time         `json:"saved"`
	Env   string            `json:"env,omitempty"`
	Hash  string            `json:"hash,omitempty"`
	State map[string]string `json:"state"`
}

func (h *Handler) sessionFile() string {
	dir := filepath.Dir(h.File)
	if d, err := spec.DataDir(dir); err == nil {
		return filepath.Join(d, "sessions", "web.json")
	}
	return filepath.Join(dir, ".rig", "sessions", "web.json")
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	path := h.sessionFile()
	switch r.Method {
	case http.MethodGet:
		b, err := os.ReadFile(path)
		if err != nil {
			writeJSON(w, map[string]any{})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	case http.MethodPost:
		var s webSession
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.Saved = time.Now()
		b, _ := json.Marshal(s)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = os.WriteFile(path, b, 0o600)
		}
		writeJSON(w, map[string]any{"saved": s.Saved})
	case http.MethodDelete:
		_ = os.Remove(path)
		writeJSON(w, map[string]any{})
	}
}
