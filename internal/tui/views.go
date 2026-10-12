package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/contracts"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// viewTab is a screen drawn from a view spec: a widget tree whose bound widgets the host fetches,
// only while they show, sharing one request per distinct source and args.
type viewTab struct {
	title string
	v     *view.View
	// dir is where the view's file: and widgets' file: props resolve
	dir string
	// remote is <plugin>.<view> for a view its plugin renders (render)
	remote string

	root  *wnode
	eng   *engine.Local
	fetch *view.Fetcher
	ctx   context.Context
	stop  context.CancelFunc
	focus *wnode
	// order is every focusable widget in the last frame, in draw order
	order []*wnode
	err   string
	// filtering is typing into the focused table's / filter
	filtering bool
	// previewRow is the row the preview panes show, for ${row.x} in their titles
	previewRow map[string]any
	// plugin serves the tab and secrets are the credentials it asks for; needSecret is when none is set yet
	plugin     string
	secrets    []string
	needSecret bool
}

// wnode is a widget of the tree with its live state.
type wnode struct {
	n     protocol.Node
	kids  []*wnode
	frame *protocol.Frame
	err   error
	// loading is a fetch in flight; streaming a stream feeding the widget
	loading, streaming bool
	// tab is a tabs widget's shown child
	tab        int
	x, y, w, h int
	id         int
	st         any
	filter     string
	// matches is the rows the table showed under filter; base the binding before a server search replaced it
	matches int
	fold    *tableFold
	// marks are the rows picked with space, by their mark: field; actions then run on each
	marks map[string]bool
	// expect is what a lookup asked for: when it arrives empty the screen says it was not found
	expect string
	base   *protocol.Binding
	// preview is the pane beside a table or board showing its selected row (preview: prop)
	preview *wnode
	// bound is the binding a preview pane last fetched, to drop answers for rows left behind; tpl
	// its binding before ${row.x} expands
	bound string
	tpl   protocol.Binding
	// previewOff hides the preview pane (v; a board's enter)
	previewOff bool
}

type viewDataMsg struct {
	t   *viewTab
	n   *wnode
	f   *protocol.Frame
	err error
	key string
}

type viewStreamMsg struct {
	t    *viewTab
	n    *wnode
	f    *protocol.Frame
	done bool
	err  error
}

func newViewTab(title string, v *view.View, dir string) *viewTab {
	return &viewTab{title: title, v: v, dir: dir}
}

func (t *viewTab) name() string { return t.title }

func (t *viewTab) typing() bool {
	if t.filtering {
		return true
	}
	if t.focus == nil {
		return false
	}
	switch st := t.focus.st.(type) {
	case *terminal:
		return st.input
	}
	return false
}

func (t *viewTab) clearFilter() bool {
	if t.focus != nil && t.focus.filter == "" && len(t.focus.marks) > 0 {
		clear(t.focus.marks)
		return true
	}
	if t.focus == nil || t.focus.filter == "" && t.focus.base == nil {
		return false
	}
	t.focus.filter = ""
	t.unsearch(t.focus)
	return true
}

// searchable is a widget whose source takes a query: a filter with no local match can ask it for more.
func searchable(wn *wnode) bool {
	return wn.n.Bind != nil && wn.n.Bind.Args["query"] != nil
}

// search re-queries the source with q, which reaches rows the first page did not hold.
func (t *viewTab) search(wn *wnode, q string) tea.Cmd {
	setQuery(wn, q)
	return t.load(wn)
}

func setQuery(wn *wnode, q string) {
	if wn.base == nil {
		b := *wn.n.Bind
		wn.base = &b
	}
	b := *wn.base
	b.Args = maps.Clone(b.Args)
	b.Args["query"] = q
	wn.n.Bind, wn.frame, wn.err, wn.filter = &b, nil, nil, ""
}

// walkAll visits every widget of the tree, shown or not.
func (t *viewTab) walkAll(fn func(*wnode)) {
	var walk func(*wnode)
	walk = func(wn *wnode) {
		if wn == nil {
			return
		}
		fn(wn)
		walk(wn.preview)
		for _, k := range wn.kids {
			walk(k)
		}
	}
	walk(t.root)
}

// keep and resume: the queries the user changed (Q, a search) belong to a saved session only.
func (t *viewTab) keep() map[string]string {
	out := map[string]string{}
	t.walkAll(func(wn *wnode) {
		if wn.base != nil {
			out[strconv.Itoa(wn.id)] = fmt.Sprint(wn.n.Bind.Args["query"])
		}
	})
	return out
}

func (t *viewTab) resume(m *model, v map[string]string) tea.Cmd {
	t.walkAll(func(wn *wnode) {
		if q, ok := v[strconv.Itoa(wn.id)]; ok && searchable(wn) {
			setQuery(wn, q)
		}
	})
	return t.refresh(m)
}

// unsearch goes back to the binding a server search replaced.
func (t *viewTab) unsearch(wn *wnode) {
	if wn.base == nil {
		return
	}
	b := *wn.base
	wn.n.Bind, wn.base, wn.frame, wn.err, wn.loading = &b, nil, nil, nil, false
}

func (t *viewTab) interval() time.Duration { return time.Second }

func (t *viewTab) build() {
	t.err = ""
	if t.remote != "" && t.v.Root.Widget == "" {
		return
	}
	if t.v.File != "" && t.v.Root.Widget == "" {
		raw, err := os.ReadFile(filepath.Join(t.dir, t.v.File))
		if err == nil {
			err = t.v.Load(raw)
		}
		if err != nil {
			t.err = fmt.Sprintf("%s: %v", t.v.File, err)
			return
		}
	}
	id := 0
	var mk func(n protocol.Node) *wnode
	mk = func(n protocol.Node) *wnode {
		id++
		wn := &wnode{n: n, id: id}
		for _, c := range n.Children {
			wn.kids = append(wn.kids, mk(c))
		}
		return wn
	}
	t.root = mk(t.v.Root)
}

func (t *viewTab) open(m *model) tea.Cmd {
	if t.root == nil {
		t.build()
	}
	if t.root == nil && t.remote != "" && t.err == "" {
		e, name := m.eng(), t.remote
		return func() tea.Msg {
			n, err := e.Host.View(context.Background(), name, nil)
			return viewRootMsg{t: t, n: n, err: err}
		}
	}
	return t.refresh(m)
}

type viewRootMsg struct {
	t   *viewTab
	n   *protocol.Node
	err error
}

