package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

type loadTab struct {
	names []string
	sel   int
	stats map[string]core.LoadStatus
	errs  map[string]error
	hist  map[string]*loadHist
	// inst is the instance each generator shows ("" all of them); pods keeps each instance's history
	inst map[string]string
	pods map[string]*podHist

	restored map[string]savedLoadHist
	// started is when this session started each generator: the default window of W's report
	started map[string]time.Time
	// full is chart fullIdx of the selected generator shown full size; w the screen's width
	full    *chartView
	fullIdx int
	w       int
}

// podHist is one generator instance while the TUI watched it, keyed "<generator>/<instance>".
type podHist struct {
	cpu, mem, sent []core.Point
	last           loadSample
}

// loadHist keeps what a generator did while the TUI watched it; Sent deltas give the actual rate.
type loadHist struct {
	target, actual, failed []core.Point
	samples                []loadSample
}

type loadSample struct {
	at           time.Time
	sent, failed int64
}

// rates compares with a sample at least 10s old: counters scraped every few seconds look idle between scrapes.
func (h *loadHist) rates(now time.Time, st core.LoadStatus) (sent, failed float64, ok bool) {
	h.samples = append(h.samples, loadSample{at: now, sent: st.Sent, failed: st.Failed})
	for len(h.samples) > 2 && now.Sub(h.samples[1].at) >= 10*time.Second {
		h.samples = h.samples[1:]
	}
	old := h.samples[0]
	secs := now.Sub(old.at).Seconds()
	if secs < 1 || st.Sent < old.sent {
		return 0, 0, false
	}
	return float64(st.Sent-old.sent) / secs, float64(st.Failed-old.failed) / secs, true
}

type loadMsg struct {
	gen   int
	stats map[string]core.LoadStatus
	errs  map[string]error
}

func (t *loadTab) name() string { return "Load" }
func (t *loadTab) typing() bool { return false }
func (t *loadTab) hints() [][2]string {
	if t.full != nil {
		return t.full.hints()
	}
	return [][2]string{{"space", "start/stop"}, {"+/-", "rate step"}, {"r", "set rate"}, {"[ ]", "fewer/more instances"}, {"R", "set instances"},
		{"i", "instance shown"}, {"c", "edit config (KV)"}, {"v", "env vars"}, {"b", "restart"}, {"W", "save a metrics report"}, {"z click", "chart full size"}}
}

func (t *loadTab) open(m *model) tea.Cmd {
	t.names = m.app.Names(core.KindLoad)
	t.hist = map[string]*loadHist{}
	t.inst, t.pods = map[string]string{}, map[string]*podHist{}
	for n, h := range t.restored {
		t.hist[n] = &loadHist{target: h.Target, actual: h.Actual, failed: h.Failed}
	}
	return t.refresh(m)
}

func (t *loadTab) refresh(m *model) tea.Cmd {
	a, gen, ctx, names := m.app, m.gen, m.ctx, t.names
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		msg := loadMsg{gen: gen, stats: map[string]core.LoadStatus{}, errs: map[string]error{}}
		for _, n := range names {
			g, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, n)
			if err == nil {
				msg.stats[n], err = g.Status(c)
			}
			if err != nil {
				msg.errs[n] = err
			}
		}
		return msg
	}
}

