package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/engine"
)

// Test list filters, in the order of the filter tabs.
const (
	filterAll = iota
	filterFailed
	filterPassed
	filterSkipped
	filterRunning
	filterBench
	filterBuild
)

type testsDoneMsg struct {
	run *engine.TestRun
	err error
}

type testsLoadedMsg struct {
	gen   int
	suite string
	run   *engine.TestRun
	prev  map[string]engine.Bench
}

// testsTab runs the suites of rig.yaml and browses their results: a tree of packages, tests and
// subtests with filters, the output of the selected one, benchmarks against the previous run,
// coverage, saved runs, and reruns of the failures or of one test.
type testsTab struct {
	suite     int
	opts      map[string]engine.TestOptions
	runs      map[string]*engine.TestRun
	prev      map[string]map[string]engine.Bench
	cancel    context.CancelFunc
	running   string
	filter    int
	search    *regexp.Regexp
	expanded  map[string]bool // tests unfolded; for a package row, folded
	list      *grid
	bench     *grid
	outFocus  bool
	outOff    int
	outFollow bool
	outH      int
	outLen    int
	// strip is the filter each entry of the filter strip stands for, from the last render
	strip []int
}

func (t *testsTab) name() string { return "Tests" }
func (t *testsTab) typing() bool { return false }

func (t *testsTab) hints() [][2]string {
	if t.outFocus {
		return [][2]string{{"↑↓ pgup pgdn", "scroll"}, {"g G", "top/end"}, {"y", "copy"}, {"esc o", "back"}}
	}
	return [][2]string{{"r", "run"}, {"f", "rerun failed"}, {".", "rerun this"}, {"x", "stop"}, {"O", "options (race, cover, -run, …)"}, {"/", "search"},
		{"i", "filter: all, failed, passed, …"}, {"enter", "fold/output"}, {"+ -", "unfold/fold all"}, {"b", "benchmarks"}, {"h", "saved runs"}, {"w", "write report"}, {"y Y", "copy"}}
}

func (t *testsTab) init() {
	if t.opts == nil {
		t.opts, t.runs, t.prev, t.expanded = map[string]engine.TestOptions{}, map[string]*engine.TestRun{}, map[string]map[string]engine.Bench{}, map[string]bool{}
		t.list = newGrid("tlist", col("", 1), col("test", 0), col("package", 26), rcol("time", 8), rcol("cover", 6))
		t.bench = newGrid("tbench", col("benchmark", 0), col("package", 22), rcol("cpu", 4), rcol("n", 10), rcol("ns/op", 12), rcol("B/op", 10), rcol("allocs/op", 10), rcol("MB/s", 8), rcol("Δ ns/op", 9))
		t.outFollow = true
	}
}

func (t *testsTab) suites(m *model) []string { return m.app.SuiteNames() }

func (t *testsTab) current(m *model) string {
	s := t.suites(m)
	if len(s) == 0 {
		return ""
	}
	return s[min(t.suite, len(s)-1)]
}

func (t *testsTab) options(m *model) engine.TestOptions {
	name := t.current(m)
	o, ok := t.opts[name]
	if !ok {
		o = engine.OptionsFor(m.app.Spec.Tests[name])
		t.opts[name] = o
	}
	return o
}

func (t *testsTab) setOptions(m *model, o engine.TestOptions) { t.opts[t.current(m)] = o }

func (t *testsTab) open(m *model) tea.Cmd {
	t.init()
	return t.load(m)
}

func (t *testsTab) refresh(m *model) tea.Cmd { return nil }

// load reads the suite's last saved run when nothing of it is on screen yet.
func (t *testsTab) load(m *model) tea.Cmd {
	name := t.current(m)
	if name == "" || t.runs[name] != nil {
		return nil
	}
	a, gen := m.app, m.gen
	return func() tea.Msg {
		r, err := a.LoadTestRun("last", name)
		if err != nil {
			return nil
		}
		return testsLoadedMsg{gen: gen, suite: name, run: r, prev: a.PreviousBenches(r)}
	}
}

