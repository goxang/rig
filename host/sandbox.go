package host

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
)

// SandboxExe is the rig binary that runs `rig __sandbox`; empty starts plugins directly.
var SandboxExe string

type sandboxSpec struct {
	ro, rw []string
	ports  []uint16
}

func (s sandboxSpec) args() []string {
	var out []string
	for _, p := range s.ro {
		out = append(out, "--ro", p)
	}
	for _, p := range s.rw {
		out = append(out, "--rw", p)
	}
	for _, p := range s.ports {
		out = append(out, "--port", strconv.Itoa(int(p)))
	}
	return out
}

// unconfined is a sandbox that would restrict nothing (fs: / and any host), so it is not worth an exec.
func (s sandboxSpec) unconfined() bool {
	return len(s.ports) == 0 && slices.Contains(s.rw, "/")
}

type listFlag []string

func (l *listFlag) String() string     { return fmt.Sprint(*l) }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// Sandbox is `rig __sandbox [--ro dir] [--rw dir] [--port n] -- command...`: it confines itself, then
// becomes the command.
func Sandbox(args []string) error {
	fs := flag.NewFlagSet("__sandbox", flag.ContinueOnError)
	var s sandboxSpec
	var ports listFlag
	fs.Var((*listFlag)(&s.ro), "ro", "readable path")
	fs.Var((*listFlag)(&s.rw), "rw", "writable path")
	fs.Var(&ports, "port", "TCP port it may connect to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd := fs.Args()
	if len(cmd) == 0 {
		return errors.New("__sandbox: no command")
	}
	for _, p := range ports {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return fmt.Errorf("__sandbox: port %q", p)
		}
		s.ports = append(s.ports, uint16(n))
	}
	if err := confine(s); err != nil {
		return fmt.Errorf("__sandbox: %w", err)
	}
	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		return err
	}
	return syscall.Exec(bin, cmd, os.Environ())
}

// Sandboxed says whether plugins run confined on this OS.
func Sandboxed() bool { return sandboxed }
