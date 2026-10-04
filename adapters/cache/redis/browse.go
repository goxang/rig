package redis

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/goxang/rig/core"
)

const browseKeys = 2000

// Browse walks the server: databases with keys, a database's keys (the first 2000 a SCAN finds),
// then one key's value.
func (r *Cache) Browse(ctx context.Context, path []string) (core.Table, bool, error) {
	if len(path) == 0 {
		info, err := r.Info(ctx)
		if err != nil {
			return core.Table{}, false, err
		}
		t := core.Table{Columns: []string{"db", "keys", "expires"}}
		var dbs []string
		for k := range info {
			if strings.HasPrefix(k, "db") {
				dbs = append(dbs, k)
			}
		}
		sort.Slice(dbs, func(i, j int) bool { return dbNum(dbs[i]) < dbNum(dbs[j]) })
		for _, k := range dbs {
			f := fields(info[k])
			t.Rows = append(t.Rows, []string{k, f["keys"], f["expires"]})
		}
		if len(t.Rows) == 0 {
			t.Note = "every database is empty"
		}
		return t, false, nil
	}
	n := dbNum(path[0])
	if n < 0 {
		return core.Table{}, false, fmt.Errorf("%q: not a database (db0, db1, ...)", path[0])
	}
	addr, err := r.env.Resolve(ctx, r.opt.Addr)
	if err != nil {
		return core.Table{}, false, err
	}
	c := goredis.NewClient(&goredis.Options{Addr: strings.TrimPrefix(addr, "redis://"), Password: r.opt.Password, DB: n})
	defer c.Close()
	if len(path) == 1 {
		t := core.Table{Columns: []string{"key"}}
		var cursor uint64
		for {
			keys, next, err := c.Scan(ctx, cursor, "*", 500).Result()
			if err != nil {
				return t, false, err
			}
			for _, k := range keys {
				t.Rows = append(t.Rows, []string{k})
			}
			cursor = next
			if cursor == 0 || len(t.Rows) >= browseKeys {
				break
			}
		}
		sort.Slice(t.Rows, func(i, j int) bool { return t.Rows[i][0] < t.Rows[j][0] })
		if cursor != 0 {
			t.Note = fmt.Sprintf("first %d keys", len(t.Rows))
		}
		return t, false, nil
	}
	return keyValue(ctx, c, path[1])
}

// QueryAt runs a command against the database the path starts at (db0 when none).
func (r *Cache) QueryAt(ctx context.Context, path []string, q string) (core.Table, error) {
	args, err := splitArgs(q)
	if err != nil || len(args) == 0 {
		return core.Table{}, fmt.Errorf("empty command")
	}
	if unsafe[strings.ToUpper(args[0])] && !r.opt.Unsafe {
		return core.Table{}, fmt.Errorf("%s is blocked; set unsafe: true on the component to allow it", strings.ToUpper(args[0]))
	}
	n := 0
	if len(path) > 0 {
		n = max(0, dbNum(path[0]))
	}
	addr, err := r.env.Resolve(ctx, r.opt.Addr)
	if err != nil {
		return core.Table{}, err
	}
	c := goredis.NewClient(&goredis.Options{Addr: strings.TrimPrefix(addr, "redis://"), Password: r.opt.Password, DB: n})
	defer c.Close()
	in := make([]any, len(args))
	for i, a := range args {
		in[i] = a
	}
	out := "(nil)"
	v, err := c.Do(ctx, in...).Result()
	switch {
	case err == goredis.Nil:
	case err != nil:
		return core.Table{}, err
	default:
		out = render(v, "")
	}
	t := core.Table{Columns: []string{"result"}}
	for _, l := range strings.Split(out, "\n") {
		t.Rows = append(t.Rows, []string{l})
	}
	return t, nil
}

func (r *Cache) SuggestQuery(path []string) string {
	if len(path) >= 2 {
		return "TYPE " + quoteArg(path[1])
	}
	return "SCAN 0 MATCH * COUNT 100"
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func keyValue(ctx context.Context, c *goredis.Client, key string) (core.Table, bool, error) {
	typ, err := c.Type(ctx, key).Result()
	if err != nil {
		return core.Table{}, true, err
	}
	ttl, _ := c.TTL(ctx, key).Result()
	t := core.Table{Note: "type " + typ}
	if ttl > 0 {
		t.Note += ", expires in " + ttl.String()
	}
	switch typ {
	case "string":
		v, err := c.Get(ctx, key).Result()
		t.Columns = []string{"value"}
		for _, l := range strings.Split(v, "\n") {
			t.Rows = append(t.Rows, []string{l})
		}
		return t, true, err
	case "hash":
		m, err := c.HGetAll(ctx, key).Result()
		t.Columns = []string{"field", "value"}
		fs := make([]string, 0, len(m))
		for f := range m {
			fs = append(fs, f)
		}
		sort.Strings(fs)
		for _, f := range fs {
			t.Rows = append(t.Rows, []string{f, m[f]})
		}
		return t, true, err
	case "list":
		vs, err := c.LRange(ctx, key, 0, 499).Result()
		t.Columns = []string{"#", "value"}
		for i, v := range vs {
			t.Rows = append(t.Rows, []string{strconv.Itoa(i), v})
		}
		return t, true, err
	case "set":
		vs, err := c.SMembers(ctx, key).Result()
		sort.Strings(vs)
		t.Columns = []string{"member"}
		for _, v := range vs {
			t.Rows = append(t.Rows, []string{v})
		}
		return t, true, err
	case "zset":
		zs, err := c.ZRangeWithScores(ctx, key, 0, 499).Result()
		t.Columns = []string{"member", "score"}
		for _, z := range zs {
			t.Rows = append(t.Rows, []string{fmt.Sprint(z.Member), strconv.FormatFloat(z.Score, 'f', -1, 64)})
		}
		return t, true, err
	case "none":
		return t, true, fmt.Errorf("%s: no such key (expired?)", key)
	}
	t.Columns = []string{"value"}
	t.Rows = [][]string{{"(" + typ + ": not shown)"}}
	return t, true, nil
}

func dbNum(s string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "db"))
	if err != nil || !strings.HasPrefix(s, "db") {
		return -1
	}
	return n
}

// fields splits Redis INFO keyspace values: keys=3,expires=0,avg_ttl=0
func fields(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}
