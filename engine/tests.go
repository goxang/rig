package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/spec"
)

// Test statuses; a test still going is TestRunning, one cut short by a stop TestStopped.
const (
	TestRunning = "run"
	TestPass    = "pass"
	TestFail    = "fail"
	TestSkip    = "skip"
	TestStopped = "stopped"
)

// maxOutput caps the lines kept per test; the newest stay.
const maxOutput = 5000

// TestOptions are the go test flags of one run; Suite fills them from rig.yaml and the caller changes them.
type TestOptions struct {
	Packages  []string      `json:"packages,omitempty"`
	Run       string        `json:"run,omitempty"`
	Skip      string        `json:"skip,omitempty"`
	Race      bool          `json:"race,omitempty"`
	Cover     bool          `json:"cover,omitempty"`
	Short     bool          `json:"short,omitempty"`
	Failfast  bool          `json:"failfast,omitempty"`
	Shuffle   bool          `json:"shuffle,omitempty"`
	Count     int           `json:"count,omitempty"`
	Parallel  int           `json:"parallel,omitempty"`
	CPU       string        `json:"cpu,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
	Bench     string        `json:"bench,omitempty"`
	Benchtime string        `json:"benchtime,omitempty"`
	Benchmem  bool          `json:"benchmem,omitempty"`
}

// OptionsFor is what a suite runs with unless changed.
func OptionsFor(s *spec.TestSuite) TestOptions {
	return TestOptions{Packages: s.Packages, Run: s.Run, Skip: s.Skip, Race: s.Race, Cover: s.Cover, Short: s.Short,
		Count: s.Count, Parallel: s.Parallel, Timeout: s.Timeout, Bench: s.Bench, Benchtime: s.Benchtime, Benchmem: s.Benchmem}
}

// Args are the go test arguments for these options and the suite's fixed ones.
func (o TestOptions) Args(s *spec.TestSuite) []string {
	args := []string{"test", "-json"}
	if s.Tags != "" {
		args = append(args, "-tags", s.Tags)
	}
	run := o.Run
	if o.Bench != "" {
		args = append(args, "-bench", o.Bench)
		if run == "" {
			run = "^$" // benchmarks only, unless a -run asks for tests too
		}
		if o.Benchtime != "" {
			args = append(args, "-benchtime", o.Benchtime)
		}
		if o.Benchmem {
			args = append(args, "-benchmem")
		}
	}
	if run != "" {
		args = append(args, "-run", run)
	}
	if o.Skip != "" {
		args = append(args, "-skip", o.Skip)
	}
	for _, f := range []struct {
		on   bool
		flag string
	}{{o.Race, "-race"}, {o.Cover, "-cover"}, {o.Short, "-short"}, {o.Failfast, "-failfast"}, {o.Shuffle, "-shuffle=on"}} {
		if f.on {
			args = append(args, f.flag)
		}
	}
	if o.Count > 0 {
		args = append(args, "-count", strconv.Itoa(o.Count))
	}
	if o.Parallel > 0 {
		args = append(args, "-parallel", strconv.Itoa(o.Parallel))
	}
	if o.CPU != "" {
		args = append(args, "-cpu", o.CPU)
	}
	if o.Timeout > 0 {
		args = append(args, "-timeout", o.Timeout.String())
	}
	args = append(args, s.Flags...)
	pkgs := o.Packages
	if len(pkgs) == 0 {
		pkgs = []string{"./..."}
	}
	args = append(args, pkgs...)
	if len(s.Args) > 0 {
		args = append(append(args, "-args"), s.Args...)
	}
	return args
}

// excluding lists the packages with tests matched by pkgs (./... when empty) and not by exclude.
func excluding(ctx context.Context, dir string, pkgs, exclude []string) ([]string, error) {
	if len(pkgs) == 0 {
		pkgs = []string{"./..."}
	}
	list := func(patterns []string) ([]string, error) {
		cmd := exec.CommandContext(ctx, "go", append([]string{"list", "-e", "-f", "{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}"}, patterns...)...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list %s: %w", strings.Join(patterns, " "), err)
		}
		return strings.Fields(string(out)), nil
	}
	all, err := list(pkgs)
	if err != nil {
		return nil, err
	}
	drop, err := list(exclude)
	if err != nil {
		return nil, err
	}
	gone := map[string]bool{}
	for _, p := range drop {
		gone[p] = true
	}
	var out []string
	for _, p := range all {
		if !gone[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("every package of %s is excluded", strings.Join(pkgs, " "))
	}
	return out, nil
}

// TestCase is one package (Name "") or test as go test -json reported it.
type TestCase struct {
	Package  string    `json:"package"`
	Name     string    `json:"name,omitempty"`
	Status   string    `json:"status"`
	Elapsed  float64   `json:"elapsed,omitempty"`
	Parallel bool      `json:"parallel,omitempty"`
	Started  time.Time `json:"started"`
	Output   []string  `json:"output,omitempty"`
}

// Depth is how many levels deep a test is: 1 for TestX, 2 for TestX/sub, 0 for a package.
func (c *TestCase) Depth() int {
	if c.Name == "" {
		return 0
	}
	return strings.Count(c.Name, "/") + 1
}

// Bench is one benchmark result line.
type Bench struct {
	Package string             `json:"package"`
	Name    string             `json:"name"`
	Procs   int                `json:"procs,omitempty"`
	N       int64              `json:"n"`
	Metrics map[string]float64 `json:"metrics"` // ns/op, B/op, allocs/op, MB/s and custom units
}

// TestRun is one go test invocation (or several, for a rerun of failures at different depths) and
// everything it reported. It is safe to read with Read while the run goes on.
type TestRun struct {
	ID       string             `json:"id"`
	Suite    string             `json:"suite"`
	Env      string             `json:"env"`
	Options  TestOptions        `json:"options"`
	Commands [][]string         `json:"commands"`
	Started  time.Time          `json:"started"`
	Ended    time.Time          `json:"ended"`
	Err      string             `json:"err,omitempty"`
	Tests    []*TestCase        `json:"tests"`
	Benches  []Bench            `json:"benches,omitempty"`
	Coverage map[string]float64 `json:"coverage,omitempty"`
	// Stderr is what go test printed outside its JSON: build errors, mostly.
	Stderr []string `json:"stderr,omitempty"`
	// Metrics is the suite's report: measured over the run.
	Metrics *ReportResult `json:"metrics,omitempty"`

	mu    sync.Mutex
	index map[string]*TestCase
}

// Read calls f with the run locked.
func (r *TestRun) Read(f func(*TestRun)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

// Done reports whether the run has ended.
func (r *TestRun) Done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.Ended.IsZero()
}

// Counts are the tests (not packages) per status.
func (r *TestRun) Counts() map[string]int {
	out := map[string]int{}
	for _, c := range r.Tests {
		if c.Name != "" {
			out[c.Status]++
		}
	}
	return out
}

// FailedPackages are packages that failed on their own (a build failure, a panic in TestMain),
// not through a failed test.
func (r *TestRun) FailedPackages() int {
	failed := map[string]bool{}
	for _, c := range r.Tests {
		if c.Name != "" && c.Status == TestFail {
			failed[c.Package] = true
		}
	}
	n := 0
	for _, c := range r.Tests {
		if c.Name == "" && c.Status == TestFail && !failed[c.Package] {
			n++
		}
	}
	return n
}

func (r *TestRun) get(pkg, name string, at time.Time) *TestCase {
	k := pkg + "\x00" + name
	c := r.index[k]
	if c == nil {
		c = &TestCase{Package: pkg, Name: name, Status: TestRunning, Started: at}
		r.index[k] = c
		r.Tests = append(r.Tests, c)
	}
	return c
}

type testEvent struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

var (
	benchLine = regexp.MustCompile(`^(Benchmark\S+?)(?:-(\d+))?\s+(\d+)\s+(.+)$`)
	coverLine = regexp.MustCompile(`coverage: ([\d.]+)% of statements`)
)

func (r *TestRun) apply(e testEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Package == "" {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	c := r.get(e.Package, e.Test, e.Time)
	switch e.Action {
	case "run":
		c.Status = TestRunning
	case "pause":
		c.Parallel = true
	case "pass", "fail", "skip":
		c.Status, c.Elapsed = e.Action, e.Elapsed
		if e.Test == "" {
			// benchmarks get no end event of their own, and a test still going when its package
			// ends (a timeout's panic) ended with it
			for _, x := range r.Tests {
				if x.Package == e.Package && x.Status == TestRunning {
					x.Status = e.Action
				}
			}
		}
	case "output", "build-output":
		line := strings.TrimRight(e.Output, "\n")
		c.Output = append(c.Output, line)
		if len(c.Output) > maxOutput {
			c.Output = c.Output[len(c.Output)-maxOutput:]
		}
		if m := coverLine.FindStringSubmatch(line); m != nil && e.Test == "" {
			if r.Coverage == nil {
				r.Coverage = map[string]float64{}
			}
			r.Coverage[e.Package], _ = strconv.ParseFloat(m[1], 64)
		}
		if b, ok := parseBench(e.Package, strings.TrimSpace(line)); ok {
			r.Benches = append(r.Benches, b)
		}
	}
}

func parseBench(pkg, line string) (Bench, bool) {
	m := benchLine.FindStringSubmatch(line)
	if m == nil {
		return Bench{}, false
	}
	b := Bench{Package: pkg, Name: m[1], Metrics: map[string]float64{}}
	b.Procs, _ = strconv.Atoi(m[2])
	b.N, _ = strconv.ParseInt(m[3], 10, 64)
	f := strings.Fields(m[4])
	for i := 0; i+1 < len(f); i += 2 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return Bench{}, false
		}
		b.Metrics[f[i+1]] = v
	}
	return b, len(b.Metrics) > 0
}

// Suite is a test suite of rig.yaml by name.
func (a *App) Suite(name string) (*spec.TestSuite, error) {
	s, ok := a.Spec.Tests[name]
	if !ok {
		return nil, fmt.Errorf("no test suite %q in %s (have %v)", name, a.Spec.File, a.SuiteNames())
	}
	return s, nil
}

// SuiteNames are the test suites in rig.yaml's order.
func (a *App) SuiteNames() []string {
	if len(a.Spec.TestOrder) == len(a.Spec.Tests) {
		return a.Spec.TestOrder
	}
	return SortedKeys(a.Spec.Tests)
}

// TestJob is one go test invocation of a run: these options, perhaps narrowed to some tests.
type TestJob struct {
	Options TestOptions
}

// NewTestRun starts the record of a run of suite.
func (a *App) NewTestRun(suite string, o TestOptions) *TestRun {
	env := ""
	if a.Env != nil {
		env = a.Env.Name
	}
	now := time.Now()
	return &TestRun{ID: now.Format("20060102-150405") + "-" + suite, Suite: suite, Env: env, Options: o, Started: now, index: map[string]*TestCase{}}
}

// RunTests runs the jobs of a suite one after another into run, stopping at ctx's end, and saves the
// run when done. Every job but the first keeps the results the earlier ones reported.
func (a *App) RunTests(ctx context.Context, s *spec.TestSuite, run *TestRun, jobs []TestJob) error {
	env, err := a.testEnv(ctx, s)
	var runErr error
	if err == nil {
		for _, j := range jobs {
			if runErr = a.runJob(ctx, s, run, j.Options, env); runErr != nil || ctx.Err() != nil {
				break
			}
		}
	} else {
		runErr = err
	}
	run.mu.Lock()
	run.Ended = time.Now()
	for _, c := range run.Tests {
		if c.Status == TestRunning {
			c.Status = TestStopped
		}
	}
	if runErr != nil {
		run.Err = runErr.Error()
	} else if ctx.Err() != nil {
		run.Err = "stopped"
	}
	run.mu.Unlock()
	if s.Report != "" {
		// the run's own context may be cancelled already; the report is still wanted
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		rep, err := a.MeasureReport(c, s.Report, "", run.Started, run.Ended)
		cancel()
		if err != nil {
			rep = &ReportResult{Name: s.Report, From: run.Started, To: run.Ended, Rows: []ReportRow{{Metric: s.Report, Err: err.Error()}}}
		}
		run.mu.Lock()
		run.Metrics = rep
		run.mu.Unlock()
	}
	if err := a.SaveTestRun(run); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

// testEnv is the suite's env with svc:// addresses resolved in the active environment.
func (a *App) testEnv(ctx context.Context, s *spec.TestSuite) ([]string, error) {
	var env []string
	for _, k := range SortedKeys(s.Env) {
		var resolveErr error
		v := svcRef.ReplaceAllStringFunc(s.Env[k], func(ref string) string {
			addr, err := a.Resolve(ctx, ref)
			if err != nil {
				resolveErr = err
			}
			return addr
		})
		if resolveErr != nil {
			return nil, fmt.Errorf("test env %s: %w", k, resolveErr)
		}
		env = append(env, k+"="+v)
	}
	return env, nil
}

func (a *App) runJob(ctx context.Context, s *spec.TestSuite, run *TestRun, o TestOptions, env []string) error {
	dir := a.Spec.Dir
	if s.Dir != "" {
		dir = filepath.Join(a.Spec.Dir, s.Dir)
	}
	if len(s.Exclude) > 0 {
		pkgs, err := excluding(ctx, dir, o.Packages, s.Exclude)
		if err != nil {
			return err
		}
		o.Packages = pkgs
	}
	args := o.Args(s)
	run.mu.Lock()
	run.Commands = append(run.Commands, append([]string{"go"}, args...))
	run.mu.Unlock()
	cmd := exec.Command("go", args...)
	sh.OwnGroup(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { sh.KillGroup(cmd) })
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			run.mu.Lock()
			run.Stderr = append(run.Stderr, sc.Text())
			run.mu.Unlock()
		}
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e testEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			run.mu.Lock()
			run.Stderr = append(run.Stderr, sc.Text())
			run.mu.Unlock()
			continue
		}
		run.apply(e)
	}
	wg.Wait()
	err = cmd.Wait()
	if _, failed := err.(*exec.ExitError); failed {
		return nil // failing tests are results, not an error of the run
	}
	return err
}

// Pattern is a -run expression that matches exactly these tests (full names, TestX/sub/...),
// level by level: every name of a level is allowed at that level, so a few more may run.
func Pattern(names []string) string {
	var levels [][]string
	seen := []map[string]bool{}
	for _, n := range names {
		for i, part := range strings.Split(n, "/") {
			if i == len(levels) {
				levels, seen = append(levels, nil), append(seen, map[string]bool{})
			}
			if !seen[i][part] {
				seen[i][part] = true
				levels[i] = append(levels[i], regexp.QuoteMeta(part))
			}
		}
	}
	parts := make([]string, len(levels))
	for i, l := range levels {
		if len(l) == 1 {
			parts[i] = "^" + l[0] + "$"
		} else {
			parts[i] = "^(" + strings.Join(l, "|") + ")$"
		}
	}
	return strings.Join(parts, "/")
}

// RerunFailed are the jobs that run a run's failures again. Each failed test is cut back to its
// deepest parallel ancestor (a case that stands on its own, like one mode of an integration case),
// else to its top-level test, so the steps before it run too; tests cut to the same depth share a job.
func RerunFailed(r *TestRun, base TestOptions) []TestJob {
	var names map[int]map[string][]string // depth → package → names
	r.Read(func(r *TestRun) {
		failed := map[string]bool{}
		parallel := map[string]bool{}
		for _, c := range r.Tests {
			k := c.Package + "\x00" + c.Name
			if c.Name != "" && (c.Status == TestFail || c.Status == TestStopped) {
				failed[k] = true
			}
			parallel[k] = c.Parallel
		}
		names = map[int]map[string][]string{}
		cut := map[string]bool{}
		for _, c := range r.Tests {
			k := c.Package + "\x00" + c.Name
			if !failed[k] || hasFailedChild(r.Tests, c, failed) {
				continue
			}
			parts := strings.Split(c.Name, "/")
			name := parts[0]
			for i := len(parts); i >= 1; i-- {
				n := strings.Join(parts[:i], "/")
				if parallel[c.Package+"\x00"+n] {
					name = n
					break
				}
			}
			if cut[c.Package+"\x00"+name] {
				continue
			}
			cut[c.Package+"\x00"+name] = true
			d := strings.Count(name, "/") + 1
			if names[d] == nil {
				names[d] = map[string][]string{}
			}
			names[d][c.Package] = append(names[d][c.Package], name)
		}
	})
	var depths []int
	for d := range names {
		depths = append(depths, d)
	}
	sort.Ints(depths)
	var jobs []TestJob
	for _, d := range depths {
		var pkgs, all []string
		for _, p := range SortedKeys(names[d]) {
			pkgs = append(pkgs, p)
			all = append(all, names[d][p]...)
		}
		o := base
		o.Packages, o.Run, o.Bench = pkgs, Pattern(all), ""
		jobs = append(jobs, TestJob{Options: o})
	}
	return jobs
}

// hasFailedChild: a test under c failed too (any test, for a package).
func hasFailedChild(tests []*TestCase, c *TestCase, failed map[string]bool) bool {
	prefix := c.Name + "/"
	for _, x := range tests {
		if x.Package == c.Package && x != c && (c.Name == "" || strings.HasPrefix(x.Name, prefix)) && failed[x.Package+"\x00"+x.Name] {
			return true
		}
	}
	return false
}

// Only narrows options to these tests of one package.
func Only(base TestOptions, pkg string, names ...string) TestOptions {
	o := base
	o.Packages, o.Run, o.Bench = []string{pkg}, Pattern(names), ""
	return o
}

// ---- saved runs ----

func (a *App) testDir() string {
	dir, err := spec.DataDir(a.Spec.Dir)
	if err != nil {
		return filepath.Join(a.Spec.Dir, ".rig", "tests")
	}
	return filepath.Join(dir, "tests")
}

// keepRuns is how many saved test runs a project keeps.
const keepRuns = 50

func (a *App) SaveTestRun(r *TestRun) error {
	dir := a.testDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	r.mu.Lock()
	raw, err := json.Marshal(r)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, r.ID+".json"), raw, 0o644); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	for len(files) > keepRuns {
		_ = os.Remove(files[0])
		files = files[1:]
	}
	return nil
}

// TestRuns are the saved runs' ids, newest first; suite "" is every suite.
func (a *App) TestRuns(suite string) []string {
	files, _ := filepath.Glob(filepath.Join(a.testDir(), "*.json"))
	var ids []string
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if suite == "" || strings.HasSuffix(id, "-"+suite) {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

// LoadTestRun reads a saved run; id "" or "last" is the newest (of suite, when given).
func (a *App) LoadTestRun(id, suite string) (*TestRun, error) {
	if id == "" || id == "last" {
		ids := a.TestRuns(suite)
		if len(ids) == 0 {
			return nil, fmt.Errorf("no saved test runs in %s", a.testDir())
		}
		id = ids[0]
	}
	raw, err := os.ReadFile(filepath.Join(a.testDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	r := &TestRun{}
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, fmt.Errorf("test run %s: %w", id, err)
	}
	r.index = map[string]*TestCase{}
	for _, c := range r.Tests {
		r.index[c.Package+"\x00"+c.Name] = c
	}
	return r, nil
}

// PreviousBenches are the benchmarks of the newest saved run of suite before r that has any, by package and name.
func (a *App) PreviousBenches(r *TestRun) map[string]Bench {
	for _, id := range a.TestRuns(r.Suite) {
		if id >= r.ID {
			continue
		}
		p, err := a.LoadTestRun(id, "")
		if err != nil || len(p.Benches) == 0 {
			continue
		}
		out := map[string]Bench{}
		for _, b := range p.Benches {
			out[b.Package+" "+b.Name] = b
		}
		return out
	}
	return nil
}

// ---- reports ----

// Summary is one line about a run: counts, time, coverage.
func (r *TestRun) Summary() string {
	var out string
	r.Read(func(r *TestRun) {
		c := r.Counts()
		parts := []string{fmt.Sprintf("%d passed", c[TestPass]), fmt.Sprintf("%d failed", c[TestFail]), fmt.Sprintf("%d skipped", c[TestSkip])}
		if n := r.FailedPackages(); n > 0 {
			parts = append(parts, fmt.Sprintf("%d packages failed", n))
		}
		if n := c[TestRunning]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d running", n))
		}
		if n := c[TestStopped]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d stopped", n))
		}
		if len(r.Benches) > 0 {
			parts = append(parts, fmt.Sprintf("%d benchmarks", len(r.Benches)))
		}
		end := r.Ended
		if end.IsZero() {
			end = time.Now()
		}
		parts = append(parts, end.Sub(r.Started).Round(100*time.Millisecond).String())
		if len(r.Coverage) > 0 {
			sum := 0.0
			for _, v := range r.Coverage {
				sum += v
			}
			parts = append(parts, fmt.Sprintf("coverage %.1f%% (mean of %d packages)", sum/float64(len(r.Coverage)), len(r.Coverage)))
		}
		out = strings.Join(parts, ", ")
	})
	return out
}

// WriteReport writes a plain-text report: the summary, failures with their output, the slowest
// tests, benchmarks and coverage. failedOnly leaves out everything but the failures.
func (r *TestRun) WriteReport(w io.Writer, failedOnly bool) {
	sum := r.Summary()
	defer r.Read(func(r *TestRun) {
		if r.Metrics != nil {
			fmt.Fprintln(w)
			r.Metrics.Markdown(w)
		}
	})
	r.Read(func(r *TestRun) {
		fmt.Fprintf(w, "suite %s on %s, %s\n%s\n", r.Suite, r.Env, r.Started.Format("2006-01-02 15:04:05"), sum)
		for _, c := range r.Commands {
			fmt.Fprintf(w, "  $ %s\n", strings.Join(c, " "))
		}
		if r.Err != "" {
			fmt.Fprintf(w, "error: %s\n", r.Err)
		}
		if len(r.Stderr) > 0 {
			fmt.Fprintf(w, "\nbuild output:\n  %s\n", strings.Join(lastLines(r.Stderr, 40), "\n  "))
		}
		failed := map[string]bool{}
		for _, c := range r.Tests {
			if c.Status == TestFail || c.Status == TestStopped {
				failed[c.Package+"\x00"+c.Name] = true
			}
		}
		var fails []*TestCase
		for _, c := range r.Tests {
			if failed[c.Package+"\x00"+c.Name] && !hasFailedChild(r.Tests, c, failed) {
				fails = append(fails, c)
			}
		}
		if len(fails) > 0 {
			fmt.Fprintf(w, "\nFAILED (%d):\n", len(fails))
			for _, c := range fails {
				name := c.Name
				if name == "" {
					name = "(package)"
				}
				fmt.Fprintf(w, "\n--- %s  %s  %s\n", strings.ToUpper(c.Status), c.Package, name)
				for _, l := range lastLines(relevant(c.Output), 30) {
					fmt.Fprintf(w, "    %s\n", l)
				}
			}
		}
		if failedOnly {
			return
		}
		var tests []*TestCase
		for _, c := range r.Tests {
			if c.Depth() == 1 {
				tests = append(tests, c)
			}
		}
		sort.SliceStable(tests, func(i, j int) bool { return tests[i].Elapsed > tests[j].Elapsed })
		if len(tests) > 0 {
			fmt.Fprintf(w, "\nslowest:\n")
			for _, c := range tests[:min(10, len(tests))] {
				fmt.Fprintf(w, "  %8.2fs  %-6s %s %s\n", c.Elapsed, c.Status, c.Package, c.Name)
			}
		}
		if len(r.Benches) > 0 {
			fmt.Fprintf(w, "\nbenchmarks:\n")
			for _, b := range r.Benches {
				fmt.Fprintf(w, "  %-50s %10d %s\n", b.Name, b.N, benchMetrics(b))
			}
		}
		if len(r.Coverage) > 0 {
			fmt.Fprintf(w, "\ncoverage:\n")
			for _, p := range SortedKeys(r.Coverage) {
				fmt.Fprintf(w, "  %5.1f%%  %s\n", r.Coverage[p], p)
			}
		}
	})
}

func benchMetrics(b Bench) string {
	var parts []string
	for _, u := range SortedKeys(b.Metrics) {
		parts = append(parts, strconv.FormatFloat(b.Metrics[u], 'f', -1, 64)+" "+u)
	}
	return strings.Join(parts, "  ")
}

// relevant drops go test's framing lines (=== RUN, === PAUSE, ...) from a test's output.
func relevant(lines []string) []string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "=== RUN") || strings.HasPrefix(t, "=== PAUSE") || strings.HasPrefix(t, "=== CONT") || strings.HasPrefix(t, "=== NAME") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func lastLines(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// WriteJUnit writes the run as JUnit XML (one testsuite per package), for CI.
func (r *TestRun) WriteJUnit(w io.Writer) error {
	var doc junitSuites
	r.Read(func(r *TestRun) {
		byPkg := map[string]*junitSuite{}
		var order []string
		for _, c := range r.Tests {
			s := byPkg[c.Package]
			if s == nil {
				s = &junitSuite{Name: c.Package}
				byPkg[c.Package] = s
				order = append(order, c.Package)
			}
			if c.Name == "" {
				s.Time = fmt.Sprintf("%.3f", c.Elapsed)
				continue
			}
			jc := junitCase{Name: c.Name, Classname: c.Package, Time: fmt.Sprintf("%.3f", c.Elapsed)}
			body := strings.Join(lastLines(relevant(c.Output), 200), "\n")
			switch c.Status {
			case TestFail, TestStopped:
				jc.Failure = &junitMessage{Message: c.Status, Body: body}
				s.Failures++
			case TestSkip:
				jc.Skipped = &junitMessage{Message: "skipped", Body: body}
				s.Skipped++
			}
			s.Tests++
			s.Cases = append(s.Cases, jc)
		}
		for _, p := range order {
			doc.Suites = append(doc.Suites, *byPkg[p])
		}
	})
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	return enc.Encode(doc)
}