func (t *loadTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loadMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.stats, t.errs = msg.stats, msg.errs
		now := time.Now()
		for n, st := range msg.stats {
			h := t.hist[n]
			if h == nil {
				h = &loadHist{}
				t.hist[n] = h
			}
			if sent, failed, ok := h.rates(now, st); ok {
				h.actual = keep(append(h.actual, core.Point{T: now, V: sent}))
				h.failed = keep(append(h.failed, core.Point{T: now, V: failed}))
			}
			target := st.Rate
			if !st.Running {
				target = 0
			}
			h.target = keep(append(h.target, core.Point{T: now, V: target}))
			t.trackPods(m, n, st, now)
		}
	case loadConfigMsg:
		if msg.err != nil {
			m.setStatus("load config: "+msg.err.Error(), true)
			return nil
		}
		m.setStatus("rate is read every 5s; other fields need a restart of the generator (b)", false)
		return editKV(m, msg.comp, msg.key, msg.value)
	case tea.KeyMsg:
		if t.full != nil {
			if msg.String() == "n" {
				t.openChart(m, t.fullIdx+1)
			} else if !t.full.key(m, msg) {
				t.full = nil
			}
			return nil
		}
		if msg.String() == "z" {
			t.openChart(m, 0)
			return nil
		}
		if listKeys(msg, &t.sel, len(t.names)) || len(t.names) == 0 {
			return nil
		}
		n := t.names[t.sel]
		a := m.app
		g, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, n)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		switch msg.String() {
		case "i", "shift+right", "shift+left":
			ids := t.instanceIDs(m, n)
			d := 1
			if msg.String() == "shift+left" {
				d = len(ids) - 1
			}
			t.inst[n] = ids[(slices.Index(ids, t.inst[n])+d)%len(ids)]
			return nil
		case " ", "enter":
			if t.stats[n].Running {
				return m.act("stop "+n, false, g.Stop)
			}
			if t.started == nil {
				t.started = map[string]time.Time{}
			}
			t.started[n] = time.Now()
			return m.act("start "+n, false, g.Start)
		case "W":
			window := 15 * time.Minute
			if at, ok := t.started[n]; ok {
				window = time.Since(at).Round(time.Minute) + time.Minute
			}
			saveReport(m, window)
			return nil
		case "[", "]":
			ls, ok := g.(core.LoadScaler)
			if !ok {
				m.setStatus(n+" does not run as instances", true)
				return nil
			}
			d := 1
			if msg.String() == "[" {
				d = -1
			}
			return m.act(fmt.Sprintf("%s instances %+d", n, d), false, func(ctx context.Context) error {
				cur, err := ls.Replicas(ctx)
				if err != nil {
					return err
				}
				return ls.SetReplicas(ctx, max(cur+d, 0))
			})
		case "R":
			ls, ok := g.(core.LoadScaler)
			if !ok {
				m.setStatus(n+" does not run as instances", true)
				return nil
			}
			m.ask("instances of "+n, t.stats[n].Extra["replicas"], func(v string) tea.Cmd {
				k, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil {
					m.setStatus("instances: "+err.Error(), true)
					return nil
				}
				return m.act(fmt.Sprintf("%s to %d instances", n, k), false, func(ctx context.Context) error { return ls.SetReplicas(ctx, k) })
			})
		case "c", "v", "b":
			lc, ok := g.(core.LoadConfigured)
			if !ok {
				m.setStatus(n+" has no config key or services", true)
				return nil
			}
			return t.configure(m, msg.String(), n, lc)
		case "+", "=":
			return m.act("raise "+n, false, func(ctx context.Context) error { _, err := a.Nudge(ctx, n, 1); return err })
		case "-":
			return m.act("lower "+n, false, func(ctx context.Context) error { _, err := a.Nudge(ctx, n, -1); return err })
		case "r":
			m.ask("rate for "+n+" (req/s)", strconv.FormatFloat(t.stats[n].Rate, 'f', -1, 64), func(v string) tea.Cmd {
				r, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					m.setStatus("rate: "+err.Error(), true)
					return nil
				}
				return m.act(fmt.Sprintf("set %s to %s/s on every instance", n, v), false, func(ctx context.Context) error { return a.SetRate(ctx, n, r, false) })
			})
		}
	}
	return nil
}

func keep(p []core.Point) []core.Point {
	if len(p) > 3600 {
		return p[len(p)-3600:]
	}
	return p
}

// openChart shows chart i (wrapping) of the selected generator full size, keeping the range and zoom.
func (t *loadTab) openChart(m *model, i int) {
	if len(t.names) == 0 {
		return
	}
	cs := t.charts(m, t.names[t.sel])
	i = (i%len(cs) + len(cs)) % len(cs)
	c := newChartView("lfull:")
	if t.full != nil {
		c.win = t.full.win
	}
	t.full, t.fullIdx = c, i
}

