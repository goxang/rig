package scrape

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/goxang/rig/core"
)

// A small PromQL subset, enough for dashboard panels without a Prometheus server:
// selectors with = != =~ !~ matchers, rate/irate/increase and <agg>_over_time over [range],
// sum/avg/min/max/count with by (...) or without (...), topk/bottomk, histogram_quantile,
// sort/sort_desc, abs, clamp_min/clamp_max, vector(n), time(), number literals, + - * /, comparisons
// against a number (filters), or, and parentheses.

type node interface {
	eval(db *store, t time.Time) vector
}

type sample struct {
	labels map[string]string
	v      float64
}

type vector []sample

type matcher struct {
	name, op, value string
	re              *regexp.Regexp
}

func (m matcher) match(v string) bool {
	switch m.op {
	case "=":
		return v == m.value
	case "!=":
		return v != m.value
	case "=~":
		return m.re.MatchString(v)
	case "!~":
		return !m.re.MatchString(v)
	}
	return false
}

type selector struct {
	matchers []matcher
	window   time.Duration
}

type call struct {
	fn  string
	sel *selector
}

type agg struct {
	op      string
	by      []string
	without bool
	param   float64 // k of topk/bottomk
	x       node
}

// fn is a function of a vector and optional number arguments: histogram_quantile, sort, abs, clamp_*.
type fn struct {
	name string
	args []float64
	x    node
}

func scalar(n node) bool {
	switch n.(type) {
	case number, now:
		return true
	}
	return false
}

// now is time(): the evaluation time in Unix seconds.
type now struct{}

func (now) eval(_ *store, t time.Time) vector {
	return vector{{labels: map[string]string{}, v: float64(t.UnixNano()) / 1e9}}
}

// or is the union of two vectors, the left side winning on equal label sets.
type or struct{ l, r node }

type number float64

type binary struct {
	op   byte
	l, r node
}

func (n number) eval(*store, time.Time) vector {
	return vector{{labels: map[string]string{}, v: float64(n)}}
}

func (s *selector) eval(db *store, t time.Time) vector {
	var out vector
	for _, sr := range db.match(s.matchers) {
		if p, ok := sr.at(t, db.stale); ok {
			out = append(out, sample{labels: withoutName(sr.labels), v: p.V})
		}
	}
	return out
}

func (c *call) eval(db *store, t time.Time) vector {
	w := c.sel.window
	if w == 0 {
		w = time.Minute
	}
	var out vector
	for _, sr := range db.match(c.sel.matchers) {
		pts := sr.between(t.Add(-w), t)
		if strings.HasSuffix(c.fn, "_over_time") {
			if len(pts) > 0 {
				out = append(out, sample{labels: withoutName(sr.labels), v: overTime(c.fn, pts)})
			}
			continue
		}
		if len(pts) < 2 {
			continue
		}
		first, last := pts[0], pts[len(pts)-1]
		if c.fn == "irate" {
			first = pts[len(pts)-2]
		}
		var inc float64
		prev := first.V
		for _, p := range pts {
			if p.T.Before(first.T) {
				continue
			}
			if p.V < prev {
				inc += p.V // counter reset
			} else {
				inc += p.V - prev
			}
			prev = p.V
		}
		secs := last.T.Sub(first.T).Seconds()
		if secs <= 0 {
			continue
		}
		v := inc / secs
		if c.fn == "increase" {
			v = inc * w.Seconds() / secs
		}
		out = append(out, sample{labels: withoutName(sr.labels), v: v})
	}
	return out
}

func (a *agg) group(l map[string]string) map[string]string {
	out := map[string]string{}
	if a.without {
		for k, v := range l {
			if !slices.Contains(a.by, k) {
				out[k] = v
			}
		}
		return out
	}
	for _, b := range a.by {
		if v, ok := l[b]; ok {
			out[b] = v
		}
	}
	return out
}

