package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/sh"
)

func TestTermEndsWhenPageLeaves(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rig.yaml")
	os.WriteFile(file, []byte("project: w\nenvironments:\n  dev: { runtime: webtest }\nservices:\n  api: { run: { command: [sleep, \"60\"] } }\n"), 0o644)
	h := &Handler{File: file, Token: "t", Open: engine.Open}
	defer h.Close()
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	post := func(ctx context.Context, path, body string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		return http.DefaultClient.Do(req)
	}
	res, err := post(context.Background(), "/v1/term", `{"command":["sleep","60"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ ID string }
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	pid := h.terms.get(out.ID).cmd.Process.Pid

	// Headers and body in separate writes, as curl and browsers send them.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + out.ID + `"}`
	fmt.Fprintf(conn, "POST /v1/term/out HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer t\r\nContent-Length: %d\r\n\r\n", len(body))
	time.Sleep(50 * time.Millisecond)
	conn.Write([]byte(body))
	head, _ := bufio.NewReader(conn).ReadString('\n')
	if !strings.Contains(head, "200") {
		t.Fatal(head)
	}
	conn.Close()
	for deadline := time.Now().Add(3 * time.Second); sh.Alive(pid); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the terminal's process outlived the page")
		}
	}
}