func (t *loadTab) view(m *model, w, h int) string {
	t.w = w
	if t.full != nil && len(t.names) > 0 {
		cs := t.charts(m, t.names[t.sel])
		c := cs[min(t.fullIdx, len(cs)-1)]
		t.full.title, t.full.unit, t.full.lines = c.title, c.unit, c.lines
		return t.full.view(m, 0, 0, w, h)
	}
	if len(t.names) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sDim.Render(`no load generators — add one to rig.yaml:

components:
  load-api:
    type: http                       # or kv (a generator service reading its rate from Consul), command
    target: "svc://api:8080/ping"
    rate: 20
    max: 500
    step: 10`))
	}
	lw := min(44, w/3)
	var rows [][]string
	for _, n := range t.names {
		st, ok := t.stats[n]
		dot := sDim.Render("○")
		if t.errs[n] != nil {
			dot = sRed.Render("✖")
		} else if ok && st.Running {
			dot = sGreen.Render("●")
		}
		_, typ, _ := m.app.Kind(n)
		rows = append(rows, []string{dot, n, typ, viz.Human(st.Rate, "/s")})
	}
	t.sel = min(t.sel, len(rows)-1)
	list := panel("generators", table([]string{"", "NAME", "TYPE", "RATE"}, []int{1, lw - 26, 8, 9}, rows, t.sel, 0, h-2), lw, h, true)

	n := t.names[t.sel]
	st := t.stats[n]
	rw := w - lw
	if err := t.errs[n]; err != nil {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, panel(n, sRed.Render(wrap(err.Error(), rw-4)), rw, h, false))
	}
	m.zone("load:list", 1, 2, lw-2, len(rows))
	ids := t.instanceIDs(m, n)
	if !slices.Contains(ids, t.inst[n]) {
		t.inst[n] = ""
	}
	labels := make([]string, len(ids))
	for i, id := range ids {
		labels[i] = shortInstance(id)
		if id == "" {
			labels[i] = fmt.Sprintf("all instances (%d)", len(ids)-1)
		}
	}
	strip := truncate(" "+m.strip("load:inst", lw+1, 0, labels, slices.Index(ids, t.inst[n]))+sDim.Render("  (⇧←→ i) · rate and config are shared by every instance"), rw)
	var right string
	if id := t.inst[n]; id != "" {
		right = t.instanceView(m, n, id, rw, h-1)
	} else {
		right = t.allView(m, n, st, rw, h-1)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, list, lipgloss.JoinVertical(lipgloss.Left, strip, right))
}

func (t *loadTab) allView(m *model, n string, st core.LoadStatus, rw, h int) string {
	lim := m.app.LoadLimits(n)
	hist := t.hist[n]
	if hist == nil {
		hist = &loadHist{}
	}
	actual := 0.0
	if len(hist.actual) > 0 {
		actual = hist.actual[len(hist.actual)-1].V
	}
	failPct := 0.0
	if st.Sent > 0 {
		failPct = float64(st.Failed) / float64(st.Sent) * 100
	}
	state, stateColor := "IDLE", lipgloss.TerminalColor(cDim)
	if st.Running {
		state, stateColor = "RUNNING", cGreen
	}
	tw := rw / 4
	tiles := lipgloss.JoinHorizontal(lipgloss.Top,
		tile("target", viz.Human(st.Rate, ""), "req/s each · "+lipgloss.NewStyle().Foreground(stateColor).Bold(true).Render(state), cAccent, tw),
		tile("actual", viz.Human(actual, ""), "req/s sent", cGreen, tw),
		tile("errors", fmt.Sprintf("%.1f%%", failPct), fmt.Sprintf("%d of %d", st.Failed, st.Sent), failColor(failPct), tw),
		tile("p99", latency(st.Latency.P99), "p50 "+latency(st.Latency.P50)+" · p95 "+latency(st.Latency.P95), cPurple, rw-3*tw),
	)
	gauge := ""
	if lim.Max > 0 {
		gauge = sDim.Render(" rate ") + viz.Gauge(st.Rate/lim.Max, rw-30) + sDim.Render(fmt.Sprintf(" %s / max %s", viz.Human(st.Rate, "/s"), viz.Human(lim.Max, "/s")))
	} else {
		gauge = sDim.Render(fmt.Sprintf(" step %s/s · no max set", viz.Human(lim.Step, "")))
	}
	if len(st.Extra) > 0 {
		var ex []string
		for _, k := range engine.SortedKeys(st.Extra) {
			ex = append(ex, k+" "+st.Extra[k])
		}
		gauge += sDim.Render("   " + strings.Join(ex, " · "))
	}
	head := lipgloss.JoinVertical(lipgloss.Left, tiles, truncate(gauge, rw))
	return head + "\n" + t.chartStack(m, n, rw, h-lipgloss.Height(head), 1+lipgloss.Height(head))
}

type loadChart struct {
	title, unit, empty string
	lines              []viz.Line
}