func (t *viewTab) hide(m *model) {
	if m.session == nil {
		t.walkAll(t.unsearch)
	}
	if t.stop != nil {
		t.stop()
		t.stop = nil
	}
	t.walkVisible(func(wn *wnode) {
		wn.loading, wn.streaming = false, false
		if term, ok := wn.st.(*terminal); ok && !term.keep {
			term.close()
			wn.st = nil
		}
	})
}

// walkVisible visits the widgets that show: only the open child of each tabs widget.
func (t *viewTab) walkVisible(fn func(*wnode)) {
	var walk func(*wnode)
	walk = func(wn *wnode) {
		if wn == nil {
			return
		}
		fn(wn)
		if wn.preview != nil {
			walk(wn.preview)
		}
		if view.Kind(wn.n) == "tabs" {
			if wn.tab < len(wn.kids) {
				walk(wn.kids[wn.tab])
			}
			return
		}
		for _, k := range wn.kids {
			walk(k)
		}
	}
	walk(t.root)
}

func (t *viewTab) refresh(m *model) tea.Cmd {
	if t.root == nil {
		return nil
	}
	t.checkSecret(m)
	if e := m.eng(); t.eng != e {
		t.eng, t.fetch = e, &view.Fetcher{Query: e.Query}
	}
	if t.ctx == nil || t.ctx.Err() != nil {
		t.ctx, t.stop = context.WithCancel(m.ctx)
	}
	var cmds []tea.Cmd
	t.walkVisible(func(wn *wnode) {
		if wn.n.Bind == nil || wn.n.Bind.Source == "" || wn.loading || wn.streaming {
			return
		}
		if streams(wn.n) {
			cmds = append(cmds, t.stream(wn))
			return
		}
		if wn.frame != nil && t.fetch.Fresh(*wn.n.Bind) {
			return
		}
		cmds = append(cmds, t.load(wn))
	})
	return batch(cmds...)
}

func streams(n protocol.Node) bool {
	s, _ := n.Props["stream"].(bool)
	return s
}

func (t *viewTab) load(wn *wnode) tea.Cmd {
	wn.loading = true
	b, fetch, ctx := *wn.n.Bind, t.fetch, t.ctx
	ops := wn.n.Props["transform"]
	return func() tea.Msg {
		f, err := fetch.Get(ctx, b)
		if err == nil {
			f, err = view.Apply(f, ops, func(o protocol.Binding) (*protocol.Frame, error) { return fetch.Get(ctx, o) })
		}
		return viewDataMsg{t: t, n: wn, f: f, err: err, key: view.Key(b)}
	}
}

// stream feeds a widget from a streaming source: each batch's rows append, up to keep: (5000).
func (t *viewTab) stream(wn *wnode) tea.Cmd {
	wn.streaming = true
	b, e, ctx := *wn.n.Bind, t.eng, t.ctx
	return func() tea.Msg {
		ch, err := e.Stream(ctx, b.Source, b.Args)
		if err != nil {
			return viewStreamMsg{t: t, n: wn, done: true, err: err}
		}
		go func() {
			for batch := range ch {
				msg := viewStreamMsg{t: t, n: wn, f: batch.Frame, done: batch.Done}
				if len(batch.Lines) > 0 {
					rows := make([]map[string]any, len(batch.Lines))
					for i, l := range batch.Lines {
						rows[i] = map[string]any{"line": l}
					}
					msg.f = frames.FromRows([]protocol.Field{{Name: "line", Type: protocol.TypeString}}, rows)
				}
				if batch.Error != nil {
					msg.err = batch.Error
				}
				program.Send(msg)
			}
			program.Send(viewStreamMsg{t: t, n: wn, done: true})
		}()
		return nil
	}
}

func (t *viewTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case viewDataMsg:
		if msg.t != t {
			return nil
		}
		if msg.n.bound != "" && msg.n.bound != msg.key {
			return nil
		}
		if wn := msg.n; (wn.base != nil || wn.expect != "") && wn.n.Bind != nil && msg.key != view.Key(*wn.n.Bind) {
			return nil
		}
		msg.n.loading = false
		if msg.err != nil && msg.t.ctx.Err() != nil {
			return nil
		}
		msg.n.err = msg.err
		if msg.f != nil || msg.err == nil {
			msg.n.frame = msg.f
		}
		if q := msg.n.expect; q != "" {
			msg.n.expect = ""
			switch {
			case msg.err != nil:
			case msg.f == nil || frames.Len(msg.f) == 0:
				m.setStatus("not found: "+q+" ("+t.title+")", true)
			default:
				msg.n.previewOff = false
			}
		}
		return t.syncPreviews()
	case viewStreamMsg:
		if msg.t != t {
			return nil
		}
		wn := msg.n
		if msg.err != nil {
			wn.err = msg.err
		}
		if msg.f != nil {
			wn.frame = appendFrame(wn.frame, msg.f, keepRows(wn.n))
		}
		if msg.done {
			wn.streaming = false
		}
		return nil
	case viewRootMsg:
		if msg.t != t {
			return nil
		}
		if msg.err != nil {
			t.err = t.remote + ": " + msg.err.Error()
			return nil
		}
		t.v.Root = *msg.n
		t.build()
		return t.refresh(m)
	case canvasMsg:
		msg.cv.busy, msg.cv.lines, msg.cv.err = false, msg.lines, msg.err
	case tea.KeyPressMsg:
		return t.key(m, msg)
	case tea.PasteMsg:
		if term, ok := t.focusTerm(); ok && term.input {
			term.paste(msg.Content)
		}
	}
	return nil
}

func keepRows(n protocol.Node) int {
	if k, err := strconv.Atoi(view.Prop(n, "keep")); err == nil && k > 0 {
		return k
	}
	return 5000
}

// appendFrame adds b's rows under a's, keeping the last keep rows.
func appendFrame(a, b *protocol.Frame, keep int) *protocol.Frame {
	if a == nil || len(a.Fields) != len(b.Fields) {
		a = &protocol.Frame{Fields: b.Fields, Columns: make([][]any, len(b.Fields))}
	}
	for i := range a.Columns {
		if i < len(b.Columns) {
			a.Columns[i] = append(a.Columns[i], b.Columns[i]...)
		}
		if over := len(a.Columns[i]) - keep; over > 0 {
			a.Columns[i] = a.Columns[i][over:]
		}
	}
	return a
}

