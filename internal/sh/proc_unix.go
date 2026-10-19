//go:build unix

package sh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Detach starts cmd in a session of its own, so it outlives rig and a group signal reaches all of it.
func Detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// Alive says whether a process with this pid exists.
func Alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// Interrupt sends pid SIGINT.
func Interrupt(pid int) error { return syscall.Kill(pid, syscall.SIGINT) }

// StopGroup asks the process group led by pid to stop (SIGTERM), or forces it (SIGKILL).
func StopGroup(pid int, force bool) {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pid, sig)
}

// ListenerPIDs are the processes listening on TCP port, rig itself left out.
func ListenerPIDs(ctx context.Context, port int) ([]int, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-t", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && len(out) == 0) { // lsof exits 1 when nothing listens
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, _ := strconv.Atoi(f); pid > 0 && pid != os.Getpid() && !slices.Contains(pids, pid) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// Kill sends pids SIGTERM and SIGKILLs the ones still alive after grace.
func Kill(pids []int, grace time.Duration) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for end := time.Now().Add(grace); time.Now().Before(end) && slices.ContainsFunc(pids, Alive); {
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range pids {
		if Alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
