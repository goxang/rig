package tui

import (
	"math"
	"strings"
	"time"
)

// timeWindow is a chart's time range, Grafana style: a preset ("last 15m") or, after a drag across
// a plot or a shift, a fixed window; zooms stack so zooming out goes back one at a time.
type timeWindow struct {
	presets []time.Duration
	rng     int
	zooms   [][2]time.Time
	// the drag across a plot: band is [a, b] as fractions of the plot's time axis
	plot     string
	band     [2]float64
	dragging bool
}

func (w *timeWindow) zoomed() bool { return len(w.zooms) > 0 }

// span is the window to show at now.
func (w *timeWindow) span(now time.Time) (time.Time, time.Time) {
	if w.zoomed() {
		z := w.zooms[len(w.zooms)-1]
		return z[0], z[1]
	}
	return now.Add(-w.presets[w.rng]), now
}

func (w *timeWindow) label() string {
	if !w.zoomed() {
		return "last " + rangeText(w.presets[w.rng])
	}
	from, to := w.span(time.Now())
	f := "15:04:05"
	if to.Sub(from) >= time.Hour {
		f = "15:04"
	}
	if from.YearDay() != to.YearDay() || from.YearDay() != time.Now().YearDay() {
		f = "Jan 2 " + f
	}
	return from.Format(f) + " → " + to.Format(f)
}

func (w *timeWindow) preset(i int) {
	w.rng, w.zooms = (i+len(w.presets))%len(w.presets), nil
}

// zoomOut pops the last zoom, or widens the preset when there is none; false when it is the widest.
func (w *timeWindow) zoomOut() bool {
	if w.zoomed() {
		w.zooms = w.zooms[:len(w.zooms)-1]
		return true
	}
	if w.rng+1 < len(w.presets) {
		w.rng++
		return true
	}
	return false
}

// shift moves the window by half its width, back (dir < 0) or forward; forward past now goes live again.
func (w *timeWindow) shift(dir int) {
	now := time.Now()
	from, to := w.span(now)
	d := to.Sub(from) / 2
	if dir < 0 {
		d = -d
	}
	from, to = from.Add(d), to.Add(d)
	if now.Sub(to) < d.Abs()/4 {
		w.zooms = nil
		return
	}
	w.zooms = append(w.zooms, [2]time.Time{from, to})
}

// zoomAround narrows (in) or widens the window around fraction f of [from, to].
func (w *timeWindow) zoomAround(f float64, in bool, from, to time.Time) {
	if !in {
		w.zoomOut()
		return
	}
	span := to.Sub(from)
	at := from.Add(time.Duration(f * float64(span)))
	half := span / 4
	if half < 5*time.Second {
		return
	}
	w.zooms = append(w.zooms, [2]time.Time{at.Add(-half), at.Add(half)})
}

// drag takes a press, moves and the release on a plot (frac across its time axis, [from, to] its
// range); a release that covered enough of it zooms in and reports true.
func (w *timeWindow) drag(plot string, frac float64, phase dragPhase, from, to time.Time) bool {
	frac = math.Max(0, math.Min(1, frac))
	switch phase {
	case dragPress:
		w.plot, w.band, w.dragging = plot, [2]float64{frac, frac}, true
	case dragMove:
		if w.dragging && w.plot == plot {
			w.band[1] = frac
		}
	case dragRelease:
		if !w.dragging {
			return false
		}
		w.dragging = false
		a, b := math.Min(w.band[0], w.band[1]), math.Max(w.band[0], w.band[1])
		if b-a < 0.02 || from.IsZero() {
			return false
		}
		span := float64(to.Sub(from))
		w.zooms = append(w.zooms, [2]time.Time{from.Add(time.Duration(a * span)), from.Add(time.Duration(b * span))})
		return true
	}
	return false
}

// bandOn is the band to shade on the plot whose zone id starts with prefix.
func (w *timeWindow) bandOn(prefix string) [2]float64 {
	if !w.dragging || !strings.HasPrefix(w.plot, prefix) {
		return [2]float64{}
	}
	return [2]float64{math.Min(w.band[0], w.band[1]), math.Max(w.band[0], w.band[1])}
}
