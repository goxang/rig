package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/spec"
)

// flowTab is the Flow screen: flow: in rig.yaml drawn as nodes and links, with packets moving at
// the measured rates, hot and overloaded nodes marked, and a click opening a node's own page.
type flowTab struct {
	sel     string
	stats   map[string]*flowStat
	links   []flowLinkStat
	packets [][]packet
	emit    []float64 // per link, packets owed to the next frame
	errAcc  []float64
	frame   int
	ticking bool
	paused  bool
	last    time.Time
	lay     flowLayout

	loaded bool
	live   bool
	levels map[string][3]float64 // per node low, hot, over set with t, over rig.yaml's
	// origins are the nodes nothing links to; reach[n] the ones whose requests get to n
	origins []string
	reach   map[string][]int
	oacc    [][]float64

	fit        bool
	zoom       int
	offX, offY int
	follow     bool
	dragFrom   [4]int // where a drag began and the offsets then
}

type packet struct {
	p      float64
	failed bool
	origin int
}

type flowStat struct {
	rate, errs, lat, backlog, offered float64
	has                               uint8
	hist, blog                        []float64
	running, gens                     int
	err                               string
	// ops is a database's or cache's operations counter at at, conns its connections
	ops   float64
	at    time.Time
	conns int
}

const (
	hasRate uint8 = 1 << iota
	hasErrs
	hasLat
	hasBacklog
	hasOffered
	hasOps
)

type flowLinkStat struct {
	rate, errs float64
	has        uint8
}

type flowMsg struct {
	gen   int
	nodes map[string]*flowStat
	links []flowLinkStat
}

type flowTickMsg struct{ gen int }

const flowFrame = 60 * time.Millisecond

func (t *flowTab) name() string            { return "Flow" }
func (t *flowTab) typing() bool            { return false }
func (t *flowTab) interval() time.Duration { return 2 * time.Second }
func (t *flowTab) refresh(m *model) tea.Cmd {
	if !t.live {
		return nil
	}
	return batch(t.fetch(m), t.tick(m))
}
func (t *flowTab) flow(m *model) *spec.Flow               { return m.app.Spec.Flow }
func (t *flowTab) node(m *model, n string) *spec.FlowNode { return t.flow(m).Nodes[n] }

func (t *flowTab) hints() [][2]string {
	return [][2]string{{"←→↑↓", "pick a node"}, {"enter click", "open it"}, {"+ - z", "zoom, fit"}, {"HJKL wheel drag", "scroll"},
		{"t", "set low/hot/over"}, {"e", "live data on/off"}, {"p", "pause"}}
}

type flowSaved struct {
	Live   *bool                 `json:"live,omitempty"`
	Levels map[string][3]float64 `json:"levels,omitempty"`
}

func flowFile(m *model) string { return filepath.Join(m.app.Spec.Dir, ".rig", "flow.json") }

func (t *flowTab) save(m *model) {
	live := t.live
	raw, _ := json.MarshalIndent(flowSaved{Live: &live, Levels: t.levels}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(flowFile(m)), 0o755); err == nil {
		_ = os.WriteFile(flowFile(m), append(raw, '\n'), 0o644)
	}
}

func (t *flowTab) load(m *model) {
	t.loaded, t.fit = true, true
	f := t.flow(m)
	t.live = f.Live == nil || *f.Live
	t.levels = map[string][3]float64{}
	var saved flowSaved
	if raw, err := os.ReadFile(flowFile(m)); err == nil && json.Unmarshal(raw, &saved) == nil {
		if saved.Live != nil {
			t.live = *saved.Live
		}
		for n, l := range saved.Levels {
			t.levels[n] = l
		}
	}
}

// originsOf finds the nodes requests start from and, for every node, which of them reach it.
func (t *flowTab) originsOf(f *spec.Flow) {
	t.origins, t.reach = nil, map[string][]int{}
	fed := map[string]bool{}
	for _, l := range f.Links {
		if l.From != l.To {
			fed[l.To] = true
		}
	}
	for _, n := range flowOrder(f) {
		if !fed[n] {
			t.origins = append(t.origins, n)
		}
	}
	for i, o := range t.origins {
		seen, todo := map[string]bool{o: true}, []string{o}
		for len(todo) > 0 {
			n := todo[0]
			todo = todo[1:]
			t.reach[n] = append(t.reach[n], i)
			for _, l := range f.Links {
				if l.From == n && !seen[l.To] {
					seen[l.To] = true
					todo = append(todo, l.To)
				}
			}
		}
	}
}

// levelsOf are a node's low, hot and over rates: set with t, else rig.yaml's; hot and over default
// to 70% and 90% of max.
func (t *flowTab) levelsOf(name string, n *spec.FlowNode) (low, hot, over float64) {
	low, hot, over = n.Low, n.Hot, n.Over
	if hot == 0 {
		hot = 0.7 * n.Max
	}
	if over == 0 {
		over = 0.9 * n.Max
	}
	if l, ok := t.levels[name]; ok {
		low, hot, over = l[0], l[1], l[2]
	}
	return
}

