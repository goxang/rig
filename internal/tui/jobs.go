package tui

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// job is one operation started from the UI (a build, a deploy, a task): its output, kept while it
// runs and after, for the activity view (!) and the line above the footer.
type job struct {
	label string
	start time.Time
	stop  context.CancelFunc
	// stdin, when set, takes what the user types for the job (a task step that reads input)
	stdin io.WriteCloser

	mu      sync.Mutex
	end     time.Time
	err     error
	lines   []string
	partial string
	wrote   time.Time
}

const jobLines = 2000

func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.partial + strings.ReplaceAll(string(p), "\r\n", "\n")
	parts := strings.Split(s, "\n")
	for _, l := range parts[:len(parts)-1] {
		// a progress bar redraws its line with \r: keep what it says last
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		j.lines = append(j.lines, ansi.Strip(l))
	}
	if over := len(j.lines) - jobLines; over > 0 {
		j.lines = append(j.lines[:0], j.lines[over:]...)
	}
	j.partial = parts[len(parts)-1]
	j.wrote = time.Now()
	return len(p), nil
}

func (j *job) finish(err error) {
	j.mu.Lock()
	j.end, j.err = time.Now(), err
	j.mu.Unlock()
	if j.stop != nil {
		j.stop()
	}
	if j.stdin != nil {
		j.stdin.Close()
	}
}

// waiting guesses that a running job with an input waits for it: it printed a line without ending
// it and has been quiet since.
func (j *job) waiting() (prompt string, ok bool) {
	if j.stdin == nil {
		return "", false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p := strings.TrimSpace(ansi.Strip(j.partial))
	return p, j.end.IsZero() && p != "" && time.Since(j.wrote) > 700*time.Millisecond
}

// answer sends a line to the job's input.
func (j *job) answer(line string) error {
	j.mu.Lock()
	j.lines = append(j.lines, ansi.Strip(j.partial)+line)
	j.partial = ""
	j.mu.Unlock()
	_, err := io.WriteString(j.stdin, line+"\n")
	return err
}

func (j *job) running() bool {
	end, _, _ := j.state()
	return end.IsZero()
}

func (j *job) state() (end time.Time, last string, err error) {
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

func (j *job) output() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := append([]string(nil), j.lines...)
	if j.partial != "" {
		out = append(out, ansi.Strip(j.partial))
	}
	return out
}

func (j *job) took() time.Duration {
	end, _, _ := j.state()
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(j.start).Round(time.Second)
}

type jobKey struct{}

// jobOut is where an operation started by act or do writes its progress; it shows under ! as it runs.
func jobOut(ctx context.Context) io.Writer {
	if j, ok := ctx.Value(jobKey{}).(*job); ok {
		return j
	}
	return io.Discard
}

// startJob records an operation; it is safe from any goroutine.
func (m *model) startJob(ctx context.Context, label string) (context.Context, *job) {
	ctx, stop := context.WithCancel(ctx)
	j := &job{label: label, start: time.Now(), stop: stop}
	m.jobsMu.Lock()
	m.jobs = append(m.jobs, j)
	if len(m.jobs) > 30 {
		m.jobs = m.jobs[len(m.jobs)-30:]
	}
	m.jobsMu.Unlock()
	return context.WithValue(ctx, jobKey{}, j), j
}

func (m *model) jobList() []*job {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	return append([]*job(nil), m.jobs...)
}

// jobLine is the line above the footer while an operation runs, and a few seconds after it ends.
func (m *model) jobLine() string {
	js := m.jobList()
	var running []*job
	for _, j := range js {
		if end, _, _ := j.state(); end.IsZero() {
			running = append(running, j)
		}
	}
	var j *job
	switch {
	case len(running) > 0:
		j = running[0]
	case len(js) > 0:
		j = js[len(js)-1]
		if end, _, _ := j.state(); time.Since(end) > 8*time.Second {
			return ""
		}
	default:
		return ""
	}
	end, last, err := j.state()
	icon, st := spinner(), sAccent
	switch {
	case !end.IsZero() && err != nil:
		icon, st = "✗", sRed
	case !end.IsZero():
		icon, st = "✓", sGreen
	}
	head := " " + st.Render(icon+" "+j.label) + sDim.Render(" · "+j.took().String())
	if n := len(running); n > 1 {
		head += sDim.Render(fmt.Sprintf(" · +%d more", n-1))
	}
	tail := sDim.Render("  ! output, stop")
	for _, r := range running {
		if prompt, ok := r.waiting(); ok {
			return " " + sAmber.Render("⌨ "+r.label+" waits for input") + sDim.Render(" │ ") + truncate(prompt, max(10, m.w/2)) + sDim.Render("  ! answers")
		}
	}
	if room := m.w - lipgloss.Width(head) - lipgloss.Width(tail) - 3; room > 10 && strings.TrimSpace(last) != "" {
		head += sDim.Render(" │ ") + truncate(strings.TrimSpace(last), room)
	}
	return head + tail
}

func spinner() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[time.Now().Second()%len(frames)]
}

