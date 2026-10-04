package sql

import (
	"context"
	gosql "database/sql"
	"fmt"
	"strings"

	"github.com/goxang/rig/core"
)

// keySQL lists a table's primary key columns in order (%s: the table, a quoted literal).
var keySQL = map[string]string{
	"mssql": `SELECT c.name FROM sys.indexes i JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.is_primary_key = 1 AND i.object_id = OBJECT_ID(%s) ORDER BY ic.key_ordinal`,
	"postgres": `SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indisprimary AND i.indrelid = %s::regclass ORDER BY array_position(i.indkey, a.attnum)`,
	"mysql": `SELECT column_name FROM information_schema.key_column_usage
		WHERE table_schema = DATABASE() AND table_name = %s AND constraint_name = 'PRIMARY' ORDER BY ordinal_position`,
}

func (d *DB) param(i int) string {
	switch d.opt.Driver {
	case "mssql":
		return fmt.Sprintf("@p%d", i)
	case "postgres":
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// table is the quoted name of the table a path ends at; only a table's rows can change.
func (d *DB) table(path []string) (string, error) {
	if len(path) != 3 || path[1] != "tables" {
		return "", fmt.Errorf("only a table's rows can be changed here: use Q to run SQL")
	}
	b := browsers[d.opt.Driver]
	var parts []string
	for _, p := range strings.Split(path[2], ".") {
		parts = append(parts, b.quote(p))
	}
	return strings.Join(parts, "."), nil
}

func (d *DB) primaryKey(ctx context.Context, path []string, quoted string, t core.Table) ([]int, error) {
	name := path[2]
	switch d.opt.Driver {
	case "postgres":
		name = quoted
	case "mysql":
		name = name[strings.LastIndex(name, ".")+1:]
	}
	kt, err := d.Query(ctx, path[0], fmt.Sprintf(keySQL[d.opt.Driver], "'"+strings.ReplaceAll(name, "'", "''")+"'"))
	if err != nil {
		return nil, err
	}
	var idx []int
	for _, r := range kt.Rows {
		i := index(t.Columns, r[0])
		if i < 0 {
			return nil, fmt.Errorf("key column %s is not in the rows shown", r[0])
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 {
		return nil, fmt.Errorf("%s has no primary key, so a row cannot be told apart: use Q to run SQL", path[2])
	}
	return idx, nil
}

// change runs stmt, narrowed to each row by its primary key, in one transaction; each must touch
// exactly one row.
func (d *DB) change(ctx context.Context, path []string, t core.Table, rows []int, stmt func(table string) (string, []any)) error {
	table, err := d.table(path)
	if err != nil {
		return err
	}
	keys, err := d.primaryKey(ctx, path, table, t)
	if err != nil {
		return err
	}
	p, err := d.pool(ctx, path[0])
	if err != nil {
		return err
	}
	tx, err := p.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	b := browsers[d.opt.Driver]
	for _, r := range rows {
		q, args := stmt(table)
		var where []string
		for _, k := range keys {
			args = append(args, t.Rows[r][k])
			where = append(where, b.quote(t.Columns[k])+" = "+d.param(len(args)))
		}
		q += " WHERE " + strings.Join(where, " AND ")
		if err := one(tx.ExecContext(ctx, q, args...)); err != nil {
			return fmt.Errorf("row %d: %w", r+1, err)
		}
	}
	return tx.Commit()
}

func one(res gosql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n != 1 {
		return fmt.Errorf("would change %d rows, not 1: nothing changed", n)
	}
	return nil
}

func (d *DB) Delete(ctx context.Context, path []string, t core.Table, rows []int) error {
	return d.change(ctx, path, t, rows, func(table string) (string, []any) {
		return "DELETE FROM " + table, nil
	})
}

// Set writes value into one cell; the text NULL stores NULL.
func (d *DB) Set(ctx context.Context, path []string, t core.Table, row, col int, value string) error {
	var v any = value
	if value == "NULL" {
		v = nil
	}
	return d.change(ctx, path, t, []int{row}, func(table string) (string, []any) {
		return "UPDATE " + table + " SET " + browsers[d.opt.Driver].quote(t.Columns[col]) + " = " + d.param(1), []any{v}
	})
}

func index(xs []string, x string) int {
	for i, s := range xs {
		if strings.EqualFold(s, x) {
			return i
		}
	}
	return -1
}