func (t *flowTab) askLevels(m *model) {
	n := t.node(m, t.sel)
	if n == nil {
		return
	}
	name := t.sel
	low, hot, over := t.levelsOf(name, n)
	cur := fmt.Sprintf("%g %g %g", low, hot, over)
	m.ask("low hot over for "+name+" (per second; 0 is none, empty puts rig.yaml's back)", cur, func(v string) tea.Cmd {
		if strings.TrimSpace(v) == "" {
			delete(t.levels, name)
			t.save(m)
			return nil
		}
		var l [3]float64
		words := strings.Fields(strings.ReplaceAll(v, ",", " "))
		if len(words) != 3 {
			m.setStatus("three numbers: low hot over, like 5 300 450", true)
			return nil
		}
		for i, w := range words {
			x, err := strconv.ParseFloat(w, 64)
			if err != nil || x < 0 {
				m.setStatus(fmt.Sprintf("%q is not a rate", w), true)
				return nil
			}
			l[i] = x
		}
		if l[2] > 0 && l[1] > l[2] {
			m.setStatus("hot must not be above over", true)
			return nil
		}
		t.levels[name] = l
		t.save(m)
		return nil
	})
}

func (t *flowTab) open(m *model) tea.Cmd {
	f := t.flow(m)
	if f == nil {
		return nil
	}
	if !t.loaded {
		t.load(m)
	}
	t.originsOf(f)
	t.stats = map[string]*flowStat{}
	t.links = make([]flowLinkStat, len(f.Links))
	t.packets, t.emit, t.errAcc = make([][]packet, len(f.Links)), make([]float64, len(f.Links)), make([]float64, len(f.Links))
	t.oacc = make([][]float64, len(f.Links))
	if order := flowOrder(f); t.sel == "" && len(order) > 0 {
		t.sel = order[0]
	}
	return t.refresh(m)
}

func (t *flowTab) tick(m *model) tea.Cmd {
	if t.ticking {
		return nil
	}
	t.ticking, t.last = true, time.Now()
	gen := m.gen
	return tea.Tick(flowFrame, func(time.Time) tea.Msg { return flowTickMsg{gen: gen} })
}

// fetch asks every query of the flow at once, plus the load generators and queues nodes name.
func (t *flowTab) fetch(m *model) tea.Cmd {
	f, a, gen, ctx := t.flow(m), m.app, m.gen, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		src, _, srcErr := engine.Get[core.Metrics](a, core.KindMetrics, f.Source)
		instant := func(q string) (float64, bool, error) {
			if q == "" {
				return 0, false, nil
			}
			if srcErr != nil {
				return 0, false, srcErr
			}
			ss, err := src.Instant(c, engine.ExpandQuery(q, nil, 5*time.Minute, 15*time.Second))
			if err != nil {
				return 0, false, err
			}
			if len(ss) == 0 {
				return 0, false, nil
			}
			v := 0.0
			for _, s := range ss {
				if !math.IsNaN(s.Value) && !math.IsInf(s.Value, 0) {
					v += s.Value
				}
			}
			return v, true, nil
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		out := flowMsg{gen: gen, nodes: map[string]*flowStat{}, links: make([]flowLinkStat, len(f.Links))}
		for name, n := range f.Nodes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := &flowStat{}
				var errs []string
				for _, q := range []struct {
					query string
					to    *float64
					bit   uint8
				}{{n.Rate, &s.rate, hasRate}, {n.Errors, &s.errs, hasErrs}, {n.Latency, &s.lat, hasLat}, {n.Backlog, &s.backlog, hasBacklog}} {
					v, ok, err := instant(q.query)
					if err != nil {
						errs = append(errs, err.Error())
					}
					if ok {
						*q.to, s.has = v, s.has|q.bit
					}
				}
				for _, g := range n.Load {
					lg, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, g)
					if err != nil {
						errs = append(errs, err.Error())
						continue
					}
					st, err := lg.Status(c)
					if err != nil {
						errs = append(errs, g+": "+err.Error())
						continue
					}
					s.gens++
					s.has |= hasOffered
					if st.Running {
						s.running++
						s.offered += st.Rate
					}
				}
				// a queue component counts by itself: messages waiting, and the publish rate
				if k, _, err := a.Kind(n.Open); err == nil && k == core.KindMessaging && n.Rate == "" && n.Backlog == "" {
					if mq, _, err := engine.Get[core.Messaging](a, core.KindMessaging, n.Open); err == nil {
						if qs, err := mq.Queues(c); err == nil {
							for _, q := range qs {
								s.backlog += float64(q.Messages)
								s.rate += q.InRate
							}
							s.has |= hasRate | hasBacklog
						} else {
							errs = append(errs, err.Error())
						}
					}
				}
				// a database or cache without a rate query counts by itself: operations and connections
				if k, _, err := a.Kind(n.Open); err == nil && (k == core.KindDatabase || k == core.KindCache) && n.Rate == "" {
					if ar, _, err := engine.Get[core.ActivityReader](a, k, n.Open); err == nil {
						if act, err := ar.Activity(c); err == nil {
							s.ops, s.at, s.conns, s.has = act.Ops, time.Now(), act.Conns, s.has|hasOps
						} else {
							errs = append(errs, err.Error())
						}
					}
				}
				s.err = strings.Join(errs, "; ")
				mu.Lock()
				out.nodes[name] = s
				mu.Unlock()
			}()
		}
		for i, l := range f.Links {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ls := &out.links[i]
				if v, ok, _ := instant(l.Rate); ok {
					ls.rate, ls.has = v, ls.has|hasRate
				}
				if v, ok, _ := instant(l.Errors); ok {
					ls.errs, ls.has = v, ls.has|hasErrs
				}
			}()
		}
		wg.Wait()
		return out
	}
}