func (t *testsTab) start(m *model, label string, jobs []engine.TestJob) tea.Cmd {
	name := t.current(m)
	if t.running != "" {
		m.setStatus("tests of "+t.running+" are running: x stops them", true)
		return nil
	}
	if len(jobs) == 0 {
		m.setStatus("nothing to run", false)
		return nil
	}
	s := m.app.Spec.Tests[name]
	a := m.app
	run := a.NewTestRun(name, jobs[0].Options)
	ctx, cancel := context.WithCancel(m.ctx)
	t.runs[name], t.cancel, t.running = run, cancel, name
	t.outOff, t.outFollow, t.outFocus = 0, true, false
	m.setStatus(label+"…", false)
	return func() tea.Msg {
		err := a.RunTests(ctx, s, run, jobs)
		cancel()
		return testsDoneMsg{run: run, err: err}
	}
}

func (t *testsTab) update(m *model, msg tea.Msg) tea.Cmd {
	t.init()
	switch msg := msg.(type) {
	case testsLoadedMsg:
		if msg.gen == m.gen && t.runs[msg.suite] == nil {
			t.runs[msg.suite], t.prev[msg.suite] = msg.run, msg.prev
		}
	case testsDoneMsg:
		t.running, t.cancel = "", nil
		r := msg.run
		t.prev[r.Suite] = m.app.PreviousBenches(r)
		if msg.err != nil {
			m.setStatus("tests "+r.Suite+": "+msg.err.Error(), true)
		} else {
			m.setStatus("tests "+r.Suite+": "+r.Summary(), r.Counts()[engine.TestFail] > 0)
		}
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

func (t *testsTab) key(m *model, k tea.KeyMsg) tea.Cmd {
	if t.outFocus {
		switch k.String() {
		case "esc", "o", "left":
			t.outFocus = false
		case "up", "k":
			t.scrollOut(-1)
		case "down", "j":
			t.scrollOut(1)
		case "pgup":
			t.scrollOut(-t.outH)
		case "pgdown", " ":
			t.scrollOut(t.outH)
		case "home", "g":
			t.outOff, t.outFollow = 0, false
		case "end", "G":
			t.outFollow = true
		case "y":
			t.copyOutput(m)
		}
		return nil
	}
	name := t.current(m)
	if name == "" {
		return nil
	}
	o := t.options(m)
	switch k.String() {
	case "r":
		return t.start(m, "tests "+name, []engine.TestJob{{Options: o}})
	case "f":
		if r := t.runs[name]; r != nil {
			return t.start(m, "rerun failed of "+name, engine.RerunFailed(r, o))
		}
	case ".":
		return t.rerunSelected(m)
	case "x":
		if t.cancel != nil {
			t.cancel()
			m.setStatus("stopping tests…", false)
		}
	case "i":
		t.filter = (t.filter + 1) % filterBench
	case "b":
		if t.filter == filterBench {
			t.filter = filterAll
		} else {
			t.filter = filterBench
		}
	case "/":
		cur := ""
		if t.search != nil {
			cur = strings.TrimPrefix(t.search.String(), "(?i)")
		}
		m.ask("tests matching (regex, any case; empty: all)", cur, func(v string) tea.Cmd {
			t.search = nil
			if v != "" {
				re, err := regexp.Compile("(?i)" + v)
				if err != nil {
					m.setStatus("search: "+err.Error(), true)
					return nil
				}
				t.search = re
			}
			return nil
		})
	case "enter":
		if r, ok := t.list.current(); ok && t.foldable(m, r.id) {
			t.expanded[r.id] = !t.expanded[r.id]
			return nil
		}
		t.outFocus = true
	case "o", "right":
		t.outFocus = true
	case "+":
		if run := t.runs[name]; run != nil {
			run.Read(func(r *engine.TestRun) {
				for _, c := range r.Tests {
					if c.Name != "" {
						t.expanded[c.Package+"\x00"+c.Name] = true
					}
				}
			})
		}
	case "-":
		t.expanded = map[string]bool{}
	case "h":
		t.pickRun(m)
	case "e":
		m.ask("-run (only tests matching; empty: all)", o.Run, func(v string) tea.Cmd {
			o.Run = v
			t.setOptions(m, o)
			return nil
		})
	case "p":
		m.ask("packages (space separated)", strings.Join(o.Packages, " "), func(v string) tea.Cmd {
			o.Packages = strings.Fields(v)
			t.setOptions(m, o)
			return nil
		})
	case "w":
		t.writeReport(m)
	case "O":
		t.pickOptions(m)
	case "y":
		t.copyOutput(m)
	case "Y":
		if r := t.runs[name]; r != nil {
			var b bytes.Buffer
			r.WriteReport(&b, true)
			osc52(b.String())
			m.setStatus("failure report copied", false)
		}
	default:
		if opt, ok := map[string]string{"R": "race", "c": "cover", "s": "short", "F": "failfast", "u": "shuffle", "B": "bench", "m": "benchmem", "n": "count", "t": "timeout"}[k.String()]; ok {
			return t.toggle(m, opt)
		}
		g := t.list
		if t.filter == filterBench {
			g = t.bench
		}
		if g.key(k) {
			t.outOff, t.outFollow = 0, true
		}
	}
	return nil
}

// toggle flips a flag of the current suite's options, or asks for its value.
func (t *testsTab) toggle(m *model, opt string) tea.Cmd {
	o := t.options(m)
	ask := func(label, cur string, set func(string) error) {
		m.ask(label, cur, func(v string) tea.Cmd {
			if err := set(strings.TrimSpace(v)); err != nil {
				m.setStatus(opt+": "+err.Error(), true)
				return nil
			}
			t.setOptions(m, o)
			return nil
		})
	}
	switch opt {
	case "race":
		o.Race = !o.Race
	case "cover":
		o.Cover = !o.Cover
	case "short":
		o.Short = !o.Short
	case "failfast":
		o.Failfast = !o.Failfast
	case "shuffle":
		o.Shuffle = !o.Shuffle
	case "benchmem":
		o.Benchmem = !o.Benchmem
	case "bench":
		if o.Bench == "" {
			ask("benchmarks matching (go test -bench; only benchmarks run unless -run is set)", ".", func(v string) error { o.Bench = v; return nil })
			return nil
		}
		o.Bench = ""
	case "count":
		ask("count (each test n times; 1 skips the cache, 0 go's default)", strconv.Itoa(o.Count), func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("a number")
			}
			o.Count = n
			return nil
		})
		return nil
	case "timeout":
		cur := ""
		if o.Timeout > 0 {
			cur = o.Timeout.String()
		}
		ask("timeout (10m; empty: go's default)", cur, func(v string) error {
			if v == "" {
				o.Timeout = 0
				return nil
			}
			d, err := time.ParseDuration(v)
			o.Timeout = d
			return err
		})
		return nil
	case "run":
		return t.key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	case "pkgs":
		return t.key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	}
	t.setOptions(m, o)
	return nil
}