func (t *viewTab) focusTerm() (*terminal, bool) {
	if t.focus == nil {
		return nil, false
	}
	term, ok := t.focus.st.(*terminal)
	return term, ok
}

func (t *viewTab) key(m *model, k tea.KeyPressMsg) tea.Cmd {
	cmd := t.keyFocused(m, k)
	return batch(cmd, t.syncPreviews())
}

func (t *viewTab) keyFocused(m *model, k tea.KeyPressMsg) tea.Cmd {
	if term, ok := t.focusTerm(); ok && term.input {
		if s := k.String(); s == "ctrl+]" || s == "esc" {
			term.input, term.left = false, true
			return nil
		}
		term.key(k)
		return nil
	}
	if t.filtering && t.focus != nil {
		switch k.String() {
		case "esc":
			t.focus.filter, t.filtering = "", false
			t.unsearch(t.focus)
			return nil
		case "enter", "down", "up":
			if _, _, ok := describing(t.focus.filter); ok && k.String() == "enter" {
				return t.askFilter(m, t.focus)
			}
			if wn := t.focus; k.String() == "enter" && wn.filter != "" && wn.matches == 0 && searchable(wn) {
				t.filtering = false
				return t.search(wn, wn.filter)
			}
			t.filtering = false
			return t.widgetKey(m, t.focus, k)
		case "backspace":
			if r := []rune(t.focus.filter); len(r) > 0 {
				t.focus.filter = string(r[:len(r)-1])
			}
		default:
			if k.Text != "" {
				t.focus.filter += k.Text
			}
		}
		return nil
	}
	if k.String() == "S" && t.needSecret {
		return t.askSecret(m)
	}
	if k.String() == "/" && t.focus != nil && filterable[view.Kind(t.focus.n)] {
		t.filtering = true
		return nil
	}
	if t.focus != nil {
		if b, ok := t.focus.st.(*boardState); ok && b.key(k.String()) {
			return nil
		}
	}
	if wn := t.focus; k.String() == "Q" && wn != nil && wn.n.Bind != nil && wn.n.Bind.Args["query"] != nil {
		m.ask(wn.n.Bind.Source+" query", fmt.Sprint(wn.n.Bind.Args["query"]), func(q string) tea.Cmd {
			return t.search(wn, q)
		})
		return nil
	}
	if k.String() == "v" && t.focus != nil && t.focus.preview != nil {
		t.focus.previewOff = !t.focus.previewOff
		return nil
	}
	if wn := t.focus; wn != nil && wn.fold != nil {
		if g, ok := wn.st.(*grid); ok && wn.fold.key(g, k.String()) {
			return nil
		}
	}
	if wn := t.focus; wn != nil && markField(wn) != "" && t.markKey(wn, k.String()) {
		return nil
	}
	if g, ok := t.focusGrid(); ok && (k.String() == "left" || k.String() == "right") && g.scrollX(k.String() == "right") {
		return nil
	}
	switch k.String() {
	case "left", "right":
		t.moveFocus(map[string]int{"left": -1, "right": 1}[k.String()])
		return nil
	case "ctrl+left", "ctrl+right", "shift+left", "shift+right":
		if tb := t.tabsOf(t.focus); tb != nil && len(tb.kids) > 0 {
			d := 1
			if strings.HasSuffix(k.String(), "left") {
				d = -1
			}
			t.hide(m)
			tb.tab = (tb.tab + d + len(tb.kids)) % len(tb.kids)
			t.focus = nil
			return t.refresh(m)
		}
		return nil
	case "r":
		if t.fetch != nil {
			t.fetch.Forget()
		}
		t.walkVisible(func(wn *wnode) { wn.loading = false })
		return t.refresh(m)
	}
	if t.focus == nil {
		return nil
	}
	if cmd, ok := t.rowAction(m, t.focus, k.String()); ok {
		return cmd
	}
	return t.widgetKey(m, t.focus, k)
}

// markField is the column a table's rows are picked by (mark: path); empty when it takes no marks.
func markField(wn *wnode) string {
	f, _ := wn.n.Props["mark"].(string)
	return f
}

func rowMark(wn *wnode, i int, field string) string {
	return fmt.Sprint(frames.Row(wn.frame, i)[field])
}

// markKey picks rows: space marks the selected one and steps down, ctrl+a marks or clears every row shown.
func (t *viewTab) markKey(wn *wnode, k string) bool {
	g, ok := wn.st.(*grid)
	if !ok || wn.frame == nil || (k != "space" && k != "ctrl+a") || wn.fold != nil {
		return false
	}
	field := markField(wn)
	if wn.marks == nil {
		wn.marks = map[string]bool{}
	}
	toggle := func(r grow) {
		i, err := strconv.Atoi(r.id)
		if err != nil || i >= frames.Len(wn.frame) {
			return
		}
		key := rowMark(wn, i, field)
		if wn.marks[key] {
			delete(wn.marks, key)
		} else {
			wn.marks[key] = true
		}
	}
	if k == "ctrl+a" {
		all := true
		for _, r := range g.rows {
			if i, err := strconv.Atoi(r.id); err == nil && i < frames.Len(wn.frame) && !wn.marks[rowMark(wn, i, field)] {
				all = false
			}
		}
		for _, r := range g.rows {
			if i, err := strconv.Atoi(r.id); err == nil && i < frames.Len(wn.frame) && wn.marks[rowMark(wn, i, field)] == all {
				toggle(r)
			}
		}
		return true
	}
	if r, ok := g.current(); ok {
		toggle(r)
		g.sel = min(g.sel+1, len(g.rows)-1)
	}
	return true
}

// markedRows are the picked rows in frame order, or nothing when none is.
func (t *viewTab) markedRows(wn *wnode) []map[string]any {
	field := markField(wn)
	if field == "" || len(wn.marks) == 0 || wn.frame == nil {
		return nil
	}
	var out []map[string]any
	for i := range frames.Len(wn.frame) {
		if r := frames.Row(wn.frame, i); wn.marks[fmt.Sprint(r[field])] {
			out = append(out, r)
		}
	}
	return out
}

func (t *viewTab) focusGrid() (*grid, bool) {
	if t.focus == nil {
		return nil, false
	}
	g, ok := t.focus.st.(*grid)
	return g, ok
}

