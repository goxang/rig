package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/spec"
)

// ---- scheduler: saved queries that run on their own while the UI is open ----

type qresult struct {
	table core.Table
	err   error
	at    time.Time
	took  time.Duration
	hist  []float64
}

type scheduler struct {
	a       *engine.App
	mu      sync.Mutex
	active  map[string]bool
	running map[string]bool
	results map[string]*qresult
	extra   map[string]*spec.Query // ad hoc queries written in the UI
	runs    []queryRun             // every run this session, oldest first
}

// queryRun is one run of a query, kept for the history (H) and saved with the session.
type queryRun struct {
	Name  string        `json:"name"`
	Text  string        `json:"text"`
	Auto  bool          `json:"auto,omitempty"`
	At    time.Time     `json:"at"`
	Took  time.Duration `json:"took"`
	Table core.Table    `json:"table"`
	Err   string        `json:"err,omitempty"`
}

const (
	maxRuns    = 1000
	maxRunRows = 300
)

type schedMsg struct {
	s    *scheduler
	name string
	text string
	auto bool
	res  qresult
}

func newScheduler(a *engine.App) *scheduler {
	s := &scheduler{a: a, active: map[string]bool{}, running: map[string]bool{}, results: map[string]*qresult{}, extra: map[string]*spec.Query{}}
	for n, q := range a.Queries() {
		s.active[n] = q.Active && q.Every > 0
	}
	for n, on := range s.loadPrefs() {
		if _, ok := a.Queries()[n]; ok {
			s.active[n] = on
		}
	}
	return s
}

// prefs keep which schedules the user switched on or off, per environment, on this machine only.
func (s *scheduler) prefsFile() string { return filepath.Join(s.a.StateDir(), "ui.json") }

func (s *scheduler) loadPrefs() map[string]bool {
	var p struct {
		Active map[string]bool `json:"active_queries"`
	}
	raw, err := os.ReadFile(s.prefsFile())
	if err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	return p.Active
}

func (s *scheduler) savePrefs() {
	p := struct {
		Active map[string]bool `json:"active_queries"`
	}{Active: map[string]bool{}}
	s.mu.Lock()
	for n, on := range s.active {
		if _, adhoc := s.extra[n]; !adhoc {
			p.Active[n] = on
		}
	}
	s.mu.Unlock()
	raw, _ := json.MarshalIndent(p, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.prefsFile()), 0o755)
	_ = os.WriteFile(s.prefsFile(), raw, 0o644)
}

func (s *scheduler) queries() map[string]*spec.Query {
	out := s.a.Queries()
	for n, q := range s.extra {
		out[n] = q
	}
	return out
}

func (s *scheduler) activeCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, on := range s.active {
		if on {
			n++
		}
	}
	return n
}

// due starts every active query whose interval has passed.
func (s *scheduler) due(m *model) tea.Cmd {
	var cmds []tea.Cmd
	qs := s.queries()
	s.mu.Lock()
	var names []string
	for n, on := range s.active {
		q := qs[n]
		if !on || q == nil || q.Every <= 0 || s.running[n] {
			continue
		}
		if r := s.results[n]; r != nil && time.Since(r.at) < q.Every {
			continue
		}
		names = append(names, n)
	}
	s.mu.Unlock()
	for _, n := range names {
		text, _ := engine.FillQuery(qs[n], nil)
		cmds = append(cmds, s.runAs(m.ctx, n, text, true))
	}
	return batch(cmds...)
}

// run executes a query (text already filled in) under its name.
func (s *scheduler) run(ctx context.Context, name, text string) tea.Cmd {
	return s.runAs(ctx, name, text, false)
}

