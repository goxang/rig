// Package localhost describes this machine as a host and probes local HTTP endpoints;
// runtimes that run on this machine (local processes, docker) embed Host.
package localhost

import (
	"bufio"
	"cmp"
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
)


type Host struct{}

func (Host) Hosts(ctx context.Context) ([]core.Host, error) {
	name, _ := os.Hostname()
	h := core.Host{Name: name, Addr: "127.0.0.1", Roles: []string{"local"}, Ready: true, OS: runtime.GOOS, CPUs: runtime.NumCPU()}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		h.Load1, _ = strconv.ParseFloat(strings.Fields(string(raw))[0], 64)
	}
	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		h.Kernel = strings.TrimSpace(string(raw))
	}
	if runtime.GOOS == "darwin" {
		darwinStats(ctx, &h)
		return []core.Host{h}, nil
	}
	mem := meminfo()
	h.MemTotal = mem["MemTotal"]
	h.MemUsed = mem["MemTotal"] - mem["MemAvailable"]
	h.CPUUsed = cpuBusy(ctx)
	return []core.Host{h}, nil
}

// darwinStats reads what /proc gives on Linux from sysctl, vm_stat and ps.
func darwinStats(ctx context.Context, h *core.Host) {
	out := func(name string, args ...string) string {
		raw, _ := exec.CommandContext(ctx, name, args...).Output()
		return strings.TrimSpace(string(raw))
	}
	if f := strings.Fields(strings.Trim(out("sysctl", "-n", "vm.loadavg"), "{ }")); len(f) > 0 {
		h.Load1, _ = strconv.ParseFloat(f[0], 64)
	}
	h.Kernel = out("uname", "-r")
	h.MemTotal, _ = strconv.ParseInt(out("sysctl", "-n", "hw.memsize"), 10, 64)
	page, pages := int64(4096), map[string]int64{}
	for _, l := range strings.Split(out("vm_stat"), "\n") {
		if strings.Contains(l, "page size of") {
			if f := strings.Fields(l); len(f) >= 8 {
				page, _ = strconv.ParseInt(f[7], 10, 64)
			}
		}
		if k, v, ok := strings.Cut(l, ":"); ok {
			pages[k], _ = strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		}
	}
	free := (pages["Pages free"] + pages["Pages inactive"] + pages["Pages speculative"]) * page
	if h.MemTotal > 0 && free > 0 {
		h.MemUsed = h.MemTotal - free
	}
	var total float64
	for _, l := range strings.Split(out("ps", "-A", "-o", "%cpu="), "\n") {
		v, _ := strconv.ParseFloat(strings.TrimSpace(l), 64)
		total += v
	}
	h.CPUUsed = min(100, total/float64(max(1, h.CPUs)))
}

func (Host) Shell(ctx context.Context, _ string, command []string) (*exec.Cmd, error) {
	if len(command) > 0 {
		return exec.CommandContext(ctx, sh.Shell(), "-c", strings.Join(command, " ")), nil
	}
	login := os.Getenv("SHELL")
	switch {
	case login != "":
	case runtime.GOOS == "windows":
		return exec.CommandContext(ctx, cmp.Or(os.Getenv("COMSPEC"), "cmd.exe")), nil
	default:
		login = "/bin/sh"
	}
	return exec.CommandContext(ctx, login, "-l"), nil
}

func meminfo() map[string]int64 {
	out := map[string]int64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
		out[k] = n * 1024
	}
	return out
}

// cpuBusy samples /proc/stat twice, 200ms apart.
func cpuBusy(ctx context.Context) float64 {
	a, ok := cpuTimes()
	if !ok {
		return 0
	}
	select {
	case <-ctx.Done():
		return 0
	case <-time.After(200 * time.Millisecond):
	}
	b, _ := cpuTimes()
	total, idle := b[0]-a[0], b[1]-a[1]
	if total <= 0 {
		return 0
	}
	return 1 - idle/total
}

func cpuTimes() ([2]float64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return [2]float64{}, false
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	f := strings.Fields(line)
	var total, idle float64
	for i, v := range f[1:] {
		n, _ := strconv.ParseFloat(v, 64)
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return [2]float64{total, idle}, true
}

var client = &http.Client{Timeout: 2 * time.Second}

// Probe reports whether url answers with a status below 500.
func Probe(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