func (t *flowTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case flowMsg:
		if msg.gen != m.gen {
			return nil
		}
		for n, s := range msg.nodes {
			if old := t.stats[n]; old != nil {
				s.hist, s.blog = old.hist, old.blog
				if s.has&hasOps != 0 && old.has&hasOps != 0 && s.ops >= old.ops {
					if dt := s.at.Sub(old.at).Seconds(); dt > 0 {
						s.rate, s.has = (s.ops-old.ops)/dt, s.has|hasRate
					}
				}
			}
			if s.has&hasRate != 0 {
				s.hist = last(append(s.hist, s.rate), 90)
			}
			if s.has&hasBacklog != 0 {
				s.blog = last(append(s.blog, s.backlog), 90)
			}
			t.stats[n] = s
		}
		t.links = msg.links
	case flowTickMsg:
		if msg.gen != m.gen || m.tabs[m.active] != tab(t) || !t.live {
			t.ticking = false
			return nil
		}
		now := time.Now()
		dt := min(now.Sub(t.last).Seconds(), 0.25)
		t.last = now
		if !t.paused {
			t.advance(m, dt)
		}
		t.frame++
		gen := m.gen
		return tea.Tick(flowFrame, func(time.Time) tea.Msg { return flowTickMsg{gen: gen} })
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h", "right", "l", "up", "k", "down", "j":
			t.move(msg.String())
			t.follow = true
		case "enter", "o":
			return t.openNode(m, t.sel)
		case "p", " ":
			t.paused = !t.paused
		case "+", "=":
			t.zoomBy(1)
		case "-", "_":
			t.zoomBy(-1)
		case "z", "0":
			t.fit, t.offX, t.offY = true, 0, 0
		case "H":
			t.offX -= 8
		case "L":
			t.offX += 8
		case "K":
			t.offY -= 3
		case "J":
			t.offY += 3
		case "t":
			t.askLevels(m)
		case "e":
			t.live = !t.live
			t.save(m)
			if t.live {
				m.setStatus("Flow: live data on", false)
				return t.refresh(m)
			}
			t.stats, t.links = map[string]*flowStat{}, make([]flowLinkStat, len(t.flow(m).Links))
			for i := range t.packets {
				t.packets[i] = nil
			}
			m.setStatus("Flow: live data off, no queries", false)
		}
	}
	return nil
}

func (t *flowTab) zoomBy(d int) {
	t.zoom = min(max(t.zoom+d, 0), len(flowZooms)-1)
	t.fit, t.follow = false, true
}

func (t *flowTab) wheel(m *model, z hit, up bool) (tea.Cmd, bool) {
	if !strings.HasPrefix(z.id, "flow:") {
		return nil, false
	}
	d := 3
	if up {
		d = -3
	}
	if z.mod {
		t.offX += 3 * d
	} else {
		t.offY += d
	}
	return nil, true
}

func (t *flowTab) drag(m *model, z hit, phase dragPhase) bool {
	if phase == dragPress {
		if z.id != "flow:canvas" {
			return false
		}
		t.dragFrom = [4]int{z.x, z.y, t.offX, t.offY}
		return true
	}
	t.offX = t.dragFrom[2] - (z.x - t.dragFrom[0])
	t.offY = t.dragFrom[3] - (z.y - t.dragFrom[1])
	return true
}

// ---- what the numbers mean ----

const (
	stUnknown = iota
	stIdle
	stLow
	stOK
	stHot
	stOver
)

// state is how a node is doing, with the reasons when it is hot or overloaded.
func (t *flowTab) state(m *model, name string) (int, []string) {
	n, s := t.node(m, name), t.stats[name]
	if svc, ok := t.service(m, n.Open); ok {
		if svc.Desired > 0 && svc.Ready == 0 {
			return stOver, []string{fmt.Sprintf("%s is down: 0/%d up", n.Open, svc.Desired)}
		}
		if svc.Desired == 0 && (s == nil || s.rate == 0) {
			return stIdle, []string{n.Open + " is stopped"}
		}
	}
	if s == nil || s.has == 0 {
		return stUnknown, nil
	}
	level, why := stIdle, []string(nil)
	if s.rate > 0 || s.running > 0 {
		level = stOK
	}
	low, hot, over := t.levelsOf(name, n)
	if r, ok := s.shown(); ok && r > 0 && r < low {
		level, why = stLow, []string{fmt.Sprintf("below its low %s", viz.Human(low, "/s"))}
	}
	raise := func(to int, reason string) {
		level = max(level, to)
		why = append(why, reason)
	}
	if r, ok := s.shown(); ok {
		switch {
		case over > 0 && r >= over:
			raise(stOver, fmt.Sprintf("%s, over %s", viz.Human(r, "/s"), viz.Human(over, "/s")))
		case hot > 0 && r >= hot:
			raise(stHot, fmt.Sprintf("%s, hot from %s", viz.Human(r, "/s"), viz.Human(hot, "/s")))
		}
	}
	if n.Slow > 0 && s.has&hasLat != 0 {
		switch {
		case s.lat >= n.Slow:
			raise(stOver, fmt.Sprintf("latency %s over %s", ms(s.lat), ms(n.Slow)))
		case s.lat >= 0.7*n.Slow:
			raise(stHot, fmt.Sprintf("latency %s nears %s", ms(s.lat), ms(n.Slow)))
		}
	}
	if r := s.errRatio(); r >= 0.05 {
		raise(stOver, fmt.Sprintf("%.1f%% errors", r*100))
	} else if r >= 0.01 {
		raise(stHot, fmt.Sprintf("%.1f%% errors", r*100))
	}
	if d := growth(s.blog); s.backlog > 100 && d > 0 {
		raise(stHot, fmt.Sprintf("backlog growing: %s waiting, +%s in %ds", viz.Human(s.backlog, ""), viz.Human(d, ""), 2*min(len(s.blog)-1, 5)))
	}
	if s.has&hasOffered != 0 && s.has&hasRate != 0 && s.offered > 5 && s.rate < 0.85*s.offered {
		raise(stHot, fmt.Sprintf("sends %s of the %s asked", viz.Human(s.rate, "/s"), viz.Human(s.offered, "/s")))
	}
	if spike(s.hist) {
		why = append(why, "rate spiked")
	}
	return level, why
}

