package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/ide"
	"github.com/goxang/rig/spec"
)

// servicesTab lists services; enter opens one: its instances and its live log, which is only
// streamed while the service is open.
type servicesTab struct {
	list   *grid
	marked map[string]bool
	filter string
	// section narrows the list: "" every app and load generator, a rig.yaml section, "other" (apps in
	// no section) or "infra"
	section  string
	sections map[string]string

	open_ string // the service being shown, "" for the list
	pods  *grid
	// lt is the open service's log: the Logs screen itself, pinned to that service
	lt     *logsTab
	detail string
	prof   *profView
	debug  map[string]core.DebugSession
	// desired is each service's last seen replica count; scaled the last change of it, so a
	// scale by an autoscaler (or anyone) shows on the list for a while
	desired map[string]int
	scaled  map[string]scaleChange
}

type scaleChange struct {
	from, to int
	at       time.Time
}

const scaleShown = 10 * time.Minute

// noteScale records a change of a service's replica count since the last refresh.
func (t *servicesTab) noteScale(st core.Status) {
	if t.desired == nil {
		t.desired, t.scaled = map[string]int{}, map[string]scaleChange{}
	}
	if st.State == core.StateAbsent {
		return
	}
	prev, seen := t.desired[st.Service]
	t.desired[st.Service] = st.Desired
	if seen && prev != st.Desired {
		t.scaled[st.Service] = scaleChange{from: prev, to: st.Desired, at: time.Now()}
	}
}

// scaleNote is "↑ 2→3 40s ago" while the last change is recent.
func (t *servicesTab) scaleNote(svc string) (arrow, note string) {
	c, ok := t.scaled[svc]
	if !ok || time.Since(c.at) > scaleShown {
		return "", ""
	}
	arrow = sGreen.Render("↑")
	if c.to < c.from {
		arrow = sAmber.Render("↓")
	}
	return arrow, fmt.Sprintf("scaled %d→%d %s ago", c.from, c.to, shortAge(time.Since(c.at)))
}

// resultMsg carries an operation's outcome into the detail pane (and the footer).
type resultMsg struct {
	gen     int
	text    string
	status  string
	err     bool
	debug   *debugAttach
	profile *profView
}

type debugAttach struct {
	svc  string
	sess core.DebugSession
}

func (t *servicesTab) name() string { return "Services" }
func (t *servicesTab) typing() bool { return false }

func (t *servicesTab) open(m *model) tea.Cmd {
	t.list = newGrid("svc", col("", 1), col("", 1), col("SERVICE", 48), col("SECTION", 12), col("GROUPS", 16), rcol("READY", 13), rcol("RESTARTS", 8),
		rcol("CPU", 6), rcol("MEMORY", 7), col("IMAGE", 28), col("MESSAGE", 0))
	t.list.sortBy = 3
	t.list.simple = []int{0, 1, 2, 5, 10}
	t.sections = m.app.Spec.SectionMap()
	t.pods = newGrid("pods", col("", 1), col("INSTANCE", 0), col("HOST", 18), rcol("READY", 5), rcol("RESTARTS", 8), rcol("AGE", 8), rcol("CPU", 6), rcol("MEMORY", 7))
	t.pods.simple = []int{0, 1, 3, 5}
	t.marked = map[string]bool{}
	t.debug = map[string]core.DebugSession{}
	t.lt = &logsTab{id: "svc", fixed: true, log: newLogView("svc:body", 2000, logsUI(m))}
	return nil
}

func (t *servicesTab) refresh(m *model) tea.Cmd { return nil }

