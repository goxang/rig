// Command hello is the smallest rig example: one HTTP service with JSON logs and Prometheus metrics.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

var (
	log      = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	requests atomic.Int64
	failures atomic.Int64
)

func main() {
	http.HandleFunc("/hello", hello)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	go serveMetrics()
	addr := ":" + env("PORT", "7070")
	log.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Error("serve", "error", err.Error())
		os.Exit(1)
	}
}

func hello(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requests.Add(1)
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "world"
	}
	if rand.IntN(50) == 0 {
		failures.Add(1)
		log.Error("hello failed", "name", name, "error", "the dice said no")
		http.Error(w, "unlucky", http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, greet(name))
	log.Info("hello", "name", name, "ms", time.Since(start).Milliseconds())
}

func greet(name string) string { return "hello, " + name }

func serveMetrics() {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "hello_requests_total %d\nhello_failures_total %d\n", requests.Load(), failures.Load())
	})
	_ = http.ListenAndServe(":"+env("METRICS_PORT", "7071"), mux)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