func (s *flowStat) util(n *spec.FlowNode) (float64, bool) {
	if n.Max <= 0 {
		return 0, false
	}
	switch {
	case s.has&hasRate != 0:
		return s.rate / n.Max, true
	case s.has&hasOffered != 0:
		return s.offered / n.Max, true
	}
	return 0, false
}

func (s *flowStat) errRatio() float64 {
	if s.has&hasErrs == 0 || s.rate <= 0 {
		return 0
	}
	return s.errs / s.rate
}

// shown is a node's rate as drawn: measured, else what its load generators are asked for.
func (s *flowStat) shown() (float64, bool) {
	switch {
	case s == nil:
		return 0, false
	case s.has&hasRate != 0:
		return s.rate, true
	case s.has&hasOffered != 0:
		return s.offered, true
	}
	return 0, false
}

// growth is how much a series rose over its last five samples (0 when it fell or is too short).
func growth(h []float64) float64 {
	if len(h) < 3 {
		return 0
	}
	from := h[max(0, len(h)-6)]
	if d := h[len(h)-1] - from; d > 0 && h[len(h)-1] >= h[len(h)-2] {
		return d
	}
	return 0
}

// trend is ↑ ↓ or → from the mean of the last three samples against the three before.
func trend(h []float64) (string, flowStyle) {
	if len(h) < 4 {
		return "", fsNone
	}
	n := min(3, len(h)/2)
	var a, b float64
	for i := 0; i < n; i++ {
		a += h[len(h)-1-i]
		b += h[len(h)-1-n-i]
	}
	switch {
	case a > b*1.1+0.5:
		return "↑", fsGreen
	case a < b*0.9-0.5:
		return "↓", fsAmber
	}
	return "→", fsDim
}

// spike is a last sample over twice the median of the ones before.
func spike(h []float64) bool {
	if len(h) < 10 {
		return false
	}
	prev := append([]float64(nil), h[:len(h)-1]...)
	for i := range prev {
		for j := i + 1; j < len(prev); j++ {
			if prev[j] < prev[i] {
				prev[i], prev[j] = prev[j], prev[i]
			}
		}
	}
	med := prev[len(prev)/2]
	return med > 1 && h[len(h)-1] > 2*med
}

func ms(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fs", v/1000)
	}
	return fmt.Sprintf("%.0fms", v)
}

func (t *flowTab) service(m *model, name string) (core.Status, bool) {
	if name == "" || m.app.Spec.Services[name] == nil {
		return core.Status{}, false
	}
	for _, s := range m.services {
		if s.Service == name {
			return s, true
		}
	}
	return core.Status{}, false
}

// linkRate is what a link carries: its own query, else its target's rate shared over the target's
// inputs, else its source's shared over the source's outputs.
func (t *flowTab) linkRate(m *model, i int) (float64, float64) {
	f := t.flow(m)
	l := f.Links[i]
	var ls flowLinkStat
	if i < len(t.links) {
		ls = t.links[i]
	}
	share := func(node string, in bool) (float64, float64) {
		r, ok := t.stats[node].shown()
		if !ok {
			return 0, 0
		}
		k := 0
		for _, o := range f.Links {
			if in && o.To == node || !in && o.From == node {
				k++
			}
		}
		s := t.stats[node]
		return r / float64(max(k, 1)), s.errRatio()
	}
	rate, ratio := 0.0, 0.0
	switch {
	case ls.has&hasRate != 0:
		rate = ls.rate
		ratio = t.stats[l.To].errRatioOrZero()
	default:
		if rate, ratio = share(l.To, true); rate == 0 {
			rate, ratio = share(l.From, false)
		}
	}
	if ls.has&hasErrs != 0 && rate > 0 {
		ratio = ls.errs / rate
	}
	return rate, min(ratio, 1)
}

func (s *flowStat) errRatioOrZero() float64 {
	if s == nil {
		return 0
	}
	return s.errRatio()
}

