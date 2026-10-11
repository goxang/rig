package ai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Request is what rig's MCP server asks the rig that started the assistant: a person's go-ahead
// (op "approve") or an action in the UI (op "ui").
type Request struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
	// Command is the exact action an "approve" is for, which "always allow" remembers.
	Command string            `json:"command,omitempty"`
	Action  string            `json:"action,omitempty"`
	Args    map[string]string `json:"args,omitempty"`
}

// Reply answers a Request. For an "approve", Text names who said yes, or why not.
type Reply struct {
	OK   bool   `json:"ok"`
	Text string `json:"text,omitempty"`
	// NoOne is an "approve" nobody could be asked (no terminal): the user's quoted words decide.
	NoOne bool `json:"no_one,omitempty"`
}

// SocketPath is one per rig process: short, since unix socket paths are capped near 100 bytes.
func SocketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("rig-ai-%d.sock", os.Getpid()))
}

// Serve answers requests on a unix socket until close is called; handle may block (a person deciding).
func Serve(path string, handle func(Request) Reply) (close func(), err error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var req Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				_ = json.NewEncoder(c).Encode(handle(req))
			}()
		}
	}()
	return func() { _ = l.Close(); _ = os.Remove(path) }, nil
}

// Call sends one request and waits for the reply, at most wait.
func Call(path string, req Request, wait time.Duration) (Reply, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return Reply{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(wait))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Reply{}, err
	}
	var r Reply
	err = json.NewDecoder(bufio.NewReader(c)).Decode(&r)
	return r, err
}