func (t *testsTab) rerunSelected(m *model) tea.Cmd {
	r, ok := t.list.current()
	if !ok {
		return nil
	}
	pkg, name, _ := strings.Cut(r.id, "\x00")
	o := t.options(m)
	if name == "" {
		o.Packages = []string{pkg}
		return t.start(m, "tests of "+pkg, []engine.TestJob{{Options: o}})
	}
	return t.start(m, "rerun "+name, []engine.TestJob{{Options: engine.Only(o, pkg, name)}})
}

func (t *testsTab) pickRun(m *model) {
	name := t.current(m)
	ids := m.app.TestRuns(name)
	if len(ids) == 0 {
		m.setStatus("no saved runs of "+name, false)
		return
	}
	var desc []string
	for _, id := range ids {
		d := ""
		if r, err := m.app.LoadTestRun(id, ""); err == nil {
			d = r.Env + "  " + r.Summary()
		}
		desc = append(desc, d)
	}
	m.pick("runs of "+name, ids, desc, 0, false, func(chosen []string) tea.Cmd {
		if len(chosen) == 0 {
			return nil
		}
		if t.running == name {
			m.setStatus("a run of "+name+" is going on: x stops it first", true)
			return nil
		}
		r, err := m.app.LoadTestRun(chosen[0], "")
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		t.runs[name], t.prev[name] = r, m.app.PreviousBenches(r)
		return nil
	})
}

