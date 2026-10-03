package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/engine"
	"github.com/MohammadmahdiAhmadi/rig/internal/viz"
)

// dataTab shows every data component at once: databases, queues (with depth history), caches, kv.
type dataTab struct {
	snap   dataSnap
	depth  map[string][]float64
	focus  int
	sel    int
	offset int
}

type dataSnap struct {
	dbs    map[string][]string
	queues map[string][]core.Queue
	caches map[string]map[string]string
	kvs    map[string][]string
	errs   map[string]error
}

type dataMsg struct {
	gen  int
	snap dataSnap
}

func (t *dataTab) name() string { return "Data" }
func (t *dataTab) typing() bool { return false }
func (t *dataTab) hints() [][2]string {
	return [][2]string{{"←→", "panel"}, {"↑↓", "select"}, {"P", "purge queue"}}
}

func (t *dataTab) open(m *model) tea.Cmd {
	t.depth = map[string][]float64{}
	return t.refresh(m)
}

func (t *dataTab) refresh(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		s := dataSnap{dbs: map[string][]string{}, queues: map[string][]core.Queue{}, caches: map[string]map[string]string{}, kvs: map[string][]string{}, errs: map[string]error{}}
		dbs, errs := engine.All[core.Database](a, core.KindDatabase)
		for n, d := range dbs {
			s.dbs[n], errs[n] = d.Databases(c)
		}
		qs, e2 := engine.All[core.Messaging](a, core.KindMessaging)
		for n, q := range qs {
			s.queues[n], e2[n] = q.Queues(c)
		}
		cs, e3 := engine.All[core.Cache](a, core.KindCache)
		for n, x := range cs {
			s.caches[n], e3[n] = x.Info(c)
		}
		kvs, e4 := engine.All[core.KV](a, core.KindKV)
		for n, k := range kvs {
			s.kvs[n], e4[n] = k.List(c, "")
		}
		for _, m := range []map[string]error{errs, e2, e3, e4} {
			for k, v := range m {
				if v != nil {
					s.errs[k] = v
				}
			}
		}
		return dataMsg{gen: gen, snap: s}
	}
}

func (t *dataTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case dataMsg:
		if msg.gen != m.gen {
			return nil
		}
		t.snap = msg.snap
		for comp, qs := range msg.snap.queues {
			for _, q := range qs {
				k := comp + "/" + q.Name
				h := append(t.depth[k], float64(q.Messages))
				if len(h) > 60 {
					h = h[len(h)-60:]
				}
				t.depth[k] = h
			}
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h":
			t.focus = max(0, t.focus-1)
			t.sel, t.offset = 0, 0
		case "right", "l":
			t.focus = min(3, t.focus+1)
			t.sel, t.offset = 0, 0
		case "P":
			comp, q, ok := t.selectedQueue()
			if !ok {
				return nil
			}
			a := m.app
			return m.mutate("purge "+q, func(ctx context.Context) error {
				mq, _, err := engine.Get[core.Messaging](a, core.KindMessaging, comp)
				if err != nil {
					return err
				}
				return mq.Purge(ctx, q)
			})
		default:
			listKeys(msg, &t.sel, t.count())
		}
	}
	return nil
}

func (t *dataTab) queueRows() [][2]string {
	var out [][2]string
	for _, comp := range engine.SortedKeys(t.snap.queues) {
		for _, q := range t.snap.queues[comp] {
			out = append(out, [2]string{comp, q.Name})
		}
	}
	return out
}

func (t *dataTab) selectedQueue() (string, string, bool) {
	if t.focus != 1 {
		return "", "", false
	}
	rows := t.queueRows()
	if t.sel >= len(rows) {
		return "", "", false
	}
	return rows[t.sel][0], rows[t.sel][1], true
}

func (t *dataTab) count() int {
	switch t.focus {
	case 0:
		n := 0
		for _, d := range t.snap.dbs {
			n += len(d)
		}
		return n
	case 1:
		return len(t.queueRows())
	case 3:
		n := 0
		for _, k := range t.snap.kvs {
			n += len(k)
		}
		return n
	}
	return 0
}

