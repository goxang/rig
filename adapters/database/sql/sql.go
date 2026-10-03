// Package sql is the database adapter for SQL servers: one implementation, a dialect per driver
// (postgres, mssql, mysql) for listing, creating and dropping databases.
package sql

import (
	"context"
	gosql "database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindDatabase, "sql", "SQL servers: postgres, mssql, mysql (driver option)", New)
}

type Seed struct {
	File string `yaml:"file"`
	DB   string `yaml:"db"`
}

type Options struct {
	Driver   string            `yaml:"driver"`
	Addr     string            `yaml:"addr"`
	User     string            `yaml:"user"`
	Password string            `yaml:"password"`
	Database string            `yaml:"database"`
	Params   map[string]string `yaml:"params"`
	Seeds    []Seed            `yaml:"seeds"`
	// Clear holds the statements `clear` runs to empty the run's data without recreating the database.
	Clear []string `yaml:"clear"`
}

type DB struct {
	opt     Options
	env     core.Env
	dialect dialect

	mu    sync.Mutex
	pools map[string]*gosql.DB
	addr  string
}

type dialect struct {
	driver  string
	list    string
	create  string
	drop    []string
	dsn     func(o Options, addr, db string) string
	batches func(string) []string
}

var dialects = map[string]dialect{
	"postgres": {
		driver: "pgx",
		list:   "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY 1",
		create: `CREATE DATABASE "%s"`,
		drop:   []string{`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`},
		dsn: func(o Options, addr, db string) string {
			u := url.URL{Scheme: "postgres", User: url.UserPassword(o.User, o.Password), Host: addr, Path: "/" + db}
			q := url.Values{"sslmode": {"disable"}}
			for k, v := range o.Params {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			return u.String()
		},
		batches: whole,
	},
	"mssql": {
		driver: "sqlserver",
		list:   "SELECT name FROM sys.databases ORDER BY name",
		create: "CREATE DATABASE [%s]",
		drop:   []string{"IF DB_ID('%[1]s') IS NOT NULL ALTER DATABASE [%[1]s] SET SINGLE_USER WITH ROLLBACK IMMEDIATE", "IF DB_ID('%[1]s') IS NOT NULL DROP DATABASE [%[1]s]"},
		dsn: func(o Options, addr, db string) string {
			u := url.URL{Scheme: "sqlserver", User: url.UserPassword(o.User, o.Password), Host: addr}
			q := url.Values{}
			if db != "" {
				q.Set("database", db)
			}
			for k, v := range o.Params {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			return u.String()
		},
		batches: goBatches,
	},
	"mysql": {
		driver: "mysql",
		list:   "SHOW DATABASES",
		create: "CREATE DATABASE `%s`",
		drop:   []string{"DROP DATABASE IF EXISTS `%s`"},
		dsn: func(o Options, addr, db string) string {
			q := url.Values{"parseTime": {"true"}, "multiStatements": {"true"}}
			for k, v := range o.Params {
				q.Set(k, v)
			}
			return fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", o.User, o.Password, addr, db, q.Encode())
		},
		batches: whole,
	},
}

func New(env core.Env, c *spec.Component) (any, error) {
	d := &DB{env: env, pools: map[string]*gosql.DB{}}
	if err := c.Decode(&d.opt); err != nil {
		return nil, err
	}
	switch d.opt.Driver {
	case "postgresql", "pg":
		d.opt.Driver = "postgres"
	case "sqlserver":
		d.opt.Driver = "mssql"
	}
	dl, ok := dialects[d.opt.Driver]
	if !ok {
		return nil, fmt.Errorf("driver %q: have postgres, mssql, mysql", d.opt.Driver)
	}
	d.dialect = dl
	return d, nil
}

func (d *DB) pool(ctx context.Context, db string) (*gosql.DB, error) {
	if db == "" {
		db = d.opt.Database
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.pools[db]; ok {
		return p, nil
	}
	if d.addr == "" {
		a, err := d.env.Resolve(ctx, d.opt.Addr)
		if err != nil {
			return nil, err
		}
		d.addr = strings.TrimPrefix(strings.TrimPrefix(a, "tcp://"), "http://")
	}
	p, err := gosql.Open(d.dialect.driver, d.dialect.dsn(d.opt, d.addr, db))
	if err != nil {
		return nil, err
	}
	p.SetMaxOpenConns(4)
	p.SetConnMaxIdleTime(time.Minute)
	d.pools[db] = p
	return p, nil
}

func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, p := range d.pools {
		_ = p.Close()
		delete(d.pools, k)
	}
	return nil
}

func (d *DB) Ping(ctx context.Context) error {
	p, err := d.pool(ctx, "")
	if err != nil {
		return err
	}
	return p.PingContext(ctx)
}

func (d *DB) Query(ctx context.Context, db, q string) (core.Table, error) {
	p, err := d.pool(ctx, db)
	if err != nil {
		return core.Table{}, err
	}
	rows, err := p.QueryContext(ctx, q)
	if err != nil {
		return core.Table{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return core.Table{}, err
	}
	t := core.Table{Columns: cols}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return t, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = format(v)
		}
		t.Rows = append(t.Rows, row)
		if len(t.Rows) >= 10000 {
			t.Note = "first 10000 rows"
			break
		}
	}
	return t, rows.Err()
}

func format(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	}
	return fmt.Sprint(v)
}

