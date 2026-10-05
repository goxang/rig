// Command app is the kind example's workload: one binary, two roles chosen by ROLE.
//
//	api      GET /echo calls the worker
//	worker   GET /work burns some CPU, fails 1% of the time
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"strconv"

	"github.com/goxang/rig/examples/internal/demo"
)

func main() {
	demo.ServeMetrics()
	switch demo.Role {
	case "api":
		http.HandleFunc("/echo", demo.Traced("GET /echo", echo))
	case "worker":
		http.HandleFunc("/work", demo.Traced("GET /work", work))
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	addr := ":" + demo.Env("PORT", "8080")
	demo.Log.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		demo.Log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func echo(w http.ResponseWriter, r *http.Request, sp *demo.Span) {
	n := 2000 + mrand.IntN(8000)
	req, _ := http.NewRequestWithContext(r.Context(), "GET", demo.Env("WORKER_URL", "http://worker:8080")+"/work?n="+strconv.Itoa(n), nil)
	child := sp.Child("GET /work")
	child.Inject(req.Header)
	resp, err := demo.Client.Do(req)
	child.Finish(err != nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		demo.Log.Warn("worker call failed", "err", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Fprintf(w, "echo %s %s\n", r.URL.Query().Get("q"), body)
}

func work(w http.ResponseWriter, r *http.Request, _ *demo.Span) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	sum := sha256.Sum256([]byte(r.URL.RawQuery))
	for i := 0; i < n; i++ {
		sum = sha256.Sum256(sum[:])
	}
	if mrand.IntN(100) == 0 {
		http.Error(w, "unlucky", http.StatusInternalServerError)
		return
	}
	io.WriteString(w, hex.EncodeToString(sum[:4]))
}
