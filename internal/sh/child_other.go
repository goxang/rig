//go:build !linux

package sh

import "os/exec"

func TiedToParent(*exec.Cmd) {}
