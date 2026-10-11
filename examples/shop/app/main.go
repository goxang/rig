// Command app is the shop example's workload: one binary, two roles chosen by ROLE.
//
//	api     POST /orders?item=x stores an order in Postgres and queues it on RabbitMQ;
//	        GET /orders lists the latest, cached in Redis for 5s
//	worker  takes orders off the queue, "ships" them (marks them done in Postgres); one in
//	        twenty fails and goes to orders.failed
//
// RabbitMQ is reached through its management API to keep the example on the standard library
// plus pgx and go-redis; a real service would speak AMQP.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/goxang/rig/examples/internal/demo"
)

var (
	db     *pgxpool.Pool
	cache  *redis.Client
	rabbit = demo.Env("RABBIT_URL", "http://guest:guest@rabbitmq:15672")
)

type order struct {
	ID      int64     `json:"id"`
	Item    string    `json:"item"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
}

func main() {
	ctx := context.Background()
	demo.ServeMetrics()
	var err error
	if db, err = connect(ctx); err != nil {
		demo.Log.Error("postgres", "error", err.Error())
		os.Exit(1)
	}
	for _, q := range []string{"orders", "orders.failed"} {
		if err := declare(ctx, q); err != nil {
			demo.Log.Error("rabbitmq", "queue", q, "error", err.Error())
			os.Exit(1)
		}
	}
	switch demo.Role {
	case "api":
		cache = redis.NewClient(&redis.Options{Addr: demo.Env("REDIS_ADDR", "redis:6379")})
		http.HandleFunc("/orders", demo.Traced("orders", orders))
	case "worker":
		go consume(ctx)
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	addr := ":" + demo.Env("PORT", "8080")
	demo.Log.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		demo.Log.Error("serve", "error", err.Error())
		os.Exit(1)
	}
}

// connect waits for Postgres (it starts slower than the app) and creates the table.
func connect(ctx context.Context) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, demo.Env("DATABASE_URL", "postgres://postgres:shop@postgres:5432/shop"))
	if err != nil {
		return nil, err
	}
	for i := 0; ; i++ {
		_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS orders (id bigserial PRIMARY KEY, item text NOT NULL,
			status text NOT NULL DEFAULT 'new', created timestamptz NOT NULL DEFAULT now())`)
		if err == nil || i == 30 {
			return pool, err
		}
		time.Sleep(time.Second)
	}
}

func orders(w http.ResponseWriter, r *http.Request, sp *demo.Span) {
	ctx := r.Context()
	if r.Method == http.MethodPost {
		item := r.URL.Query().Get("item")
		if item == "" {
			http.Error(w, "item is required", http.StatusBadRequest)
			return
		}
		var o order
		err := db.QueryRow(ctx, `INSERT INTO orders (item) VALUES ($1) RETURNING id, item, status, created`, item).Scan(&o.ID, &o.Item, &o.Status, &o.Created)
		if err == nil {
			child := sp.Child("publish orders")
			child.Tag("queue", "orders")
			child.Tag("order.id", fmt.Sprint(o.ID))
			body, _ := json.Marshal(map[string]any{"id": o.ID, "item": o.Item, "trace": child.Trace, "span": child.ID()})
			err = publish(ctx, "orders", body)
			child.Finish(err != nil)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cache.Del(ctx, "orders:latest")
		cache.Incr(ctx, "orders:count")
		demo.Metrics.Counter("shop_orders_total", "").Add(1)
		json.NewEncoder(w).Encode(o)
		return
	}
	if raw, err := cache.Get(ctx, "orders:latest").Bytes(); err == nil {
		w.Header().Set("X-Cache", "hit")
		w.Write(raw)
		return
	}
	rows, err := db.Query(ctx, `SELECT id, item, status, created FROM orders ORDER BY id DESC LIMIT 20`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []order{}
	for rows.Next() {
		var o order
		if rows.Scan(&o.ID, &o.Item, &o.Status, &o.Created) == nil {
			list = append(list, o)
		}
	}
	raw, _ := json.Marshal(list)
	cache.Set(ctx, "orders:latest", raw, 5*time.Second)
	w.Write(raw)
}

func consume(ctx context.Context) {
	for {
		msgs, err := take(ctx, "orders", 10)
		if err != nil {
			demo.Log.Warn("take orders", "error", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}
		if len(msgs) == 0 {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, m := range msgs {
			ship(ctx, m)
		}
	}
}

func ship(ctx context.Context, raw []byte) {
	var m struct {
		ID    int64  `json:"id"`
		Item  string `json:"item"`
		Trace string `json:"trace"`
		Span  string `json:"span"`
	}
	_ = json.Unmarshal(raw, &m)
	sp := demo.Continue(m.Trace, m.Span, "ship order")
	sp.Tag("order.id", fmt.Sprint(m.ID))
	sp.Tag("order.item", m.Item)
	time.Sleep(time.Duration(20+mrand.IntN(80)) * time.Millisecond)
	if mrand.IntN(20) == 0 {
		demo.Log.Error("shipping failed", "order", m.ID, "item", m.Item, "error", "carrier refused the parcel")
		_ = publish(ctx, "orders.failed", raw)
		_, _ = db.Exec(ctx, `UPDATE orders SET status = 'failed' WHERE id = $1`, m.ID)
		demo.Metrics.Counter("shop_shipped_total", `result="failed"`).Add(1)
		sp.Finish(true)
		return
	}
	_, err := db.Exec(ctx, `UPDATE orders SET status = 'shipped' WHERE id = $1`, m.ID)
	demo.Metrics.Counter("shop_shipped_total", `result="ok"`).Add(1)
	demo.Log.Info("shipped", "order", m.ID, "item", m.Item)
	sp.Finish(err != nil)
}

// ---- RabbitMQ through the management API ----

func mgmt(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, rabbit+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := demo.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, msg)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// declare waits for RabbitMQ and makes the queue.
func declare(ctx context.Context, queue string) error {
	var err error
	for i := 0; i < 60; i++ {
		if err = mgmt(ctx, "PUT", "/api/queues/%2F/"+queue, map[string]any{"durable": true}, nil); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

func publish(ctx context.Context, queue string, body []byte) error {
	req := map[string]any{"properties": map[string]any{"content_type": "application/json"}, "routing_key": queue,
		"payload": base64.StdEncoding.EncodeToString(body), "payload_encoding": "base64"}
	return mgmt(ctx, "POST", "/api/exchanges/%2F/amq.default/publish", req, nil)
}

func take(ctx context.Context, queue string, n int) ([][]byte, error) {
	var got []struct {
		Payload  string `json:"payload"`
		Encoding string `json:"payload_encoding"`
	}
	err := mgmt(ctx, "POST", "/api/queues/%2F/"+queue+"/get", map[string]any{"count": n, "ackmode": "ack_requeue_false", "encoding": "auto"}, &got)
	var out [][]byte
	for _, m := range got {
		if m.Encoding == "base64" {
			raw, _ := base64.StdEncoding.DecodeString(m.Payload)
			out = append(out, raw)
		} else {
			out = append(out, []byte(m.Payload))
		}
	}
	return out, err
}
