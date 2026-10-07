package docker

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/goxang/rig/internal/sh"
)

type tunnel struct {
	local string
	done  chan struct{}
}

// tunnel forwards a local port to addr on the daemon's machine with ssh -L, one per addr while it lives.
func (r *Runtime) tunnel(ctx context.Context, dest, addr string) (string, error) {
	r.tunMu.Lock()
	defer r.tunMu.Unlock()
	if t, ok := r.tunnels[addr]; ok {
		select {
		case <-t.done:
		default:
			return t.local, nil
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	local := l.Addr().String()
	l.Close()
	cmd := sh.New("ssh", "-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-L", local+":"+addr, dest).Exec(context.Background())
	if err := cmd.Start(); err != nil {
		return "", err
	}
	t := &tunnel{local: local, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(t.done) }()
	deadline := time.After(15 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp", local, 300*time.Millisecond); err == nil {
			c.Close()
			break
		}
		select {
		case <-t.done:
			return "", fmt.Errorf("ssh -L %s to %s exited (try: ssh %s)", addr, dest, dest)
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return "", ctx.Err()
		case <-deadline:
			_ = cmd.Process.Kill()
			return "", fmt.Errorf("ssh -L %s to %s: no listener after 15s", addr, dest)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if r.tunnels == nil {
		r.tunnels = map[string]*tunnel{}
	}
	r.tunnels[addr] = t
	return local, nil
}