// advance moves the packets: more and faster on busier links (on a log scale, so 5/s and 5000/s
// both read), failed ones in red, and slowing near a node that is overloaded, where they queue.
func (t *flowTab) advance(m *model, dt float64) {
	f := t.flow(m)
	for i := range f.Links {
		if i >= len(t.packets) || len(t.lay.paths) <= i || t.lay.paths[i] == nil {
			continue
		}
		rate, ratio := t.linkRate(m, i)
		over, _ := t.state(m, f.Links[i].To)
		// speed in cells a second, so long and short links look alike
		travel := float64(len(t.lay.paths[i])) / (18 + 12*math.Log10(1+rate))
		ps := t.packets[i][:0]
		for _, p := range t.packets[i] {
			v := dt / travel
			if over == stOver && p.p > 0.7 {
				v *= 0.3
			}
			if p.p += v; p.p < 1 {
				ps = append(ps, p)
			}
		}
		if rate > 0 {
			t.emit[i] += dt * min(max(0.4+0.8*math.Log10(1+rate), 0.4), 3.5)
			for t.emit[i] >= 1 {
				t.emit[i]--
				t.errAcc[i] += ratio
				failed := t.errAcc[i] >= 1
				if failed {
					t.errAcc[i]--
				}
				ps = append(ps, packet{failed: failed, origin: t.pickOrigin(i, f.Links[i].From)})
			}
		}
		t.packets[i] = ps
	}
}

// pickOrigin is whose request the next packet on link i is: the origins reaching its source take
// turns in proportion to their rates.
func (t *flowTab) pickOrigin(i int, from string) int {
	rs := t.reach[from]
	switch len(rs) {
	case 0:
		return -1
	case 1:
		return rs[0]
	}
	if len(t.oacc[i]) != len(rs) {
		t.oacc[i] = make([]float64, len(rs))
	}
	sum := 0.0
	ws := make([]float64, len(rs))
	for k, o := range rs {
		r, _ := t.stats[t.origins[o]].shown()
		ws[k] = r
		sum += r
	}
	best := 0
	for k := range rs {
		if sum > 0 {
			t.oacc[i][k] += ws[k] / sum
		} else {
			t.oacc[i][k] += 1 / float64(len(rs))
		}
		if t.oacc[i][k] > t.oacc[i][best] {
			best = k
		}
	}
	t.oacc[i][best]--
	return rs[best]
}

// inflow is what the links into a node carry, for a node with no numbers of its own.
func (t *flowTab) inflow(m *model, name string) float64 {
	sum := 0.0
	for i, l := range t.flow(m).Links {
		if l.To == name && l.From != name {
			r, _ := t.linkRate(m, i)
			sum += r
		}
	}
	return sum
}

// ---- keys, clicks, opening a node's page ----

// move picks the nearest node in the direction pressed: up and down in the column, left and right
// in the next column that has one.
func (t *flowTab) move(dir string) {
	cur, ok := t.lay.index[t.sel]
	if !ok {
		return
	}
	a := t.lay.boxes[cur]
	best, bestD := -1, math.MaxFloat64
	for i, b := range t.lay.boxes {
		dx, dy := float64(b.x-a.x), float64(b.y-a.y)
		var fit bool
		switch dir {
		case "left", "h":
			fit = b.col < a.col
		case "right", "l":
			fit = b.col > a.col
		case "up", "k":
			fit = b.col == a.col && b.y < a.y
		case "down", "j":
			fit = b.col == a.col && b.y > a.y
		}
		if !fit || i == cur {
			continue
		}
		if d := math.Abs(dx)*3 + math.Abs(dy); d < bestD {
			best, bestD = i, d
		}
	}
	if best >= 0 {
		t.sel = t.lay.boxes[best].name
	}
}

func (t *flowTab) click(m *model, h hit) tea.Cmd {
	if n, ok := strings.CutPrefix(h.id, "flow:node:"); ok {
		t.sel = n
		return t.openNode(m, n)
	}
	return nil
}

// openNode opens what a node's open: names (else the node's own name): a service's page, a
// component on its screen, dashboard:<name>, or a screen. esc or backspace there comes back.
func (t *flowTab) openNode(m *model, name string) tea.Cmd {
	n := t.node(m, name)
	if n == nil {
		return nil
	}
	target := n.Open
	if target == "" {
		target = name
	}
	if d, ok := strings.CutPrefix(target, "dashboard:"); ok {
		for i, tb := range m.tabs {
			if mt, ok := tb.(*metricsTab); ok {
				return batch(m.jumpDirect(i), mt.showDash(m, d))
			}
		}
	}
	if m.app.Spec.Services[target] != nil {
		for i, tb := range m.tabs {
			if st, ok := tb.(*servicesTab); ok {
				cmd := m.jumpDirect(i)
				st.focusService(m, target)
				return cmd
			}
		}
	}
	if k, _, err := m.app.Kind(target); err == nil {
		for i, tb := range m.tabs {
			switch x := tb.(type) {
			case *loadTab:
				if k == core.KindLoad {
					cmd := m.jumpDirect(i)
					x.pick(target)
					return cmd
				}
			case *dataTab:
				if k == core.KindMessaging || k == core.KindDatabase || k == core.KindCache {
					cmd := m.jumpDirect(i)
					return batch(cmd, x.pick(m, target))
				}
			case *kvTab:
				if k == core.KindKV {
					return m.jumpDirect(i)
				}
			case *metricsTab:
				if k == core.KindMetrics {
					return m.jumpDirect(i)
				}
			case *tracesTab:
				if k == core.KindTracing {
					return m.jumpDirect(i)
				}
			}
		}
	}
	for i, tb := range m.tabs {
		if strings.EqualFold(tb.name(), target) {
			return m.jumpDirect(i)
		}
	}
	m.setStatus(fmt.Sprintf("%s: nothing to open for %q (open: a service, a component, dashboard:<name> or a screen)", name, target), true)
	return nil
}

