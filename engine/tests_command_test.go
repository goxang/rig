package engine

import (
	"testing"
	"time"
)

func TestApplyJUnitFromPytest(t *testing.T) {
	raw := `<?xml version="1.0" encoding="utf-8"?><testsuites><testsuite name="pytest" tests="3">
<testcase classname="tests.test_api" name="test_ok" time="0.01"/>
<testcase classname="tests.test_api" name="test_bad[a/b]" time="0.02"><failure message="assert 1 == 2">E   assert 1 == 2</failure></testcase>
<testcase classname="tests.test_db" name="test_later" time="0"><skipped message="needs db"/></testcase>
</testsuite></testsuites>`
	r := &TestRun{index: map[string]*TestCase{}}
	if n := r.applyJUnit([]byte(raw), time.Now()); n != 1 {
		t.Fatalf("failed = %d", n)
	}
	got := map[string]string{}
	for _, c := range r.Tests {
		got[c.Package+" "+c.Name] = c.Status
	}
	want := map[string]string{"tests.test_api ": TestFail, "tests.test_api test_ok": TestPass,
		"tests.test_api test_bad[a∕b]": TestFail, "tests.test_db ": TestPass, "tests.test_db test_later": TestSkip}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}
