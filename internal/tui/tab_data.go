package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

// dataTab shows queues (depth trend, consumers, rates), caches and databases. Queues and caches
// refresh every 5s while showing; the database list is read once.
type dataTab struct {
	queues *grid
	snap   dataSnap
	dbs    map[string][]string
	depth  map[string][]float64
}

type dataSnap struct {
	queues map[string][]core.Queue
	caches map[string]map[string]string
	errs   map[string]error
}

type dataMsg struct {
	gen  int
	snap dataSnap
}

type dbsMsg struct {
	gen int
	dbs map[string][]string
}

func (t *dataTab) name() string { return "Data" }
func (t *dataTab) typing() bool { return false }
func (t *dataTab) hints() [][2]string {
	return [][2]string{{"↑↓", "queue"}, {"P", "purge queue"}, {"< >", "sort"}, {"D", "reload databases"}}
}
func (t *dataTab) interval() time.Duration { return 5 * time.Second }

func (t *dataTab) open(m *model) tea.Cmd {
	t.depth = map[string][]float64{}
	t.queues = newGrid("queues", col("QUEUE", 0), col("SOURCE", 10), rcol("DEPTH", 8), col("TREND", 16), rcol("UNACKED", 8), rcol("CONS", 5), rcol("IN", 8), rcol("OUT", 8))
	t.queues.sortBy, t.queues.desc = 2, true
	return tea.Batch(t.refresh(m), t.loadDBs(m))
}

func (t *dataTab) loadDBs(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out := map[string][]string{}
		dbs, errs := engine.All[core.Database](a, core.KindDatabase)
		for n, d := range dbs {
			names, err := d.Databases(c)
			if err != nil {
				names = []string{"✖ " + err.Error()}
			}
			out[n] = names
		}
		for n, err := range errs {
			out[n] = []string{"✖ " + err.Error()}
		}
		return dbsMsg{gen: gen, dbs: out}
	}
}

func (t *dataTab) refresh(m *model) tea.Cmd {
	a, gen, ctx := m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		s := dataSnap{queues: map[string][]core.Queue{}, caches: map[string]map[string]string{}, errs: map[string]error{}}
		qs, e1 := engine.All[core.Messaging](a, core.KindMessaging)
		for n, q := range qs {
			var err error
			if s.queues[n], err = q.Queues(c); err != nil {
				e1[n] = err
			}
		}
		cs, e2 := engine.All[core.Cache](a, core.KindCache)
		for n, x := range cs {
			var err error
			if s.caches[n], err = x.Info(c); err != nil {
				e2[n] = err
			}
		}
		for _, m := range []map[string]error{e1, e2} {
			for k, v := range m {
				s.errs[k] = v
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
		var rows []grow
		for comp, qs := range msg.snap.queues {
			for _, q := range qs {
				k := comp + "/" + q.Name
				t.depth[k] = last(append(t.depth[k], float64(q.Messages)), 60)
				depth := strconv.Itoa(q.Messages)
				if q.Messages > 0 {
					depth = sAmber.Render(depth)
				}
				cons := strconv.Itoa(q.Consumers)
				if q.Consumers == 0 {
					cons = sRed.Render("0")
				}
				rows = append(rows, grow{id: k, cells: []string{q.Name, sDim.Render(comp), depth, viz.Sparkline(t.depth[k], 16, viz.Palette[1]), strconv.Itoa(q.Unacked), cons, viz.Human(q.InRate, "/s"), viz.Human(q.OutRate, "/s")},
					keys: []any{q.Name, comp, float64(q.Messages), float64(q.Messages), float64(q.Unacked), float64(q.Consumers), q.InRate, q.OutRate}})
			}
		}
		t.queues.set(rows)
	case dbsMsg:
		if msg.gen == m.gen {
			t.dbs = msg.dbs
		}
	case tea.KeyMsg:
		if t.queues.key(msg) {
			return nil
		}
		switch msg.String() {
		case "D":
			return t.loadDBs(m)
		case "P":
			r, ok := t.queues.current()
			if !ok {
				return nil
			}
			comp, q, _ := strings.Cut(r.id, "/")
			a := m.app
			return m.act("purge queue "+q, true, func(ctx context.Context) error {
				mq, _, err := engine.Get[core.Messaging](a, core.KindMessaging, comp)
				if err != nil {
					return err
				}
				return mq.Purge(ctx, q)
			})
		}
	}
	return nil
}

func (t *dataTab) click(m *model, h hit) tea.Cmd {
	t.queues.click(h)
	return nil
}

func (t *dataTab) view(m *model, w, h int) string {
	if len(t.snap.queues)+len(t.snap.caches)+len(t.snap.errs)+len(t.dbs) == 0 {
		return panel("data", sDim.Render("no database, messaging or cache components in this environment (or still loading)"), w, h, true)
	}
	topH := h * 3 / 5
	var errs strings.Builder
	for _, n := range engine.SortedKeys(t.snap.errs) {
		errs.WriteString(sRed.Render(n+": "+t.snap.errs[n].Error()) + "\n")
	}
	eh := lipgloss.Height(strings.TrimRight(errs.String(), "\n"))
	if errs.Len() == 0 {
		eh = 0
	}
	qbody := errs.String() + t.queues.view(m, 1, 1+eh, w-2, topH-2-eh, true)
	queues := panel(fmt.Sprintf("queues · %d", len(t.queues.rows)), qbody, w, topH, true)

	lw := w / 2
	var cb strings.Builder
	for _, comp := range engine.SortedKeys(t.snap.caches) {
		info := t.snap.caches[comp]
		cb.WriteString(sTitle.Render(comp) + "\n")
		for _, k := range []string{"redis_version", "used_memory_human", "connected_clients", "instantaneous_ops_per_sec", "db0"} {
			if v, ok := info[k]; ok {
				cb.WriteString(sDim.Render(padRight(k, 28)) + v + "\n")
			}
		}
		if hits, _ := strconv.ParseFloat(info["keyspace_hits"], 64); hits > 0 {
			miss, _ := strconv.ParseFloat(info["keyspace_misses"], 64)
			ratio := hits / (hits + miss)
			cb.WriteString(sDim.Render(padRight("hit ratio", 28)) + viz.Gauge(ratio, 16) + fmt.Sprintf(" %.1f%%", ratio*100) + "\n")
		}
	}
	var db strings.Builder
	for _, comp := range engine.SortedKeys(t.dbs) {
		db.WriteString(sTitle.Render(comp) + sDim.Render("  "+strings.Join(t.dbs[comp], " · ")) + "\n")
	}
	if db.Len() == 0 {
		db.WriteString(sDim.Render("loading…"))
	}
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, panel("caches", cb.String(), lw, h-topH, false), panel("databases", db.String(), w-lw, h-topH, false))
	return lipgloss.JoinVertical(lipgloss.Left, queues, bottom)
}