// tabsOf is the tabs widget that holds wn, else the first one showing.
func (t *viewTab) tabsOf(wn *wnode) *wnode {
	var found, first *wnode
	var walk func(n *wnode, tabs *wnode)
	walk = func(n *wnode, tabs *wnode) {
		if n == nil {
			return
		}
		if view.Kind(n.n) == "tabs" {
			tabs = n
			if first == nil {
				first = n
			}
		}
		if n == wn && tabs != nil {
			found = tabs
		}
		for _, k := range n.kids {
			walk(k, tabs)
		}
	}
	walk(t.root, nil)
	if found != nil {
		return found
	}
	return first
}

func (t *viewTab) moveFocus(d int) {
	if len(t.order) == 0 {
		return
	}
	i := -1
	for j, wn := range t.order {
		if wn == t.focus {
			i = j
		}
	}
	t.focus = t.order[(i+d+len(t.order))%len(t.order)]
}

// rowAction runs a table action bound to key: actions: [{key: R, action: x.retry, args: {id: "${row.id}"}}].
func (t *viewTab) rowAction(m *model, wn *wnode, key string) (tea.Cmd, bool) {
	acts, _ := wn.n.Props["actions"].([]any)
	for _, a := range acts {
		am, _ := a.(map[string]any)
		if fmt.Sprint(am["key"]) != key || unserved(t.eng, am) {
			continue
		}
		row := t.currentRow(wn)
		if c, ok := am["command"].(string); ok {
			for _, ref := range rowRefs.FindAllStringSubmatch(c, -1) {
				if v, has := row[ref[1]]; !has || fmt.Sprint(v) == "" || v == nil {
					m.setStatus(fmt.Sprintf("%s: this row has no %s", cmp.Or(fmt.Sprint(am["name"]), key), ref[1]), true)
					return nil, true
				}
			}
			label, line := fmt.Sprint(am["name"]), fmt.Sprint(expandRow(c, row))
			if danger, _ := am["danger"].(bool); danger {
				m.confirm = &confirm{text: label + ": rig " + line + "?", run: func() tea.Msg { return commandMsg(line) }}
				return nil, true
			}
			return m.commandAsking(label, line), true
		}
		name := fmt.Sprint(am["action"])
		args := protocol.Args{}
		if raw, ok := am["args"].(map[string]any); ok {
			for k, v := range raw {
				args[k] = expandRow(v, row)
			}
		}
		label := fmt.Sprint(am["name"])
		if label == "<nil>" {
			label = name
		}
		if p, ok := am["ai"].(string); ok {
			c := m.chatOpen()
			c.input.SetValue(fmt.Sprint(expandRow(p, row)))
			c.input.CursorEnd()
			return nil, true
		}
		marked := t.markedRows(wn)
		if len(marked) == 0 || !usesRow(am["args"]) {
			return t.fill(m, wn, label, args, row, func(a protocol.Args) tea.Cmd { return t.run(m, label, name, a) }), true
		}
		raw, _ := am["args"].(map[string]any)
		label += fmt.Sprintf(" (%d marked)", len(marked))
		return t.fill(m, wn, label, args, marked[0], func(a protocol.Args) tea.Cmd {
			list := make([]protocol.Args, len(marked))
			for i, r := range marked {
				list[i] = protocol.Args{}
				for k, v := range a {
					if rv, ok := raw[k].(string); ok && strings.Contains(rv, "${row.") {
						v = expandRow(rv, r)
					}
					list[i][k] = v
				}
			}
			clear(wn.marks)
			return t.run(m, label, name, list...)
		}), true
	}
	return nil, false
}

// fill asks for the args written ${ask} (an input; ${ask:field} starts from the row's field, to edit
// it) and ${pick:field} (one of the values the frame has in field: a board's columns), then runs done.
func (t *viewTab) fill(m *model, wn *wnode, label string, args protocol.Args, row map[string]any, done func(protocol.Args) tea.Cmd) tea.Cmd {
	if s := subject(row); s != "" && !strings.Contains(label, s) {
		label += " " + s
	}
	for _, k := range slices.Sorted(maps.Keys(args)) {
		v, _ := args[k].(string)
		set := func(val string) tea.Cmd {
			args[k] = val
			return t.fill(m, wn, label, args, row, done)
		}
		if f, ok := strings.CutPrefix(v, "${pick:"); ok && strings.HasSuffix(f, "}") {
			var items []string
			for _, x := range frames.Column(wn.frame, strings.TrimSuffix(f, "}")) {
				if s := fmt.Sprint(x); !slices.Contains(items, s) {
					items = append(items, s)
				}
			}
			m.pick(label+": "+k, items, nil, 0, false, func(c []string) tea.Cmd { return set(c[0]) })
			return nil
		}
		if v == "${ask}" || strings.HasPrefix(v, "${ask:") && strings.HasSuffix(v, "}") {
			init := ""
			if f := strings.TrimSuffix(strings.TrimPrefix(v, "${ask:"), "}"); f != v && row[f] != nil {
				init = fmt.Sprint(row[f])
			}
			m.ask(label+": "+k, init, set)
			if longText[k] || len(init) > 60 {
				m.asPopup()
			}
			return nil
		}
	}
	return done(args)
}

// longText are the args asked in a box over the screen rather than the footer's line.
var longText = map[string]bool{"body": true, "description": true, "text": true, "comment": true, "message": true}

// subject names the row an action asks about: its key or id and its title, so the prompt says which
// issue a comment goes to.
func subject(row map[string]any) string {
	id := ""
	for _, k := range []string{"key", "iid", "id", "name"} {
		if v, ok := row[k]; ok && v != nil && fmt.Sprint(v) != "" {
			id = fmt.Sprint(v)
			break
		}
	}
	for _, k := range []string{"title", "summary"} {
		if v, ok := row[k]; ok && v != nil && fmt.Sprint(v) != "" {
			return strings.TrimSpace(id + " " + truncate(fmt.Sprint(v), 50))
		}
	}
	return id
}

