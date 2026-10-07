//go:build !linux

package sh

import "os/exec"

func TiedToParent(*exec.Cmd) {}

func OwnGroup(*exec.Cmd) {}

func Detached(*exec.Cmd) {}

func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
