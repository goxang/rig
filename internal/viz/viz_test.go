package viz

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/MohammadmahdiAhmadi/rig/core"
)

func TestLineChartFitsItsBox(t *testing.T) {
	t0 := time.Unix(0, 0)
	var a, b []core.Point
	for i := 0; i < 50; i++ {
		a = append(a, core.Point{T: t0.Add(time.Duration(i) * time.Second), V: float64(i * i)})
		b = append(b, core.Point{T: t0.Add(time.Duration(i) * time.Second), V: float64(100 - i)})
	}
	for _, size := range [][2]int{{40, 8}, {100, 20}, {23, 5}} {
		out := LineChart([]Line{{Name: "a", Points: a}, {Name: "b", Points: b}}, size[0], size[1], "/s")
		lines := strings.Split(out, "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for _, l := range lines {
			if lipgloss.Width(l) > size[0] {
				t.Errorf("%v: line %d wide: %q", size, lipgloss.Width(l), l)
			}
		}
		if !strings.ContainsAny(out, "⠁⠂⠄⡀⠈⠐⠠⢀⣀⣿") && !strings.ContainsRune(out, 0x2800) {
			t.Errorf("%v: no braille drawn", size)
		}
	}
}

func TestHuman(t *testing.T) {
	for _, c := range []struct {
		v    float64
		unit string
		want string
	}{{1234, "/s", "1.23k/s"}, {0.25, "s", "250ms"}, {2048, "bytes", "2KiB"}, {0, "", "0"}, {12.5, "%", "12.5%"}} {
		if got := Human(c.v, c.unit); got != c.want {
			t.Errorf("Human(%v, %q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
}

func TestWaterfallNests(t *testing.T) {
	t0 := time.Unix(0, 0)
	spans := []core.Span{
		{ID: "1", Service: "gw", Name: "GET /", Start: t0, Duration: 10 * time.Millisecond},
		{ID: "2", Parent: "1", Service: "api", Name: "handle", Start: t0.Add(time.Millisecond), Duration: 8 * time.Millisecond},
		{ID: "3", Parent: "2", Service: "db", Name: "query", Start: t0.Add(2 * time.Millisecond), Duration: 3 * time.Millisecond, Error: true},
	}
	out := Waterfall(spans, 100)
	if !strings.Contains(out, "    db: query") || !strings.Contains(out, "3 spans") {
		t.Errorf("waterfall:\n%s", out)
	}
}
