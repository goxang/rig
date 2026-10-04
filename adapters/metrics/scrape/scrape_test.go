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
	if v := eval(t, db, `count({__name__="req_total"})`); len(v) != 1 || v[0].v != 2 {
		t.Errorf("count = %v", v)
	}
	for _, bad := range []string{`sum(`, `rate(x[1m]`, `x{a=}`, `1 +`} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestEvalFunctions(t *testing.T) {
	db := fill()
	t0 := time.Unix(1000, 0)
	for i := 0; i <= 12; i++ {
		ts := t0.Add(time.Duration(i) * 5 * time.Second)
		// 10 observations/s: 6 under 0.1, 9 under 0.5, all under +Inf
		for le, per := range map[string]float64{"0.1": 6, "0.5": 9, "+Inf": 10} {
			db.add(map[string]string{"__name__": "lat_bucket", "svc": "a", "le": le}, ts, float64(i*5)*per)
		}
	}
	v := eval(t, db, `histogram_quantile(0.5, sum by (le) (rate(lat_bucket[1m])))`)
	if len(v) != 1 || !approx(v[0].v, 0.1*5/6) {
		t.Errorf("p50 = %v", v)
	}
	if v := eval(t, db, `histogram_quantile(0.99, sum by (le, svc) (rate(lat_bucket[1m])))`); len(v) != 1 || v[0].v != 0.5 || v[0].labels["svc"] != "a" {
		t.Errorf("p99 in the +Inf bucket = %v", v)
	}
	if v := eval(t, db, `topk(1, sum by (svc) (rate(req_total[1m])))`); len(v) != 1 || v[0].labels["svc"] != "a" {
		t.Errorf("topk = %v", v)
	}
	if v := eval(t, db, `bottomk(1, sum by (svc) (rate(req_total[1m])))`); len(v) != 1 || v[0].labels["svc"] != "b" {
		t.Errorf("bottomk = %v", v)
	}
	if v := eval(t, db, `sort(sum without (code) (rate(req_total[1m])))`); len(v) != 2 || v[0].labels["svc"] != "b" || v[0].labels["code"] != "" {
		t.Errorf("sort without = %v", v)
	}
	if v := eval(t, db, `sum by (svc) (rate(req_total[1m])) > 5`); len(v) != 1 || v[0].labels["svc"] != "a" {
		t.Errorf("filter = %v", v)
	}
	if v := eval(t, db, `sum(rate(nothing[1m])) or vector(0)`); len(v) != 1 || v[0].v != 0 {
		t.Errorf("or vector = %v", v)
	}
	if v := eval(t, db, `max_over_time(temp[1m])`); len(v) != 1 || v[0].v != 21.5 {
		t.Errorf("max_over_time = %v", v)
	}
	if v := eval(t, db, `clamp_max(temp, 10)`); len(v) != 1 || v[0].v != 10 {
		t.Errorf("clamp_max = %v", v)
	}
	if v := eval(t, db, `sum(order_total) or temp`); len(v) != 1 {
		t.Errorf("or after a name starting with or = %v", v)
	}
}

func TestTime(t *testing.T) {
	db := fill()
	if v := eval(t, db, `time() - temp`); len(v) != 1 || !approx(v[0].v, 1060-21.5) {
		t.Errorf("time() - x = %v", v)
	}
}