func (a *agg) eval(db *store, t time.Time) vector {
	in := a.x.eval(db, t)
	if a.op == "topk" || a.op == "bottomk" {
		return a.top(in)
	}
	groups := map[string]*sample{}
	counts := map[string]int{}
	var order []string
	for _, s := range in {
		lbl := a.group(s.labels)
		k := key(lbl)
		g, ok := groups[k]
		if !ok {
			g = &sample{labels: lbl, v: s.v}
			if a.op == "count" {
				g.v = 0
			}
			groups[k] = g
			order = append(order, k)
		} else {
			switch a.op {
			case "sum", "avg":
				g.v += s.v
			case "min":
				g.v = math.Min(g.v, s.v)
			case "max":
				g.v = math.Max(g.v, s.v)
			}
		}
		counts[k]++
	}
	out := make(vector, 0, len(order))
	for _, k := range order {
		g := groups[k]
		switch a.op {
		case "avg":
			g.v /= float64(counts[k])
		case "count":
			g.v = float64(counts[k])
		}
		out = append(out, *g)
	}
	return out
}

// top keeps the k largest (topk) or smallest (bottomk) samples of each group, with their labels.
func (a *agg) top(in vector) vector {
	groups := map[string]vector{}
	var order []string
	for _, s := range in {
		if math.IsNaN(s.v) {
			continue
		}
		k := key(a.group(s.labels))
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}
	var out vector
	for _, k := range order {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool {
			if a.op == "topk" {
				return g[i].v > g[j].v
			}
			return g[i].v < g[j].v
		})
		out = append(out, g[:min(len(g), int(a.param))]...)
	}
	return out
}

func overTime(name string, pts []core.Point) float64 {
	v := pts[0].V
	switch name {
	case "count_over_time":
		return float64(len(pts))
	case "last_over_time":
		return pts[len(pts)-1].V
	}
	sum := 0.0
	for _, p := range pts {
		sum += p.V
		switch name {
		case "max_over_time":
			v = math.Max(v, p.V)
		case "min_over_time":
			v = math.Min(v, p.V)
		}
	}
	switch name {
	case "sum_over_time":
		return sum
	case "avg_over_time":
		return sum / float64(len(pts))
	}
	return v
}

func (f *fn) eval(db *store, t time.Time) vector {
	in := f.x.eval(db, t)
	switch f.name {
	case "histogram_quantile":
		return quantile(f.args[0], in)
	case "sort", "sort_desc":
		out := slices.Clone(in)
		sort.SliceStable(out, func(i, j int) bool {
			if f.name == "sort" {
				return out[i].v < out[j].v
			}
			return out[i].v > out[j].v
		})
		return out
	}
	out := make(vector, 0, len(in))
	for _, s := range in {
		v := s.v
		switch f.name {
		case "abs":
			v = math.Abs(v)
		case "clamp_min":
			v = math.Max(v, f.args[0])
		case "clamp_max":
			v = math.Min(v, f.args[0])
		}
		out = append(out, sample{labels: s.labels, v: v})
	}
	return out
}

// quantile is Prometheus' histogram_quantile: buckets grouped by every label but le, the quantile
// interpolated linearly inside the bucket it falls in.
func quantile(q float64, in vector) vector {
	type bucket struct{ le, n float64 }
	groups := map[string][]bucket{}
	labels := map[string]map[string]string{}
	var order []string
	for _, s := range in {
		le, err := strconv.ParseFloat(s.labels["le"], 64)
		if err != nil {
			continue
		}
		l := map[string]string{}
		for k, v := range s.labels {
			if k != "le" {
				l[k] = v
			}
		}
		k := key(l)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
			labels[k] = l
		}
		groups[k] = append(groups[k], bucket{le, s.v})
	}
	var out vector
	for _, k := range order {
		bs := groups[k]
		sort.Slice(bs, func(i, j int) bool { return bs[i].le < bs[j].le })
		total := bs[len(bs)-1].n
		if len(bs) < 2 || !math.IsInf(bs[len(bs)-1].le, 1) || total == 0 {
			continue
		}
		rank := q * total
		v := math.NaN()
		for i, b := range bs {
			if b.n < rank {
				continue
			}
			switch {
			case math.IsInf(b.le, 1):
				v = bs[i-1].le
			case i == 0:
				v = b.le * rank / max(b.n, 1e-300)
			default:
				lo, prev := bs[i-1].le, bs[i-1].n
				v = lo + (b.le-lo)*(rank-prev)/max(b.n-prev, 1e-300)
			}
			break
		}
		out = append(out, sample{labels: labels[k], v: v})
	}
	return out
}

