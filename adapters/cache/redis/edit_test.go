package redis

import (
	"context"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/goxang/rig/core"
)

type addrEnv struct{ core.Env }

func (addrEnv) Resolve(_ context.Context, a string) (string, error) { return a, nil }

// RIG_TEST_REDIS=host:port runs it against database 15, which it empties.
func TestEdit(t *testing.T) {
	addr := os.Getenv("RIG_TEST_REDIS")
	if addr == "" {
		t.Skip("RIG_TEST_REDIS not set")
	}
	ctx := context.Background()
	c := goredis.NewClient(&goredis.Options{Addr: addr, DB: 15})
	defer c.Close()
	defer c.FlushDB(ctx)
	c.FlushDB(ctx)
	c.HSet(ctx, "h", "a", "1", "b", "2")
	c.RPush(ctx, "l", "x", "y", "z")
	c.Set(ctx, "k1", "v", 0)
	c.Set(ctx, "k2", "v", 0)
	r := &Cache{env: addrEnv{}, opt: Options{Addr: addr}}

	browse := func(path ...string) ([]string, core.Table) {
		tb, _, err := r.Browse(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		return path, tb
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	p, tb := browse("db15", "h")
	must(r.Set(ctx, p, tb, 0, 1, "one"))
	must(r.Set(ctx, p, tb, 1, 0, "bee"))
	if got := c.HGetAll(ctx, "h").Val(); len(got) != 2 || got["a"] != "one" || got["bee"] != "2" {
		t.Fatalf("hash %v", got)
	}
	p, tb = browse("db15", "l")
	must(r.Delete(ctx, p, tb, []int{1}))
	must(r.Set(ctx, p, tb, 0, 1, "X"))
	if got := c.LRange(ctx, "l", 0, -1).Val(); len(got) != 2 || got[0] != "X" || got[1] != "z" {
		t.Fatalf("list %v", got)
	}
	p, tb = browse("db15")
	var rows []int
	for i, row := range tb.Rows {
		if row[0] == "k1" || row[0] == "k2" {
			rows = append(rows, i)
		}
	}
	must(r.Delete(ctx, p, tb, rows))
	if n := c.Exists(ctx, "k1", "k2").Val(); n != 0 {
		t.Fatalf("%d keys left", n)
	}
}
