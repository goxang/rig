package tui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

// dataTab walks the data components: queues (depth trend, consumers, rates), and databases and
// caches as trees (databases › objects › rows; caches › dbs › keys › value). Left picks the
// component, right walks it; / filters what the right side lists.
type dataTab struct {
	comps []dataComp
	left  *grid
	right *grid
	focus int // 0 the components, 1 what the selected one shows

	queues   map[string][]core.Queue
	queueErr map[string]error
	depth    map[string][]float64

	paths   map[string][]string // per component, where its walk stands
	table   core.Table
	leaf    bool
	loading bool
	err     error
	seq     int

	filter   string
	restored map[string][]string
	// picked is the row last opened at each place of a walk, so going back lands on it; restore
	// is the one the next fill selects
	picked  map[string]string
	restore string
	// query is what Q ran at queryAt, shown instead of the walk until esc
	query   string
	queryAt []string
	// marked rows of the walk (grid ids) that D deletes; changing is the label of a change in flight,
	// whose status reloads the walk
	marked   map[string]bool
	changing string

	// qmarked are the queues space marked, for P and D; qview is the messaging strip's list (0 the
	// queues, else brokerViews[qview-1]) and qtable what it lists
	qmarked map[string]bool
	qview   int
	qtable  core.Table
	// detail is the selected queue's settings or peeked messages (focus 2); payload one message's
	// body field by field
	detail      *grid
	detailFor   string
	detailKind  string
	detailTable core.Table
	payload     *jsonTree
}

// brokerViews are the other lists of a RabbitMQ component, as management API queries.
var brokerViews = [][2]string{
	{"exchanges", "exchanges name type durable auto_delete internal message_stats.publish_in_details.rate message_stats.publish_out_details.rate"},
	{"bindings", "bindings source destination destination_type routing_key"},
	{"connections", "connections name user state channels client_properties.connection_name recv_oct_details.rate send_oct_details.rate"},
	{"channels", "channels name user state consumer_count prefetch_count messages_unacknowledged"},
	{"consumers", "consumers queue.name consumer_tag channel_details.connection_name prefetch_count ack_required active"},
}

type queueDetailMsg struct {
	gen   int
	queue string
	kind  string
	t     core.Table
	err   error
}

type brokerViewMsg struct {
	gen, view int
	t         core.Table
	err       error
}

type dataComp struct {
	name, kind, adapter string
	browse, edit        bool
}

type dataCompsMsg struct {
	gen   int
	comps []dataComp
}

type queuesMsg struct {
	gen    int
	queues map[string][]core.Queue
	errs   map[string]error
}

type browseMsg struct {
	gen, seq int
	t        core.Table
	leaf     bool
	err      error
}

func (t *dataTab) name() string { return "Data" }
func (t *dataTab) typing() bool { return false }
func (t *dataTab) hints() [][2]string {
	c := t.current()
	switch {
	case t.focus == 0:
		return [][2]string{{"↑↓", "component"}, {"enter →", "open"}}
	case t.focus == 2 && t.payload != nil:
		return [][2]string{{"↑↓", "move"}, {"←→ space", "fold"}, {"y", "copy value"}, {"esc", "back to messages"}}
	case t.focus == 2:
		return [][2]string{{"↑↓", "move"}, {"enter", "message body as JSON"}, {"m", "peek messages"}, {"i", "queue info"}, {"y", "copy row"}, {"esc", "back to queues"}}
	case c.kind == string(core.KindMessaging) && t.qview > 0:
		return [][2]string{{"⇧←→", "queues, exchanges, bindings, ..."}, {"/", "filter"}, {"y Y", "copy row, all"}, {"< >", "sort"}, {"esc ←", "components"}}
	case c.kind == string(core.KindMessaging):
		return [][2]string{{"enter", "queue details"}, {"m", "peek messages"}, {"space a", "mark, mark all"}, {"P", "purge marked/selected"}, {"X", "purge every queue shown"},
			{"D", "delete marked/selected"}, {"p", "publish to queue"}, {"⇧←→", "exchanges, bindings, connections, ..."}, {"/", "filter (*word*)"}, {"< >", "sort"}, {"esc ←", "components"}}
	}
	if t.query != "" {
		return [][2]string{{"esc Q", "edit the query (esc again: back)"}, {"y Y", "copy row, all"}, {"/", "filter (*word*)"}, {"< >", "sort"}}
	}
	h := [][2]string{{"enter →", "open"}, {"Q", "query here (on a row: that row)"}, {"y Y", "copy row, all"}, {"esc ←", "up"}, {"/", "filter (*word*)"}, {"r", "reload"}, {"< >", "sort"}}
	if t.editable(c) {
		h = append([][2]string{{"e", "edit cell"}, {"space", "mark"}, {"D", "delete"}}, h...)
	}
	return h
}