func (t *dataTab) view(m *model, w, h int) string {
	if len(t.snap.dbs)+len(t.snap.queues)+len(t.snap.caches)+len(t.snap.kvs)+len(t.snap.errs) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sDim.Render("no database, messaging, cache or kv components in this environment"))
	}
	topH := h / 2
	lw := w / 3

	// databases
	var dbRows [][]string
	for _, comp := range engine.SortedKeys(t.snap.dbs) {
		for _, d := range t.snap.dbs[comp] {
			dbRows = append(dbRows, []string{comp, d})
		}
	}
	dbBody := t.errLines("db", t.snap.dbs) + table([]string{"COMPONENT", "DATABASE"}, []int{12, lw - 17}, dbRows, t.selIf(0), t.offIf(0, topH-3, len(dbRows)), topH-2)

	// queues with depth sparkline
	qw := w - lw
	var qRows [][]string
	for _, r := range t.queueRows() {
		var q core.Queue
		for _, x := range t.snap.queues[r[0]] {
			if x.Name == r[1] {
				q = x
			}
		}
		depth := strconv.Itoa(q.Messages)
		if q.Messages > 0 {
			depth = sAmber.Render(depth)
		}
		cons := strconv.Itoa(q.Consumers)
		if q.Consumers == 0 {
			cons = sRed.Render("0")
		}
		qRows = append(qRows, []string{q.Name, depth, viz.Sparkline(t.depth[r[0]+"/"+r[1]], 16, viz.Palette[1]), cons, viz.Human(q.InRate, "/s"), viz.Human(q.OutRate, "/s")})
	}
	queueBody := t.errLines("queue", t.snap.queues) + table([]string{"QUEUE", "DEPTH", "TREND", "CONS", "IN", "OUT"},
		[]int{max(10, qw-4-8-16-5-8-8-6), 8, 16, 5, 8, 8}, qRows, t.selIf(1), t.offIf(1, topH-3, len(qRows)), topH-2)

	// caches
	var cb strings.Builder
	for _, comp := range engine.SortedKeys(t.snap.caches) {
		info := t.snap.caches[comp]
		cb.WriteString(sTitle.Render(comp) + "\n")
		for _, k := range []string{"redis_version", "used_memory_human", "connected_clients", "instantaneous_ops_per_sec", "keyspace_hits", "keyspace_misses", "db0"} {
			if v, ok := info[k]; ok {
				cb.WriteString(sDim.Render(padRight(k, 28)) + v + "\n")
			}
		}
		if hits, _ := strconv.ParseFloat(info["keyspace_hits"], 64); hits > 0 {
			miss, _ := strconv.ParseFloat(info["keyspace_misses"], 64)
			ratio := hits / (hits + miss)
			cb.WriteString(sDim.Render(padRight("hit ratio", 28)) + viz.Gauge(ratio, 20) + fmt.Sprintf(" %.1f%%", ratio*100) + "\n")
		}
	}
	cacheBody := t.errLines("cache", t.snap.caches) + cb.String()

	// kv
	var kvRows [][]string
	for _, comp := range engine.SortedKeys(t.snap.kvs) {
		for _, k := range t.snap.kvs[comp] {
			kvRows = append(kvRows, []string{comp, k})
		}
	}
	kvBody := t.errLines("kv", t.snap.kvs) + table([]string{"COMPONENT", "KEY"}, []int{12, w - lw - 17}, kvRows, t.selIf(3), t.offIf(3, h-topH-3, len(kvRows)), h-topH-2)

	top := lipgloss.JoinHorizontal(lipgloss.Top, panel("databases", dbBody, lw, topH, t.focus == 0), panel("queues", queueBody, w-lw, topH, t.focus == 1))
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, panel("caches", cacheBody, lw, h-topH, t.focus == 2), panel("key-value", kvBody, w-lw, h-topH, t.focus == 3))
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

func (t *dataTab) selIf(f int) int {
	if t.focus == f {
		return t.sel
	}
	return -1
}

func (t *dataTab) offIf(f, h, n int) int {
	if t.focus != f {
		return 0
	}
	t.offset = scroll(t.sel, t.offset, h, n)
	return t.offset
}

// errLines shows errors of the components of one kind that the snapshot holds no data for.
func (t *dataTab) errLines(_ string, have any) string {
	var b strings.Builder
	for _, n := range engine.SortedKeys(t.snap.errs) {
		var present bool
		switch h := have.(type) {
		case map[string][]string:
			_, present = h[n]
		case map[string][]core.Queue:
			_, present = h[n]
		case map[string]map[string]string:
			_, present = h[n]
		}
		if present {
			b.WriteString(sRed.Render(n+": "+t.snap.errs[n].Error()) + "\n")
		}
	}
	return b.String()
}
