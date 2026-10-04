package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/goxang/rig/core"
)

func (r *Cache) db(ctx context.Context, path []string) (*goredis.Client, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("pick a database: whole databases empty with `rig do <cache> flushdb`")
	}
	n := dbNum(path[0])
	if n < 0 {
		return nil, fmt.Errorf("%q: not a database (db0, db1, ...)", path[0])
	}
	addr, err := r.env.Resolve(ctx, r.opt.Addr)
	if err != nil {
		return nil, err
	}
	return goredis.NewClient(&goredis.Options{Addr: strings.TrimPrefix(addr, "redis://"), Password: r.opt.Password, DB: n}), nil
}

// Delete removes keys (a database's list) or entries of one key's value: hash fields, list items,
// set and sorted-set members, lines of a string.
func (r *Cache) Delete(ctx context.Context, path []string, t core.Table, rows []int) error {
	c, err := r.db(ctx, path)
	if err != nil {
		return err
	}
	defer c.Close()
	cells := func(col int) []any {
		var out []any
		for _, i := range rows {
			out = append(out, t.Rows[i][col])
		}
		return out
	}
	if len(path) == 1 {
		var keys []string
		for _, k := range cells(0) {
			keys = append(keys, k.(string))
		}
		return c.Del(ctx, keys...).Err()
	}
	key := path[1]
	switch typ := c.Type(ctx, key).Val(); typ {
	case "hash":
		var fs []string
		for _, f := range cells(0) {
			fs = append(fs, f.(string))
		}
		return c.HDel(ctx, key, fs...).Err()
	case "set":
		return c.SRem(ctx, key, cells(0)...).Err()
	case "zset":
		return c.ZRem(ctx, key, cells(0)...).Err()
	case "list":
		// LREM goes by value, so the items are first marked by index
		const gone = "\x00rig-deleted\x00"
		p := c.TxPipeline()
		for _, i := range rows {
			p.LSet(ctx, key, int64(i), gone)
		}
		p.LRem(ctx, key, 0, gone)
		_, err := p.Exec(ctx)
		return err
	case "string":
		drop := map[int]bool{}
		for _, i := range rows {
			drop[i] = true
		}
		var keep []string
		for i, l := range t.Rows {
			if !drop[i] {
				keep = append(keep, l[0])
			}
		}
		return c.SetArgs(ctx, key, strings.Join(keep, "\n"), goredis.SetArgs{KeepTTL: true}).Err()
	default:
		return fmt.Errorf("%s is a %s: use Q to change it", key, typ)
	}
}

// Set renames a key (a database's list) or writes one cell of a key's value.
func (r *Cache) Set(ctx context.Context, path []string, t core.Table, row, col int, value string) error {
	c, err := r.db(ctx, path)
	if err != nil {
		return err
	}
	defer c.Close()
	cell := t.Rows[row]
	if len(path) == 1 {
		ok, err := c.RenameNX(ctx, cell[0], value).Result()
		if err == nil && !ok {
			err = fmt.Errorf("%s already exists", value)
		}
		return err
	}
	key := path[1]
	switch typ := c.Type(ctx, key).Val(); typ {
	case "string":
		lines := make([]string, len(t.Rows))
		for i, l := range t.Rows {
			lines[i] = l[0]
		}
		lines[row] = value
		return c.SetArgs(ctx, key, strings.Join(lines, "\n"), goredis.SetArgs{KeepTTL: true}).Err()
	case "hash":
		if col == 1 {
			return c.HSet(ctx, key, cell[0], value).Err()
		}
		p := c.TxPipeline()
		p.HSet(ctx, key, value, cell[1])
		p.HDel(ctx, key, cell[0])
		_, err := p.Exec(ctx)
		return err
	case "list":
		return c.LSet(ctx, key, int64(row), value).Err()
	case "set":
		p := c.TxPipeline()
		p.SRem(ctx, key, cell[0])
		p.SAdd(ctx, key, value)
		_, err := p.Exec(ctx)
		return err
	case "zset":
		if col == 1 {
			score, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("score %q is not a number", value)
			}
			return c.ZAdd(ctx, key, goredis.Z{Member: cell[0], Score: score}).Err()
		}
		score, _ := strconv.ParseFloat(cell[1], 64)
		p := c.TxPipeline()
		p.ZRem(ctx, key, cell[0])
		p.ZAdd(ctx, key, goredis.Z{Member: value, Score: score})
		_, err := p.Exec(ctx)
		return err
	default:
		return fmt.Errorf("%s is a %s: use Q to change it", key, typ)
	}
}