// editable is whether D and e work on what the right side shows: rows of a walk (not a query's) of
// a component that can change them.
func (t *dataTab) editable(c dataComp) bool {
	return c.edit && t.query == "" && !t.loading && t.err == nil && len(t.paths[c.name]) > 0
}

// rowIndex is a walk row's index into t.table, from its grid id.
func rowIndex(id string) int {
	n, _ := strconv.Atoi(strings.SplitN(id, "\x00", 2)[0])
	return n
}

// del deletes the marked rows, else the selected one.
func (t *dataTab) del(m *model, c dataComp) tea.Cmd {
	var rows []int
	for _, r := range t.right.rows {
		if t.marked[r.id] {
			rows = append(rows, rowIndex(r.id))
		}
	}
	if len(rows) == 0 {
		r, ok := t.right.current()
		if !ok {
			return nil
		}
		rows = []int{rowIndex(r.id)}
	}
	what := fmt.Sprintf("%d rows", len(rows))
	if len(rows) == 1 {
		what = t.table.Rows[rows[0]][0]
	}
	return t.change(m, c, "delete "+what+" from "+strings.Join(t.paths[c.name], " › "), true, func(ctx context.Context, e core.Editor, path []string, tb core.Table) error {
		return e.Delete(ctx, path, tb, rows)
	})
}

// edit asks for a new value of one cell of the selected row: the column first when there are several.
func (t *dataTab) edit(m *model, c dataComp) {
	r, ok := t.right.current()
	if !ok {
		return
	}
	row := rowIndex(r.id)
	ask := func(col int) tea.Cmd {
		name := t.table.Columns[col]
		m.ask(name+" of "+truncate(t.table.Rows[row][0], 40), t.table.Rows[row][col], func(v string) tea.Cmd {
			if v == t.table.Rows[row][col] {
				return nil
			}
			return t.change(m, c, "set "+name, false, func(ctx context.Context, e core.Editor, path []string, tb core.Table) error {
				return e.Set(ctx, path, tb, row, col, v)
			})
		})
		return nil
	}
	if len(t.table.Columns) == 1 {
		ask(0)
		return
	}
	var desc []string
	for _, v := range t.table.Rows[row] {
		desc = append(desc, truncate(printable(v), 60))
	}
	m.pick("edit which column", t.table.Columns, desc, 0, false, func(ch []string) tea.Cmd {
		if len(ch) > 0 {
			return ask(index(t.table.Columns, ch[0]))
		}
		return nil
	})
}

// changed reloads the walk when the status of the change it started arrives.
func (t *dataTab) changed(m *model, status string) tea.Cmd {
	if t.changing == "" || !strings.HasPrefix(status, t.changing) {
		return nil
	}
	t.changing = ""
	return t.load(m)
}

func (t *dataTab) change(m *model, c dataComp, label string, dangerous bool, f func(context.Context, core.Editor, []string, core.Table) error) tea.Cmd {
	a, path, tb := m.app, append([]string{}, t.paths[c.name]...), t.table
	t.changing = label
	return m.act(label, dangerous, func(ctx context.Context) error {
		v, err := a.Component(c.name)
		if err != nil {
			return err
		}
		return f(ctx, v.(core.Editor), path, tb)
	})
}

func index(xs []string, x string) int {
	for i, s := range xs {
		if s == x {
			return i
		}
	}
	return 0
}
func (t *dataTab) interval() time.Duration { return 5 * time.Second }

func (t *dataTab) open(m *model) tea.Cmd {
	t.depth, t.paths = map[string][]float64{}, map[string][]string{}
	if t.restored != nil {
		t.paths = t.restored
	}
	t.left = newGrid("dcomp", col("COMPONENT", 0), col("KIND", 9))
	t.right = newGrid("dright")
	a, gen := m.app, m.gen
	return func() tea.Msg {
		var out []dataComp
		for _, n := range engine.SortedKeys(a.Spec.Components) {
			k, typ, err := a.Kind(n)
			if err != nil || (k != core.KindMessaging && k != core.KindDatabase && k != core.KindCache) {
				continue
			}
			c := dataComp{name: n, kind: string(k), adapter: typ}
			if v, err := a.Component(n); err == nil {
				_, c.browse = v.(core.Browser)
				_, c.edit = v.(core.Editor)
			}
			out = append(out, c)
		}
		return dataCompsMsg{gen: gen, comps: out}
	}
}