// writeReport saves the shown run as a text report and as JUnit XML.
func (t *testsTab) writeReport(m *model) {
	r := t.runs[t.current(m)]
	if r == nil {
		return
	}
	dir := filepath.Join(os.TempDir(), "rig-tests")
	base := filepath.Join(dir, r.ID)
	var txt bytes.Buffer
	r.WriteReport(&txt, false)
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		err = os.WriteFile(base+".txt", txt.Bytes(), 0o644)
	}
	var f *os.File
	if err == nil {
		if f, err = os.Create(base + ".xml"); err == nil {
			err = r.WriteJUnit(f)
			f.Close()
		}
	}
	if err != nil {
		m.setStatus("write report: "+err.Error(), true)
		return
	}
	m.setStatus("report "+base+".txt, JUnit "+base+".xml", false)
}

func (t *testsTab) copyOutput(m *model) {
	lines := t.output(m)
	osc52(strings.Join(lines, "\n"))
	m.setStatus(fmt.Sprintf("%d lines copied", len(lines)), false)
}

func (t *testsTab) scrollOut(d int) {
	end := max(0, t.outLen-t.outH)
	if t.outFollow {
		t.outOff = end
	}
	t.outOff = max(0, min(t.outOff+d, end))
	t.outFollow = t.outOff >= end
}

// foldable: the row has rows under it in the tree (only the "all" filter shows a tree).
func (t *testsTab) foldable(m *model, id string) bool {
	r := t.runs[t.current(m)]
	if r == nil || t.filter != filterAll || t.search != nil {
		return false
	}
	pkg, name, _ := strings.Cut(id, "\x00")
	found := false
	r.Read(func(r *engine.TestRun) {
		for _, c := range r.Tests {
			if c.Package == pkg && c.Name != name && c.Name != "" && (name == "" || strings.HasPrefix(c.Name, name+"/")) {
				found = true
				return
			}
		}
	})
	return found
}

// ---- clicks and the wheel ----

func (t *testsTab) click(m *model, h hit) tea.Cmd {
	if i, ok := stripHit(h, "tsuite"); ok {
		t.suite, t.outOff, t.outFollow = i, 0, true
		return t.load(m)
	}
	if i, ok := stripHit(h, "tfilter"); ok {
		if i < len(t.strip) {
			t.filter = t.strip[i]
		}
		return nil
	}
	switch {
	case strings.HasPrefix(h.id, "topt:"):
		return t.toggle(m, strings.TrimPrefix(h.id, "topt:"))
	case strings.HasPrefix(h.id, "tbtn:"):
		key := map[string]string{"run": "r", "failed": "f", "this": ".", "stop": "x", "options": "O", "runs": "h", "report": "w"}[strings.TrimPrefix(h.id, "tbtn:")]
		return t.key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	case h.id == "tout":
		t.outFocus = true
	case strings.HasPrefix(h.id, "tlist:"):
		t.outFocus = false
		if t.list.click(h) {
			t.outOff, t.outFollow = 0, true
			if r, _ := t.list.current(); h.double && t.foldable(m, r.id) {
				t.expanded[r.id] = !t.expanded[r.id]
			}
		}
	case strings.HasPrefix(h.id, "tbench:"):
		t.bench.click(h)
	}
	return nil
}

func (t *testsTab) wheel(m *model, z hit, up bool) (tea.Cmd, bool) {
	if z.id != "tout" {
		return nil, false
	}
	if up {
		t.scrollOut(-3)
	} else {
		t.scrollOut(3)
	}
	return nil, true
}

// ---- rendering ----

func statusIcon(s string) string {
	switch s {
	case engine.TestPass:
		return sGreen.Render("✓")
	case engine.TestFail:
		return sRed.Render("✖")
	case engine.TestSkip:
		return sDim.Render("○")
	case engine.TestStopped:
		return sAmber.Render("■")
	}
	return sAmber.Render("◐")
}

func (t *testsTab) matches(status string) bool {
	switch t.filter {
	case filterFailed:
		return status == engine.TestFail || status == engine.TestStopped
	case filterPassed:
		return status == engine.TestPass
	case filterSkipped:
		return status == engine.TestSkip
	case filterRunning:
		return status == engine.TestRunning
	}
	return true
}

