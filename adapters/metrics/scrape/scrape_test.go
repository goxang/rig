package scrape

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	in := `# HELP x
# TYPE http_requests_total counter
http_requests_total{code="200",path="/a b"} 10
http_requests_total{code="500",path="/"} 2 1700000000
go_goroutines 7
weird{l="quote\"d"} 1e3
`
	got := map[string]float64{}
	err := parseExposition(strings.NewReader(in), func(l map[string]string, v float64) { got[key(l)] = v })
	if err != nil {
		t.Fatal(err)
	}
	if got[key(map[string]string{"__name__": "http_requests_total", "code": "200", "path": "/a b"})] != 10 {
		t.Errorf("labels with spaces: %v", got)
	}
	if got[key(map[string]string{"__name__": "go_goroutines"})] != 7 {
		t.Errorf("plain metric: %v", got)
	}
	if got[key(map[string]string{"__name__": "weird", "l": `quote"d`})] != 1000 {
		t.Errorf("escaped label: %v", got)
	}
}

func fill() *store {
	db := &store{series: map[string]*series{}, stale: 15 * time.Second, retention: time.Hour}
	t0 := time.Unix(1000, 0)
	for i := 0; i <= 12; i++ {
		ts := t0.Add(time.Duration(i) * 5 * time.Second)
		// 10/s on a, 2/s on b, and a counter reset on b halfway
		db.add(map[string]string{"__name__": "req_total", "svc": "a", "code": "200"}, ts, float64(i*50))
		bv := float64(i * 10)
		if i > 6 {
			bv = float64((i - 7) * 10)
		}
		db.add(map[string]string{"__name__": "req_total", "svc": "b", "code": "500"}, ts, bv)
		db.add(map[string]string{"__name__": "temp", "svc": "a"}, ts, 21.5)
	}
	return db
}

func eval(t *testing.T, db *store, q string) vector {
	t.Helper()
	n, err := parse(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n.eval(db, time.Unix(1060, 0))
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestEval(t *testing.T) {
	db := fill()
	// b restarts at 0 once, so over the window it grew 100 in 55s; a grew 550 in 55s
	b := 100.0 / 55
	if v := eval(t, db, `sum(rate(req_total[1m]))`); len(v) != 1 || !approx(v[0].v, 10+b) {
		t.Errorf("sum rate = %v", v)
	}
	v := eval(t, db, `sum by (svc) (rate(req_total{code=~"2.."}[30s]))`)
	if len(v) != 1 || v[0].labels["svc"] != "a" || !approx(v[0].v, 10) {
		t.Errorf("by + regex = %v", v)
	}
	if v := eval(t, db, `rate(req_total{svc="b"}[1m])`); len(v) != 1 || !approx(v[0].v, b) {
		t.Errorf("counter reset handling = %v", v)
	}
	if v := eval(t, db, `100 * sum(rate(req_total{code="500"}[1m])) / sum(rate(req_total[1m]))`); len(v) != 1 || !approx(v[0].v, 100*b/(10+b)) {
		t.Errorf("arithmetic = %v", v)
	}
	if v := eval(t, db, `temp{svc!="b"} - 1.5`); len(v) != 1 || !approx(v[0].v, 20) {
		t.Errorf("selector minus scalar = %v", v)
	}
	if v := eval(t, db, `count(req_total)`); len(v) != 1 || v[0].v != 2 {
		t.Errorf("count = %v", v)
	}
	for _, bad := range []string{`sum(`, `rate(x[1m]`, `x{a=}`, `1 +`} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
