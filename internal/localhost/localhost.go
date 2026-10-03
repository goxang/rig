// Package localhost describes this machine as a host and probes local HTTP endpoints;
// runtimes that run on this machine (local processes, docker) embed Host.
package localhost

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
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
	mem := meminfo()
	h.MemTotal = mem["MemTotal"]
	h.MemUsed = mem["MemTotal"] - mem["MemAvailable"]
	h.CPUUsed = cpuBusy(ctx)
	return []core.Host{h}, nil
}

func (Host) Shell(ctx context.Context, _ string, command []string) (*exec.Cmd, error) {
	if len(command) > 0 {
		return exec.CommandContext(ctx, "sh", "-c", strings.Join(command, " ")), nil
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return exec.CommandContext(ctx, sh, "-l"), nil
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