func (t *dataTab) current() dataComp {
	if t.left == nil {
		return dataComp{}
	}
	if r, ok := t.left.current(); ok {
		for _, c := range t.comps {
			if c.name == r.id {
				return c
			}
		}
	}
	return dataComp{}
}

func (t *dataTab) refresh(m *model) tea.Cmd {
	c := t.current()
	switch {
	case c.kind == string(core.KindMessaging) && t.qview > 0:
		return t.loadView(m)
	case c.kind == string(core.KindMessaging):
		return t.loadQueues(m)
	case t.leaf && len(t.paths[c.name]) > 0 && t.paths[c.name][len(t.paths[c.name])-1] == "running":
		return t.load(m)
	}
	return nil
}

func (t *dataTab) loadQueues(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.work()
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		qs, errs := engine.All[core.Messaging](a, core.KindMessaging)
		out := map[string][]core.Queue{}
		for n, q := range qs {
			var err error
			if out[n], err = q.Queues(c); err != nil {
				errs[n] = err
			}
		}
		return queuesMsg{gen: gen, queues: out, errs: errs}
	}
}

func (t *dataTab) loadView(m *model) tea.Cmd {
	a, gen, ctx, comp, view := m.app, m.gen, m.work(), t.current().name, t.qview
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		v, err := a.Component(comp)
		if err != nil {
			return brokerViewMsg{gen: gen, view: view, err: err}
		}
		q, ok := v.(core.Querier)
		if !ok {
			return brokerViewMsg{gen: gen, view: view, err: fmt.Errorf("%s has no other lists", comp)}
		}
		tb, err := q.RunQuery(c, brokerViews[view-1][1])
		return brokerViewMsg{gen: gen, view: view, t: tb, err: err}
	}
}

// inspector is the selected messaging component when it can show a queue in depth.
func (t *dataTab) inspector(m *model) (core.QueueInspector, bool) {
	v, err := m.app.Component(t.current().name)
	if err != nil {
		return nil, false
	}
	qi, ok := v.(core.QueueInspector)
	return qi, ok
}

func (t *dataTab) loadDetail(m *model, queue, kind string) tea.Cmd {
	qi, ok := t.inspector(m)
	if !ok {
		m.setStatus(t.current().adapter+" cannot show a queue in depth", true)
		return nil
	}
	if t.detailFor != queue || t.detailKind != kind {
		t.detail, t.payload = nil, nil
	}
	t.detailFor, t.detailKind, t.focus = queue, kind, 2
	gen, ctx := m.gen, m.work()
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var tb core.Table
		var err error
		if kind == "messages" {
			tb, err = qi.Peek(c, queue, 20)
		} else {
			tb, err = qi.QueueInfo(c, queue)
		}
		return queueDetailMsg{gen: gen, queue: queue, kind: kind, t: tb, err: err}
	}
}

// queueTargets are the marked queues, else the selected one.
func (t *dataTab) queueTargets() []string {
	var out []string
	for _, r := range t.right.rows {
		if t.qmarked[r.id] {
			out = append(out, r.id)
		}
	}
	if len(out) == 0 {
		if r, ok := t.right.current(); ok {
			out = []string{r.id}
		}
	}
	return out
}

func (t *dataTab) queueAct(m *model, verb string, names []string, f func(ctx context.Context, mq core.Messaging, q string) error) tea.Cmd {
	if len(names) == 0 {
		return nil
	}
	a, comp := m.app, t.current().name
	what := names[0]
	if len(names) > 1 {
		what = fmt.Sprintf("%d queues", len(names))
	}
	t.qmarked = nil
	return m.act(verb+" "+what, true, func(ctx context.Context) error {
		mq, _, err := engine.Get[core.Messaging](a, core.KindMessaging, comp)
		if err != nil {
			return err
		}
		return each(names, func(q string) error { return f(ctx, mq, q) })
	})
}

// load walks the selected component to its current path.
func (t *dataTab) load(m *model) tea.Cmd {
	c := t.current()
	if !c.browse {
		return nil
	}
	t.seq++
	t.loading, t.err, t.marked = true, nil, nil
	a, gen, seq, ctx, path := m.app, m.gen, t.seq, m.work(), append([]string{}, t.paths[c.name]...)
	query, queryAt := t.query, t.queryAt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		v, err := a.Component(c.name)
		if err != nil {
			return browseMsg{gen: gen, seq: seq, err: err}
		}
		if query != "" {
			pq, ok := v.(core.PathQuerier)
			if !ok {
				return browseMsg{gen: gen, seq: seq, leaf: true, err: fmt.Errorf("%s cannot run queries", c.name)}
			}
			tb, err := pq.QueryAt(ctx, queryAt, query)
			return browseMsg{gen: gen, seq: seq, t: tb, leaf: true, err: err}
		}
		tb, leaf, err := v.(core.Browser).Browse(ctx, path)
		return browseMsg{gen: gen, seq: seq, t: tb, leaf: leaf, err: err}
	}
}

