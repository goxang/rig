package tui

import (
	"testing"
	"time"
)

func TestTimeWindowDragZoomAndBack(t *testing.T) {
	w := timeWindow{presets: ranges, rng: 1}
	from := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	to := from.Add(100 * time.Minute)
	w.drag("mplot:0:80", 0.5, dragPress, from, to)
	w.drag("mplot:0:80", 0.2, dragMove, from, to)
	if b := w.bandOn("mplot:0:"); b != [2]float64{0.2, 0.5} {
		t.Fatalf("band %v", b)
	}
	if !w.drag("mplot:0:80", 0.2, dragRelease, from, to) {
		t.Fatal("no zoom")
	}
	if f, e := w.span(time.Now()); !f.Equal(from.Add(20*time.Minute)) || !e.Equal(from.Add(50*time.Minute)) {
		t.Fatalf("window %v %v", f, e)
	}
	w.zoomOut()
	if w.zoomed() || w.rng != 1 {
		t.Fatal("zoom out did not go back to the preset")
	}
	w.drag("mplot:0:80", 0.5, dragPress, from, to)
	if w.drag("mplot:0:80", 0.505, dragRelease, from, to) || w.zoomed() {
		t.Fatal("a click zoomed")
	}
	w.shift(-1)
	w.shift(1)
	if w.zoomed() {
		t.Fatal("shifting back to now should go live")
	}
}