func (t *servicesTab) hints() [][2]string {
	if t.open_ != "" && t.lt.inspect != nil {
		return t.lt.hints()
	}
	if t.open_ != "" {
		return [][2]string{{"esc", "back"}, {"↑↓ click", "pick a log line"}, {"enter v", "inspect the line"}, {"/", "filter: text, regex, a.b=value"}, {"i", "pick instances"},
			{"w", "wrap"}, {"s", "structured/raw"}, {"h", "fields shown"}, {"G", "follow again"}, {"y/Y", "copy shown/all"}, {"c", "clear the log"},
			{"r", "restart"}, {"u/x", "start/stop"}, {"+/-", "scale"}, {"a", "autoscaler"}, {"R", "requests/limits"}, {"F", "manifests: edit, sync, apply"},
			{"d", "deploy"}, {"b", "build+deploy"}, {"e", "shell"}, {"p", "profile: cpu, heap, goroutine…"}, {"D", "debug"}, {"l", "this service on the Logs screen"}, {"m", "metrics"},
			{"dbl-click instance", "its log only"}, {"drag", "select log text: copied (past an edge scrolls)"}}
	}
	return [][2]string{{"enter", "open"}, {"space", "mark"}, {"a", "mark section"}, {"⇧←→", "section"}, {"i", "infra"}, {"r", "restart"}, {"s/x", "start/stop"}, {"+/-", "scale"},
		{"h", "autoscaler"}, {"R", "requests/limits"}, {"F", "manifests"}, {"d", "deploy"}, {"b", "build+deploy"}, {"D", "debug"}, {"m", "metrics"}, {"o", "open in GoLand (grouped, logs, stop, debug)"}, {"/", "filter"}, {"< >", "sort"}}
}

// targets are the marked services, else the selected (or open) one.
func (t *servicesTab) targets() []string {
	if t.open_ != "" {
		return []string{t.open_}
	}
	var out []string
	for n, on := range t.marked {
		if on {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		if r, ok := t.list.current(); ok {
			out = []string{r.id}
		}
	}
	return out
}

func label(verb string, names []string) string {
	if len(names) == 1 {
		return verb + " " + names[0]
	}
	return fmt.Sprintf("%s %d services", verb, len(names))
}

func (t *servicesTab) status(m *model, name string) core.Status {
	for _, s := range m.services {
		if s.Service == name {
			return s
		}
	}
	return core.Status{Service: name}
}

func (t *servicesTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case resultMsg:
		m.busy = max(0, m.busy-1)
		m.setStatus(msg.status, msg.err)
		if msg.gen != m.gen {
			return nil
		}
		if msg.text != "" {
			t.detail = msg.text
		}
		if msg.debug != nil {
			t.debug[msg.debug.svc] = msg.debug.sess
		}
		if msg.profile != nil {
			t.prof = msg.profile
		}
		return nil
	case logStartMsg, logBatchMsg:
		if t.lt != nil {
			return t.lt.update(m, msg)
		}
	case tea.KeyMsg:
		if t.open_ != "" {
			return t.detailKey(m, msg)
		}
		return t.listKey(m, msg)
	}
	return nil
}

func (t *servicesTab) listKey(m *model, k tea.KeyMsg) tea.Cmd {
	if t.list.key(k) {
		return nil
	}
	switch k.String() {
	case "enter":
		if r, ok := t.list.current(); ok {
			return t.openService(m, r.id, "")
		}
	case " ":
		if r, ok := t.list.current(); ok {
			t.marked[r.id] = !t.marked[r.id]
			t.list.sel = min(t.list.sel+1, len(t.list.rows)-1)
		}
	case "a":
		all := true
		for _, r := range t.list.rows {
			all = all && t.marked[r.id]
		}
		for _, r := range t.list.rows {
			t.marked[r.id] = !all
		}
	case "m":
		if r, ok := t.list.current(); ok {
			return m.showMetrics(r.id)
		}
	case "o":
		a := m.app
		m.setStatus("writing the services into GoLand…", false)
		return func() tea.Msg {
			r, err := ide.Write(a, a.Spec.ServiceNames())
			if err != nil {
				return statusMsg{text: "GoLand: " + err.Error(), err: true}
			}
			text := fmt.Sprintf("GoLand: %d services in %d folders (Services view, alt+8): run one for its live logs, stop stops it, · debug attaches", r.Services, len(r.Folders))
			if err := ide.Open(a.Spec.Dir); err != nil {
				text += " · " + err.Error()
			}
			return statusMsg{text: text}
		}
	case "i":
		if t.section == "infra" {
			t.setSection("")
		} else {
			t.setSection("infra")
		}
	case "[", "]", "shift+left", "shift+right":
		names := t.sectionNames(m)
		i := slices.Index(names, t.section)
		if s := k.String(); s == "[" || s == "shift+left" {
			i = (i - 1 + len(names)) % len(names)
		} else {
			i = (i + 1) % len(names)
		}
		t.setSection(names[i])
	case "D":
		if r, ok := t.list.current(); ok {
			return t.toggleDebug(m, r.id)
		}
	case "/":
		m.ask("filter services (name or group)", t.filter, func(v string) tea.Cmd {
			t.filter = strings.TrimSpace(v)
			return nil
		})
	case "esc":
		t.marked, t.filter = map[string]bool{}, ""
	default:
		return t.ops(m, k.String(), t.targets())
	}
	return nil
}