func (t *dataTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case dataCompsMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.comps = msg.comps
		var rows []grow
		for _, c := range t.comps {
			rows = append(rows, grow{id: c.name, cells: []string{c.name, sDim.Render(c.kind)}})
		}
		t.left.set(rows)
		return t.show(m)
	case queuesMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.queues, t.queueErr = msg.queues, msg.errs
		for comp, qs := range msg.queues {
			for _, q := range qs {
				k := comp + "/" + q.Name
				t.depth[k] = last(append(t.depth[k], float64(q.Messages)), 60)
			}
		}
		if t.current().kind == string(core.KindMessaging) {
			t.fill()
		}
	case brokerViewMsg:
		if msg.gen != m.gen || msg.view != t.qview {
			return nil
		}
		t.qtable, t.err = msg.t, msg.err
		t.fill()
	case queueDetailMsg:
		if msg.gen != m.gen || msg.queue != t.detailFor || msg.kind != t.detailKind {
			return nil
		}
		if msg.err != nil {
			m.setStatus(msg.queue+": "+msg.err.Error(), true)
			return nil
		}
		t.detailTable = msg.t
		cols := []gcol{col("FIELD", 34), col("VALUE", 0)}
		if msg.kind == "messages" {
			cols = []gcol{rcol("#", 3), col("EXCHANGE", 16), col("ROUTING KEY", 24), col("REDELIV", 7), col("PROPERTIES", 24), col("PAYLOAD", 0)}
		}
		if t.detail == nil {
			t.detail = newGrid("dqd", cols...)
		}
		var rows []grow
		for i, r := range msg.t.Rows {
			rows = append(rows, grow{id: strconv.Itoa(i), cells: r})
		}
		t.detail.set(rows)
	case suggestMsg:
		if msg.gen == m.gen && m.prompt == nil && msg.comp == t.current().name {
			m.setStatus("", false)
			t.askQuery(m, t.current(), msg.at, "", msg.text)
		}
	case browseMsg:
		if msg.gen != m.gen || msg.seq != t.seq {
			return nil
		}
		t.loading, t.table, t.leaf, t.err = false, msg.t, msg.leaf, msg.err
		t.fill()
	case tea.KeyMsg:
		return t.key(m, msg)
	}
	return nil
}

// show switches the right side to the selected component.
func (t *dataTab) show(m *model) tea.Cmd {
	t.table, t.err, t.leaf, t.loading, t.query = core.Table{}, nil, false, false, ""
	t.right = newGrid("dright")
	t.detail, t.detailFor, t.payload, t.qmarked, t.qtable = nil, "", nil, nil, core.Table{}
	if t.current().kind == string(core.KindMessaging) && t.qview > 0 {
		t.fill()
		return t.loadView(m)
	}
	if t.current().kind == string(core.KindMessaging) {
		t.right = newGrid("dright", col("QUEUE", 0), rcol("DEPTH", 8), col("TREND", 16), rcol("UNACKED", 8), rcol("CONS", 5), rcol("IN", 8), rcol("OUT", 8))
		t.right.sortBy, t.right.desc = 1, true
		t.right.simple = []int{0, 1, 4}
		t.fill()
		return t.loadQueues(m)
	}
	return t.load(m)
}

