package sh

import (
	"os/exec"
	"syscall"
)

// TiedToParent makes cmd die with this process, even when this process is killed: a port-forward
// must not outlive the rig that opened it.
func TiedToParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// OwnGroup puts cmd in a process group of its own, so KillGroup stops what it started too
// (go test's test binaries).
func OwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
}

// Detached runs cmd in a session of its own, without the terminal: a background task must not
// query the terminal, whose answers would land in the TUI's input. KillGroup stops it.
func Detached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setsid: true}
}

func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
