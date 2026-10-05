//go:build !windows

package sh

import (
	"os/exec"
	"syscall"
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