// ops are the actions that work on one service or on every marked one.
func (t *servicesTab) ops(m *model, key string, names []string) tea.Cmd {
	if len(names) == 0 {
		return nil
	}
	a := m.app
	switch key {
	case "s":
		return m.act(label("start", names), false, func(ctx context.Context) error {
			return a.StartInOrder(ctx, names, 3*time.Minute)
		})
	case "x":
		return m.act(label("stop", names), true, func(ctx context.Context) error {
			return each(names, func(n string) error { return a.Stop(ctx, n) })
		})
	case "r":
		return m.act(label("restart", names), false, func(ctx context.Context) error {
			return each(names, func(n string) error { return a.Restart(ctx, n) })
		})
	case "+", "=":
		return t.scale(m, names, 1)
	case "-":
		return t.scale(m, names, -1)
	case "h":
		return t.editAutoscale(m, names, nil)
	case "F":
		return serviceManifests(m, names[0])
	case "R":
		return editResources(m, names[0])
	case "d":
		m.ask(label("deploy", names)+": image tag (empty: the last built one)", "", func(tag string) tea.Cmd {
			tag = strings.TrimSpace(tag)
			return m.act(label("deploy", names)+tagNote(tag), true, func(ctx context.Context) error {
				return each(names, func(n string) error { return a.Deploy(ctx, n, tag) })
			})
		})
	case "b":
		m.ask(label("build and deploy", names)+": image tag", a.DefaultTag(m.ctx, ""), func(tag string) tea.Cmd {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				return nil
			}
			return m.act(label("build and deploy", names)+tagNote(tag), true, func(ctx context.Context) error {
				err := each(names, func(n string) error {
					rt, s, err := a.Owner(n)
					if err != nil || s.Build == nil || !a.ImageBased() {
						if err == nil {
							err = rt.Deploy(ctx, s, core.Release{})
						}
						return err
					}
					img, err := a.Build(ctx, s, tag, nil)
					if err != nil {
						return err
					}
					return rt.Deploy(ctx, s, core.Release{Image: img})
				})
				if err != nil {
					return err
				}
				return a.SetState(ctx, map[string]string{"tag": tag})
			})
		})
		return nil
	case "l":
		for i, tb := range m.tabs {
			if lt, ok := tb.(*logsTab); ok {
				lt.services, lt.instances = names, nil
				lt.restart = true
				return m.openTab(i)
			}
		}
	}
	return nil
}

func tagNote(tag string) string {
	if tag == "" {
		return ""
	}
	return " at " + tag
}