// rows are the run's tests as list rows: a tree of packages, tests and subtests under the "all"
// filter, else every match by its full name.
func (t *testsTab) rows(r *engine.TestRun) []grow {
	byKey := map[string]*engine.TestCase{}
	for _, c := range r.Tests {
		byKey[c.Package+"\x00"+c.Name] = c
	}
	// a test's parent is its longest existing prefix: t.Run("a/b") makes TestX/a/b with no TestX/a
	parent := func(c *engine.TestCase) string {
		parts := strings.Split(c.Name, "/")
		for i := len(parts) - 1; i >= 1; i-- {
			if p := c.Package + "\x00" + strings.Join(parts[:i], "/"); byKey[p] != nil {
				return p
			}
		}
		return c.Package + "\x00"
	}
	row := func(c *engine.TestCase, label string) grow {
		el := c.Elapsed
		if c.Status == engine.TestRunning {
			el = time.Since(c.Started).Seconds()
		}
		cover, coverKey := "", any(nil)
		if v, ok := r.Coverage[c.Package]; ok && c.Name == "" {
			cover, coverKey = fmt.Sprintf("%.1f%%", v), v
		}
		return grow{id: c.Package + "\x00" + c.Name,
			cells: []string{statusIcon(c.Status), label, shortPkg(c.Package), fmt.Sprintf("%.2fs", el), cover},
			keys:  []any{c.Status, c.Name, c.Package, el, coverKey}}
	}
	var out []grow
	if t.filter != filterAll || t.search != nil {
		for _, c := range r.Tests {
			if c.Name == "" && t.filter != filterAll {
				continue
			}
			if !t.matches(c.Status) || t.search != nil && !t.search.MatchString(c.Package+" "+c.Name) {
				continue
			}
			label := c.Name
			if label == "" {
				label = sTitle.Render(c.Package)
			}
			out = append(out, row(c, label))
		}
		return out
	}
	children := map[string][]*engine.TestCase{}
	var pkgs []string
	for _, c := range r.Tests {
		if c.Name == "" {
			pkgs = append(pkgs, c.Package)
			continue
		}
		p := parent(c)
		children[p] = append(children[p], c)
	}
	fold := func(open bool) string {
		if open {
			return sDim.Render("▾ ")
		}
		return sDim.Render("▸ ")
	}
	var walk func(key string, depth int)
	walk = func(key string, depth int) {
		for _, c := range children[key] {
			k := c.Package + "\x00" + c.Name
			label := c.Name
			if pk := parent(c); pk != c.Package+"\x00" {
				_, pn, _ := strings.Cut(pk, "\x00")
				label = strings.TrimPrefix(c.Name, pn+"/")
			}
			mark := "  "
			if len(children[k]) > 0 {
				mark = fold(t.expanded[k])
			}
			out = append(out, row(c, strings.Repeat("  ", depth)+mark+label))
			if t.expanded[k] {
				walk(k, depth+1)
			}
		}
	}
	for _, p := range pkgs {
		k := p + "\x00"
		mark := "  "
		if len(children[k]) > 0 {
			mark = fold(!t.expanded[k])
		}
		out = append(out, row(byKey[k], mark+sTitle.Render(p)))
		if !t.expanded[k] {
			walk(k, 1)
		}
	}
	return out
}

func shortPkg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// output is what the selected row printed, or go test's output outside JSON under the build filter.
func (t *testsTab) output(m *model) []string {
	r := t.runs[t.current(m)]
	if r == nil {
		return nil
	}
	var out []string
	r.Read(func(run *engine.TestRun) {
		if t.filter == filterBuild {
			out = slices.Clone(run.Stderr)
			return
		}
		sel, ok := t.list.current()
		if !ok {
			return
		}
		pkg, name, _ := strings.Cut(sel.id, "\x00")
		for _, c := range run.Tests {
			if c.Package == pkg && c.Name == name {
				out = slices.Clone(c.Output)
				return
			}
		}
	})
	return out
}

