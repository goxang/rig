//go:build unix

package sh

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestKillPortListener(t *testing.T) {
	if p := os.Getenv("SH_TEST_LISTEN"); p != "" {
		l, _ := net.Listen("tcp", "127.0.0.1:"+p)
		defer l.Close()
		time.Sleep(time.Minute)
		return
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("no lsof")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	child := exec.Command(os.Args[0], "-test.run=TestKillPortListener")
	child.Env = append(os.Environ(), "SH_TEST_LISTEN="+strconv.Itoa(port))
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	ctx := context.Background()
	var pids []int
	for range 50 {
		if pids, _ = ListenerPIDs(ctx, port); len(pids) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(pids) != 1 || pids[0] != child.Process.Pid {
		t.Fatalf("pids on %d = %v, want [%d]", port, pids, child.Process.Pid)
	}
	Kill(pids, time.Second)
	child.Wait()
	if pids, _ = ListenerPIDs(ctx, port); len(pids) != 0 {
		t.Fatalf("still listening: %v", pids)
	}
}
