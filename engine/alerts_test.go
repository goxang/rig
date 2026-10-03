package engine

import (
	"testing"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

func TestAlertLevels(t *testing.T) {
	r := spec.Alert{Warn: 90, Crit: 98}
	for v, want := range map[float64]Level{50: LevelOK, 90: LevelWarn, 97.9: LevelWarn, 98: LevelCrit} {
		if got := level(r, v); got != want {
			t.Errorf("%v: %v, want %v", v, got, want)
		}
	}
	below := spec.Alert{Warn: 2, Crit: 1, Below: true}
	if level(below, 0) != LevelCrit || level(below, 1.5) != LevelWarn || level(below, 3) != LevelOK {
		t.Error("below thresholds")
	}
	vals := hostValues([]core.Host{{Name: "n1", MemTotal: 100, MemUsed: 93, DiskTotal: 0}}, "memory")
	if vals["n1"] != 93 || len(hostValues([]core.Host{{Name: "n1"}}, "disk")) != 0 {
		t.Errorf("host values %v", vals)
	}
}