// run runs an action through the Engine, so policy, confirmation and audit apply.
func (t *viewTab) run(m *model, label, action string, list ...protocol.Args) tea.Cmd {
	e := m.eng()
	args := list[0]
	for _, a := range list {
		for k, v := range a {
			if s, ok := v.(string); ok && (strings.HasPrefix(s, "${ask") || strings.HasPrefix(s, "${pick:") || strings.Contains(s, "${row.")) {
				m.setStatus(fmt.Sprintf("%s not run: %s still reads %q", label, k, s), true)
				return nil
			}
		}
	}
	run := func(ctx context.Context) error {
		for _, args := range list {
			_, res, err := e.Run(ctx, action, args, api.RunOpts{})
			if err != nil {
				return err
			}
			if res != nil && res.Message != "" {
				program.Send(statusMsg{text: res.Message})
			}
		}
		return nil
	}
	if a, ok := e.Action(action); ok && a.Effect == protocol.EffectRead {
		return m.do(label, run)
	}
	refresh := func(ctx context.Context) error {
		err := run(ctx)
		if t.fetch != nil {
			t.fetch.Forget()
		}
		return err
	}
	// a plugin's danger (a push, a discard) is not undone by switching environments: always ask,
	// naming what it acts on
	danger := actionDangerous(e, action)
	if danger {
		for _, k := range slices.Sorted(maps.Keys(args)) {
			if s, ok := args[k].(string); ok && s != "" {
				label += " " + s
			}
		}
	}
	return m.actIf(danger || m.needsConfirm(false), label, refresh)
}

var rowRefs = regexp.MustCompile(`\$\{row\.([a-zA-Z0-9_]+)\}`)

// usesRow is args mentioning ${row.x}: only those can differ between marked rows.
func usesRow(args any) bool {
	m, _ := args.(map[string]any)
	for _, v := range m {
		if s, ok := v.(string); ok && strings.Contains(s, "${row.") {
			return true
		}
	}
	return false
}

// unserved is an action of a plugin not in use, so a view can offer gitlab.scm.open and
// github.scm.open on the same key.
func unserved(e *engine.Local, am map[string]any) bool {
	name, ok := am["action"].(string)
	if !ok || e == nil {
		return false
	}
	_, found := e.Action(name)
	return !found
}

func actionDangerous(e *engine.Local, name string) bool {
	a, ok := e.Action(name)
	return ok && a.Effect == protocol.EffectDanger
}

// expandRow fills ${row.col} from the selected row.
func expandRow(v any, row map[string]any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if strings.HasPrefix(s, "${row.") && strings.HasSuffix(s, "}") && strings.Count(s, "${") == 1 {
		return row[s[6:len(s)-1]]
	}
	for k, x := range row {
		s = strings.ReplaceAll(s, "${row."+k+"}", fmt.Sprint(x))
	}
	return s
}

func (t *viewTab) currentRow(wn *wnode) map[string]any {
	if wn.frame == nil {
		return nil
	}
	if b, ok := wn.st.(*boardState); ok {
		if b.cur < 0 || b.cur >= frames.Len(wn.frame) {
			return nil
		}
		return frames.Row(wn.frame, b.cur)
	}
	if g, ok := wn.st.(*grid); ok {
		if r, ok := g.current(); ok {
			if i, err := strconv.Atoi(r.id); err == nil && i < frames.Len(wn.frame) {
				return frames.Row(wn.frame, i)
			}
		}
	}
	if frames.Len(wn.frame) > 0 {
		return frames.Row(wn.frame, 0)
	}
	return nil
}

// hints lead with the focused widget's own actions: the footer shows only the first few.
func (t *viewTab) hints() [][2]string {
	var h [][2]string
	if t.needSecret {
		h = append(h, [2]string{"S", "set " + t.secrets[0]})
	}
	if t.focus != nil {
		acts, _ := t.focus.n.Props["actions"].([]any)
		for _, a := range acts {
			am, _ := a.(map[string]any)
			if !unserved(t.eng, am) {
				h = append(h, [2]string{fmt.Sprint(am["key"]), fmt.Sprint(firstNonNil(am["name"], am["action"]))})
			}
		}
		if t.focus.preview != nil {
			h = append(h, [2]string{"v", "details"})
		}
		switch view.Kind(t.focus.n) {
		case "table":
			h = append(h, [2]string{"enter", "open"}, [2]string{"/", "filter"}, [2]string{"y Y", "copy"})
			if markField(t.focus) != "" {
				h = append(h, [2]string{"space ctrl+a", "mark rows"})
			}
		case "board":
			h = append(h, [2]string{"/", "filter"}, [2]string{"< > alt+↑↓", "sort"})
		case "terminal":
			h = append(h, [2]string{"enter", "type into it"}, [2]string{"R", "restart"})
		case "form":
			h = append(h, [2]string{"enter", "edit"}, [2]string{"ctrl+s", "submit"})
		}
		if b := t.focus.n.Bind; b != nil && b.Args["query"] != nil {
			h = append(h, [2]string{"Q", "query"})
		}
	}
	if t.tabsOf(nil) != nil {
		h = append(h, [2]string{"ctrl+←→", "tabs"})
	}
	return append(h, [2]string{"←→", "focus"}, [2]string{"r", "refresh"})
}

func firstNonNil(v ...any) any {
	for _, x := range v {
		if x != nil {
			return x
		}
	}
	return nil
}

func (t *viewTab) view(m *model, w, h int) string {
	if t.err != "" {
		return sRed.Render(t.err)
	}
	if t.root == nil {
		return ""
	}
	t.order = t.order[:0]
	banner, y := "", 0
	if t.needSecret && h > 2 {
		banner, y, h = t.secretBanner(m, w)+"\n", 1, h-1
	}
	out := t.draw(m, t.root, 0, y, w, h)
	if t.focus == nil && len(t.order) > 0 {
		t.focus = t.order[0]
	}
	return banner + out
}

// draw lays wn out in exactly w×h cells at x, y of the body.
func (t *viewTab) draw(m *model, wn *wnode, x, y, w, h int) string {
	wn.x, wn.y, wn.w, wn.h = x, y, w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	switch view.Kind(wn.n) {
	case "grid", "stack":
		return t.stackDraw(m, wn.kids, x, y, w, h, true)
	case "row":
		return t.stackDraw(m, wn.kids, x, y, w, h, false)
	case "split":
		return t.stackDraw(m, wn.kids, x, y, w, h, view.Prop(wn.n, "dir") == "v" || view.Prop(wn.n, "dir") == "vertical")
	case "tabs":
		return t.tabsDraw(m, wn, x, y, w, h)
	case "box":
		if len(wn.kids) == 0 {
			return panel(view.Prop(wn.n, "title"), "", w, h, false)
		}
		return panel(view.Prop(wn.n, "title"), t.draw(m, wn.kids[0], x+1, y+1, w-2, h-2), w, h, false)
	case "scroll":
		return t.scrollDraw(m, wn, x, y, w, h)
	}
	if p := t.previewOf(wn); p != nil && !wn.previewOff && w >= 60 {
		lw := w * 45 / 100
		if pct, err := strconv.Atoi(strings.TrimSuffix(fmt.Sprint(p.n.Props["width"]), "%")); err == nil && pct > 0 && pct < 100 {
			lw = w - w*pct/100
		}
		lw = m.paneSize(fmt.Sprintf("view.%s.%d.preview", t.title, wn.id), splitGeo{start: x, total: w, minA: 20, minB: 20}, lw, x, y, h)
		left := t.drawLeaf(m, wn, x, y, lw, h)
		return lipgloss.JoinHorizontal(lipgloss.Top, left, t.drawLeaf(m, p, x+lw, y, w-lw, h))
	}
	return t.drawLeaf(m, wn, x, y, w, h)
}