func (o *or) eval(db *store, t time.Time) vector {
	l := o.l.eval(db, t)
	seen := map[string]bool{}
	for _, s := range l {
		seen[key(s.labels)] = true
	}
	for _, s := range o.r.eval(db, t) {
		if !seen[key(s.labels)] {
			l = append(l, s)
		}
	}
	return l
}

func (b *binary) eval(db *store, t time.Time) vector {
	l, r := b.l.eval(db, t), b.r.eval(db, t)
	lScalar, rScalar := scalar(b.l), scalar(b.r)
	if cmp := b.op; cmp == '>' || cmp == '<' || cmp == 'g' || cmp == 'l' || cmp == '=' || cmp == '!' {
		return b.filter(l, r, lScalar, rScalar)
	}
	op := func(x, y float64) float64 {
		switch b.op {
		case '+':
			return x + y
		case '-':
			return x - y
		case '*':
			return x * y
		}
		if y == 0 {
			return math.NaN()
		}
		return x / y
	}
	var out vector
	switch {
	case lScalar && len(l) == 1:
		for _, s := range r {
			out = append(out, sample{labels: s.labels, v: op(l[0].v, s.v)})
		}
	case rScalar && len(r) == 1:
		for _, s := range l {
			out = append(out, sample{labels: s.labels, v: op(s.v, r[0].v)})
		}
	default:
		idx := map[string]float64{}
		for _, s := range r {
			idx[key(s.labels)] = s.v
		}
		for _, s := range l {
			if y, ok := idx[key(s.labels)]; ok {
				out = append(out, sample{labels: s.labels, v: op(s.v, y)})
			}
		}
	}
	return out
}

// filter keeps the samples a comparison against a number holds for (g >=, l <=, = ==, ! !=).
func (b *binary) filter(l, r vector, lScalar, rScalar bool) vector {
	holds := func(x, y float64) bool {
		switch b.op {
		case '>':
			return x > y
		case '<':
			return x < y
		case 'g':
			return x >= y
		case 'l':
			return x <= y
		case '=':
			return x == y
		}
		return x != y
	}
	var out vector
	switch {
	case rScalar && len(r) == 1:
		for _, s := range l {
			if holds(s.v, r[0].v) {
				out = append(out, s)
			}
		}
	case lScalar && len(l) == 1:
		for _, s := range r {
			if holds(l[0].v, s.v) {
				out = append(out, s)
			}
		}
	default:
		idx := map[string]float64{}
		for _, s := range r {
			idx[key(s.labels)] = s.v
		}
		for _, s := range l {
			if y, ok := idx[key(s.labels)]; ok && holds(s.v, y) {
				out = append(out, s)
			}
		}
	}
	return out
}

func withoutName(l map[string]string) map[string]string {
	out := make(map[string]string, len(l))
	for k, v := range l {
		if k != "__name__" {
			out[k] = v
		}
	}
	return out
}

func key(l map[string]string) string {
	ks := make([]string, 0, len(l))
	for k := range l {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b strings.Builder
	for _, k := range ks {
		b.WriteString(k + "=" + l[k] + ",")
	}
	return b.String()
}

// ---- parser ----

type parser struct {
	s   string
	pos int
}

func parse(s string) (node, error) {
	p := &parser{s: s}
	n, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("unexpected %q at %d", p.s[p.pos:], p.pos)
	}
	return n, nil
}

func prec(op byte) int {
	switch op {
	case 'o':
		return 1
	case '>', '<', 'g', 'l', '=', '!':
		return 2
	case '+', '-':
		return 3
	case '*', '/':
		return 4
	}
	return 0
}