func (s *scheduler) runAs(ctx context.Context, name, text string, auto bool) tea.Cmd {
	s.mu.Lock()
	if s.running[name] {
		s.mu.Unlock()
		return nil
	}
	s.running[name] = true
	q := s.queries()[name]
	s.mu.Unlock()
	return func() tea.Msg {
		start := time.Now()
		t, err := s.a.RunQuery(ctx, q.Source, text)
		return schedMsg{s: s, name: name, text: text, auto: auto, res: qresult{table: t, err: err, at: time.Now(), took: time.Since(start)}}
	}
}

func (s *scheduler) done(msg schedMsg) {
	if msg.s != s {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, msg.name)
	prev := s.results[msg.name]
	r := msg.res
	if prev != nil {
		r.hist = prev.hist
	}
	if v, ok := firstNumber(r.table); ok && r.err == nil {
		r.hist = append(r.hist, v)
		if len(r.hist) > 120 {
			r.hist = r.hist[len(r.hist)-120:]
		}
	}
	s.results[msg.name] = &r
	run := queryRun{Name: msg.name, Text: msg.text, Auto: msg.auto, At: r.at, Took: r.took, Table: r.table}
	if len(run.Table.Rows) > maxRunRows {
		run.Table.Rows = run.Table.Rows[:maxRunRows]
		run.Table.Note = strings.TrimSpace(run.Table.Note + fmt.Sprintf(" (history keeps the first %d rows)", maxRunRows))
	}
	if r.err != nil {
		run.Err = r.err.Error()
	}
	s.runs = append(s.runs, run)
	if len(s.runs) > maxRuns {
		s.runs = s.runs[len(s.runs)-maxRuns:]
	}
}

func (s *scheduler) result(name string) *qresult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results[name]
}

func (s *scheduler) toggle(name string) bool {
	s.mu.Lock()
	s.active[name] = !s.active[name]
	on := s.active[name]
	s.mu.Unlock()
	s.savePrefs()
	return on
}

