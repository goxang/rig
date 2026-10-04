package local

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	metricsMu    sync.Mutex
	metricsFound = map[int]int{} // pid → its metrics port
)

// metricsPort is the port of pid serving Prometheus metrics: want when pid listens there, else the
// first of its listening ports whose path answers like a metrics endpoint. Found ports are cached per pid.
func metricsPort(ctx context.Context, pid, want int, path string) (int, bool) {
	metricsMu.Lock()
	port, ok := metricsFound[pid]
	metricsMu.Unlock()
	if ok {
		return port, true
	}
	ports := listening(pid)
	if len(ports) == 0 || slices.Contains(ports, want) {
		return want, false
	}
	if path == "" {
		path = "/metrics"
	}
	c := http.Client{Timeout: time.Second}
	for _, p := range ports {
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", p, path), nil)
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		head := make([]byte, 4096)
		n, _ := resp.Body.Read(head)
		resp.Body.Close()
		if resp.StatusCode == 200 && (strings.Contains(string(head[:n]), "# TYPE") || strings.Contains(string(head[:n]), "# HELP")) {
			metricsMu.Lock()
			metricsFound[pid] = p
			metricsMu.Unlock()
			return p, true
		}
	}
	return want, false
}

// family is pid and its descendants: a service started under dlv or a wrapper script listens in a child.
func family(pid int) []int {
	out := []int{pid}
	for i := 0; i < len(out); i++ {
		tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", out[i]))
		for _, t := range tasks {
			raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", out[i], t.Name()))
			for _, c := range strings.Fields(string(raw)) {
				if n, err := strconv.Atoi(c); err == nil && !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// listening are the TCP ports pid (or a descendant) listens on (Linux: socket inodes matched in /proc/net/tcp*).
func listening(pid int) []int {
	inodes := map[string]bool{}
	for _, p := range family(pid) {
		fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", p))
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", p), fd.Name()))
			if ino, ok := strings.CutPrefix(link, "socket:["); err == nil && ok {
				inodes[strings.TrimSuffix(ino, "]")] = true
			}
		}
	}
	if len(inodes) == 0 {
		return nil
	}
	var ports []int
	for _, f := range []string{"tcp", "tcp6"} {
		file, err := os.Open(fmt.Sprintf("/proc/%d/net/%s", pid, f))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			// sl local_address rem_address st ... inode
			c := strings.Fields(sc.Text())
			if len(c) < 10 || c[3] != "0A" || !inodes[c[9]] {
				continue
			}
			_, hexPort, _ := strings.Cut(c[1], ":")
			if p, err := strconv.ParseInt(hexPort, 16, 32); err == nil && !slices.Contains(ports, int(p)) {
				ports = append(ports, int(p))
			}
		}
		file.Close()
	}
	slices.Sort(ports)
	return ports
}