func colorLine(l string) string {
	t := strings.TrimSpace(l)
	switch {
	case strings.HasPrefix(t, "--- FAIL"), strings.HasPrefix(t, "FAIL"), strings.HasPrefix(t, "panic:"), strings.HasPrefix(t, "Error Trace:"), strings.HasPrefix(t, "Error:"):
		return sRed.Render(l)
	case strings.HasPrefix(t, "--- PASS"), strings.HasPrefix(t, "ok "), t == "PASS":
		return sGreen.Render(l)
	case strings.HasPrefix(t, "--- SKIP"), strings.HasPrefix(t, "==="):
		return sDim.Render(l)
	}
	return l
}

func (t *testsTab) toolbar(m *model, w int) string {
	names := t.suites(m)
	o := t.options(m)
	name := t.current(m)
	// line 1: the suites, then the shown run's state
	line1 := " " + m.strip("tsuite", 1, 0, names, min(t.suite, len(names)-1))
	state := ""
	if r := t.runs[name]; r != nil {
		mark := sGreen.Render("● ")
		switch {
		case t.running == name:
			mark = sAmber.Render("◐ running ")
		case r.Counts()[engine.TestFail] > 0 || r.FailedPackages() > 0 || r.Err != "":
			mark = sRed.Render("✖ ")
		}
		state = mark + sDim.Render(r.Summary()+" · "+r.Env+" "+r.Started.Format("15:04:05"))
		if r.Err != "" && t.running != name {
			state += sRed.Render(" · " + r.Err)
		}
	}
	if s := m.app.Spec.Tests[name]; s != nil && len(s.Needs) > 0 {
		if down := t.down(m, s.Needs); len(down) > 0 {
			state = sAmber.Render("⚠ not up: "+strings.Join(down, ",")+"  ") + state
		}
	}
	line1 = truncate(line1+"  "+state, w)

	// line 2: the command it runs, then the actions; O changes the flags
	var b strings.Builder
	x := 1
	b.WriteString(" ")
	add := func(id, s string) {
		m.zone(id, x, 1, lipgloss.Width(s), 1)
		x += lipgloss.Width(s) + 1
		b.WriteString(s + " ")
	}
	button := func(id, text string, bg lipgloss.TerminalColor) {
		add("tbtn:"+id, lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(bg).Bold(true).Padding(0, 1).Render(text))
	}
	if t.running == "" {
		button("run", "▶ run", cAccent)
		add("tbtn:failed", sTabOff.Render("↻ failed"))
	} else {
		button("stop", "■ stop", cRed)
	}
	add("tbtn:options", sTabOff.Render("⚙ options"))
	add("tbtn:runs", sTabOff.Render("runs"))
	add("tbtn:report", sTabOff.Render("report"))
	cmd := "go " + strings.Join(o.Args(m.app.Spec.Tests[name]), " ")
	b.WriteString(" " + sDim.Render(truncate(cmd, max(0, w-x-2))))
	return line1 + "\n" + truncate(b.String(), w)
}

// pickOptions lists go test's flags with their values; enter flips one (or asks its value) and the
// list comes back, esc closes it.
func (t *testsTab) pickOptions(m *model) {
	o := t.options(m)
	onOff := func(b bool) string {
		if b {
			return sGreen.Render("on")
		}
		return sDim.Render("off")
	}
	val := func(s string) string {
		if s == "" {
			return sDim.Render("-")
		}
		return s
	}
	timeout := ""
	if o.Timeout > 0 {
		timeout = o.Timeout.String()
	}
	count := ""
	if o.Count > 0 {
		count = strconv.Itoa(o.Count)
	}
	items := []string{"race", "cover", "short", "failfast", "shuffle", "benchmem", "bench", "count", "timeout", "run", "pkgs"}
	desc := []string{onOff(o.Race), onOff(o.Cover), onOff(o.Short), onOff(o.Failfast), onOff(o.Shuffle), onOff(o.Benchmem),
		val(o.Bench), val(count), val(timeout), val(o.Run) + sDim.Render("  (-run: only tests matching)"), strings.Join(o.Packages, " ")}
	sel := 0
	if m.picker != nil && m.picker.title == "test options" {
		sel = m.picker.sel
	}
	m.pick("test options", items, desc, sel, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		cmd := t.toggle(m, c[0])
		if m.prompt == nil {
			t.pickOptions(m)
			m.picker.sel = slices.Index(items, c[0])
		}
		return cmd
	})
}