// firstNumber is the value a scheduled query charts: the first numeric cell of the first row.
func firstNumber(t core.Table) (float64, bool) {
	if len(t.Rows) == 0 {
		return 0, false
	}
	for _, c := range t.Rows[0] {
		if f, err := strconv.ParseFloat(strings.TrimSpace(c), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ---- the screen ----

// queriesTab is saved queries (from rig.yaml) and ad hoc ones in one list: run any, schedule any,
// sort the result like any grid.
type queriesTab struct {
	list      *grid
	result    *grid
	focusRes  bool
	shownFor  string
	langs     map[string]string
	adhocSeq  int
	resultFor string
	history   bool // H: every run of this session instead of the query list
	hist      *grid
}

func (t *queriesTab) name() string { return "Queries" }
func (t *queriesTab) typing() bool { return false }
func (t *queriesTab) hints() [][2]string {
	if t.focusRes {
		return [][2]string{{"←", "query list"}, {"↑↓", "rows"}, {"y Y", "copy row, all"}, {"ctrl+alt+←→↑↓", "sort, order"}}
	}
	if t.history {
		return [][2]string{{"↑↓", "run"}, {"→", "its result"}, {"H ctrl+←→", "back to queries"}}
	}
	return [][2]string{{"enter", "run"}, {"e", "edit & run"}, {"n", "new query"}, {"y Y", "copy query, result"}, {"a", "schedule on/off"}, {"H ctrl+←→", "history"}, {"→", "result"}, {"ctrl+alt+←→↑↓", "sort"}}
}

func (t *queriesTab) interval() time.Duration { return time.Second }

func (t *queriesTab) open(m *model) tea.Cmd {
	t.list = newGrid("queries", col("", 2), col("QUERY", 26), col("GROUP", 12), col("SOURCE", 10), rcol("EVERY", 6), rcol("LAST", 8), rcol("ROWS", 5), col("TREND", 14), col("WHAT", 0))
	t.list.sortBy = 2
	t.result = newGrid("result")
	t.hist = newGrid("hist", col("TIME", 8), col("QUERY", 26), col("BY", 9), rcol("ROWS", 6), rcol("TOOK", 7), col("FIRST VALUE", 0))
	a, gen := m.app, m.gen
	return func() tea.Msg {
		langs := map[string]string{}
		for n, q := range a.Queriers() {
			langs[n] = q.QueryLanguage()
		}
		return queryLangsMsg{gen: gen, langs: langs}
	}
}

type queryLangsMsg struct {
	gen   int
	langs map[string]string
}

func (t *queriesTab) refresh(m *model) tea.Cmd { return nil }

func (t *queriesTab) selected(m *model) (string, *spec.Query) {
	r, ok := t.list.current()
	if !ok {
		return "", nil
	}
	return r.id, m.sched.queries()[r.id]
}

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

// runSelected runs the query, asking for its {{placeholders}} first when it has any.
func (t *queriesTab) runSelected(m *model, name string, q *spec.Query) tea.Cmd {
	keys := placeholder.FindAllStringSubmatch(q.Query, -1)
	if len(keys) == 0 {
		t.shownFor = name
		return m.sched.run(m.work(), name, q.Query)
	}
	seen := map[string]bool{}
	var parts []string
	for _, k := range keys {
		if !seen[k[1]] {
			seen[k[1]] = true
			parts = append(parts, k[1]+"="+q.Params[k[1]])
		}
	}
	m.ask(name+" parameters", strings.Join(parts, " "), func(v string) tea.Cmd {
		args := map[string]string{}
		for _, f := range strings.Fields(v) {
			k, val, _ := strings.Cut(f, "=")
			args[k] = val
		}
		text, missing := engine.FillQuery(q, args)
		if len(missing) > 0 {
			m.setStatus(name+" needs "+strings.Join(missing, ", "), true)
			return nil
		}
		t.shownFor = name
		return m.sched.run(m.work(), name, text)
	})
	return nil
}

func (t *queriesTab) newQuery(m *model, comp string) {
	lang := t.langs[comp]
	defer m.asPopup()
	m.askTemplate(comp+" ("+lang+") query", examples[lang], "a "+lang+" query on component "+comp, func(v string) tea.Cmd {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		t.adhocSeq++
		name := fmt.Sprintf("adhoc-%d", t.adhocSeq)
		m.sched.extra[name] = &spec.Query{Name: name, Source: comp, Query: v, Group: "ad hoc", Every: 10 * time.Second, Help: v}
		t.list.set(t.rows(m))
		for i, r := range t.list.rows {
			if r.id == name {
				t.list.sel = i
			}
		}
		t.shownFor = name
		return m.sched.run(m.work(), name, v)
	})
}

var examples = map[string]string{
	"sql": "SELECT 1", "promql": "up", "logql": `{app=~".+"}`, "redis": "INFO", "kubectl": "get pods -o wide",
	"http": "GET /", "traces": "limit=20", "kv": "", "rabbitmq": "queues",
}

func (t *queriesTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case queryLangsMsg:
		if msg.gen == m.gen {
			t.langs = msg.langs
		}
	case tea.KeyMsg:
		if t.focusRes {
			if msg.String() == "left" || msg.String() == "esc" {
				t.focusRes = false
				return nil
			}
			if !t.result.key(msg) {
				t.copyResult(m, msg.String())
			}
			return nil
		}
		if s := msg.String(); s == "H" || s == "shift+left" || s == "shift+right" {
			t.history, t.resultFor = !t.history, ""
			return nil
		}
		if t.history {
			if !t.hist.key(msg) {
				switch msg.String() {
				case "right", "l", "enter":
					t.focusRes = true
				}
			}
			t.resultFor = ""
			return nil
		}
		if t.list.key(msg) {
			return nil
		}
		name, q := t.selected(m)
		switch msg.String() {
		case "right", "l":
			t.focusRes = true
		case "y":
			if q != nil {
				copyText(q.Query)
				m.setStatus("copied "+name+"'s query", false)
			}
		case "Y":
			t.copyResult(m, "Y")
		case "enter":
			if q != nil {
				return t.runSelected(m, name, q)
			}
		case "e":
			if q != nil {
				m.askAI("edit "+name+" ("+q.Source+")", q.Query, "a "+t.langs[q.Source]+" query on component "+q.Source, func(v string) tea.Cmd {
					nq := *q
					nq.Query = v
					t.adhocSeq++
					n := fmt.Sprintf("%s*", name)
					nq.Name, nq.Group = n, "ad hoc"
					m.sched.extra[n] = &nq
					return t.runSelected(m, n, &nq)
				})
				m.asPopup()
			}
		case "a":
			if q != nil {
				if q.Every <= 0 {
					m.ask("run "+name+" every", "30s", func(v string) tea.Cmd {
						d, err := time.ParseDuration(strings.TrimSpace(v))
						if err != nil || d < time.Second {
							m.setStatus("interval: give a duration like 30s or 1m", true)
							return nil
						}
						nq := *q
						nq.Every = d
						m.sched.extra[name] = &nq
						m.sched.toggle(name)
						return nil
					})
					return nil
				}
				if m.sched.toggle(name) {
					m.setStatus(name+" runs every "+q.Every.String()+" while rig is open", false)
				} else {
					m.setStatus(name+" schedule off", false)
				}
			}
		case "n":
			comps := engine.SortedKeys(t.langs)
			var desc []string
			for _, c := range comps {
				desc = append(desc, t.langs[c])
			}
			m.pick("query which component?", comps, desc, 0, false, func(c []string) tea.Cmd {
				if len(c) > 0 {
					t.newQuery(m, c[0])
				}
				return nil
			})
		}
	}
	return nil
}

func (t *queriesTab) click(m *model, h hit) tea.Cmd {
	if i, ok := stripHit(h, "q:mode"); ok {
		if (i == 1) != t.history {
			t.history, t.resultFor = i == 1, ""
		}
		return nil
	}
	if strings.HasPrefix(h.id, "result:") {
		t.focusRes = true
		t.result.click(h)
		return nil
	}
	if t.history {
		if t.hist.click(h) {
			t.focusRes, t.resultFor = false, ""
		}
		return nil
	}
	if t.list.click(h) {
		t.focusRes = false
		if name, q := t.selected(m); q != nil {
			t.shownFor = name
			if h.double {
				return t.runSelected(m, name, q)
			}
		}
	}
	return nil
}

func (t *queriesTab) rows(m *model) []grow {
	qs := m.sched.queries()
	names := engine.SortedKeys(qs)
	var rows []grow
	for _, n := range names {
		q := qs[n]
		r := m.sched.result(n)
		m.sched.mu.Lock()
		on, running := m.sched.active[n], m.sched.running[n]
		m.sched.mu.Unlock()
		state := "  "
		switch {
		case running:
			state = sAmber.Render("⟳ ")
		case on:
			state = sGreen.Render("⏱ ")
		}
		every, last, count, trend := "", "", "", ""
		if q.Every > 0 {
			every = q.Every.String()
		}
		if r != nil {
			last = shortAge(time.Since(r.at))
			count = fmt.Sprint(len(r.table.Rows))
			if r.err != nil {
				count = sRed.Render("err")
			}
			if len(r.hist) > 1 {
				trend = viz.Sparkline(r.hist, 14, viz.Palette[0])
			}
		}
		help := q.Help
		if help == "" {
			help = strings.Join(strings.Fields(q.Query), " ")
		}
		lastKey := 0.0
		if r != nil {
			lastKey = float64(-r.at.Unix())
		}
		rows = append(rows, grow{id: n, cells: []string{state, n, sDim.Render(q.Group), q.Source, every, last, count, trend, sDim.Render(help)},
			keys: []any{nil, n, q.Group, q.Source, float64(q.Every), lastKey, nil, nil, help}})
	}
	return rows
}

func (t *queriesTab) historyView(m *model, w, h int) string {
	m.sched.mu.Lock()
	runs := append([]queryRun{}, m.sched.runs...)
	m.sched.mu.Unlock()
	rows := make([]grow, 0, len(runs))
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		by, first := "manual", ""
		if r.Auto {
			by = sDim.Render("schedule")
		}
		if v, ok := firstNumber(r.Table); ok {
			first = strconv.FormatFloat(v, 'f', -1, 64)
		}
		rowsN := strconv.Itoa(len(r.Table.Rows))
		if r.Err != "" {
			rowsN, first = sRed.Render("err"), sRed.Render(r.Err)
		}
		rows = append(rows, grow{id: strconv.Itoa(i), cells: []string{r.At.Format("15:04:05"), r.Name, by, rowsN, r.Took.Round(time.Millisecond).String(), first},
			keys: []any{float64(-r.At.UnixNano()), r.Name, by, float64(len(r.Table.Rows)), float64(r.Took), first}})
	}
	t.hist.set(rows)
	listH := m.paneSize("queries", splitGeo{total: h, minA: 4, minB: 5, down: true}, max(5, min(h*2/5, len(rows)+3)), 0, 0, w)
	list := panel(fmt.Sprintf("history · %d runs this session · H back", len(runs)), t.hist.view(m, 1, 1, w-2, listH-2, !t.focusRes), w, listH, !t.focusRes)
	resH := h - listH
	cur, ok := t.hist.current()
	if !ok {
		return lipgloss.JoinVertical(lipgloss.Left, list, panel("result", sDim.Render("nothing has run yet"), w, resH, false))
	}
	i, _ := strconv.Atoi(cur.id)
	r := runs[i]
	title := fmt.Sprintf("%s at %s · %d rows · %s", r.Name, r.At.Format("15:04:05"), len(r.Table.Rows), r.Took.Round(time.Millisecond))
	if r.Err != "" {
		return lipgloss.JoinVertical(lipgloss.Left, list, panel(title, sRed.Render(wrap(r.Err, w-4)), w, resH, t.focusRes))
	}
	t.setResult("run:"+cur.id, r.Table)
	body := t.result.view(m, 1, listH+1, w-2, resH-2-boolInt(r.Table.Note != ""), t.focusRes)
	if r.Table.Note != "" {
		body += "\n" + sDim.Render(r.Table.Note)
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, panel(title, body, w, resH, t.focusRes))
}

func (t *queriesTab) view(m *model, w, h int) string {
	return m.withStrip("q:mode", []string{"saved queries", "history (H)"}, boolInt(t.history), w, h, func(h int) string {
		if t.history {
			return t.historyView(m, w, h)
		}
		return t.savedView(m, w, h)
	})
}

func (t *queriesTab) savedView(m *model, w, h int) string {
	t.list.set(t.rows(m))
	if len(t.list.rows) == 0 && len(t.langs) == 0 {
		return panel("queries", sDim.Render("no saved queries and no component that answers queries.\n\nadd saved queries to rig.yaml:\n\nqueries:\n  slow-requests:\n    source: prom\n    query: topk(5, rate(http_request_duration_seconds_sum[5m]))\n    every: 30s"), w, h, true)
	}
	listH := 4
	if len(t.list.rows) > 0 {
		listH = m.paneSize("queries", splitGeo{total: h, minA: 4, minB: 5, down: true}, max(5, min(h*2/5, len(t.list.rows)+3)), 0, 0, w)
	}
	listTitle := fmt.Sprintf("queries · %d saved · n writes a new one · a schedules", len(m.app.Queries()))
	list := panel(listTitle, t.list.view(m, 1, 1, w-2, listH-2, !t.focusRes), w, listH, !t.focusRes)

	name := t.shownFor
	if name == "" {
		name, _ = t.selected(m)
	}
	resH := h - listH
	r := m.sched.result(name)
	q := m.sched.queries()[name]
	var body, title string
	switch {
	case q == nil:
		body, title = sDim.Render("press n to write a query"), "result"
	case r == nil:
		body, title = sDim.Render(wordWrap(strings.TrimSpace(q.Query), w-4)+"\n\nenter runs it · y copies it"), name
	case r.err != nil:
		body, title = sDim.Render(wordWrap("› "+strings.Join(strings.Fields(q.Query), " "), w-4))+"\n\n"+sRed.Render(wrap(r.err.Error(), w-4)), name
	default:
		t.setResult(name, r.table)
		title = fmt.Sprintf("%s · %d rows · %s · %s ago", name, len(r.table.Rows), r.took.Round(time.Millisecond), shortAge(time.Since(r.at)))
		lines := strings.Split(wordWrap("› "+strings.Join(strings.Fields(q.Query), " "), w-4), "\n")
		if len(lines) > 3 {
			lines = append(lines[:2], truncate(lines[2], w-6)+"…")
		}
		head := sDim.Render(strings.Join(lines, "\n"))
		body = head + "\n" + t.result.view(m, 1, listH+1+len(lines), w-2, resH-2-len(lines)-boolInt(r.table.Note != ""), t.focusRes)
		if r.table.Note != "" {
			body += "\n" + sDim.Render(r.table.Note)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, panel(title, body, w, resH, t.focusRes))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// setResult loads a table into the result grid, sizing columns to their content.
func (t *queriesTab) setResult(name string, tb core.Table) {
	ws := colWidths(tb, 1<<20)
	var cols []gcol
	for i, c := range tb.Columns {
		w := 8
		if i < len(ws) {
			w = min(max(ws[i], 4), 60)
		}
		cols = append(cols, col(c, w))
	}
	if len(cols) > 0 {
		cols[len(cols)-1].width = 0
	}
	if len(t.result.cols) != len(cols) || name != t.resultFor {
		t.result = newGrid("result", cols...)
		t.resultFor = name
	}
	rows := make([]grow, len(tb.Rows))
	for i, r := range tb.Rows {
		rows[i] = grow{id: strconv.Itoa(i) + "|" + strings.Join(r, "|"), cells: r}
	}
	t.result.set(rows)
}

// copyResult puts the shown result on the clipboard: y the selected row, Y every row.
func (t *queriesTab) copyResult(m *model, key string) {
	if key != "y" && key != "Y" {
		return
	}
	var tb core.Table
	if t.history {
		m.sched.mu.Lock()
		if cur, ok := t.hist.current(); ok {
			if i, err := strconv.Atoi(cur.id); err == nil && i < len(m.sched.runs) {
				tb = m.sched.runs[i].Table
			}
		}
		m.sched.mu.Unlock()
	} else {
		name := t.shownFor
		if name == "" {
			name, _ = t.selected(m)
		}
		if r := m.sched.result(name); r != nil {
			tb = r.table
		}
	}
	rows := tb.Rows
	if r, ok := t.result.current(); ok && key == "y" {
		rows = [][]string{r.cells}
	}
	if len(tb.Columns) == 0 {
		m.setStatus("no result to copy", true)
		return
	}
	copyText(tsv(tb.Columns, rows))
	m.setStatus(fmt.Sprintf("copied %d rows (tab-separated, with the header)", len(rows)), false)
}

func tsv(cols []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(strings.Join(cols, "\t") + "\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	return b.String()
}

// colWidths sizes columns to their content.
func colWidths(t core.Table, _ int) []int {
	ws := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		ws[i] = lipgloss.Width(c)
	}
	for _, r := range t.Rows[:min(len(t.Rows), 200)] {
		for i := range ws {
			if i < len(r) {
				ws[i] = max(ws[i], min(lipgloss.Width(r[i]), 60))
			}
		}
	}
	return ws
}
