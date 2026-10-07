package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

// profileKinds say what each profile answers; the picker shows them as its hints.
var profileKinds = [][2]string{
	{"cpu", "where CPU time goes, sampled over a duration (default 10s)"},
	{"heap", "memory in use now, by where it was allocated"},
	{"allocs", "every allocation since start: what churns the garbage collector"},
	{"goroutine", "each goroutine's stack: leaks, and where they all wait"},
	{"block", "where goroutines block on channels and locks (needs runtime.SetBlockProfileRate)"},
	{"mutex", "contended mutexes (needs runtime.SetMutexProfileFraction)"},
	{"trace", "execution trace over a duration, for go tool trace (no table)"},
}

// profView is a captured profile as a sortable table (go tool pprof -top).
type profView struct {
	svc  string
	res  core.Profile
	head []string // the lines above the table: type, duration, totals
	grid *grid
}

// pickProfile asks which profile to take, with what each one is for, then the duration when it
// samples over one.
func (t *servicesTab) pickProfile(m *model, svc string) {
	var names, desc []string
	for _, k := range profileKinds {
		names, desc = append(names, k[0]), append(desc, k[1])
	}
	names, desc = append(names, "type it…"), append(desc, "kind [duration] [instance], e.g. cpu 30s or heap - <pod>")
	m.pick("profile "+svc, names, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		switch c[0] {
		case "cpu", "trace":
			m.ask(fmt.Sprintf("profile %s: %s for how long", svc, c[0]), "10s", func(v string) tea.Cmd {
				return t.capture(m, svc, c[0]+" "+strings.TrimSpace(v))
			})
			return nil
		case "type it…":
			m.ask("profile "+svc+": kind [duration] [instance]", "cpu 10s", func(v string) tea.Cmd { return t.capture(m, svc, v) })
			return nil
		}
		return t.capture(m, svc, c[0])
	})
}

func (t *servicesTab) capture(m *model, svc, v string) tea.Cmd {
	f := strings.Fields(v)
	kind, dur, inst := "cpu", 10*time.Second, ""
	if len(f) > 0 {
		kind = f[0]
	}
	if len(f) > 1 && f[1] != "-" {
		d, err := time.ParseDuration(f[1])
		if err != nil {
			m.setStatus("profile: "+f[1]+" is not a duration (10s, 1m)", true)
			return nil
		}
		dur = d
	}
	if len(f) > 2 {
		inst = f[2]
	}
	s := m.app.Spec.Services[svc]
	a, gen, ctx := m.app, m.gen, m.work()
	m.busy++
	m.setStatus(fmt.Sprintf("profiling %s (%s, %s)…", svc, kind, dur), false)
	return func() tea.Msg {
		p, _, err := engine.Get[core.Profiler](a, core.KindProfiler, "")
		if err != nil {
			return resultMsg{gen: gen, status: "profile: " + err.Error(), err: true}
		}
		res, err := p.Capture(ctx, core.ProfileRequest{Service: s, Instance: inst, Kind: kind, Duration: dur})
		if err != nil {
			return resultMsg{gen: gen, status: "profile: " + err.Error(), err: true}
		}
		return resultMsg{gen: gen, status: "profile saved: " + res.File, profile: newProfView(svc, res)}
	}
}

func newProfView(svc string, res core.Profile) *profView {
	v := &profView{svc: svc, res: res}
	v.grid = newGrid("prof", rcol("FLAT", 10), rcol("FLAT%", 7), rcol("SUM%", 7), rcol("CUM", 10), rcol("CUM%", 7), col("FUNCTION", 0))
	v.grid.sortDefault(0, true)
	var rows []grow
	table := false
	for _, l := range strings.Split(res.Summary, "\n") {
		f := strings.Fields(l)
		switch {
		case !table && len(f) >= 5 && f[0] == "flat" && f[1] == "flat%":
			table = true
		case !table:
			if strings.TrimSpace(l) != "" {
				v.head = append(v.head, strings.TrimSpace(l))
			}
		case len(f) >= 6:
			fn := strings.Join(f[5:], " ")
			rows = append(rows, grow{id: fmt.Sprint(len(rows)), cells: append(f[:5:5], fn),
				keys: []any{quantity(f[0]), quantity(f[1]), quantity(f[2]), quantity(f[3]), quantity(f[4]), fn}})
		}
	}
	v.grid.set(rows)
	return v
}

// quantity reads a pprof -top cell (1.50s, 12.5MB, 33.3%, 1200) as a number in base units.
func quantity(s string) float64 {
	units := []struct {
		suffix string
		mul    float64
	}{{"%", 1}, {"ns", 1e-9}, {"us", 1e-6}, {"µs", 1e-6}, {"ms", 1e-3}, {"min", 60}, {"hrs", 3600}, {"h", 3600}, {"s", 1},
		{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"kB", 1 << 10}, {"KB", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				return f * u.mul
			}
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func (v *profView) view(m *model, x, y, w, h int) string {
	head := strings.Join(v.head, " · ")
	if len(v.grid.rows) == 0 {
		msg := "no table: go is not installed here, or this profile has none"
		if v.res.Kind == "trace" {
			msg = "go tool trace " + v.res.File
		}
		return truncate(head, w) + "\n" + sDim.Render(msg)
	}
	return truncate(head, w) + "\n" + v.grid.view(m, x, y+1, w, h-1, true)
}

// save writes the table as a markdown report next to the other reports.
func (v *profView) save(m *model) {
	name := fmt.Sprintf("profile-%s-%s", v.svc, v.res.Kind)
	file := m.app.ReportFile(name, time.Now())
	err := engine.WriteReportFile(file, func(w io.Writer) {
		fmt.Fprintf(w, "# %s %s profile\n\n", v.svc, v.res.Kind)
		fmt.Fprintf(w, "%s\n\nprofile file: `%s` (`go tool pprof -http=: %s`)\n\n", strings.Join(v.head, "  \n"), v.res.File, v.res.File)
		fmt.Fprintln(w, "| flat | flat% | sum% | cum | cum% | function |")
		fmt.Fprintln(w, "|---:|---:|---:|---:|---:|---|")
		for _, r := range v.grid.rows {
			fmt.Fprintf(w, "| %s |\n", strings.Join(r.cells, " | "))
		}
	})
	if err != nil {
		m.setStatus("save profile: "+err.Error(), true)
		return
	}
	if rel, err := filepath.Rel(m.app.Spec.Dir, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	m.setStatus("profile report saved: "+file, false)
}