func each(names []string, f func(string) error) error {
	errs := make([]error, len(names))
	done := make(chan struct{}, len(names))
	for i, n := range names {
		go func() {
			if err := f(n); err != nil {
				errs[i] = fmt.Errorf("%s: %w", n, err)
			}
			done <- struct{}{}
		}()
	}
	for range names {
		<-done
	}
	var msgs []string
	for _, e := range errs {
		if e != nil {
			msgs = append(msgs, e.Error())
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return nil
}

// openService shows one service and streams its log (or one instance's) until it is closed.
func (t *servicesTab) openService(m *model, name, instance string) tea.Cmd {
	t.open_, t.detail = name, ""
	t.lt.services, t.lt.instances, t.lt.inspect = []string{name}, nil, nil
	if instance != "" {
		t.lt.instances = []string{instance}
	}
	return t.lt.start(m)
}

func (t *servicesTab) closeLogs() {
	if t.lt != nil && t.lt.cancel != nil {
		t.lt.cancel()
		t.lt.cancel = nil
	}
}

func (t *servicesTab) detailKey(m *model, k tea.KeyMsg) tea.Cmd {
	if t.prof != nil && t.prof.svc == t.open_ {
		if t.prof.grid.key(k) {
			return nil
		}
		switch k.String() {
		case "W":
			t.prof.save(m)
			return nil
		case "esc", "backspace":
			t.prof = nil
			return nil
		}
	}
	name := t.open_
	if t.lt.inspect != nil {
		if cmd, ok := t.lt.keyHandled(m, k); ok {
			return cmd
		}
	}
	switch k.String() {
	case "m":
		return m.showMetrics(name)
	case "esc", "backspace":
		if t.detail != "" {
			t.detail = ""
			return nil
		}
		t.closeLogs()
		t.open_ = ""
		return nil
	case "e":
		inst := ""
		if len(t.lt.instances) > 0 {
			inst = t.lt.instances[0]
		} else if r, ok := t.pods.current(); ok {
			inst = r.id
		}
		return t.shell(m, name, inst)
	case "p":
		t.pickProfile(m, name)
		return nil
	case "D":
		return t.toggleDebug(m, name)
	case "u":
		return t.ops(m, "s", []string{name})
	case "a":
		return t.ops(m, "h", []string{name})
	case "s", "h":
		t.detail = ""
	}
	if cmd, ok := t.lt.keyHandled(m, k); ok {
		t.detail = ""
		return cmd
	}
	if k.String() == "enter" {
		t.lt.pickInstance(m)
		return nil
	}
	switch k.String() {
	case "s", "h":
		return nil
	}
	return t.ops(m, k.String(), []string{name})
}

func (t *servicesTab) click(m *model, h hit) tea.Cmd {
	if i, ok := stripHit(h, "svc:section"); ok {
		names := t.sectionNames(m)
		if i < len(names) {
			t.setSection(names[i])
			if h.double {
				t.markShown()
			}
		}
		return nil
	}
	if t.open_ == "" {
		if t.list.click(h) && h.double {
			if r, ok := t.list.current(); ok {
				return t.openService(m, r.id, "")
			}
		}
		if h.id == "svc:mark" {
			if i := t.list.offset + h.y; i < len(t.list.rows) {
				id := t.list.rows[i].id
				t.marked[id] = !t.marked[id]
			}
		}
		return nil
	}
	if h.id == "back" {
		t.closeLogs()
		t.open_ = ""
		return nil
	}
	if t.prof != nil && t.prof.grid.click(h) {
		return nil
	}
	if t.pods.click(h) {
		if r, ok := t.pods.current(); ok && h.double {
			if len(t.lt.instances) == 1 && t.lt.instances[0] == r.id {
				return t.openService(m, t.open_, "")
			}
			return t.openService(m, t.open_, r.id)
		}
		return nil
	}
	return t.lt.click(m, h)
}

// shell hands the terminal to `rig exec` for the service; the TUI comes back when it exits.
func (t *servicesTab) shell(m *model, svc, instance string) tea.Cmd {
	self, err := os.Executable()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	args := []string{"-f", m.app.Spec.File, "-e", m.app.Env.Name, "exec", svc}
	if instance != "" {
		args = append(args, "-i", instance)
	}
	return execProcess(exec.Command(self, args...), func(err error) tea.Msg {
		if err != nil {
			return statusMsg{text: "shell: " + err.Error(), err: true}
		}
		return statusMsg{text: "shell on " + svc + " closed"}
	})
}

func (t *servicesTab) toggleDebug(m *model, svc string) tea.Cmd {
	if sess, ok := t.debug[svc]; ok {
		delete(t.debug, svc)
		return m.do("detach debugger from "+svc, func(context.Context) error {
			if sess.Close != nil {
				return sess.Close()
			}
			return nil
		})
	}
	a, s, gen, ctx := m.app, m.app.Spec.Services[svc], m.gen, m.ctx
	m.busy++
	m.setStatus("attaching debugger to "+svc+"…", false)
	return func() tea.Msg {
		d, _, err := engine.Get[core.Debugger](a, core.KindDebugger, "")
		if err != nil {
			return resultMsg{gen: gen, status: "debug: " + err.Error(), err: true}
		}
		sess, err := d.Attach(ctx, s, "")
		if err != nil {
			return resultMsg{gen: gen, status: "debug: " + err.Error(), err: true}
		}
		text := sAccent.Render("debugger on "+sess.Addr) + "\n" + sess.Hint + "\n" + sDim.Render("D again detaches")
		return resultMsg{gen: gen, text: text, status: "debugger on " + sess.Addr, debug: &debugAttach{svc: svc, sess: sess}}
	}
}

func usage(st core.Status) (cpu float64, mem int64, restarts int) {
	for _, in := range st.Instances {
		cpu += in.CPU
		mem += in.Memory
		restarts += in.Restarts
	}
	return
}

func cpuText(c float64) string {
	if c <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0fm", c*1000)
}

func (t *servicesTab) rows(m *model) []grow {
	var rows []grow
	f := strings.ToLower(t.filter)
	for _, st := range m.services {
		s := m.app.Spec.Services[st.Service]
		if s == nil {
			continue
		}
		sec := t.sectionOf(s)
		if t.section == "" && sec == "infra" || t.section != "" && sec != t.section {
			continue
		}
		groups := strings.Join(s.Groups, ",")
		if f != "" && !strings.Contains(strings.ToLower(st.Service+" "+groups+" "+s.Role), f) {
			continue
		}
		cpu, mem, restarts := usage(st)
		name := st.Service
		if _, dbg := t.debug[st.Service]; dbg {
			name += sAccent.Render(" ⬢")
		}
		if m.app.SharedElsewhere(st.Service) {
			name += sDim.Render(" ⇄" + m.app.Env.Infra)
		} else if s.Role != "app" {
			name += sDim.Render(" " + s.Role)
		}
		rs := fmt.Sprint(restarts)
		if restarts > 0 {
			rs = sAmber.Render(rs)
		}
		arrow, note := t.scaleNote(st.Service)
		msg := st.Message
		if msg != "" {
			msg = sAmber.Render(msg)
		}
		if note != "" {
			if msg != "" {
				msg += " "
			}
			msg += sDim.Render(note)
		}
		mark := " "
		if t.marked[st.Service] {
			mark = sAccent.Render("●")
		}
		rows = append(rows, grow{id: st.Service,
			cells: []string{mark, stateDot(st.State), name, sec, sDim.Render(groups), fmt.Sprintf("%d/%d", st.Ready, st.Desired) + arrow + hpaNote(st), rs, cpuText(cpu), bytesText(mem), tagOf(st.Image), msg},
			keys:  []any{nil, stateRank(st.State), st.Service, t.sectionRank(m, sec, st.Service), groups, float64(st.Ready), float64(restarts), cpu, float64(mem), nil, nil}})
	}
	return rows
}

func tagOf(img string) string {
	img = lastPath(img)
	if len(img) > 28 {
		return "…" + img[len(img)-27:]
	}
	return img
}

func (t *servicesTab) view(m *model, w, h int) string {
	if t.open_ != "" {
		return t.serviceView(m, w, h)
	}
	t.list.set(t.rows(m))
	marked := 0
	for _, on := range t.marked {
		if on {
			marked++
		}
	}
	title := fmt.Sprintf("%s · %d", map[string]string{"": "services", "infra": "infrastructure"}[t.section], len(t.list.rows))
	if t.section != "" && t.section != "infra" {
		title = fmt.Sprintf("%s · %d", t.section, len(t.list.rows))
		if sec := m.app.Spec.Sections[t.section]; sec != nil && sec.Help != "" {
			title += " · " + sec.Help
		}
	}
	if t.filter != "" {
		title += " · filter " + t.filter
	}
	if marked > 0 {
		title += fmt.Sprintf(" · %d marked (actions apply to all; esc clears)", marked)
	}
	if len(m.services) == 0 {
		return panel(title, sDim.Render("asking the runtime…"), w, h, true)
	}
	names := t.sectionNames(m)
	strip := m.strip("svc:section", 1, 1, t.sectionLabels(m, names), slices.Index(names, t.section)) + sDim.Render("  ⇧←→ · dbl-click marks")
	body := strip + "\n" + m.stripRule(0, w-2) + "\n" + t.list.view(m, 1, 3, w-2, h-4, true)
	m.zone("svc:mark", 1, 4, 2, t.list.shown)
	return panel(title, body, w, h, true)
}

func (t *servicesTab) serviceView(m *model, w, h int) string {
	name := t.open_
	st := t.status(m, name)
	s := m.app.Spec.Services[name]
	var b strings.Builder
	b.WriteString(sKey.Render("‹ back") + "  " + sTitle.Render(name) + "  " + stateText(st.State) + fmt.Sprintf("  %d/%d ready", st.Ready, st.Desired))
	m.zone("back", 1, 0, 6, 1)
	if len(s.Groups) > 0 {
		b.WriteString(sDim.Render("  groups " + strings.Join(s.Groups, ",")))
	}
	if len(s.DependsOn) > 0 {
		b.WriteString(sDim.Render("  needs " + strings.Join(s.DependsOn, ",")))
	}
	if as := st.Autoscale; as != nil {
		b.WriteString(sDim.Render(fmt.Sprintf("  autoscaler %d-%d (h)", as.Min, as.Max)))
	}
	b.WriteString("\n")
	if st.Image != "" {
		b.WriteString(sDim.Render("image ") + st.Image)
	}
	if len(s.Ports) > 0 {
		var ps []string
		for _, k := range engine.SortedKeys(s.Ports) {
			ps = append(ps, fmt.Sprintf("%s:%d", k, s.Ports[k]))
		}
		b.WriteString(sDim.Render("   ports " + strings.Join(ps, " ")))
	}
	if st.Message != "" {
		b.WriteString("\n" + sAmber.Render(st.Message))
	}
	head := b.String()
	headH := lipgloss.Height(head)

	var rows []grow
	for _, in := range st.Instances {
		age := "-"
		if !in.Started.IsZero() {
			age = shortAge(time.Since(in.Started))
		}
		id := in.ID
		if contains(t.lt.instances, in.ID) {
			id = sAccent.Render("▸ ") + id
		}
		rows = append(rows, grow{id: in.ID, cells: []string{stateDot(in.State), id, in.Host, fmt.Sprint(in.Ready), fmt.Sprint(in.Restarts), age, cpuText(in.CPU), bytesText(in.Memory)},
			keys: []any{stateRank(in.State), in.ID, in.Host, nil, float64(in.Restarts), float64(-in.Started.Unix()), in.CPU, float64(in.Memory)}})
	}
	t.pods.set(rows)
	podsH := min(len(rows)+3, max(5, (h-headH)/3))
	podsBox := panel(fmt.Sprintf("instances · %d", len(rows)), t.pods.view(m, 1, headH+2, w-2, podsH-2, true), w, podsH, true)

	logH := h - headH - podsH - 1
	var logs string
	switch {
	case t.prof != nil && t.prof.svc == name:
		title := fmt.Sprintf("%s profile · %s · ↑↓ < > I sort · W save report · esc close", t.prof.res.Kind, relTo(m.app.Spec.Dir, t.prof.res.File))
		logs = panel(title, t.prof.view(m, 1, headH+1+podsH+1, w-2, logH-2), w, logH, true)
	case t.detail != "":
		logs = panel("result · esc closes", t.detail, w, logH, true)
	default:
		// the log draws as if at the top of the screen; its zones shift down to where it sits
		m.originY += headH + 1 + podsH
		logs = t.lt.view(m, w, logH)
		m.originY -= headH + 1 + podsH
	}
	return lipgloss.JoinVertical(lipgloss.Left, " "+head, "", podsBox, logs)
}

func shortInstance(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 && len(id)-i <= 6 {
		return id[i+1:]
	}
	return id
}

func shortAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func lastPath(img string) string {
	if i := strings.LastIndex(img, "/"); i >= 0 {
		return img[i+1:]
	}
	return img
}

func (t *servicesTab) setSection(name string) {
	t.section = name
	t.marked = map[string]bool{}
	t.list.sel = 0
}

func (t *servicesTab) markShown() {
	for _, r := range t.list.rows {
		t.marked[r.id] = true
	}
}

// sectionOf is the section a service shows under: infrastructure first, then rig.yaml's sections.
func (t *servicesTab) sectionOf(s *spec.Service) string {
	if s.Role == spec.RoleInfra {
		return "infra"
	}
	if sec, ok := t.sections[s.Name]; ok {
		return sec
	}
	return "other"
}

// sectionNames are the strip's entries: everything, each section with services here, other, infra.
func (t *servicesTab) sectionNames(m *model) []string {
	have := map[string]bool{}
	for _, st := range m.services {
		if s := m.app.Spec.Services[st.Service]; s != nil {
			have[t.sectionOf(s)] = true
		}
	}
	names := []string{""}
	for _, n := range append(slices.Clone(m.app.Spec.SectionOrder), "other", "infra") {
		if have[n] {
			names = append(names, n)
		}
	}
	return names
}

// sectionRank sorts by section, then by where the section lists the service (unlisted ones, which
// came in through a group, after the listed ones).
func (t *servicesTab) sectionRank(m *model, sec, svc string) float64 {
	i := slices.Index(m.app.Spec.SectionOrder, sec)
	if i < 0 {
		return float64(len(m.app.Spec.SectionOrder) + map[string]int{"other": 0, "infra": 1}[sec])
	}
	pos := slices.Index(m.app.Spec.Sections[sec].Services, svc)
	if pos < 0 {
		pos = 999
	}
	return float64(i) + float64(pos)/1000
}

// sectionLabels show each section's running count: "core 7/9".
func (t *servicesTab) sectionLabels(m *model, names []string) []string {
	up, all := map[string]int{}, map[string]int{}
	for _, st := range m.services {
		s := m.app.Spec.Services[st.Service]
		if s == nil {
			continue
		}
		sec := t.sectionOf(s)
		for _, k := range []string{sec, ""} {
			if k == "" && sec == "infra" {
				continue
			}
			all[k]++
			if engine.Ready(st) {
				up[k]++
			}
		}
	}
	out := make([]string, len(names))
	for i, n := range names {
		l := n
		if n == "" {
			l = "all"
		}
		out[i] = fmt.Sprintf("%s %d/%d", l, up[n], all[n])
	}
	return out
}

func hpaNote(st core.Status) string {
	if st.Autoscale == nil {
		return ""
	}
	return sDim.Render(fmt.Sprintf(" ⇕%d-%d", st.Autoscale.Min, st.Autoscale.Max))
}

// scale moves replicas by d; when that leaves an autoscaler's range (which would put it back), it
// asks whether to move the autoscaler too.
func (t *servicesTab) scale(m *model, names []string, d int) tea.Cmd {
	a := m.app
	verb := "scale up"
	if d < 0 {
		verb = "scale down"
	}
	dangerous := false
	want := map[string]int{}
	var outside []string
	for _, n := range names {
		st := t.status(m, n)
		want[n] = max(st.Desired+d, 0)
		dangerous = dangerous || d < 0 && st.Desired <= 1
		// at 0 replicas an HPA stands still, so stopping never fights it
		if b := st.Autoscale; b != nil && want[n] > 0 && (want[n] < b.Min || want[n] > b.Max) {
			outside = append(outside, n)
		}
	}
	scale := func(ctx context.Context) error { return a.ScaleBy(ctx, names, d, false) }
	if len(outside) == 0 {
		return m.act(label(verb, names), dangerous, scale)
	}
	fit := map[string]core.Bounds{}
	var fits []string
	for _, n := range outside {
		b := *t.status(m, n).Autoscale
		b.Min, b.Max = min(b.Min, want[n]), max(b.Max, want[n])
		fit[n] = b
		fits = append(fits, fmt.Sprintf("%s %d-%d", n, b.Min, b.Max))
	}
	cur := t.status(m, outside[0]).Autoscale
	opts := []string{"move the autoscaler", "set autoscaler bounds…", "scale only", "cancel"}
	desc := []string{
		"and scale: " + strings.Join(fits, ", "),
		"type min and max, then scale",
		fmt.Sprintf("the autoscaler (%d-%d) will put it back", cur.Min, cur.Max),
		"",
	}
	title := fmt.Sprintf("%s: %s outside its autoscaler's range", label(verb, names), strings.Join(outside, ", "))
	m.pick(title, opts, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		switch c[0] {
		case opts[0]:
			return m.act(label(verb, names)+" and its autoscaler", dangerous, func(ctx context.Context) error {
				if err := each(outside, func(n string) error { return a.SetAutoscale(ctx, n, fit[n]) }); err != nil {
					return err
				}
				return scale(ctx)
			})
		case opts[1]:
			return t.editAutoscale(m, outside, scale)
		case opts[2]:
			return m.act(label(verb, names), dangerous, scale)
		}
		return nil
	})
	return nil
}

// editAutoscale asks for new autoscaler bounds of names, then runs then (when set) after them.
func (t *servicesTab) editAutoscale(m *model, names []string, then func(context.Context) error) tea.Cmd {
	var cur *core.Bounds
	for _, n := range names {
		if b := t.status(m, n).Autoscale; b != nil {
			cur = b
			break
		}
	}
	a := m.app
	if cur == nil {
		if _, ok := a.Runtime().(core.Autoscaler); !ok {
			m.setStatus(m.app.Env.Name+": the runtime has no autoscalers", true)
			return nil
		}
		n := max(1, t.status(m, names[0]).Desired)
		m.ask(label("new autoscaler for", names)+": min max cpu% [memory%]", fmt.Sprintf("%d %d 75", n, n*3), func(v string) tea.Cmd {
			var b core.Bounds
			f := strings.Fields(v)
			if len(f) < 3 {
				m.setStatus("autoscaler: type min, max and a CPU target (percent of requests), memory optional", true)
				return nil
			}
			fmt.Sscan(strings.Join(f, " "), &b.Min, &b.Max, &b.CPU, &b.Memory)
			return m.act(fmt.Sprintf("%s %d-%d at cpu %d%%", label("new autoscaler for", names), b.Min, b.Max, b.CPU), false, func(ctx context.Context) error {
				if err := each(names, func(n string) error { return a.SetAutoscale(ctx, n, b) }); err != nil || then == nil {
					return err
				}
				return then(ctx)
			})
		})
		return nil
	}
	m.ask(label("autoscaler of", names)+": min max", fmt.Sprintf("%d %d", cur.Min, cur.Max), func(v string) tea.Cmd {
		var b core.Bounds
		if _, err := fmt.Sscan(v, &b.Min, &b.Max); err != nil {
			m.setStatus("autoscaler: type two numbers, min and max", true)
			return nil
		}
		return m.act(fmt.Sprintf("%s to %d-%d", label("autoscaler of", names), b.Min, b.Max), false, func(ctx context.Context) error {
			if err := each(names, func(n string) error { return a.SetAutoscale(ctx, n, b) }); err != nil || then == nil {
				return err
			}
			return then(ctx)
		})
	})
	return nil
}

func (t *servicesTab) drag(m *model, h hit, phase dragPhase) bool {
	if t.open_ == "" {
		return false
	}
	return t.lt.drag(m, h, phase)
}

func (t *servicesTab) wheel(m *model, h hit, up bool) (tea.Cmd, bool) {
	if t.open_ == "" {
		return nil, false
	}
	return t.lt.wheel(m, h, up)
}