// charts are what the right side plots for generator n: its rates and, with several instances, one
// line per instance; for one instance, its rate and CPU.
func (t *loadTab) charts(m *model, n string) []loadChart {
	if id := t.inst[n]; id != "" {
		p := t.pods[n+"/"+id]
		if p == nil {
			p = &podHist{}
		}
		sent := loadChart{title: shortInstance(id) + " · req/s", unit: "/s", lines: []viz.Line{{Name: "sent", Points: p.sent, Color: "#73BF69"}}}
		if len(p.sent) == 0 {
			sent.empty = "no per-instance counter: give the generator's kv load component metrics.per_instance, a PromQL sent counter labelled pod"
		}
		return []loadChart{sent, {title: shortInstance(id) + " · CPU while watching", lines: []viz.Line{{Name: "cpu (cores)", Points: p.cpu, Color: "#B877D9"}}}}
	}
	hist := t.hist[n]
	if hist == nil {
		hist = &loadHist{}
	}
	out := []loadChart{{title: n + " · req/s while watching", unit: "/s", lines: []viz.Line{
		{Name: "target", Points: hist.target, Color: "#5794F2"},
		{Name: "actual", Points: hist.actual, Color: "#73BF69"},
		{Name: "failed", Points: hist.failed, Color: "#F2495C"},
	}}}
	ids := t.instanceIDs(m, n)[1:]
	if len(ids) < 2 {
		return out
	}
	// one line per instance: sent/s when the generator reports it, else CPU
	per := loadChart{title: n + " · req/s per instance", unit: "/s"}
	for _, id := range ids {
		p := t.pods[n+"/"+id]
		if p == nil {
			continue
		}
		pts := p.sent
		if len(pts) == 0 {
			pts, per.unit, per.title = p.cpu, "", n+" · CPU cores per instance"
		}
		per.lines = append(per.lines, viz.Line{Name: shortInstance(id), Points: pts, Color: colorFor(id)})
	}
	return append(out, per)
}