// activityView is !: the operations of this session with the output of the selected one, and the
// errors (tab switches). Its size stays put while output arrives or the selection moves.
func (m *model) activityView(h int) string {
	w := min(m.w-4, 160)
	if m.actErrors || len(m.jobList()) == 0 {
		return m.errorsView(h)
	}
	js := m.jobList()
	m.jobSel = min(max(m.jobSel, 0), len(js)-1)
	const listRows = 6
	first := max(0, m.jobSel-listRows+1)
	var list []string
	for k := first; k < len(js) && k < first+listRows; k++ {
		i := len(js) - 1 - k
		j := js[i]
		end, _, err := j.state()
		icon := spinner()
		switch {
		case !end.IsZero() && err != nil:
			icon = sRed.Render("✗")
		case !end.IsZero():
			icon = sGreen.Render("✓")
		}
		note := j.start.Format("15:04:05") + " · " + j.took().String()
		if _, ok := j.waiting(); ok {
			note += " · " + sAmber.Render("waits for input")
		}
		row := fmt.Sprintf(" %s %s  %s", icon, truncate(j.label, w-40), sDim.Render(note))
		if k == m.jobSel {
			row = sCursor.Render(padRight(ansi.Strip(row), w-6))
		}
		list = append(list, row)
	}
	for len(list) < min(listRows, len(js)) {
		list = append(list, "")
	}
	j := js[len(js)-1-m.jobSel]
	out := j.output()
	if _, _, err := j.state(); err != nil {
		out = append(out, "", "✗ "+err.Error())
	}
	room := max(3, h-len(list)-9)
	if len(out) == 0 {
		out = []string{sDim.Render("(no output yet)")}
	}
	from := max(0, len(out)-room-m.jobScroll)
	shown := append([]string(nil), out[from:min(len(out), from+room)]...)
	for i, l := range shown {
		shown[i] = truncate(l, w-6)
	}
	for len(shown) < room {
		shown = append(shown, "")
	}
	keys := [][2]string{{"↑↓", "operation"}, {"pgup/pgdn", "scroll"}, {"y", "copy output"}, {"tab", "errors"}}
	if j.running() {
		keys = append(keys, [2]string{"x", "stop it"})
		if j.stdin != nil {
			keys = append(keys, [2]string{"enter", "type its input"})
		}
	}
	keys = append(keys, [2]string{"esc", "close"})
	body := strings.Join(list, "\n") + "\n" + sDim.Render(strings.Repeat("─", w-6)) + "\n" + strings.Join(shown, "\n") + "\n\n" + keyHints(keys)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(w).
		Render(sTitle.Render("activity: what you started this session") + "\n\n" + body)
}

func (m *model) activityKey(k tea.KeyMsg) tea.Cmd {
	js := m.jobList()
	var j *job
	if !m.actErrors && len(js) > 0 {
		j = js[len(js)-1-min(m.jobSel, len(js)-1)]
	}
	switch k.String() {
	case "up", "k":
		m.jobSel, m.jobScroll = max(0, m.jobSel-1), 0
	case "down", "j":
		m.jobSel, m.jobScroll = min(len(js)-1, m.jobSel+1), 0
	case "pgup":
		m.jobScroll += 10
	case "pgdown":
		m.jobScroll = max(0, m.jobScroll-10)
	case "tab", "shift+tab":
		m.actErrors = !m.actErrors
	case "x", "delete":
		if j != nil && j.running() && j.stop != nil {
			j.stop()
			m.setStatus("stopping "+j.label, false)
		}
	case "enter", "i":
		if j != nil && j.running() && j.stdin != nil {
			prompt, _ := j.waiting()
			m.ask(cmp.Or(prompt, "input for "+j.label), "", func(v string) tea.Cmd {
				if err := j.answer(v); err != nil {
					m.setStatus(j.label+": "+err.Error(), true)
				}
				return nil
			})
			if strings.Contains(strings.ToLower(prompt), "password") {
				m.prompt.input.EchoMode = textinput.EchoPassword
			}
		}
	case "y":
		if j == nil {
			var all []string
			for _, e := range m.errLog {
				all = append(all, e.at.Format("15:04:05")+" "+e.text)
			}
			copyText(strings.Join(all, "\n"))
			m.setStatus("copied the errors", false)
		} else {
			copyText(strings.Join(j.output(), "\n"))
			m.setStatus("copied the output of "+j.label, false)
		}
	case "esc", "q", "!":
		m.showErrors = false
		if m.statusErr {
			m.status = ""
		}
	}
	return nil
}