// ---- view ----

var flowIcons = map[string]string{"load": "»", "gateway": "◈", "service": "◆", "queue": "≡", "database": "▤", "cache": "◇", "external": "⇥"}

func (t *flowTab) view(m *model, w, h int) string {
	f := t.flow(m)
	if f == nil || len(f.Nodes) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sDim.Render(`no flow — add one to rig.yaml:

flow:
  nodes:
    load: { kind: load, load: [load-api], max: 500 }
    api:  { kind: service, rate: 'sum(rate(http_requests_total[1m]))', max: 400, slow: 300 }
    db:   { kind: database, open: db }
  links:
    - { from: load, to: api }
    - { from: api, to: db }`))
	}
	if !t.loaded {
		t.load(m)
	}
	head := t.header(m, w)
	detail := t.detail(m, w)
	ch := max(4, h-lipgloss.Height(head)-lipgloss.Height(detail)-1)
	if t.fit {
		t.zoom = fitZoom(f, w, ch)
	}
	t.lay = layoutFlow(f, t.zoom)
	if _, ok := t.lay.index[t.sel]; !ok && len(t.lay.boxes) > 0 {
		t.sel = t.lay.boxes[0].name
	}
	if b, ok := t.lay.index[t.sel]; ok && t.follow {
		bx := t.lay.boxes[b]
		t.offX = min(max(t.offX, bx.x+bx.w+1-w), bx.x-1)
		t.offY = min(max(t.offY, bx.y+bx.h+1-ch), bx.y-1)
		t.follow = false
	}
	t.offX = min(max(t.offX, 0), max(0, t.lay.w-w))
	t.offY = min(max(t.offY, 0), max(0, t.lay.h-ch))
	dx, dy := max(0, (w-t.lay.w)/2)-t.offX, max(0, (ch-t.lay.h)/2)-t.offY
	hy := lipgloss.Height(head)
	m.zone("flow:canvas", 0, hy, w, ch)
	cv := newCanvas(t.lay.w, t.lay.h)
	states := map[string]int{}
	whys := map[string][]string{}
	for _, b := range t.lay.boxes {
		states[b.name], whys[b.name] = t.state(m, b.name)
	}
	for i, p := range t.lay.paths {
		if p == nil {
			continue
		}
		rate, _ := t.linkRate(m, i)
		st := fsPanel
		switch {
		case states[f.Links[i].To] == stOver:
			st = fsRed
		case states[f.Links[i].To] == stHot:
			st = fsAmber
		case rate > 0:
			st = fsDim
		}
		cv.line(p, st)
	}
	for i, ps := range t.packets {
		if i >= len(t.lay.paths) || t.lay.paths[i] == nil {
			continue
		}
		p := t.lay.paths[i]
		for _, pk := range ps {
			k := int(pk.p * float64(len(p)-1))
			glyph, st := '●', fsPacket
			if pk.origin >= 0 {
				st = originStyle(pk.origin)
			}
			if pk.failed {
				glyph, st = '◆', fsRed
			}
			cv.set(p[k].x, p[k].y, glyph, st)
		}
	}
	for i, p := range t.lay.paths {
		if lbl := f.Links[i].Label; lbl != "" && p != nil {
			// above the run into the target: the one part of a path no other link shares
			x0, x1, y := runIn(p)
			if w := x1 - x0; w >= 3 {
				lbl = ansi.Truncate(lbl, w, "…")
				cv.put(x1-ansi.StringWidth(lbl), y-1, lbl, fsDim, w)
			}
		}
	}
	blink := (t.frame/6)%2 == 0
	origin := map[string]int{}
	for i, o := range t.origins {
		origin[o] = i
	}
	for _, b := range t.lay.boxes {
		n, s := f.Nodes[b.name], t.stats[b.name]
		title := flowTitle(f, b.name)
		border, titleSt := fsPanel, fsTitle
		if o, ok := origin[b.name]; ok {
			border, titleSt = originStyle(o), originStyle(o)
		}
		switch states[b.name] {
		case stOK:
			border = fsGreen
		case stHot:
			border, titleSt = fsAmber, fsAmber
		case stOver:
			border, titleSt = fsRed, fsRed
			if blink {
				titleSt = fsRedRev
			}
		case stIdle, stLow:
			if _, ok := origin[b.name]; !ok {
				border, titleSt = fsPanel, fsDim
			}
		}
		picked := b.name == t.sel
		if picked && states[b.name] < stHot {
			if _, ok := origin[b.name]; !ok {
				border = fsAccent
			}
		}
		cv.box(b, title, border, titleSt, picked, t.boxLines(m, b, n, s, states[b.name]))
		if rs := t.reach[b.name]; len(t.origins) > 1 && len(rs) > 0 {
			x := b.x + b.w - 2 - len(rs)
			for _, o := range rs {
				cv.set(x, b.y+b.h-1, '●', originStyle(o))
				x++
			}
		}
		x0, y0 := max(b.x+dx, 0), max(b.y+dy, 0)
		if x1, y1 := min(b.x+dx+b.w, w), min(b.y+dy+b.h, ch); x1 > x0 && y1 > y0 {
			m.zone("flow:node:"+b.name, x0, hy+y0, x1-x0, y1-y0)
		}
	}
	view := cv.window(dx, dy, w, ch)
	if !t.live {
		msg := " live data off: no queries to metrics, queues, databases or caches · e turns it on "
		view.put(max(0, (w-ansi.StringWidth(msg))/2), 0, msg, fsAmber, w)
	}
	if t.lay.w > w || t.lay.h > ch {
		pos := fmt.Sprintf(" %d,%d of %dx%d ", t.offX, t.offY, t.lay.w, t.lay.h)
		view.put(w-ansi.StringWidth(pos), ch-1, pos, fsDim, w)
	}
	return head + "\n" + view.String() + "\n" + detail
}

