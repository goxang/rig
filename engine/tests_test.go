package engine

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPattern(t *testing.T) {
	p := Pattern([]string{"TestA/x.y", "TestB/x.y", "TestB/z"})
	if p != `^(TestA|TestB)$/^(x\.y|z)$` {
		t.Fatalf("pattern %q", p)
	}
	if Pattern([]string{"TestA"}) != "^TestA$" {
		t.Fatal(Pattern([]string{"TestA"}))
	}
}

func feed(r *TestRun, lines string) {
	for _, l := range strings.Split(strings.TrimSpace(lines), "\n") {
		f := strings.SplitN(l, " ", 4)
		e := testEvent{Action: f[0], Package: f[1]}
		if len(f) > 2 && f[2] != "-" {
			e.Test = f[2]
		}
		if len(f) > 3 {
			e.Output = f[3]
		}
		r.apply(e)
	}
}

func TestRerunFailed(t *testing.T) {
	r := &TestRun{index: map[string]*TestCase{}}
	feed(r, `
run p TestPurchase
run p TestPurchase/generic/confirm
run p TestPurchase/generic/confirm/pos
pause p TestPurchase/generic/confirm/pos
run p TestPurchase/generic/confirm/pos/Issuer.Acquire
fail p TestPurchase/generic/confirm/pos/TransactionStatus
fail p TestPurchase/generic/confirm/pos
pass p TestPurchase/generic/confirm/pos/Issuer.Acquire
fail p TestPurchase/generic/confirm
fail p TestPurchase
run q TestPlain
run q TestPlain/sub
fail q TestPlain/sub
fail q TestPlain
pass q TestOK
output q - BenchmarkX-8   	 1000	  1234 ns/op	  16 B/op	  1 allocs/op
output q - coverage: 42.5% of statements
fail q`)
	jobs := RerunFailed(r, TestOptions{Count: 1})
	if len(jobs) != 2 {
		t.Fatalf("jobs %+v", jobs)
	}
	// the plain test reruns whole (depth 1); the parallel mode of the integration case alone (depth 4)
	if jobs[0].Options.Run != "^TestPlain$" || jobs[0].Options.Packages[0] != "q" {
		t.Errorf("top-level job %+v", jobs[0].Options)
	}
	if jobs[1].Options.Run != "^TestPurchase$/^generic$/^confirm$/^pos$" || jobs[1].Options.Count != 1 {
		t.Errorf("parallel case job %+v", jobs[1].Options)
	}
	if !regexp.MustCompile(`^\^`).MatchString(jobs[1].Options.Run) {
		t.Error("anchored")
	}
	if len(r.Benches) != 1 || r.Benches[0].Metrics["ns/op"] != 1234 || r.Benches[0].Procs != 8 {
		t.Errorf("bench %+v", r.Benches)
	}
	if r.Coverage["q"] != 42.5 {
		t.Errorf("coverage %v", r.Coverage)
	}
	if c := r.Counts(); c[TestFail] != 6 || c[TestPass] != 2 {
		t.Errorf("counts %v", c)
	}
}

func TestExpandQuery(t *testing.T) {
	vars := map[string][]string{"service": {"a", "b"}, "one": {"x"}, "all": {AllValue}}
	q := ExpandQuery(`rate(m{s=~"$service",o="${one}",z=~"$all"}[$__rate_interval])`, vars, time.Hour, 30*time.Second)
	if q != `rate(m{s=~"(a|b)",o="x",z=~".*"}[2m])` {
		t.Fatal(q)
	}
}
