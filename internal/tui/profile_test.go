package tui

import (
	"testing"

	"github.com/goxang/rig/core"
)

func TestProfView(t *testing.T) {
	top := `File: parsersvc
Type: cpu
Duration: 10s, Total samples = 1.50s (15.00%)
Showing nodes accounting for 1.20s, 80.00% of 1.50s total
      flat  flat%   sum%        cum   cum%
     0.20s 13.33% 13.33%      0.90s 60.00%  runtime.mallocgc
     0.90s 60.00% 73.33%      0.90s 60.00%  encoding/json.(*decodeState).object
    50ms  3.33% 76.67%      0.10s  6.67%  main.(*x).y z
`
	v := newProfView("parser", core.Profile{Kind: "cpu", Summary: top})
	if len(v.head) != 4 || len(v.grid.rows) != 3 {
		t.Fatalf("head %d rows %d", len(v.head), len(v.grid.rows))
	}
	if got := v.grid.rows[0].cells[5]; got != "encoding/json.(*decodeState).object" {
		t.Fatalf("sorted by flat desc first: %q", got)
	}
	if got := v.grid.rows[2].cells[5]; got != "main.(*x).y z" {
		t.Fatalf("function with a space: %q", got)
	}
	for in, want := range map[string]float64{"1.50s": 1.5, "50ms": 0.05, "12MB": 12 << 20, "33.3%": 33.3, "1200": 1200} {
		if got := quantity(in); got != want {
			t.Errorf("quantity(%s) = %v, want %v", in, got, want)
		}
	}
}