// boxLines are a node's two lines: its rate, trend and load bar; then latency, errors, backlog or
// how many of its instances are up.
func (t *flowTab) boxLines(m *model, b flowBox, n *spec.FlowNode, s *flowStat, state int) [][]seg {
	inner := b.w - 4
	var one, two []seg
	if r, ok := s.shown(); ok {
		one = append(one, seg{viz.Human(r, "/s"), fsTitle})
		if a, st := trend(s.hist); a != "" {
			one = append(one, seg{" " + a, st})
		}
		if u, ok := s.util(n); ok {
			used := lipgloss.Width(segText(one))
			if room := inner - used - 6; room >= 3 {
				full := min(room, int(math.Round(u*float64(room))))
				st := fsGreen
				switch state {
				case stOver:
					st = fsRed
				case stHot:
					st = fsAmber
				}
				one = append(one, seg{" " + strings.Repeat("▰", full), st}, seg{strings.Repeat("▱", room-full), fsDim}, seg{fmt.Sprintf("%3.0f%%", min(u*100, 999)), st})
			}
		}
	} else if in := t.inflow(m, b.name); in > 0 {
		one = append(one, seg{"≈" + viz.Human(in, "/s"), fsText}, seg{" in", fsDim})
	} else if s != nil && s.err != "" {
		one = append(one, seg{"✖ " + s.err, fsRed})
	} else if s == nil && !t.live {
		one = append(one, seg{"–", fsDim})
	} else if s == nil {
		one = append(one, seg{spinner(), fsDim})
	} else {
		one = append(one, seg{"no data", fsDim})
	}
	if s != nil {
		if s.has&hasLat != 0 {
			st := fsDim
			if n.Slow > 0 && s.lat >= n.Slow {
				st = fsRed
			}
			two = append(two, seg{ms(s.lat) + " ", st})
		}
		if r := s.errRatio(); s.has&hasErrs != 0 {
			st := fsDim
			if r >= 0.01 {
				st = fsRed
			}
			two = append(two, seg{fmt.Sprintf("✖%.1f%% ", r*100), st})
		}
		if s.has&hasBacklog != 0 {
			st := fsDim
			if growth(s.blog) > 0 && s.backlog > 100 {
				st = fsAmber
			}
			two = append(two, seg{"≡" + viz.Human(s.backlog, "") + " ", st})
		}
		if s.gens > 0 {
			two = append(two, seg{fmt.Sprintf("%d/%d running ", s.running, s.gens), fsDim})
		}
		if s.has&hasOps != 0 {
			two = append(two, seg{fmt.Sprintf("⇄%d conns ", s.conns), fsDim})
		}
	}
	if svc, ok := t.service(m, n.Open); ok {
		st := fsGreen
		if svc.Ready < svc.Desired {
			st = fsAmber
		}
		if svc.Desired > 0 && svc.Ready == 0 {
			st = fsRed
		}
		two = append(two, seg{fmt.Sprintf("●%d/%d", svc.Ready, svc.Desired), st})
	}
	if state == stOver && b.h <= 3 {
		one = append([]seg{{"⚠ ", fsRed}}, one...)
	}
	if b.h <= 3 {
		return [][]seg{one}
	}
	return [][]seg{one, two}
}

func segText(s []seg) string {
	var b strings.Builder
	for _, x := range s {
		b.WriteString(x.text)
	}
	return b.String()
}