func (d *DB) Exec(ctx context.Context, db, q string) (int64, error) {
	p, err := d.pool(ctx, db)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, b := range d.dialect.batches(q) {
		res, err := p.ExecContext(ctx, b)
		if err != nil {
			return n, err
		}
		if c, err := res.RowsAffected(); err == nil {
			n += c
		}
	}
	return n, nil
}

func (d *DB) Databases(ctx context.Context) ([]string, error) {
	t, err := d.Query(ctx, d.adminDB(), d.dialect.list)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		out = append(out, r[0])
	}
	return out, nil
}

func (d *DB) adminDB() string {
	switch d.opt.Driver {
	case "postgres":
		return "postgres"
	case "mssql":
		return "master"
	}
	return ""
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

func (d *DB) CreateDatabase(ctx context.Context, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("database name %q: letters, digits, _ and - only", name)
	}
	_, err := d.Exec(ctx, d.adminDB(), fmt.Sprintf(d.dialect.create, name))
	return err
}

func (d *DB) DropDatabase(ctx context.Context, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("database name %q: letters, digits, _ and - only", name)
	}
	d.mu.Lock()
	if p, ok := d.pools[name]; ok {
		_ = p.Close()
		delete(d.pools, name)
	}
	d.mu.Unlock()
	for _, stmt := range d.dialect.drop {
		if _, err := d.Exec(ctx, d.adminDB(), fmt.Sprintf(stmt, name)); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) QueryLanguage() string { return "sql" }

// RunQuery runs q on the default database; "@name <sql>" picks another.
func (d *DB) RunQuery(ctx context.Context, q string) (core.Table, error) {
	db := ""
	if rest, ok := strings.CutPrefix(strings.TrimSpace(q), "@"); ok {
		db, q, _ = strings.Cut(rest, " ")
	}
	return d.Query(ctx, db, q)
}

func (d *DB) Actions() []core.Action {
	return []core.Action{
		{Name: "seed", Mutate: true, Help: "run the configured seed files, or: seed <file.sql> [db]", Run: func(ctx context.Context, args []string, out io.Writer) error {
			seeds := d.opt.Seeds
			if len(args) > 0 {
				s := Seed{File: args[0]}
				if len(args) > 1 {
					s.DB = args[1]
				}
				seeds = []Seed{s}
			}
			for _, s := range seeds {
				path := s.File
				if !filepath.IsAbs(path) {
					path = filepath.Join(d.env.Project().Dir, path)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				n, err := d.Exec(ctx, s.DB, string(raw))
				if err != nil {
					return fmt.Errorf("%s: %w", s.File, err)
				}
				fmt.Fprintf(out, "seeded %s (%d rows)\n", s.File, n)
			}
			return nil
		}},
		{Name: "clear", Mutate: true, Help: "run the configured clear statements", Run: func(ctx context.Context, args []string, out io.Writer) error {
			for _, q := range d.opt.Clear {
				n, err := d.Exec(ctx, "", q)
				if err != nil {
					return fmt.Errorf("%s: %w", q, err)
				}
				fmt.Fprintf(out, "%-60s %s rows\n", q, strconv.FormatInt(n, 10))
			}
			return nil
		}},
	}
}

func whole(s string) []string { return []string{s} }

var goLine = regexp.MustCompile(`(?im)^\s*GO\s*$`)

// goBatches splits a T-SQL script on GO lines, which are a client convention, not SQL.
func goBatches(s string) []string {
	var out []string
	for _, b := range goLine.Split(s, -1) {
		if strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
	}
	return out
}