// fill puts what the selected component holds, filtered, into the right grid.
func (t *dataTab) fill() {
	c := t.current()
	match := globMatcher(t.filter)
	tbl := t.table
	if c.kind == string(core.KindMessaging) && t.qview > 0 {
		tbl = t.qtable
	}
	if c.kind == string(core.KindMessaging) && t.qview == 0 {
		var rows []grow
		for _, q := range t.queues[c.name] {
			if !match(q.Name) {
				continue
			}
			name := q.Name
			if t.qmarked[q.Name] {
				name = sGreen.Render("● ") + name
			}
			depth := strconv.Itoa(q.Messages)
			if q.Messages > 0 {
				depth = sAmber.Render(depth)
			}
			cons := strconv.Itoa(q.Consumers)
			if q.Consumers == 0 {
				cons = sRed.Render("0")
			}
			rows = append(rows, grow{id: q.Name, cells: []string{name, depth, viz.Sparkline(t.depth[c.name+"/"+q.Name], 16, viz.Palette[1]), strconv.Itoa(q.Unacked), cons, viz.Human(q.InRate, "/s"), viz.Human(q.OutRate, "/s")},
				keys: []any{q.Name, float64(q.Messages), float64(q.Messages), float64(q.Unacked), float64(q.Consumers), q.InRate, q.OutRate}})
		}
		t.right.set(rows)
		return
	}
	ws := colWidths(tbl, 0)
	var cols []gcol
	for i, name := range tbl.Columns {
		cols = append(cols, col(name, min(max(ws[i], 4), 60)))
	}
	if len(cols) > 0 {
		cols[len(cols)-1].width = 0
	}
	if len(cols) != len(t.right.cols) {
		t.right = newGrid("dright", cols...)
	} else {
		t.right.cols = cols
	}
	var rows []grow
	for i, r := range tbl.Rows {
		if len(r) == 0 || !match(r[0]) {
			continue
		}
		id := strconv.Itoa(i) + "\x00" + r[0]
		if t.marked[id] {
			r = append([]string{sGreen.Render("● ") + r[0]}, r[1:]...)
		}
		rows = append(rows, grow{id: id, cells: r})
	}
	t.right.set(rows)
	if t.restore != "" && !t.loading {
		for i, r := range t.right.rows {
			if strings.HasSuffix(r.id, "\x00"+t.restore) {
				t.right.sel = i
			}
		}
		t.restore = ""
	}
}

// place names where c's walk stands.
func (t *dataTab) place(c dataComp) string {
	return c.name + "\x00" + strings.Join(t.paths[c.name], "\x00")
}

// remember keeps the selected row of the current place for the way back.
func (t *dataTab) remember(c dataComp) {
	r, ok := t.right.current()
	if !ok {
		return
	}
	if t.picked == nil {
		t.picked = map[string]string{}
	}
	_, child, _ := strings.Cut(r.id, "\x00")
	t.picked[t.place(c)] = child
}

func (t *dataTab) key(m *model, k tea.KeyMsg) tea.Cmd {
	c := t.current()
	if t.focus == 0 {
		if t.left.key(k) {
			if t.current().name != c.name {
				t.filter = ""
				return t.show(m)
			}
			return nil
		}
		switch k.String() {
		case "enter", "right", "l":
			t.focus = 1
		}
		return nil
	}
	if t.focus == 2 {
		return t.detailKey(m, k)
	}
	if t.right.key(k) {
		return nil
	}
	if c.kind == string(core.KindMessaging) {
		if cmd, ok := t.queueKey(m, k); ok {
			return cmd
		}
	}
	switch k.String() {
	case "/":
		m.ask("filter (text, or a glob like *word*)", t.filter, func(v string) tea.Cmd {
			t.filter = strings.TrimSpace(v)
			t.fill()
			return nil
		})
	case "r":
		if c.kind == string(core.KindMessaging) {
			return t.loadQueues(m)
		}
		return t.load(m)
	case "enter", "right", "l":
		r, ok := t.right.current()
		if !ok || !c.browse || t.leaf || t.loading {
			return nil
		}
		_, child, _ := strings.Cut(r.id, "\x00")
		t.remember(c)
		t.paths[c.name] = append(t.paths[c.name], child)
		t.filter = ""
		t.right = newGrid("dright")
		return t.load(m)
	case "esc", "left", "h", "backspace":
		switch {
		case t.query != "":
			t.askQuery(m, c, t.queryAt, t.query, "")
			if m.prompt != nil {
				// a second esc leaves the query for the walk it ran on
				m.prompt.escape = func() tea.Cmd {
					t.query, t.filter = "", ""
					t.right = newGrid("dright")
					t.restore = t.picked[t.place(c)]
					return t.load(m)
				}
			}
		case t.filter != "":
			t.filter = ""
			t.fill()
		case len(t.paths[c.name]) > 0:
			p := t.paths[c.name]
			t.paths[c.name] = p[:len(p)-1]
			t.right = newGrid("dright")
			t.restore = t.picked[t.place(c)]
			return t.load(m)
		default:
			t.focus = 0
		}
	case "Q":
		if !c.browse || t.loading {
			return nil
		}
		at := append([]string{}, t.paths[c.name]...)
		if r, ok := t.right.current(); ok && !t.leaf {
			_, child, _ := strings.Cut(r.id, "\x00")
			at = append(at, child)
		}
		if t.query != "" {
			at = t.queryAt
		} else {
			t.remember(c)
		}
		if t.query != "" {
			t.askQuery(m, c, at, t.query, "")
			return nil
		}
		v, err := m.app.Component(c.name)
		pq, ok := v.(core.PathQuerier)
		if err != nil || !ok {
			t.askQuery(m, c, at, "", "")
			return nil
		}
		gen, ctx := m.gen, m.work()
		rq, _ := v.(core.RowQuerier)
		row, tb := -1, t.table
		if r, ok := t.right.current(); ok && t.leaf && rq != nil {
			row = rowIndex(r.id)
		}
		m.setStatus("preparing a query for "+strings.Join(at, " › ")+"…", false)
		return func() tea.Msg {
			c2, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if row >= 0 {
				if q := rq.SuggestRowQuery(c2, at, tb, row); q != "" {
					return suggestMsg{gen: gen, comp: c.name, at: at, text: q}
				}
			}
			return suggestMsg{gen: gen, comp: c.name, at: at, text: pq.SuggestQuery(c2, at)}
		}
	case "y", "Y":
		tbl := t.table
		if c.kind == string(core.KindMessaging) {
			if t.qview == 0 {
				return nil
			}
			tbl = t.qtable
		}
		rows := tbl.Rows
		if r, ok := t.right.current(); ok && k.String() == "y" {
			rows = [][]string{tbl.Rows[rowIndex(r.id)]}
		}
		copyText(tsv(tbl.Columns, rows))
		m.setStatus(fmt.Sprintf("copied %d rows (tab-separated, with the header)", len(rows)), false)
	case " ":
		if r, ok := t.right.current(); ok && t.editable(c) {
			if t.marked == nil {
				t.marked = map[string]bool{}
			}
			t.marked[r.id] = !t.marked[r.id]
			t.fill()
			t.right.key(tea.KeyMsg{Type: tea.KeyDown})
		}
	case "D":
		if t.editable(c) {
			return t.del(m, c)
		}
		if c.kind != string(core.KindMessaging) {
			m.setStatus("nothing to delete here: open a table or a key", true)
		}
	case "e":
		if t.editable(c) {
			t.edit(m, c)
		}
	}
	return nil
}