func (t *viewTab) drawLeaf(m *model, wn *wnode, x, y, w, h int) string {
	wn.x, wn.y, wn.w, wn.h = x, y, w, h
	t.order = append(t.order, wn)
	focused := wn == t.focus
	m.zone(fmt.Sprintf("view:%d", wn.id), x, y, w, h)
	if b, ok := wn.n.Props["border"].(bool); ok && !b {
		return clip(t.leaf(m, wn, x, y, w, h, focused), w, h)
	}
	title := view.Prop(wn.n, "title")
	if wn.bound != "" {
		title = fmt.Sprint(expandRow(title, t.previewRow))
	}
	if term, ok := wn.st.(*terminal); ok && term.input {
		title += sDim.Render(" · esc leaves")
	}
	if wn.loading && wn.frame == nil {
		title += " " + sDim.Render("…")
	}
	return panel(title, t.leaf(m, wn, x+1, y+1, w-2, h-2, focused), w, h, focused)
}

// stackDraw splits w×h among kids: by height: or span: when given, else evenly.
func (t *viewTab) stackDraw(m *model, kids []*wnode, x, y, w, h int, vertical bool) string {
	if len(kids) == 0 {
		return ""
	}
	total := h
	if !vertical {
		total = w
	}
	sizes := shares(kids, total, vertical)
	off := 0
	for i := 1; i < len(kids); i++ {
		off += sizes[i-1]
		pair := sizes[i-1] + sizes[i]
		minPane := 8
		if vertical {
			minPane = 3
		}
		if pair < 2*minPane {
			continue
		}
		name := fmt.Sprintf("view.%s.%d", t.title, kids[i].id)
		g := splitGeo{total: pair, minA: minPane, minB: minPane, down: vertical}
		var first int
		if vertical {
			g.start = y + off - sizes[i-1]
			first = m.paneSize(name, g, sizes[i-1], x, y+off-sizes[i-1], w)
		} else {
			g.start = x + off - sizes[i-1]
			first = m.paneSize(name, g, sizes[i-1], x+off-sizes[i-1], y, h)
		}
		sizes[i-1], sizes[i] = first, pair-first
	}
	var parts []string
	off = 0
	for i, k := range kids {
		if vertical {
			parts = append(parts, t.draw(m, k, x, y+off, w, sizes[i]))
		} else {
			parts = append(parts, t.draw(m, k, x+off, y, sizes[i], h))
		}
		off += sizes[i]
	}
	if vertical {
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// shares gives each kid its fixed size (height: rows, or a percentage), then splits what is left
// by span: weights.
func shares(kids []*wnode, total int, vertical bool) []int {
	out := make([]int, len(kids))
	key := "width"
	if vertical {
		key = "height"
	}
	left, weights := total, 0
	for i, k := range kids {
		if n, ok := size(k, key, total); ok {
			out[i] = min(n, left)
			left -= out[i]
			continue
		}
		weights += span(k)
	}
	for i, k := range kids {
		if out[i] == 0 && weights > 0 {
			out[i] = left * span(k) / weights
		}
	}
	// the last flexible kid takes what rounding left
	used := 0
	for _, s := range out {
		used += s
	}
	for i := len(kids) - 1; i >= 0 && used < total; i-- {
		if _, fixed := size(kids[i], key, total); !fixed {
			out[i] += total - used
			break
		}
	}
	return out
}

func size(wn *wnode, key string, total int) (int, bool) {
	s := view.Prop(wn.n, key)
	if wn.n.Widget == "row" && len(wn.kids) > 0 && key == "height" {
		best := 0
		for _, k := range wn.kids {
			if n, ok := size(k, key, total); ok {
				best = max(best, n)
			}
		}
		return best, best > 0
	}
	if s == "" {
		return 0, false
	}
	if p, ok := strings.CutSuffix(s, "%"); ok {
		n, err := strconv.Atoi(p)
		return total * n / 100, err == nil
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

func span(wn *wnode) int {
	if n, err := strconv.Atoi(view.Prop(wn.n, "span")); err == nil && n > 0 {
		return n
	}
	return 1
}

func (t *viewTab) tabsDraw(m *model, wn *wnode, x, y, w, h int) string {
	var labels []string
	for i, k := range wn.kids {
		labels = append(labels, cmp.Or(view.Prop(k.n, "title"), strconv.Itoa(i+1)))
	}
	head := padRight(m.stripDrop(fmt.Sprintf("viewtab:%d", wn.id), x, y, labels, wn.tab, w), w)
	if wn.tab >= len(wn.kids) {
		return head
	}
	return head + "\n" + t.draw(m, wn.kids[wn.tab], x, y+1, w, h-1)
}

// scrollDraw draws the child taller (height:, else twice the box) and shows a window of it.
func (t *viewTab) scrollDraw(m *model, wn *wnode, x, y, w, h int) string {
	if len(wn.kids) == 0 {
		return ""
	}
	full, ok := size(wn, "height", h)
	if !ok || full < h {
		full = 2 * h
	}
	off, _ := wn.st.(int)
	off = max(0, min(off, full-h))
	wn.st = off
	t.order = append(t.order, wn)
	lines := strings.Split(t.draw(m, wn.kids[0], x, y-off, w, full), "\n")
	end := min(off+h, len(lines))
	return strings.Join(lines[min(off, end):end], "\n")
}

func (t *viewTab) click(m *model, h hit) tea.Cmd {
	return batch(t.clickAt(m, h), t.syncPreviews())
}

func (t *viewTab) clickAt(m *model, h hit) tea.Cmd {
	if h.id == secretZone && t.needSecret {
		return t.askSecret(m)
	}
	var id, sub int
	if n, _ := fmt.Sscanf(h.id, "viewtab:%d:%d", &id, &sub); n == 2 {
		var cmd tea.Cmd
		t.walkVisible(func(wn *wnode) {
			if wn.id == id && sub < len(wn.kids) && wn.tab != sub {
				t.hide(m)
				wn.tab, t.focus = sub, nil
				cmd = t.refresh(m)
			}
		})
		return cmd
	}
	if n, _ := fmt.Sscanf(h.id, "view:%d", &id); n == 1 {
		for _, wn := range t.order {
			if wn.id == id {
				t.focus = wn
				return t.widgetClick(m, wn, h)
			}
		}
	}
	if t.focus != nil {
		if g, ok := t.focus.st.(*grid); ok && strings.HasPrefix(h.id, g.id) {
			g.click(h)
		}
	}
	return nil
}

func (t *viewTab) wheel(m *model, z hit, up bool) (tea.Cmd, bool) {
	k := tea.KeyPressMsg{Code: tea.KeyDown}
	if up {
		k = tea.KeyPressMsg{Code: tea.KeyUp}
	}
	if t.focus == nil {
		return nil, false
	}
	if term, ok := t.focus.st.(*terminal); ok {
		term.scrollBy(map[bool]int{true: 3, false: -3}[up])
		return nil, true
	}
	return t.widgetKey(m, t.focus, k), true
}

// atRoot: esc has nothing left to close here.
func (t *viewTab) atRoot() bool { return true }

// addViews puts the project's views and the plugins' views with slot tab into all, keeping the
// ones already there (and their state), and returns them in order.
func addViews(a *engine.App, all map[string]tab) []tab {
	var out []tab
	var home []protocol.Node
	servedBy := map[*viewTab]string{}
	rank, ranks := 20, map[tab]int{}
	var owner *host.Manifest
	add := func(key, title string, v *view.View, dir, remote string) {
		if v.Slot == "home" {
			tile := v.Root
			tile.Props = maps.Clone(tile.Props)
			if tile.Props == nil {
				tile.Props = protocol.Args{}
			}
			tile.Props["title"] = cmp.Or(view.Prop(tile, "title"), v.Title, title)
			home = append(home, tile)
			return
		}
		if v.Slot == "command" && v.Command != "" {
			key = "cmd:" + v.Command
		} else if v.Slot != "tab" {
			return
		}
		t, ok := all[key].(*viewTab)
		if !ok {
			t = newViewTab(cmp.Or(v.Title, title), v, dir)
			t.remote = remote
			all[key] = t
		}
		if owner != nil {
			t.plugin, t.secrets = owner.Name, owner.Permissions.Secrets
		}
		if plugin, _, ok := strings.Cut(strings.TrimPrefix(key, "view:"), "/"); ok {
			t.title = cmp.Or(v.Title, title)
			servedBy[t] = plugin
		}
		if v.Slot == "command" {
			return
		}
		ranks[t] = rank
		out = append(out, t)
	}
	for _, n := range a.Spec.ViewOrder {
		if v := a.Spec.Views[n]; v != nil {
			add("view:"+n, n, v, a.Spec.Dir, "")
		}
	}
	ms, _ := host.Discover(a.Spec.Dir, a.Spec.Plugins)
	for _, m := range ms {
		if !m.Relevant(a.Spec.Dir, a.Spec.Plugins) {
			continue
		}
		showTabs := m.ShowsTab(a.Spec.Dir, a.Spec.Plugins)
		owner = m
		rank = viewRank(m, a.Spec.Dir)
		names := make([]string, 0, len(m.Views))
		for n := range m.Views {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			node := m.Views[n]
			var v view.View
			if err := a.Spec.Expanded(&node).Decode(&v); err != nil {
				v = view.View{Slot: "tab", Root: protocol.Node{Widget: "text", Props: protocol.Args{"text": err.Error()}}}
			}
			remote := ""
			if v.Root.Widget == "" && v.File == "" {
				remote = m.Name + "." + n
			}
			if !showTabs && v.Slot == "tab" {
				continue
			}
			add("view:"+m.Name+"."+n, n, &v, m.Dir, remote)
		}
		// a plugin with its own views has laid out what the contract views would show
		for _, id := range m.Implements {
			if len(names) > 0 || !showTabs {
				break
			}
			if c := contracts.Get(id); c != nil && c.View != nil {
				var v view.View
				if err := yaml.Unmarshal(c.View, &v); err != nil {
					panic(err)
				}
				served(&v.Root, m.Name)
				add("view:"+m.Name+"/"+c.ID, v.Title, &v, m.Dir, "")
			}
		}
	}
	// two plugins serving one contract (docs from files and from Confluence) are named after the plugins
	titles := map[string]int{}
	for t := range servedBy {
		titles[t.title]++
	}
	for t, plugin := range servedBy {
		if titles[t.title] > 1 {
			t.title = strings.ToUpper(plugin[:1]) + strings.ReplaceAll(plugin[1:], "-", " ")
		}
	}
	// Plugins is no tab: :plugins opens it for a while (pluginsScreen)
	if _, ok := all["view:Plugins"]; !ok {
		v := view.View{Slot: "tab"}
		if err := v.Load(pluginsView); err != nil {
			panic(err)
		}
		all["view:Plugins"] = newViewTab("Plugins", &v, a.Spec.Dir)
	}
	sort.SliceStable(out, func(i, j int) bool { return ranks[out[i]] < ranks[out[j]] })
	if len(home) > 0 {
		root := protocol.Node{Widget: "grid"}
		for i := 0; i < len(home); i += 2 {
			root.Children = append(root.Children, protocol.Node{Widget: "row", Children: home[i:min(i+2, len(home))]})
		}
		add("view:Home", "Home", &view.View{Title: "Home", Slot: "tab", Root: root}, a.Spec.Dir, "")
		out = append([]tab{out[len(out)-1]}, out[:len(out)-1]...)
	}
	return out
}

// viewRank orders the views: the dev loop's contracts (changes, merge requests, issues, docs), then
// the project's own, then what a user installed for themselves (games), git-ignored ones included.
func viewRank(m *host.Manifest, dir string) int {
	for i, c := range []string{"vcs", "scm", "tracker", "docs"} {
		for _, id := range m.Implements {
			if strings.HasPrefix(id, c+"/") {
				return 10 + i
			}
		}
	}
	rel, err := filepath.Rel(dir, m.Dir)
	if err != nil || strings.HasPrefix(rel, "..") || exec.Command("git", "-C", dir, "check-ignore", "-q", rel).Run() == nil {
		return 30
	}
	return 20
}

// served points a contract's standard view at the plugin serving it: scm.mrs becomes <plugin>.scm.mrs.
func served(root *protocol.Node, plugin string) {
	view.Walk(root, func(n *protocol.Node) {
		if n.Bind != nil && n.Bind.Source != "" {
			n.Bind.Source = plugin + "." + n.Bind.Source
		}
		acts, _ := n.Props["actions"].([]any)
		for _, a := range acts {
			if am, ok := a.(map[string]any); ok && am["action"] != nil {
				am["action"] = plugin + "." + fmt.Sprint(am["action"])
			}
		}
	})
}

// pluginsView lists the plugins in use; its keys run `rig plugin ...`.
var pluginsView = []byte(`
title: Plugins
widget: table
source: plugins
every: 5s
actions:
  - {key: i, name: info, command: "plugin info ${row.name}"}
  - {key: l, name: logs, command: "plugin logs ${row.name}"}
  - {key: s, name: search the registry, command: "plugin search ${ask}"}
  - {key: N, name: add, command: "plugin add ${ask}"}
  - {key: S, name: turn on, command: "plugin on ${row.name}"}
  - {key: X, name: turn off, command: "plugin off ${row.name}"}
  - {key: U, name: upgrade, command: "plugin up ${row.name}"}
  - {key: D, name: remove, command: "plugin rm ${row.name}", danger: true}
`)

// previewOf is wn's preview pane, made from its preview: prop on first use.
// filterFirst filters the focused table or board, else the first one showing.
func (t *viewTab) filterFirst(q string) {
	if t.root == nil {
		t.build()
	}
	target := t.focus
	if target == nil || !filterable[view.Kind(target.n)] {
		target = nil
		t.walkVisible(func(wn *wnode) {
			if target == nil && filterable[view.Kind(wn.n)] {
				target = wn
			}
		})
	}
	if target != nil {
		target.filter, t.focus = q, target
	}
}

// lookup finds words on this screen: its Search tab asks the server (an issue key, a title), else the
// shown table is filtered. Nothing found says so in the status bar.
func (t *viewTab) lookup(m *model, words string) tea.Cmd {
	if t.root == nil {
		t.build()
	}
	var tb, target *wnode
	t.walkAll(func(wn *wnode) {
		if view.Kind(wn.n) == "tabs" && tb == nil {
			tb = wn
		}
	})
	if tb != nil {
		for i, k := range tb.kids {
			if searchable(k) && (target == nil || strings.EqualFold(view.Prop(k.n, "title"), "search")) {
				tb.tab, target = i, k
			}
		}
	}
	if target == nil {
		t.filterFirst(words)
		return nil
	}
	t.focus, target.expect = target, words
	setQuery(target, words)
	return t.load(target)
}

// filterable are the widgets / filters.
var filterable = map[string]bool{"table": true, "board": true}

func (t *viewTab) previewOf(wn *wnode) *wnode {
	if wn.preview != nil {
		return wn.preview
	}
	raw, ok := wn.n.Props["preview"].(map[string]any)
	if !ok {
		return nil
	}
	raw = maps.Clone(raw)
	if raw["widget"] == nil {
		raw["widget"] = "markdown"
	}
	var y yaml.Node
	b, _ := yaml.Marshal(raw)
	if yaml.Unmarshal(b, &y) != nil || len(y.Content) == 0 {
		return nil
	}
	n, err := view.Parse(y.Content[0])
	if err != nil || n.Bind == nil {
		return nil
	}
	wn.preview = &wnode{n: n, id: 1<<20 + wn.id, bound: "-", tpl: *n.Bind}
	wn.previewOff, _ = n.Props["hidden"].(bool)
	wn.preview.n.Bind = nil
	return wn.preview
}

// syncPreviews points each showing preview pane at its table's selected row and fetches it.
func (t *viewTab) syncPreviews() tea.Cmd {
	var cmds []tea.Cmd
	t.walkVisible(func(wn *wnode) {
		p := t.previewOf(wn)
		row := t.currentRow(wn)
		if p == nil || row == nil {
			return
		}
		b := protocol.Binding{Source: p.tpl.Source, Every: p.tpl.Every, Args: protocol.Args{}}
		for k, v := range p.tpl.Args {
			b.Args[k] = expandRow(v, row)
		}
		if key := view.Key(b); key != p.bound {
			t.previewRow = row
			p.n.Bind, p.bound, p.frame, p.err, p.st = &b, key, nil, nil, nil
			cmds = append(cmds, t.load(p))
		}
	})
	return batch(cmds...)
}

// aiContext is the focused widget's source and selected row, and the preview beside it.
func (t *viewTab) aiContext(m *model) string {
	wn := t.focus
	if wn == nil {
		return ""
	}
	var b strings.Builder
	if wn.n.Bind != nil {
		fmt.Fprintf(&b, "widget %s from source %s %v\n", view.Kind(wn.n), wn.n.Bind.Source, wn.n.Bind.Args)
	}
	if row := t.currentRow(wn); row != nil {
		raw, _ := json.Marshal(row)
		fmt.Fprintf(&b, "selected row: %s\n", raw)
	}
	if p := wn.preview; p != nil && p.frame != nil && !wn.previewOff {
		raw, _ := json.Marshal(frames.Rows(p.frame))
		fmt.Fprintf(&b, "its details (%s): %s\n", p.tpl.Source, raw)
	}
	return b.String()
}

// askFilter turns "@?words" typed in a widget's filter into a filter the AI writes in a prompt.
func (t *viewTab) askFilter(m *model, wn *wnode) tea.Cmd {
	typed := wn.filter
	t.filtering, wn.filter = false, ""
	var cols []string
	var rows [][]string
	if f := wn.frame; f != nil {
		for _, fl := range f.Fields {
			cols = append(cols, fl.Name)
		}
		for r := range min(frames.Len(f), 30) {
			row := make([]string, len(f.Fields))
			for i, fl := range f.Fields {
				row[i] = frames.Format(fl, f.Columns[i][r])
			}
			rows = append(rows, row)
		}
	}
	hint := rowFilterHint + "; the columns: " + strings.Join(cols, ", ") + within("first rows", sampleRows(rows, 30))
	m.askChecked("filter: words, *glob*, or column=value column!=value column~text", typed, hint, keepsSome(cols, rows), func(v string) tea.Cmd {
		wn.filter = strings.TrimSpace(v)
		return nil
	})
	return m.completeLater()
}