// operator reads the binary operator at the cursor as one byte (g >=, l <=, = ==, ! !=, o or)
// and its length; 0 when there is none.
func (p *parser) operator() (byte, int) {
	rest := p.s[p.pos:]
	for _, o := range []struct {
		text string
		op   byte
	}{{">=", 'g'}, {"<=", 'l'}, {"==", '='}, {"!=", '!'}, {">", '>'}, {"<", '<'}, {"+", '+'}, {"-", '-'}, {"*", '*'}, {"/", '/'}} {
		if strings.HasPrefix(rest, o.text) {
			return o.op, len(o.text)
		}
	}
	if strings.HasPrefix(rest, "or") && (len(rest) == 2 || !isIdent(rest[2])) {
		return 'o', 2
	}
	return 0, 0
}

func isIdent(c byte) bool {
	return c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func (p *parser) expr(min int) (node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		p.space()
		if p.pos >= len(p.s) {
			return l, nil
		}
		op, n := p.operator()
		if op == 0 || prec(op) <= min {
			return l, nil
		}
		p.pos += n
		r, err := p.expr(prec(op))
		if err != nil {
			return nil, err
		}
		if op == 'o' {
			l = &or{l: l, r: r}
		} else {
			l = &binary{op: op, l: l, r: r}
		}
	}
}

// numberArg reads "<number>," at the start of a function's arguments.
func (p *parser) numberArg() (float64, error) {
	n, err := p.expr(0)
	if err != nil {
		return 0, err
	}
	v, ok := n.(number)
	if !ok {
		return 0, fmt.Errorf("expected a number at %d", p.pos)
	}
	return float64(v), p.expect(',')
}

func (p *parser) grouping(a *agg) error {
	p.space()
	switch {
	case strings.HasPrefix(p.s[p.pos:], "by"):
		p.pos += 2
	case strings.HasPrefix(p.s[p.pos:], "without"):
		p.pos += 7
		a.without = true
	default:
		return nil
	}
	by, err := p.labelList()
	a.by = by
	return err
}

func (p *parser) unary() (node, error) {
	p.space()
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("query %q ends where a value was expected", p.s)
	}
	c := p.s[p.pos]
	if c == '(' {
		p.pos++
		n, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		return n, nil
	}
	if c == '-' || c == '.' || c >= '0' && c <= '9' {
		start := p.pos
		p.pos++
		for p.pos < len(p.s) && (p.s[p.pos] == '.' || p.s[p.pos] == 'e' || p.s[p.pos] >= '0' && p.s[p.pos] <= '9') {
			p.pos++
		}
		f, err := strconv.ParseFloat(p.s[start:p.pos], 64)
		if err != nil {
			return nil, err
		}
		return number(f), nil
	}
	if c == '{' {
		return p.selector("")
	}
	name := p.ident()
	if name == "" {
		return nil, fmt.Errorf("unexpected %q at %d", string(c), p.pos)
	}
	switch name {
	case "sum", "avg", "min", "max", "count", "topk", "bottomk":
		a := &agg{op: name}
		if err := p.grouping(a); err != nil {
			return nil, err
		}
		if err := p.expect('('); err != nil {
			return nil, err
		}
		if name == "topk" || name == "bottomk" {
			k, err := p.numberArg()
			if err != nil {
				return nil, err
			}
			a.param = k
		}
		x, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		a.x = x
		if a.by == nil && !a.without {
			if err := p.grouping(a); err != nil {
				return nil, err
			}
		}
		return a, nil
	case "rate", "irate", "increase", "avg_over_time", "min_over_time", "max_over_time", "sum_over_time", "count_over_time", "last_over_time":
		if err := p.expect('('); err != nil {
			return nil, err
		}
		sel, err := p.selector(p.ident())
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		return &call{fn: name, sel: sel}, nil
	case "histogram_quantile", "sort", "sort_desc", "abs", "clamp_min", "clamp_max":
		if err := p.expect('('); err != nil {
			return nil, err
		}
		f := &fn{name: name}
		if name == "histogram_quantile" {
			q, err := p.numberArg()
			if err != nil {
				return nil, err
			}
			f.args = []float64{q}
		}
		x, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		f.x = x
		if name == "clamp_min" || name == "clamp_max" {
			if err := p.expect(','); err != nil {
				return nil, err
			}
			n, err := p.expr(0)
			if err != nil {
				return nil, err
			}
			v, ok := n.(number)
			if !ok {
				return nil, fmt.Errorf("%s: expected a number at %d", name, p.pos)
			}
			f.args = []float64{float64(v)}
		}
		return f, p.expect(')')
	case "time":
		if err := p.expect('('); err != nil {
			return nil, err
		}
		return now{}, p.expect(')')
	case "vector":
		if err := p.expect('('); err != nil {
			return nil, err
		}
		n, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if _, ok := n.(number); !ok {
			return nil, fmt.Errorf("vector: expected a number at %d", p.pos)
		}
		return n, p.expect(')')
	}
	return p.selector(name)
}

