// Package sh runs external tools (kubectl, docker, ssh, ...) with errors that carry their stderr.
package sh

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

type Cmd struct {
	Name  string
	Args  []string
	Dir   string
	Env   []string
	Stdin io.Reader
}

func New(name string, args ...string) *Cmd { return &Cmd{Name: name, Args: args} }

func (c *Cmd) cmd(ctx context.Context) *exec.Cmd {
	x := exec.CommandContext(ctx, c.Name, c.Args...)
	TiedToParent(x)
	x.Dir = c.Dir
	if len(c.Env) > 0 {
		x.Env = append(os.Environ(), c.Env...)
	}
	x.Stdin = c.Stdin
	return x
}

// Output runs the command and returns stdout; a failure includes the trimmed stderr.
func (c *Cmd) Output(ctx context.Context) ([]byte, error) {
	x := c.cmd(ctx)
	var stderr bytes.Buffer
	x.Stderr = &stderr
	out, err := x.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", c.Name, short(c.Args), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (c *Cmd) Run(ctx context.Context) error {
	_, err := c.Output(ctx)
	return err
}

// Attach runs the command on the given streams (an interactive shell, a live log).
func (c *Cmd) Attach(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	x := c.cmd(ctx)
	if stdin != nil {
		x.Stdin = stdin
	}
	x.Stdout, x.Stderr = stdout, stderr
	return x.Run()
}

// Lines streams stdout line by line until the command exits or ctx ends; the channel then closes.
func (c *Cmd) Lines(ctx context.Context) (<-chan string, error) {
	x := c.cmd(ctx)
	out, err := x.StdoutPipe()
	if err != nil {
		return nil, err
	}
	x.Stderr = x.Stdout
	if err := x.Start(); err != nil {
		return nil, err
	}
	ch := make(chan string, 256)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case ch <- sc.Text():
			case <-ctx.Done():
				_ = x.Process.Kill()
				_ = x.Wait()
				return
			}
		}
		_ = x.Wait()
	}()
	return ch, nil
}

// Exec is the *exec.Cmd form, for front ends that hand the terminal over (bubbletea's ExecProcess).
func (c *Cmd) Exec(ctx context.Context) *exec.Cmd { return c.cmd(ctx) }

func short(args []string) string {
	s := strings.Join(args, " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// Have reports whether a tool is on PATH.
func Have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Shell is the POSIX shell that task steps, test commands and editors run in: sh, or on Windows the
// one Git for Windows (or MSYS2, Cygwin) brings, since cmd.exe reads none of rig.yaml's steps.
func Shell() string {
	if runtime.GOOS != "windows" {
		return "sh"
	}
	shellOnce.Do(func() {
		for _, n := range []string{"sh", "bash"} {
			if p, err := exec.LookPath(n); err == nil {
				shellPath = p
				return
			}
		}
		for _, p := range []string{os.Getenv("ProgramFiles") + `\Git\bin\sh.exe`, os.Getenv("ProgramFiles(x86)") + `\Git\bin\sh.exe`,
			os.Getenv("LOCALAPPDATA") + `\Programs\Git\bin\sh.exe`, `C:\msys64\usr\bin\sh.exe`, `C:\cygwin64\bin\sh.exe`} {
			if _, err := os.Stat(p); err == nil {
				shellPath = p
				return
			}
		}
		shellPath = "sh" // fails with exec's own "not found"; ErrNoShell says what to install
	})
	return shellPath
}

var (
	shellOnce sync.Once
	shellPath string
)

// ErrNoShell explains a missing shell on Windows.
var ErrNoShell = errors.New("rig runs task steps and commands with a POSIX shell: install Git for Windows (it brings sh) or run rig in WSL")
