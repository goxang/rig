package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	TabName string    `json:"tab_name,omitempty"`

	LogServices  []string `json:"log_services,omitempty"`
	LogInstances []string `json:"log_instances,omitempty"`
	LogGrep      string   `json:"log_grep,omitempty"`

	Queries   map[string]*spec.Query   `json:"adhoc_queries,omitempty"`
	Scheduled map[string]bool          `json:"scheduled,omitempty"`
	Results   map[string]savedResult   `json:"results,omitempty"`
	Runs      []queryRun               `json:"runs,omitempty"`
	DataPaths map[string][]string      `json:"data_paths,omitempty"`
	Dashboard int                      `json:"dashboard,omitempty"`
	Load      map[string]savedLoadHist `json:"load,omitempty"`

	Sorts    map[string]gridSort `json:"sorts,omitempty"`
	Drafts   map[string]draft    `json:"drafts,omitempty"`
	Activity []savedJob          `json:"activity,omitempty"`
	Errors   []loggedErr         `json:"errors,omitempty"`

	Views map[string]map[string]string `json:"views,omitempty"`
}

// savedJob is an operation of the activity view (!) as a session keeps it: its last lines only.
type savedJob struct {
	Label string    `json:"label"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Err   string    `json:"err,omitempty"`
	Lines []string  `json:"lines,omitempty"`
	// Log and Pid: a task still running when the session was saved, followed again on restore
	Log string `json:"log,omitempty"`
	Pid int    `json:"pid,omitempty"`
}

const savedJobLines = 300

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

func sessionDir(projectDir string) string {
	dir, err := spec.DataDir(projectDir)
	if err != nil {
		return filepath.Join(projectDir, ".rig", "sessions")
	}
	dir = filepath.Join(dir, "sessions")
	// sessions used to live in the project's .rig; bring them along once
	if old := filepath.Join(projectDir, ".rig", "sessions"); exists(old) && !exists(dir) {
		if os.MkdirAll(filepath.Dir(dir), 0o755) == nil {
			_ = os.Rename(old, dir)
		}
	}
	return dir
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// snapshot gathers the session from the screens.
func (m *model) snapshot() *Session {
	s := m.session
	if s == nil {
		s = &Session{ID: time.Now().Format("20060102-150405"), Created: time.Now()}
	}
	s.Env, s.Saved, s.Tab, s.TabName = m.app.Env.Name, time.Now(), m.active, m.tabs[m.active].name()
	m.keepViews()
	s.Views = m.views
	for _, t := range m.tabs {
		switch t := t.(type) {
		case *logsTab:
			s.LogServices, s.LogInstances, s.LogGrep = t.services, t.instances, t.grep
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
	s.Sorts, s.Drafts, s.Errors = gridSorts, m.drafts, m.errLog
	s.Activity = nil
	for _, j := range m.jobList() {
		end, _, err := j.state()
		sj := savedJob{Label: j.label, Start: j.start, End: end, Lines: j.output()}
		switch {
		case end.IsZero() && j.log != "":
			sj.Log, sj.Pid, sj.Lines = j.log, j.pid, nil
		case end.IsZero():
			sj.End, sj.Err = time.Now(), "still running when the session was saved"
		case err != nil:
			sj.Err = err.Error()
		}
		sj.Lines = sj.Lines[max(0, len(sj.Lines)-savedJobLines):]
		s.Activity = append(s.Activity, sj)
	}
	return s
}

func (m *model) saveSession() (string, error) {
	s := m.snapshot()
	m.session = s
	return s.ID, writeSession(m.app.Spec.Dir, s)
}

// autosave keeps a saved session current as the user moves between screens.
func (m *model) autosave() {
	if m.session == nil {
		return
	}
	m.session = m.snapshot()
	raw, err := json.Marshal(m.session)
	if err != nil {
		return
	}
	dir, id := m.app.Spec.Dir, m.session.ID
	go func() {
		m.saving.Lock()
		defer m.saving.Unlock()
		_ = os.WriteFile(filepath.Join(sessionDir(dir), id+".json"), raw, 0o644)
	}()
}

func writeSession(projectDir string, s *Session) error {
	dir := sessionDir(projectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(pinFile(projectDir), []byte(s.ID), 0o644)
}

// restore puts a saved session back into freshly opened screens.
func (m *model) restore(s *Session) {
	m.session = s
	if s.Tab >= 0 && s.Tab < len(m.tabs) {
		m.active = s.Tab
	}
	for i, t := range m.tabs {
		if t.name() == s.TabName {
			m.active = i
		}
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
	for id, st := range s.Sorts {
		gridSorts[id] = st
	}
	m.drafts, m.errLog, m.views = s.Drafts, s.Errors, s.Views
	m.jobsMu.Lock()
	for _, sj := range s.Activity {
		j := &job{label: sj.Label, start: sj.Start, end: sj.End, lines: sj.Lines, log: sj.Log, pid: sj.Pid}
		if sj.Err != "" {
			j.err = errors.New(sj.Err)
		}
		if sj.Log != "" && sj.End.IsZero() {
			ctx, stop := context.WithCancel(context.Background())
			j.stop = stop
			j.killOnStop(ctx)
			go func() { j.finish(j.follow()) }()
		}
		m.jobs = append(m.jobs, j)
	}
	m.jobsMu.Unlock()
	for _, t := range m.tabs {
		switch t := t.(type) {
		case *logsTab:
			t.services, t.instances, t.grep = s.LogServices, s.LogInstances, s.LogGrep
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
	return run(ctx, a, s, nil)
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

// CloseSession forgets a saved session.
func CloseSession(projectDir, id string) error {
	if p, _ := Pinned(projectDir); p == id {
		Unpin(projectDir)
	}
	return os.Remove(filepath.Join(sessionDir(projectDir), id+".json"))
}

func pinFile(projectDir string) string { return filepath.Join(sessionDir(projectDir), "pinned") }

// Pinned is the session rig opens on (the last one S saved) and its environment; "" for none.
func Pinned(projectDir string) (id, env string) {
	raw, err := os.ReadFile(pinFile(projectDir))
	if err != nil {
		return "", ""
	}
	s, err := LoadSession(projectDir, strings.TrimSpace(string(raw)))
	if err != nil {
		return "", ""
	}
	return s.ID, s.Env
}

// Unpin has rig open fresh again.
func Unpin(projectDir string) { _ = os.Remove(pinFile(projectDir)) }

// PickSession is `rig resume`'s chooser: rows are id, kind, saved, what; enter picks one, d closes
// the selected one (through close), q leaves. It returns the picked row, -1 for none.
func PickSession(rows [][]string, close func(i int) error) (int, error) {
	p := &sessionPicker{rows: rows, close: close, pick: -1}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	p.idx = idx
	if _, err := tea.NewProgram(p).Run(); err != nil {
		return -1, err
	}
	return p.pick, nil
}

type sessionPicker struct {
	rows  [][]string
	idx   []int // rows still listed
	sel   int
	pick  int
	close func(i int) error
	note  string
}

func (p *sessionPicker) Init() tea.Cmd { return nil }

func (p *sessionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch k.String() {
	case "q", "esc", "ctrl+c":
		return p, tea.Quit
	case "enter":
		if len(p.idx) > 0 {
			p.pick = p.idx[p.sel]
		}
		return p, tea.Quit
	case "d", "delete":
		if len(p.idx) == 0 {
			return p, nil
		}
		i := p.idx[p.sel]
		if err := p.close(i); err != nil {
			p.note = sRed.Render(err.Error())
			return p, nil
		}
		p.note = sDim.Render("closed " + p.rows[i][0])
		p.idx = append(p.idx[:p.sel:p.sel], p.idx[p.sel+1:]...)
		p.sel = max(0, min(p.sel, len(p.idx)-1))
	default:
		listKeys(k, &p.sel, len(p.idx))
	}
	return p, nil
}

func (p *sessionPicker) View() string {
	var b strings.Builder
	b.WriteString(sTitle.Render("continue a session") + sDim.Render("  ↑↓ move · enter continue · d close · q quit") + "\n\n")
	if len(p.idx) == 0 {
		b.WriteString(sDim.Render("no sessions left") + "\n")
	}
	for n, i := range p.idx {
		r := p.rows[i]
		kind := sAccent.Render(padRight(r[1], 3))
		line := fmt.Sprintf("%s %s  %s  %s", kind, padRight(r[0], 22), sDim.Render(r[2]), truncate(r[3], 70))
		if n == p.sel {
			line = sAccent.Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if p.note != "" {
		b.WriteString("\n" + p.note + "\n")
	}
	return b.String()
}

func (m *model) keepViews() {
	if m.views == nil {
		m.views = map[string]map[string]string{}
	}
	for i, t := range m.tabs {
		if k, ok := t.(keeper); ok && m.opened[i] {
			m.views[t.name()] = k.keep()
		}
	}
}

// keeper is a screen whose filters a session keeps; resume runs right after its first open.
type keeper interface {
	keep() map[string]string
	resume(m *model, v map[string]string) tea.Cmd
}

func (t *servicesTab) keep() map[string]string {
	return map[string]string{"filter": t.filter, "section": t.section}
}

func (t *servicesTab) resume(m *model, v map[string]string) tea.Cmd {
	t.filter = v["filter"]
	if sec := v["section"]; sec == "other" || sec == "infra" || slices.Contains(m.app.Spec.SectionOrder, sec) {
		t.setSection(sec)
	}
	return nil
}

func (t *kvTab) keep() map[string]string {
	return map[string]string{"comp": t.comp, "prefix": t.prefix, "filter": t.filter}
}

func (t *kvTab) resume(m *model, v map[string]string) tea.Cmd {
	t.prefix, t.filter = v["prefix"], v["filter"]
	if v["comp"] == "" || v["comp"] == t.comp || !slices.Contains(t.comps(m), v["comp"]) {
		return nil
	}
	t.comp = v["comp"]
	return t.load(m)
}

func (t *manifestsTab) keep() map[string]string {
	view := "cluster"
	if !t.cluster {
		view = "files"
	}
	return map[string]string{"filter": t.filter, "view": view}
}

func (t *manifestsTab) resume(m *model, v map[string]string) tea.Cmd {
	t.filter = v["filter"]
	if v["view"] == "files" {
		t.cluster = false
	}
	return nil
}

func (t *tracesTab) keep() map[string]string {
	return map[string]string{"service": t.service, "op": t.op, "text": t.text, "min": t.min.String(),
		"back": strconv.Itoa(t.back), "errors": strconv.FormatBool(t.errsOnly)}
}

func (t *tracesTab) resume(m *model, v map[string]string) tea.Cmd {
	t.service, t.op, t.text = v["service"], v["op"], v["text"]
	t.min, _ = time.ParseDuration(v["min"])
	if b, err := strconv.Atoi(v["back"]); err == nil && b >= 0 && b < len(lookbacks) {
		t.back = b
	}
	t.errsOnly = v["errors"] == "true"
	return t.refresh(m)
}

func (t *metricsTab) keep() map[string]string {
	return map[string]string{"range": strconv.Itoa(t.win.rng), "every": strconv.Itoa(t.every)}
}

func (t *metricsTab) resume(m *model, v map[string]string) tea.Cmd {
	if r, err := strconv.Atoi(v["range"]); err == nil && r >= 0 && r < len(t.win.presets) {
		t.win.rng = r
	}
	if e, err := strconv.Atoi(v["every"]); err == nil && e >= 0 && e < len(refreshes) {
		t.every = e
	}
	return nil
}

func (t *testsTab) keep() map[string]string {
	v := map[string]string{"suite": strconv.Itoa(t.suite), "filter": strconv.Itoa(t.filter)}
	if t.search != nil {
		v["search"] = t.search.String()
	}
	return v
}

func (t *testsTab) resume(m *model, v map[string]string) tea.Cmd {
	if n, err := strconv.Atoi(v["suite"]); err == nil {
		t.suite = max(0, n)
	}
	t.filter, _ = strconv.Atoi(v["filter"])
	t.search, _ = regexp.Compile(v["search"])
	if v["search"] == "" {
		t.search = nil
	}
	return t.load(m)
}
