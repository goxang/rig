// Package jobs keeps the operations a session runs (a build, a deploy, a task, a plugin action):
// their output, progress and end, for any renderer to show while they run and after.
package jobs

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Lines is how much output a job keeps.
const Lines = 2000

type Job struct {
	ID    string
	Label string
	Start time.Time
	// Stdin, when set, takes what the user types for the job (a task step that reads input).
	Stdin io.WriteCloser
	// Log and PID, for a task: it writes to Log.log and keeps running when rig closes, so a session
	// reopened later follows it again.
	Log string
	PID int
	// Echo, when set, also gets everything written to the job, as written: a CLI's stdout.
	Echo io.Writer

	mu      sync.Mutex
	cancel  context.CancelFunc
	end     time.Time
	err     error
	lines   []string
	partial string
	wrote   time.Time
	// step is the task step running ("3/7") and since when, for its live timer
	step   string
	stepAt time.Time
	done   int
	total  int
}

// Restore is a job saved by an earlier session.
func Restore(label string, start, end time.Time, lines []string, err error) *Job {
	return &Job{Label: label, Start: start, end: end, lines: lines, err: err}
}

func (j *Job) SetCancel(c context.CancelFunc) {
	j.mu.Lock()
	j.cancel = c
	j.mu.Unlock()
}

// Cancel stops a running job's context.
func (j *Job) Cancel() {
	j.mu.Lock()
	c := j.cancel
	j.mu.Unlock()
	if c != nil {
		c()
	}
}

func (j *Job) Write(p []byte) (int, error) {
	if j.Echo != nil {
		_, _ = j.Echo.Write(p)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.partial + strings.ReplaceAll(string(p), "\r\n", "\n")
	parts := strings.Split(s, "\n")
	for _, l := range parts[:len(parts)-1] {
		// a progress bar redraws its line with \r: keep what it says last
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		l = ansi.Strip(l)
		if n, ok := strings.CutPrefix(l, "▸ ["); ok {
			j.step, _, _ = strings.Cut(n, "]")
			j.stepAt = time.Now()
		} else if strings.HasPrefix(l, "  ✓ ") {
			j.step = ""
		}
		j.lines = append(j.lines, l)
	}
	if over := len(j.lines) - Lines; over > 0 {
		j.lines = append(j.lines[:0], j.lines[over:]...)
	}
	j.partial = parts[len(parts)-1]
	j.wrote = time.Now()
	return len(p), nil
}

// Progress records how far the job is, as a plugin's $/progress says.
func (j *Job) Progress(done, total int, message string) {
	j.mu.Lock()
	j.done, j.total = done, total
	if message != "" {
		j.step, j.stepAt = message, time.Now()
	}
	j.mu.Unlock()
}

// Fraction is done/total, or false when the job has not said.
func (j *Job) Fraction() (float64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.total <= 0 {
		return 0, false
	}
	return float64(j.done) / float64(j.total), true
}

func (j *Job) Finish(err error) {
	j.mu.Lock()
	j.end, j.err = time.Now(), err
	j.mu.Unlock()
	j.Cancel()
	if j.Stdin != nil {
		j.Stdin.Close()
	}
}

// Waiting guesses that a running job with an input waits for it: it printed a line without ending
// it and has been quiet since.
func (j *Job) Waiting() (prompt string, ok bool) {
	if j.Stdin == nil {
		return "", false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p := strings.TrimSpace(ansi.Strip(j.partial))
	return p, j.end.IsZero() && p != "" && time.Since(j.wrote) > 700*time.Millisecond
}

// Answer sends a line to the job's input.
func (j *Job) Answer(line string) error {
	j.mu.Lock()
	j.lines = append(j.lines, ansi.Strip(j.partial)+line)
	j.partial = ""
	j.mu.Unlock()
	_, err := io.WriteString(j.Stdin, line+"\n")
	return err
}

func (j *Job) Running() bool {
	end, _, _ := j.State()
	return end.IsZero()
}

func (j *Job) State() (end time.Time, last string, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	last = j.partial
	if i := strings.LastIndex(last, "\r"); i >= 0 {
		last = last[i+1:]
	}
	for k := len(j.lines) - 1; strings.TrimSpace(last) == "" && k >= 0; k-- {
		last = j.lines[k]
	}
	return j.end, ansi.Strip(last), j.err
}

func (j *Job) Output() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := append([]string(nil), j.lines...)
	if j.partial != "" {
		out = append(out, ansi.Strip(j.partial))
	}
	return out
}

// StepTimer is the task step running and how long it has run.
func (j *Job) StepTimer() (string, time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.step == "" || !j.end.IsZero() {
		return "", 0
	}
	return j.step, time.Since(j.stepAt).Round(time.Second)
}

func (j *Job) Took() time.Duration {
	end, _, _ := j.State()
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(j.Start).Round(time.Second)
}

// Manager holds a session's jobs, the latest Keep of them (30 when unset). Its zero value is ready.
type Manager struct {
	Keep int

	mu   sync.Mutex
	jobs []*Job
	seq  int
}

type key struct{}

// Start records a new job; it is safe from any goroutine. The context carries the job for Out and
// is cancelled by Cancel.
func (m *Manager) Start(ctx context.Context, label string) (context.Context, *Job) {
	ctx, cancel := context.WithCancel(ctx)
	j := &Job{Label: label, Start: time.Now(), cancel: cancel}
	m.Add(j)
	return context.WithValue(ctx, key{}, j), j
}

// Add records a job made elsewhere, such as a restored one.
func (m *Manager) Add(j *Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	if j.ID == "" {
		j.ID = strconv.Itoa(m.seq)
	}
	m.jobs = append(m.jobs, j)
	if keep := cmp.Or(m.Keep, 30); len(m.jobs) > keep {
		m.jobs = m.jobs[len(m.jobs)-keep:]
	}
}

func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Job(nil), m.jobs...)
}

func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// From is the job ctx runs for, or nil.
func From(ctx context.Context) *Job {
	j, _ := ctx.Value(key{}).(*Job)
	return j
}

// Out is where an operation running for a job writes its output; nowhere outside a job.
func Out(ctx context.Context) io.Writer {
	if j := From(ctx); j != nil {
		return j
	}
	return io.Discard
}

var writers atomic.Int64

// Writer is ctx carrying a job that copies everything written to it into w, so what a plugin prints
// for a call lands in w; a nil w keeps ctx.
func Writer(ctx context.Context, w io.Writer) context.Context {
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, key{}, &Job{ID: fmt.Sprintf("w%d", writers.Add(1)), Start: time.Now(), Echo: w})
}