// queueKey handles the messaging keys: the strip of lists, and on the queue list details, peek,
// marks, purge, delete and publish.
func (t *dataTab) queueKey(m *model, k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "[", "]", "shift+left", "shift+right":
		if t.current().adapter != "rabbitmq" {
			return nil, true
		}
		n := len(brokerViews) + 1
		if s := k.String(); s == "]" || s == "shift+right" {
			t.qview = (t.qview + 1) % n
		} else {
			t.qview = (t.qview + n - 1) % n
		}
		return t.switchView(m), true
	}
	if t.qview > 0 {
		return nil, false
	}
	r, ok := t.right.current()
	switch k.String() {
	case "enter", "right", "l", "i":
		if ok {
			return t.loadDetail(m, r.id, "info"), true
		}
	case "m":
		if ok {
			return t.loadDetail(m, r.id, "messages"), true
		}
	case " ":
		if ok {
			if t.qmarked == nil {
				t.qmarked = map[string]bool{}
			}
			t.qmarked[r.id] = !t.qmarked[r.id]
			t.fill()
			t.right.key(tea.KeyMsg{Type: tea.KeyDown})
		}
	case "a":
		all := len(t.qmarked) < len(t.right.rows)
		t.qmarked = map[string]bool{}
		for _, r := range t.right.rows {
			t.qmarked[r.id] = all
		}
		t.fill()
	case "P":
		return t.queueAct(m, "purge", t.queueTargets(), func(ctx context.Context, mq core.Messaging, q string) error { return mq.Purge(ctx, q) }), true
	case "X":
		var names []string
		for _, q := range t.queues[t.current().name] {
			if q.Messages > 0 && globMatcher(t.filter)(q.Name) {
				names = append(names, q.Name)
			}
		}
		if len(names) == 0 {
			m.setStatus("no queue shown has messages", false)
			return nil, true
		}
		return t.queueAct(m, "purge", names, func(ctx context.Context, mq core.Messaging, q string) error { return mq.Purge(ctx, q) }), true
	case "D":
		if _, ok := t.inspector(m); !ok {
			m.setStatus(t.current().adapter+" cannot delete queues", true)
			return nil, true
		}
		return t.queueAct(m, "delete queue", t.queueTargets(), func(ctx context.Context, mq core.Messaging, q string) error {
			return mq.(core.QueueInspector).DeleteQueue(ctx, q)
		}), true
	case "p":
		if !ok {
			return nil, true
		}
		q, a, comp := r.id, m.app, t.current().name
		m.ask("publish to "+q+" (body)", "", func(body string) tea.Cmd {
			return m.act("publish to "+q, false, func(ctx context.Context) error {
				mq, _, err := engine.Get[core.Messaging](a, core.KindMessaging, comp)
				if err != nil {
					return err
				}
				return mq.Publish(ctx, q, []byte(body))
			})
		})
	default:
		return nil, false
	}
	return nil, true
}