// down are the needed services that are not ready, as far as the last status says.
func (t *testsTab) down(m *model, needs []string) []string {
	if len(m.services) == 0 {
		return nil
	}
	ready := map[string]bool{}
	for _, s := range m.services {
		ready[s.Service] = engine.Ready(s)
	}
	var out []string
	for _, n := range m.app.Spec.Select(needs) {
		if !ready[n] {
			out = append(out, n)
		}
	}
	if len(out) > 3 {
		out = append(out[:3], fmt.Sprintf("+%d", len(out)-3))
	}
	return out
}

func (t *testsTab) view(m *model, w, h int) string {
	t.init()
	if len(t.suites(m)) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sDim.Render(`no test suites in `+m.app.Spec.File+`

tests:
  unit:
    packages: [./...]
    race: true
  integration:
    packages: [./tests/integration/...]
    env: { API_ADDR: "svc://api:8080" }
    needs: [api]
    timeout: 30m`))
	}
	head := t.toolbar(m, w)
	h -= 2
	name := t.current(m)
	r := t.runs[name]
	if r == nil {
		return head + "\n" + lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
			sDim.Render("no runs of "+name+" yet — r runs it:\n\ngo "+strings.Join(t.options(m).Args(m.app.Spec.Tests[name]), " ")))
	}

	var rows []grow
	var counts map[string]int
	var nBench, nBuild int
	var benches []engine.Bench
	r.Read(func(r *engine.TestRun) {
		counts = r.Counts()
		nBench, nBuild = len(r.Benches), len(r.Stderr)
		if t.filter == filterBench {
			benches = slices.Clone(r.Benches)
		} else {
			rows = t.rows(r)
		}
	})

	total := counts[engine.TestPass] + counts[engine.TestFail] + counts[engine.TestSkip] + counts[engine.TestRunning] + counts[engine.TestStopped]
	// the strip shows what has entries; i also reaches passed, skipped and running
	type entry struct {
		label  string
		filter int
		n      int
	}
	entries := []entry{
		{fmt.Sprintf("all %d", total), filterAll, 1},
		{fmt.Sprintf("✖ failed %d", counts[engine.TestFail]+counts[engine.TestStopped]), filterFailed, 1},
		{fmt.Sprintf("✓ passed %d", counts[engine.TestPass]), filterPassed, 0},
		{fmt.Sprintf("○ skipped %d", counts[engine.TestSkip]), filterSkipped, 0},
		{fmt.Sprintf("◐ running %d", counts[engine.TestRunning]), filterRunning, counts[engine.TestRunning]},
		{fmt.Sprintf("benchmarks %d", nBench), filterBench, nBench},
		{fmt.Sprintf("⚠ go output %d", nBuild), filterBuild, nBuild},
	}
	if t.filter == filterBuild && nBuild == 0 {
		t.filter = filterAll
	}
	var labels []string
	active := 0
	t.strip = t.strip[:0]
	for _, e := range entries {
		if e.n == 0 && e.filter != t.filter {
			continue
		}
		if e.filter == t.filter {
			active = len(labels)
		}
		labels, t.strip = append(labels, e.label), append(t.strip, e.filter)
	}
	strip := " " + m.strip("tfilter", 1, 2, labels, active)
	if t.search != nil {
		strip += sAmber.Render("  /" + strings.TrimPrefix(t.search.String(), "(?i)") + "/")
	}
	if total > 0 && t.running == name {
		strip += "  " + progress(float64(total-counts[engine.TestRunning])/float64(total), 20)
	}
	strip = truncate(strip, w)
	h--

	if t.filter == filterBench {
		return head + "\n" + strip + "\n" + t.benchView(m, r, benches, w, h)
	}

	// the list and the output: side by side on wide terminals, else one above the other
	var listW, listH, outX, outY, outW, outH int
	switch {
	case t.filter == filterBuild:
		outY, outW, outH = 3, w, h
	case w >= 160:
		listW, listH = w*3/5, h
		outX, outY, outW, outH = listW, 3, w-listW, h
	default:
		listW, listH = w, max(6, h*11/20)
		outY, outW, outH = 3+listH, w, h-listH
	}
	var listBox string
	if listH > 0 {
		t.list.set(rows)
		listBox = panel(fmt.Sprintf("%s · %d shown", name, len(rows)), t.list.view(m, 1, 4, listW-2, listH-2, !t.outFocus), listW, listH, !t.outFocus)
	}

	lines := t.output(m)
	t.outLen, t.outH = len(lines), outH-2
	if t.outFollow {
		t.outOff = max(0, len(lines)-t.outH)
	}
	t.outOff = max(0, min(t.outOff, len(lines)-t.outH))
	var ob strings.Builder
	for i := t.outOff; i < len(lines) && i-t.outOff < t.outH; i++ {
		if i > t.outOff {
			ob.WriteByte('\n')
		}
		ob.WriteString(colorLine(printable(lines[i])))
	}
	if len(lines) == 0 {
		ob.WriteString(sDim.Render("no output"))
	}
	outTitle := "go test output outside its tests (build errors, vet)"
	if sel, ok := t.list.current(); ok && t.filter != filterBuild {
		pkg, test, _ := strings.Cut(sel.id, "\x00")
		outTitle = firstNonEmpty(test, pkg)
	}
	outTitle += fmt.Sprintf(" · %d lines", len(lines))
	if !t.outFollow && len(lines) > t.outH {
		outTitle += fmt.Sprintf(" · %d%%", 100*(t.outOff+t.outH)/max(1, len(lines)))
	}
	m.zone("tout", outX, outY, outW, outH)
	outBox := panel(outTitle, ob.String(), outW, outH, t.outFocus)

	body := outBox
	switch {
	case listH == 0:
	case outX > 0:
		body = lipgloss.JoinHorizontal(lipgloss.Top, listBox, outBox)
	default:
		body = listBox + "\n" + outBox
	}
	return head + "\n" + strip + "\n" + body
}

