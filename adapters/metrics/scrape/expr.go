package scrape

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A small PromQL subset, enough for dashboard panels without a Prometheus server:
// selectors with = != =~ !~ matchers, rate/irate/increase over [range], sum/avg/min/max/count
// with optional by (...), number literals, + - * / and parentheses.

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
	op string
	by []string
	x  node
}

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

func (a *agg) eval(db *store, t time.Time) vector {
	in := a.x.eval(db, t)
	groups := map[string]*sample{}
	counts := map[string]int{}
	var order []string
	for _, s := range in {
		lbl := map[string]string{}
		for _, b := range a.by {
			if v, ok := s.labels[b]; ok {
				lbl[b] = v
			}
		}
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

func (b *binary) eval(db *store, t time.Time) vector {
	l, r := b.l.eval(db, t), b.r.eval(db, t)
	_, lScalar := b.l.(number)
	_, rScalar := b.r.(number)
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
	case '+', '-':
		return 1
	case '*', '/':
		return 2
	}
	return 0
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
		op := p.s[p.pos]
		if prec(op) == 0 || prec(op) <= min {
			return l, nil
		}
		p.pos++
		r, err := p.expr(prec(op))
		if err != nil {
			return nil, err
		}
		l = &binary{op: op, l: l, r: r}
	}
}

func (p *parser) unary() (node, error) {
	p.space()
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("unexpected end")
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
	name := p.ident()
	if name == "" {
		return nil, fmt.Errorf("unexpected %q at %d", string(c), p.pos)
	}
	switch name {
	case "sum", "avg", "min", "max", "count":
		a := &agg{op: name}
		p.space()
		if strings.HasPrefix(p.s[p.pos:], "by") {
			p.pos += 2
			by, err := p.labelList()
			if err != nil {
				return nil, err
			}
			a.by = by
		}
		if err := p.expect('('); err != nil {
			return nil, err
		}
		x, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		a.x = x
		p.space()
		if strings.HasPrefix(p.s[p.pos:], "by") {
			p.pos += 2
			by, err := p.labelList()
			if err != nil {
				return nil, err
			}
			a.by = by
		}
		return a, nil
	case "rate", "irate", "increase":
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
			return nil, fmt.Errorf("unclosed [")
		}
		d, err := parseDuration(p.s[p.pos+1 : p.pos+end])
		if err != nil {
			return nil, err
		}
		s.window = d
		p.pos += end + 1
	}
	if len(s.matchers) == 0 {
		return nil, fmt.Errorf("empty selector")
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
		return "", fmt.Errorf("unclosed string")
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