func (t *dataTab) switchView(m *model) tea.Cmd {
	t.filter, t.err = "", nil
	t.detail, t.detailFor, t.payload = nil, "", nil
	if t.qview == 0 {
		return t.show(m)
	}
	t.right, t.qtable = newGrid("dright"), core.Table{}
	return t.loadView(m)
}

func (t *dataTab) detailKey(m *model, k tea.KeyMsg) tea.Cmd {
	if t.payload != nil {
		switch k.String() {
		case "esc", "q":
			t.payload = nil
		case "y":
			copyText(t.payload.current().text())
			m.setStatus("copied "+t.payload.current().path(), false)
		default:
			t.payload.key(k)
		}
		return nil
	}
	if t.detail == nil {
		t.focus = 1
		return nil
	}
	if t.detail.key(k) {
		return nil
	}
	r, ok := t.detail.current()
	switch k.String() {
	case "esc", "left", "h":
		t.focus = 1
	case "m":
		return t.loadDetail(m, t.detailFor, "messages")
	case "i":
		return t.loadDetail(m, t.detailFor, "info")
	case "r":
		return t.loadDetail(m, t.detailFor, t.detailKind)
	case "y":
		if ok {
			copyText(tsv(t.detailTable.Columns, [][]string{t.detailTable.Rows[rowIndex(r.id)]}))
			m.setStatus("copied the row", false)
		}
	case "enter":
		if ok && t.detailKind == "messages" {
			row := t.detailTable.Rows[rowIndex(r.id)]
			tree, err := newJSONTree("dqd:payload", []byte(row[len(row)-1]))
			if err != nil {
				m.setStatus("that body is not JSON; y copies it", true)
				return nil
			}
			t.payload = tree
		}
	}
	return nil
}

type suggestMsg struct {
	gen  int
	comp string
	at   []string
	text string
}

// askQuery asks for a query to run at a walk position, editing value or offering template; the AI
// completes it knowing where it runs and what the screen lists there.
func (t *dataTab) askQuery(m *model, c dataComp, at []string, value, template string) {
	hint := fmt.Sprintf("a %s query (%s adapter) on component %s, at %s", c.kind, c.adapter, c.name, strings.Join(at, " › "))
	if len(t.table.Columns) > 0 {
		names := []string{}
		for i, r := range t.table.Rows {
			if i == 60 {
				break
			}
			names = append(names, truncate(r[0], 60))
		}
		hint += "; the screen lists " + strings.Join(t.table.Columns, ", ") + ": " + strings.Join(names, ", ")
	}
	run := func(v string) tea.Cmd {
		if v = strings.TrimSpace(v); v == "" {
			return nil
		}
		t.query, t.queryAt, t.filter = v, at, ""
		t.right = newGrid("dright")
		return t.load(m)
	}
	label := "query " + c.name + " › " + strings.Join(at, " › ")
	defer m.asPopup()
	if value != "" || template == "" {
		m.askAI(label, value, hint, run)
		return
	}
	m.askTemplate(label, template, hint, run)
}

