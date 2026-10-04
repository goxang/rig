package engine

import (
	"testing"

	"github.com/goxang/rig/core"
)

func TestSummarise(t *testing.T) {
	var ps []core.Point
	for _, v := range []float64{4, 1, 3, 2, 10} {
		ps = append(ps, core.Point{V: v})
	}
	want := map[string]float64{"avg": 4, "min": 1, "max": 10, "last": 10, "p50": 3, "p90": 10}
	for st, w := range want {
		if got, ok := summarise(ps, st); !ok || got != w {
			t.Errorf("%s = %v, want %v", st, got, w)
		}
	}
	if _, ok := summarise(ps, "bogus"); ok {
		t.Error("bogus stat answered")
	}
}