// chartStack draws the charts of n one above the other in rw×h, the top at body row y; a click on
// one opens it full size.
func (t *loadTab) chartStack(m *model, n string, rw, h, y int) string {
	cs := t.charts(m, n)
	x := t.w - rw
	var parts []string
	for i, c := range cs {
		ch := h / len(cs)
		if i == len(cs)-1 {
			ch = h - ch*(len(cs)-1)
		}
		m.zone(fmt.Sprintf("load:chart:%d", i), x, y, rw, ch)
		y += ch
		body := viz.LineChart(c.lines, rw-2, ch-2, c.unit)
		if c.empty != "" {
			body = sDim.Render(wrap(c.empty, rw-4))
		}
		parts = append(parts, panel(c.title+sDim.Render("  · enter or click: full size"), body, rw, ch, false))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (t *loadTab) instanceView(m *model, n, id string, rw, h int) string {
	var in core.Instance
	for _, svc := range t.services(m, n) {
		for _, i := range svc.Instances {
			if i.ID == id {
				in = i
			}
		}
	}
	p := t.pods[n+"/"+id]
	if p == nil {
		p = &podHist{}
	}
	sent := "-"
	if len(p.sent) > 0 {
		sent = viz.Human(p.sent[len(p.sent)-1].V, "")
	}
	age := "-"
	if !in.Started.IsZero() {
		age = shortAge(time.Since(in.Started))
	}
	tw := rw / 4
	tiles := lipgloss.JoinHorizontal(lipgloss.Top,
		tile("state", string(in.State), fmt.Sprintf("up %s · %d restarts", age, in.Restarts), cAccent, tw),
		tile("sent", sent, "req/s · target "+viz.Human(t.stats[n].Rate, "/s"), cGreen, tw),
		tile("cpu", cpuText(in.CPU), "cores", cPurple, tw),
		tile("memory", bytesText(in.Memory), in.Host, cAmber, rw-3*tw),
	)
	return tiles + "\n" + t.chartStack(m, n, rw, h-lipgloss.Height(tiles), 1+lipgloss.Height(tiles))
}

func (t *loadTab) services(m *model, n string) []core.Status {
	g, _, err := engine.Get[core.LoadGenerator](m.app, core.KindLoad, n)
	if err != nil {
		return nil
	}
	lc, ok := g.(core.LoadConfigured)
	if !ok {
		return nil
	}
	var out []core.Status
	for _, st := range m.services {
		if slices.Contains(lc.Services(), st.Service) {
			out = append(out, st)
		}
	}
	return out
}

// instanceIDs is "" (all) followed by each instance of the generator.
func (t *loadTab) instanceIDs(m *model, n string) []string {
	ids := []string{""}
	for _, st := range t.services(m, n) {
		for _, in := range st.Instances {
			ids = append(ids, in.ID)
		}
	}
	sort.Strings(ids[1:])
	return ids
}

func (t *loadTab) trackPods(m *model, n string, st core.LoadStatus, now time.Time) {
	for _, svc := range t.services(m, n) {
		for _, in := range svc.Instances {
			k := n + "/" + in.ID
			p := t.pods[k]
			if p == nil {
				p = &podHist{}
				t.pods[k] = p
			}
			p.cpu = keep(append(p.cpu, core.Point{T: now, V: in.CPU}))
			p.mem = keep(append(p.mem, core.Point{T: now, V: float64(in.Memory)}))
			sent, ok := st.PerInstance[in.ID]
			if !ok {
				continue
			}
			if secs := now.Sub(p.last.at).Seconds(); !p.last.at.IsZero() && secs >= 1 && sent >= p.last.sent {
				p.sent = keep(append(p.sent, core.Point{T: now, V: float64(sent-p.last.sent) / secs}))
			}
			p.last = loadSample{at: now, sent: sent}
		}
	}
}

func (t *loadTab) click(m *model, h hit) tea.Cmd {
	if t.full != nil {
		if !t.full.click(m, h) {
			t.full = nil
		}
		return nil
	}
	if i, ok := strings.CutPrefix(h.id, "load:chart:"); ok {
		k, _ := strconv.Atoi(i)
		t.openChart(m, k)
		return nil
	}
	if h.id == "load:list" && h.y < len(t.names) {
		t.sel = h.y
		return nil
	}
	if i, ok := stripHit(h, "load:inst"); ok && len(t.names) > 0 {
		n := t.names[t.sel]
		if ids := t.instanceIDs(m, n); i < len(ids) {
			t.inst[n] = ids[i]
		}
	}
	return nil
}

func failColor(pct float64) lipgloss.TerminalColor {
	switch {
	case pct >= 5:
		return cRed
	case pct > 0:
		return cAmber
	}
	return cGreen
}

func latency(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
}

type loadConfigMsg struct {
	comp, key string
	value     []byte
	err       error
}

// configure edits a generator's KV config in the editor (c), sets its services' env (v) or restarts
// them (b), which fields other than the rate usually need.
func (t *loadTab) configure(m *model, op, n string, lc core.LoadConfigured) tea.Cmd {
	a, svcs := m.app, lc.Services()
	switch op {
	case "c":
		store, key := lc.ConfigKey()
		return func() tea.Msg {
			kv, _, err := engine.Get[core.KV](a, core.KindKV, store)
			if err != nil {
				return loadConfigMsg{err: err}
			}
			v, _, err := kv.Get(m.ctx, key)
			return loadConfigMsg{comp: store, key: key, value: v, err: err}
		}
	case "b":
		return m.act(label("restart", svcs), false, func(ctx context.Context) error {
			return each(svcs, func(s string) error { return a.Restart(ctx, s) })
		})
	}
	var cur []string
	if len(svcs) > 0 {
		for k, v := range a.EnvOverrides(svcs[0]) {
			cur = append(cur, k+"="+v)
		}
	}
	sort.Strings(cur)
	m.ask("env of "+strings.Join(svcs, ", ")+" (K=V ..., K= removes; redeploys)", strings.Join(cur, " "), func(v string) tea.Cmd {
		kv := map[string]string{}
		for _, f := range strings.Fields(v) {
			k, val, ok := strings.Cut(f, "=")
			if !ok || k == "" {
				m.setStatus("env: write K=V pairs", true)
				return nil
			}
			kv[k] = val
		}
		if len(kv) == 0 {
			return nil
		}
		return m.act("set env of "+n, true, func(ctx context.Context) error { return a.SetEnv(ctx, svcs, kv) })
	})
	return nil
}

func (t *loadTab) drag(m *model, h hit, phase dragPhase) bool {
	return t.full != nil && t.full.drag(m, h, phase)
}

func (t *loadTab) wheel(m *model, h hit, up bool) (tea.Cmd, bool) {
	if t.full == nil {
		return nil, false
	}
	return nil, t.full.wheel(h, up)
}