func progress(frac float64, w int) string {
	full := int(frac * float64(w))
	return sGreen.Render(strings.Repeat("█", full)) + sDim.Render(strings.Repeat("░", w-full)+fmt.Sprintf(" %d%%", int(frac*100)))
}

func (t *testsTab) benchView(m *model, r *engine.TestRun, benches []engine.Bench, w, h int) string {
	prev := t.prev[r.Suite]
	var rows []grow
	for i, b := range benches {
		ns, hasNs := b.Metrics["ns/op"]
		delta, dKey := "", any(nil)
		if p, ok := prev[b.Package+" "+b.Name]; ok && hasNs && p.Metrics["ns/op"] > 0 {
			d := (ns - p.Metrics["ns/op"]) / p.Metrics["ns/op"] * 100
			dKey, delta = d, fmt.Sprintf("%+.1f%%", d)
			switch {
			case d > 5:
				delta = sRed.Render(delta)
			case d < -5:
				delta = sGreen.Render(delta)
			}
		}
		metric := func(u string) (string, any) {
			v, ok := b.Metrics[u]
			if !ok {
				return "", nil
			}
			return strconv.FormatFloat(v, 'f', -1, 64), v
		}
		nsT, nsK := metric("ns/op")
		bT, bK := metric("B/op")
		aT, aK := metric("allocs/op")
		mT, mK := metric("MB/s")
		rows = append(rows, grow{id: strconv.Itoa(i),
			cells: []string{b.Name, shortPkg(b.Package), strconv.Itoa(b.Procs), strconv.FormatInt(b.N, 10), nsT, bT, aT, mT, delta},
			keys:  []any{b.Name, b.Package, float64(b.Procs), float64(b.N), nsK, bK, aK, mK, dKey}})
	}
	t.bench.set(rows)
	title := fmt.Sprintf("benchmarks · %d", len(rows))
	if prev != nil {
		title += " · Δ against the previous run with benchmarks"
	}
	if len(rows) == 0 {
		return panel(title, sDim.Render("no benchmarks in this run — B turns -bench on, then r"), w, h, true)
	}
	return panel(title, t.bench.view(m, 1, 4, w-2, h-2, true), w, h, true)
}
