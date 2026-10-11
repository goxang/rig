// Package bench checks docs/plan.md section 17's process budgets on a release build; CI's bench job
// runs it with RIG_BENCH=1.
package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// ponytail: status against rig, TUI paint, key to frame and idle CPU are measured by hand on switch
// (docs/plan.md section 17); add them here once CI has a runtime and a pty to drive.
func TestBudgets(t *testing.T) {
	if os.Getenv("RIG_BENCH") == "" {
		t.Skip("RIG_BENCH unset")
	}
	tmp := t.TempDir()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, filepath.Join(tmp, k))
	}
	bin := filepath.Join(tmp, "rig")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "./cmd/rig")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("binary %.1f MB", float64(st.Size())/(1<<20))
	if st.Size() > 30<<20 {
		t.Errorf("binary %d bytes, budget 30 MB", st.Size())
	}

	shop, _ := filepath.Abs("../../examples/shop")
	run := func(args ...string) (time.Duration, int64) {
		c := exec.Command(bin, args...)
		c.Dir = shop
		start := time.Now()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("rig %v: %v\n%s", args, err, out)
		}
		return time.Since(start), c.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
	}
	median := func(args ...string) (time.Duration, int64) {
		var took []time.Duration
		var rss int64
		for range 21 {
			d, r := run(args...)
			took, rss = append(took, d), max(rss, r)
		}
		slices.Sort(took)
		return took[10], rss
	}

	cold, _ := run("lint")
	t.Logf("config load cold %v", cold)
	if cold > 300*time.Millisecond {
		t.Errorf("config load cold %v, budget 300ms", cold)
	}
	for _, b := range []struct {
		args   []string
		budget time.Duration
	}{{[]string{"version"}, 20 * time.Millisecond}, {[]string{"lint"}, 50 * time.Millisecond}} {
		d, rss := median(b.args...)
		t.Logf("rig %v: median %v, RSS %d kB", b.args, d, rss)
		if d > b.budget {
			t.Errorf("rig %v: median %v, budget %v", b.args, d, b.budget)
		}
		if rss > 45<<10 {
			t.Errorf("rig %v: RSS %d kB, budget 45 MB", b.args, rss)
		}
	}
}
