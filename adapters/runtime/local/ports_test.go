package local

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"testing"
)

func TestMetricsPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "# TYPE up gauge\nup 1")
	}))
	port := l.Addr().(*net.TCPAddr).Port
	if !slices.Contains(listening(os.Getpid()), port) {
		t.Fatalf("listening(%d) = %v, want %d among them", os.Getpid(), listening(os.Getpid()), port)
	}
	got, ok := metricsPort(context.Background(), os.Getpid(), 1, "")
	if !ok || got != port {
		t.Fatalf("metricsPort = %d %v, want %d", got, ok, port)
	}
}
