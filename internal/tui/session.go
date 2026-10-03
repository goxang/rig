package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

// Session is what S saves and `rig resume` brings back: the screen, what each screen was showing,
// and the query results and history of the run.
type Session struct {
	ID      string    `json:"id"`
	Env     string    `json:"env"`
	Created time.Time `json:"created"`
	Saved   time.Time `json:"saved"`
	Tab     int       `json:"tab"`

	LogServices []string `json:"log_services,omitempty"`
	LogInstance string   `json:"log_instance,omitempty"`
	LogGrep     string   `json:"log_grep,omitempty"`

	Queries   map[string]*spec.Query   `json:"adhoc_queries,omitempty"`
	Scheduled map[string]bool          `json:"scheduled,omitempty"`
	Results   map[string]savedResult   `json:"results,omitempty"`
	Runs      []queryRun               `json:"runs,omitempty"`
	DataPaths map[string][]string      `json:"data_paths,omitempty"`
	Dashboard int                      `json:"dashboard,omitempty"`
	Load      map[string]savedLoadHist `json:"load,omitempty"`
}

type savedResult struct {
	Table core.Table    `json:"table"`
	Err   string        `json:"err,omitempty"`
	At    time.Time     `json:"at"`
	Took  time.Duration `json:"took"`
	Hist  []float64     `json:"hist,omitempty"`
}

type savedLoadHist struct {
	Target, Actual, Failed []core.Point
}

func sessionDir(projectDir string) string { return filepath.Join(projectDir, ".rig", "sessions") }

// snapshot gathers the session from the screens.
func (m *model) snapshot() *Session {
	s := m.session
	if s == nil {
		s = &Session{ID: time.Now().Format("20060102-150405"), Created: time.Now()}
	}
	s.Env, s.Saved, s.Tab = m.app.Env.Name, time.Now(), m.active
	for _, t := range m.tabs {
		switch t := t.(type) {
		case *logsTab:
			s.LogServices, s.LogInstance, s.LogGrep = t.services, t.instance, t.grep
		case *dataTab:
			s.DataPaths = t.paths
		case *metricsTab:
			s.Dashboard = t.dash
		case *loadTab:
			s.Load = map[string]savedLoadHist{}
			for n, h := range t.hist {
				s.Load[n] = savedLoadHist{Target: h.target, Actual: h.actual, Failed: h.failed}
			}
		}
	}
	sc := m.sched
	sc.mu.Lock()
	defer sc.mu.Unlock()
	s.Queries, s.Scheduled, s.Results = sc.extra, map[string]bool{}, map[string]savedResult{}
	for n, on := range sc.active {
		if on {
			s.Scheduled[n] = true
		}
	}
	for n, r := range sc.results {
		sr := savedResult{Table: r.table, At: r.at, Took: r.took, Hist: r.hist}
		if r.err != nil {
			sr.Err = r.err.Error()
		}
		s.Results[n] = sr
	}
	s.Runs = sc.runs
	return s
}

func (m *model) saveSession() (string, error) {
	s := m.snapshot()
	m.session = s
	dir := sessionDir(m.app.Spec.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return s.ID, os.WriteFile(filepath.Join(dir, s.ID+".json"), raw, 0o644)
}

// restore puts a saved session back into freshly opened screens.
func (m *model) restore(s *Session) {
	m.session = s
	if s.Tab >= 0 && s.Tab < len(m.tabs) {
		m.active = s.Tab
	}
	sc := m.sched
	sc.mu.Lock()
	for n, q := range s.Queries {
		q.Name = n
		sc.extra[n] = q
	}
	for n := range s.Scheduled {
		sc.active[n] = true
	}
	for n, r := range s.Results {
		qr := &qresult{table: r.Table, at: r.At, took: r.Took, hist: r.Hist}
		if r.Err != "" {
			qr.err = errors.New(r.Err)
		}
		sc.results[n] = qr
	}
	sc.runs = s.Runs
	sc.mu.Unlock()
	for _, t := range m.tabs {
		switch t := t.(type) {
		case *logsTab:
			t.services, t.instance, t.grep = s.LogServices, s.LogInstance, s.LogGrep
		case *dataTab:
			t.restored = s.DataPaths
		case *metricsTab:
			t.dash = s.Dashboard
		case *loadTab:
			t.restored = s.Load
		}
	}
}

// LoadSession reads a saved session of the project; id "" or "last" is the newest.
func LoadSession(projectDir, id string) (*Session, error) {
	if id == "" || id == "last" {
		ss, err := ListSessions(projectDir)
		if err != nil {
			return nil, err
		}
		if len(ss) == 0 {
			return nil, fmt.Errorf("no saved sessions in %s (press S in rig to save one)", sessionDir(projectDir))
		}
		id = ss[0].ID
	}
	raw, err := os.ReadFile(filepath.Join(sessionDir(projectDir), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", id, err)
	}
	var s Session
	return &s, json.Unmarshal(raw, &s)
}

// ListSessions are the project's saved sessions, newest first, without their results.
func ListSessions(projectDir string) ([]Session, error) {
	files, err := filepath.Glob(filepath.Join(sessionDir(projectDir), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(raw, &s) == nil {
			s.Results = nil
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Saved.After(out[j].Saved) })
	return out, nil
}

// Resume opens the saved session's environment and the UI as it was.
func Resume(ctx context.Context, open func(env string) (*engine.App, error), projectDir, id string) error {
	s, err := LoadSession(projectDir, id)
	if err != nil {
		return err
	}
	a, err := open(s.Env)
	if err != nil {
		return err
	}
	defer a.Close()
	return run(ctx, a, s)
}

// Summary is one line about a saved session for listings.
func (s Session) Summary() string {
	parts := []string{s.Env, fmt.Sprintf("%d query runs", len(s.Runs))}
	if len(s.LogServices) > 0 {
		parts = append(parts, "logs "+strings.Join(s.LogServices, ","))
	}
	if n := len(s.Scheduled); n > 0 {
		parts = append(parts, fmt.Sprintf("%d scheduled", n))
	}
	return strings.Join(parts, " · ")
}