func (t *dataTab) click(m *model, h hit) tea.Cmd {
	if i, ok := stripHit(h, "dq:view"); ok {
		if i != t.qview {
			t.qview = i
			return t.switchView(m)
		}
		return nil
	}
	if t.payload != nil && t.payload.click(h) {
		t.focus = 2
		return nil
	}
	if t.detail != nil && strings.HasPrefix(h.id, "dqd:") {
		t.focus = 2
		if t.detail.click(h) && h.double {
			return t.detailKey(m, tea.KeyMsg{Type: tea.KeyEnter})
		}
		return nil
	}
	if strings.HasPrefix(h.id, "dcomp:") {
		before := t.current().name
		t.left.click(h)
		t.focus = 0
		if t.current().name != before {
			t.filter = ""
			return t.show(m)
		}
		return nil
	}
	t.focus = 1
	if h.id == "data:back" {
		return t.key(m, tea.KeyMsg{Type: tea.KeyEsc})
	}
	if t.right.click(h) && h.double {
		return t.key(m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	return nil
}

func (t *dataTab) view(m *model, w, h int) string {
	if t.left == nil || len(t.left.rows) == 0 {
		return panel("data", sDim.Render("no database, messaging or cache components in this environment (or still loading)"), w, h, true)
	}
	lw := min(34, w/4)
	left := panel("components", t.left.view(m, 1, 1, lw-2, h-2, t.focus == 0), lw, h, t.focus == 0)

	c := t.current()
	title := c.name
	if p := t.paths[c.name]; len(p) > 0 {
		title += " › " + strings.Join(p, " › ")
	}
	if t.query != "" {
		title = c.name + " › " + strings.Join(t.queryAt, " › ") + " › " + sAccent.Render(truncate(t.query, 60))
	}
	title += fmt.Sprintf(" · %d", len(t.right.rows))
	if t.filter != "" {
		title += " · filter " + t.filter
	}
	if t.loading {
		title += " · loading…"
	}
	var head string
	switch {
	case t.err != nil:
		head = sRed.Render(wrap(t.err.Error(), w-lw-4))
	case c.kind == string(core.KindMessaging) && t.queueErr[c.name] != nil:
		head = sRed.Render(wrap(t.queueErr[c.name].Error(), w-lw-4))
	case !c.browse && c.kind != string(core.KindMessaging):
		head = sDim.Render(c.adapter + " cannot be walked")
	}
	hh := 0
	if head != "" {
		hh = lipgloss.Height(head)
		head += "\n"
	}
	if c.kind == string(core.KindMessaging) {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, t.brokerView(m, c, title, head, hh, lw, w-lw, h))
	}
	if len(t.paths[c.name]) > 0 || t.query != "" {
		m.zone("data:back", lw+1, 1+hh, 6, 1)
		head += sKey.Render("‹ back") + "\n"
		hh++
	}
	noteH := boolInt(t.table.Note != "")
	body := head + t.right.view(m, lw+1, 1+hh, w-lw-2, h-2-hh-noteH, t.focus == 1)
	if noteH > 0 {
		body += "\n" + sDim.Render(t.table.Note)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, panel(title, body, w-lw, h, t.focus == 1))
}

// brokerView is a messaging component: the strip of its lists (RabbitMQ), the list, and under it
// the selected queue's details or messages.
func (t *dataTab) brokerView(m *model, c dataComp, title, head string, hh, x, w, h int) string {
	stripH := 0
	var strip string
	if c.adapter == "rabbitmq" {
		labels := []string{"queues"}
		for _, v := range brokerViews {
			labels = append(labels, v[0])
		}
		strip = m.strip("dq:view", x+1, 1, labels, t.qview) + "\n"
		stripH = 1
	}
	if n := len(t.qmarked); n > 0 {
		marked := 0
		for _, on := range t.qmarked {
			marked += boolInt(on)
		}
		if marked > 0 {
			title += fmt.Sprintf(" · %d marked", marked)
		}
	}
	listH := h
	if t.detailFor != "" && t.qview == 0 {
		listH = max(8, h*2/5)
	}
	body := strip + head + t.right.view(m, x+1, 1+stripH+hh, w-2, listH-2-stripH-hh, t.focus == 1)
	list := panel(title, body, w, listH, t.focus == 1)
	if listH == h {
		return list
	}
	dh := h - listH
	dt := t.detailFor + " · " + map[string]string{"info": "settings, consumers, bindings · m messages", "messages": "peeked (requeued) · enter: body as JSON · i settings"}[t.detailKind]
	var db string
	switch {
	case t.payload != nil:
		dt = t.detailFor + " · message · " + t.payload.current().path()
		db = t.payload.view(m, x+1, listH+1, w-2, dh-2)
	case t.detail == nil:
		db = sDim.Render("loading…")
	default:
		noteH := boolInt(t.detailTable.Note != "")
		db = t.detail.view(m, x+1, listH+1, w-2, dh-2-noteH, t.focus == 2)
		if noteH > 0 {
			db += "\n" + sDim.Render(t.detailTable.Note)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, list, panel(dt, db, w, dh, t.focus == 2))
}

// globMatcher matches names case-insensitively: plain text anywhere in the name, or a glob
// (*word*, prefix*) against the whole name.
func globMatcher(p string) func(string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return func(string) bool { return true }
	}
	if !strings.Contains(p, "*") {
		return func(s string) bool { return strings.Contains(strings.ToLower(s), p) }
	}
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$")
	return func(s string) bool { return re.MatchString(strings.ToLower(s)) }
}