// header sums the flow up: what comes in, what goes out, errors, the busiest node, and the verdict.
func (t *flowTab) header(m *model, w int) string {
	f := t.flow(m)
	hasIn, hasOut := map[string]bool{}, map[string]bool{}
	for _, l := range f.Links {
		hasIn[l.To], hasOut[l.From] = true, true
	}
	var in, out, errs, rate float64
	hot, over := 0, 0
	worst, worstU := "", 0.0
	for _, n := range flowOrder(f) {
		s := t.stats[n]
		r, ok := s.shown()
		if ok && !hasIn[n] {
			in += r
		}
		if ok && !hasOut[n] && hasIn[n] {
			out += r
		}
		if s != nil && s.has&hasErrs != 0 {
			errs, rate = errs+s.errs, rate+s.rate
		}
		if s != nil {
			if u, ok := s.util(f.Nodes[n]); ok && u > worstU {
				worst, worstU = n, u
			}
		}
		switch st, _ := t.state(m, n); st {
		case stHot:
			hot++
		case stOver:
			over++
		}
	}
	verdict := sGreen.Render("● flowing")
	switch {
	case over > 0:
		verdict = sRed.Render(fmt.Sprintf("✖ %d overloaded", over))
		if hot > 0 {
			verdict += sAmber.Render(fmt.Sprintf(" · ▲ %d hot", hot))
		}
	case hot > 0:
		verdict = sAmber.Render(fmt.Sprintf("▲ %d hot", hot))
	case in == 0:
		verdict = sDim.Render("○ idle")
	}
	if t.paused {
		verdict += sDim.Render(" · ❚❚ paused")
	}
	if !t.live {
		verdict = sAmber.Render("○ live data off")
	}
	parts := []string{sDim.Render("in ") + sTitle.Render(viz.Human(in, "/s")), sDim.Render("out ") + sTitle.Render(viz.Human(out, "/s"))}
	if rate > 0 {
		pct := errs / rate * 100
		parts = append(parts, sDim.Render("errors ")+lipgloss.NewStyle().Foreground(failColor(pct)).Render(fmt.Sprintf("%.2f%%", pct)))
	}
	if worst != "" {
		st := sGreen
		switch {
		case worstU >= 0.9:
			st = sRed
		case worstU >= 0.7:
			st = sAmber
		}
		parts = append(parts, sDim.Render("busiest ")+st.Render(fmt.Sprintf("%s %.0f%%", worst, worstU*100)))
	}
	line := " " + verdict + "   " + strings.Join(parts, sDim.Render("  ·  "))
	var legend string
	for i, o := range t.origins {
		legend += originStyle(i).style().Render("●") + sDim.Render(" "+firstNonEmpty(f.Nodes[o].Label, o)+"  ")
	}
	legend += sRed.Render("◆") + sDim.Render(" failed")
	if gap := w - lipgloss.Width(line) - lipgloss.Width(legend) - 1; gap > 0 {
		line += strings.Repeat(" ", gap) + legend
	}
	return truncate(line, w)
}

// detail is the picked node at length: its numbers, rate history, why it is hot, what a click opens.
func (t *flowTab) detail(m *model, w int) string {
	f := t.flow(m)
	n := f.Nodes[t.sel]
	if n == nil {
		return ""
	}
	s := t.stats[t.sel]
	st, why := t.state(m, t.sel)
	title := sAccent.Render(strings.TrimSpace(flowIcons[n.Kind]+" "+firstNonEmpty(n.Label, t.sel))) + sDim.Render(" "+firstNonEmpty(n.Kind, "node"))
	var nums []string
	if r, ok := s.shown(); ok {
		nums = append(nums, viz.Human(r, "/s"))
		if u, ok := s.util(n); ok {
			nums = append(nums, fmt.Sprintf("%.0f%% of %s", u*100, viz.Human(n.Max, "/s")))
		}
	}
	if low, hot, over := t.levelsOf(t.sel, n); low+hot+over > 0 {
		nums = append(nums, sDim.Render(fmt.Sprintf("low %s hot %s over %s", viz.Human(low, "/s"), viz.Human(hot, "/s"), viz.Human(over, "/s"))))
	}
	if s != nil {
		if s.has&hasOps != 0 {
			nums = append(nums, fmt.Sprintf("%d connections", s.conns))
		}
		if s.has&hasOffered != 0 {
			nums = append(nums, fmt.Sprintf("asked %s by %d/%d generators", viz.Human(s.offered, "/s"), s.running, s.gens))
		}
		if s.has&hasLat != 0 {
			nums = append(nums, "latency "+ms(s.lat))
		}
		if s.has&hasErrs != 0 {
			nums = append(nums, fmt.Sprintf("errors %s (%.2f%%)", viz.Human(s.errs, "/s"), s.errRatio()*100))
		}
		if s.has&hasBacklog != 0 {
			nums = append(nums, "backlog "+viz.Human(s.backlog, ""))
		}
	}
	line1 := " " + title + "  " + strings.Join(nums, sDim.Render(" · "))
	var line2 string
	if s != nil && len(s.hist) > 1 {
		line2 = " " + viz.Sparkline(s.hist, min(60, w/3), viz.Palette[0]) + " "
	}
	switch {
	case len(why) > 0 && st >= stHot:
		col := sAmber
		if st == stOver {
			col = sRed
		}
		line2 += col.Render("⚠ " + strings.Join(why, " · "))
	case len(why) > 0:
		line2 += sDim.Render(strings.Join(why, " · "))
	case s != nil && s.err != "":
		line2 += sRed.Render(s.err)
	default:
		line2 += sDim.Render(firstLine(n.Help))
	}
	target := firstNonEmpty(n.Open, t.sel)
	what := "opens " + target
	if m.app.Spec.Services[target] != nil {
		what = "selects " + target + " on Services"
	}
	line3 := sDim.Render(" enter or click " + what + " · esc or backspace there comes back · t sets low/hot/over")
	return strings.Join([]string{sDim.Render(strings.Repeat("─", w)), truncate(line1, w), truncate(line2, w), truncate(line3, w)}, "\n")
}

func (t *flowTab) aiContext(m *model) string {
	var b strings.Builder
	for _, n := range flowOrder(t.flow(m)) {
		st, why := t.state(m, n)
		r, _ := t.stats[n].shown()
		fmt.Fprintf(&b, "%s: %s state=%s %s\n", n, viz.Human(r, "/s"), []string{"unknown", "idle", "low", "ok", "hot", "overloaded"}[st], strings.Join(why, "; "))
	}
	return "picked node: " + t.sel + "\n" + b.String()
}