func (p *parser) selector(name string) (*selector, error) {
	s := &selector{}
	if name != "" {
		s.matchers = append(s.matchers, matcher{name: "__name__", op: "=", value: name})
	}
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == '{' {
		p.pos++
		for {
			p.space()
			if p.pos < len(p.s) && p.s[p.pos] == '}' {
				p.pos++
				break
			}
			l := p.ident()
			p.space()
			var op string
			for _, o := range []string{"=~", "!~", "!=", "="} {
				if strings.HasPrefix(p.s[p.pos:], o) {
					op = o
					p.pos += len(o)
					break
				}
			}
			if l == "" || op == "" {
				return nil, fmt.Errorf("bad matcher at %d", p.pos)
			}
			p.space()
			v, err := p.str()
			if err != nil {
				return nil, err
			}
			m := matcher{name: l, op: op, value: v}
			if op == "=~" || op == "!~" {
				if m.re, err = regexp.Compile("^(?:" + v + ")$"); err != nil {
					return nil, err
				}
			}
			s.matchers = append(s.matchers, m)
			p.space()
			if p.pos < len(p.s) && p.s[p.pos] == ',' {
				p.pos++
			}
		}
	}
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == '[' {
		end := strings.IndexByte(p.s[p.pos:], ']')
		if end < 0 {
			return nil, fmt.Errorf("%q: range selector opened with [ but never closed with ]", p.s)
		}
		d, err := parseDuration(p.s[p.pos+1 : p.pos+end])
		if err != nil {
			return nil, err
		}
		s.window = d
		p.pos += end + 1
	}
	if len(s.matchers) == 0 {
		return nil, fmt.Errorf("%q: selector has no label matchers, e.g. {job=\"x\"}", p.s)
	}
	return s, nil
}

func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(s)
}

func (p *parser) labelList() ([]string, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	var out []string
	for {
		p.space()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
			return out, nil
		}
		l := p.ident()
		if l == "" {
			return nil, fmt.Errorf("bad label list at %d", p.pos)
		}
		out = append(out, l)
		p.space()
		if p.pos < len(p.s) && p.s[p.pos] == ',' {
			p.pos++
		}
	}
}

func (p *parser) str() (string, error) {
	if p.pos >= len(p.s) || p.s[p.pos] != '"' && p.s[p.pos] != '\'' {
		return "", fmt.Errorf("expected string at %d", p.pos)
	}
	q := p.s[p.pos]
	end := strings.IndexByte(p.s[p.pos+1:], q)
	if end < 0 {
		return "", fmt.Errorf("%q: string starting at %d has no closing %c", p.s, p.pos, q)
	}
	v := p.s[p.pos+1 : p.pos+1+end]
	p.pos += end + 2
	return v, nil
}

func (p *parser) ident() string {
	p.space()
	start := p.pos
	for p.pos < len(p.s) {
		r := rune(p.s[p.pos])
		if r == '_' || r == ':' || unicode.IsLetter(r) || p.pos > start && unicode.IsDigit(r) {
			p.pos++
			continue
		}
		break
	}
	return p.s[start:p.pos]
}

func (p *parser) expect(c byte) error {
	p.space()
	if p.pos >= len(p.s) || p.s[p.pos] != c {
		return fmt.Errorf("expected %q at %d", string(c), p.pos)
	}
	p.pos++
	return nil
}

func (p *parser) space() {
	for p.pos < len(p.s) && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}
