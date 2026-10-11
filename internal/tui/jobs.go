package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/goxang/rig/kernel/jobs"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/internal/sh"
)

type job = jobs.Job

// taskFiles makes the files a task writes: base.log its output, base.err and base.done how it ended.
// Those of tasks started over a week ago go.
func taskFiles() (base string, out *os.File, err error) {
	dir := filepath.Join(os.TempDir(), "rig-tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	old, _ := os.ReadDir(dir)
	for _, e := range old {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	base = filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 10))
	out, err = os.OpenFile(base+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	return base, out, err
}

// follow copies the task's log into j until it ends, and says how it ended.
func follow(j *job) error {
	f, err := os.Open(j.Log + ".log")
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		_, _ = io.Copy(j, f)
		if code, err := os.ReadFile(j.Log + ".done"); err == nil {
			_, _ = io.Copy(j, f)
			if strings.TrimSpace(string(code)) == "0" {
				return nil
			}
			if raw, err := os.ReadFile(j.Log + ".err"); err == nil {
				return errors.New(strings.TrimSpace(string(raw)))
			}
			return errors.New("exit status " + string(code))
		}
		if !sh.Alive(j.PID) {
			if _, err := os.Stat(j.Log + ".done"); err == nil {
				continue
			}
			_, _ = io.Copy(j, f)
			return errors.New("ended without saying how: killed?")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// killOnStop stops the task's process group when the job is stopped while it runs; once the job
// has finished, what the task left running in the background stays.
func killOnStop(j *job, ctx context.Context) {
	go func() {
		<-ctx.Done()
		if j.Running() {
			sh.StopGroup(j.PID, true)
		}
	}()
}

// startJob records an operation; it is safe from any goroutine.
func (m *model) startJob(ctx context.Context, label string) (context.Context, *job) {
	return m.jobs.Start(ctx, label)
}

func (m *model) jobList() []*job { return m.jobs.List() }

// jobLine is the line above the footer while an operation runs, and a few seconds after it ends.
func (m *model) jobLine() string {
	js := m.jobList()
	var running []*job
	for _, j := range js {
		if end, _, _ := j.State(); end.IsZero() {
			running = append(running, j)
		}
	}
	var j *job
	switch {
	case len(running) > 0:
		j = running[0]
	case len(js) > 0:
		j = js[len(js)-1]
		if end, _, _ := j.State(); time.Since(end) > 8*time.Second {
			return ""
		}
	default:
		return ""
	}
	end, last, err := j.State()
	icon, st := spinner(), sAccent
	switch {
	case !end.IsZero() && err != nil:
		icon, st = "✗", sRed
	case !end.IsZero():
		icon, st = "✓", sGreen
	}
	head := " " + st.Render(icon+" "+j.Label) + sDim.Render(" · "+j.Took().String())
	if step, d := j.StepTimer(); step != "" {
		head += sDim.Render(" · step "+step+" ") + sAccent.Render(d.String())
	}
	if n := len(running); n > 1 {
		head += sDim.Render(fmt.Sprintf(" · +%d more", n-1))
	}
	tail := sDim.Render("  ! output, stop")
	for _, r := range running {
		if prompt, ok := r.Waiting(); ok {
			return " " + sAmber.Render("⌨ "+r.Label+" waits for input") + sDim.Render(" │ ") + truncate(prompt, max(10, m.w/2)) + sDim.Render("  ! answers")
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
		end, _, err := j.State()
		icon := spinner()
		switch {
		case !end.IsZero() && err != nil:
			icon = sRed.Render("✗")
		case !end.IsZero():
			icon = sGreen.Render("✓")
		}
		note := j.Start.Format("15:04:05") + " · " + j.Took().String()
		if step, d := j.StepTimer(); step != "" {
			note += " · step " + step + " " + d.String()
		}
		if _, ok := j.Waiting(); ok {
			note += " · " + sAmber.Render("waits for input")
		}
		row := fmt.Sprintf(" %s %s  %s", icon, truncate(j.Label, w-40), sDim.Render(note))
		if k == m.jobSel {
			row = sCursor.Render(padRight(ansi.Strip(row), w-6))
		}
		list = append(list, row)
	}
	for len(list) < min(listRows, len(js)) {
		list = append(list, "")
	}
	j := js[len(js)-1-m.jobSel]
	out := j.Output()
	if _, _, err := j.State(); err != nil {
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
	if j.Running() {
		keys = append(keys, [2]string{"x", "stop it"})
		if j.Stdin != nil {
			keys = append(keys, [2]string{"enter", "type its input"})
		}
	}
	keys = append(keys, [2]string{"esc", "close"})
	body := strings.Join(list, "\n") + "\n" + sDim.Render(strings.Repeat("─", w-6)) + "\n" + strings.Join(shown, "\n") + "\n\n" + keyHints(keys)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(w + 2).
		Render(sTitle.Render("activity: what you started this session") + "\n\n" + body)
}

func (m *model) activityKey(k tea.KeyPressMsg) tea.Cmd {
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
		if j != nil && j.Running() {
			j.Cancel()
			m.setStatus("stopping "+j.Label, false)
		}
	case "enter", "i":
		if j != nil && j.Running() && j.Stdin != nil {
			prompt, _ := j.Waiting()
			m.ask(cmp.Or(prompt, "input for "+j.Label), "", func(v string) tea.Cmd {
				if err := j.Answer(v); err != nil {
					m.setStatus(j.Label+": "+err.Error(), true)
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
				all = append(all, e.At.Format("15:04:05")+" "+e.Text)
			}
			copyText(strings.Join(all, "\n"))
			m.setStatus("copied the errors", false)
		} else {
			copyText(strings.Join(j.Output(), "\n"))
			m.setStatus("copied the output of "+j.Label, false)
		}
	case "esc", "q", "!":
		m.showErrors = false
		if m.statusErr {
			m.status = ""
		}
	}
	return nil
}
