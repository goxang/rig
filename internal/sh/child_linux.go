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
