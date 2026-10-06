package sql

import (
	"context"
	"fmt"
	"strings"

	"github.com/goxang/rig/core"
)

// browseSQL is per dialect: what lists a database's objects and what reads one (%s: a quoted literal).
type browseSQL struct {
	tables, views, procedures, functions, running string
	definition                                    string
	top                                           func(object string) string
	quote                                         func(name string) string
}

var browsers = map[string]browseSQL{
	"mssql": {
		tables: `SELECT s.name + '.' + t.name AS name, SUM(p.rows) AS rows, CAST(SUM(a.total_pages)*8/1024.0 AS decimal(12,1)) AS mb
			FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id JOIN sys.partitions p ON p.object_id = t.object_id AND p.index_id IN (0,1)
			JOIN sys.allocation_units a ON a.container_id = p.partition_id GROUP BY s.name, t.name ORDER BY s.name, t.name`,
		views:      `SELECT s.name + '.' + v.name AS name, v.modify_date AS modified FROM sys.views v JOIN sys.schemas s ON s.schema_id = v.schema_id ORDER BY 1`,
		procedures: `SELECT s.name + '.' + p.name AS name, p.modify_date AS modified FROM sys.procedures p JOIN sys.schemas s ON s.schema_id = p.schema_id ORDER BY 1`,
		functions:  `SELECT s.name + '.' + o.name AS name, o.type_desc AS kind, o.modify_date AS modified FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id WHERE o.type IN ('FN','IF','TF') ORDER BY 1`,
		running: `SELECT r.session_id AS sid, r.status, r.command, r.total_elapsed_time AS ms, r.wait_type, r.blocking_session_id AS blocked_by,
			LEFT(REPLACE(REPLACE(st.text, CHAR(10), ' '), CHAR(13), ' '), 300) AS statement
			FROM sys.dm_exec_requests r CROSS APPLY sys.dm_exec_sql_text(r.sql_handle) st WHERE r.database_id = DB_ID() AND r.session_id <> @@SPID ORDER BY ms DESC`,
		definition: `SELECT OBJECT_DEFINITION(OBJECT_ID(%s))`,
		top:        func(o string) string { return "SELECT TOP 100 * FROM " + o },
		quote:      func(n string) string { return "[" + strings.ReplaceAll(n, "]", "]]") + "]" },
	},
	"postgres": {
		tables: `SELECT n.nspname || '.' || c.relname AS name, c.reltuples::bigint AS rows, round(pg_total_relation_size(c.oid)/1048576.0, 1) AS mb
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog','information_schema') ORDER BY 1`,
		views:      `SELECT table_schema || '.' || table_name AS name FROM information_schema.views WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`,
		procedures: `SELECT routine_schema || '.' || routine_name AS name FROM information_schema.routines WHERE routine_type = 'PROCEDURE' AND routine_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`,
		functions:  `SELECT routine_schema || '.' || routine_name AS name FROM information_schema.routines WHERE routine_type = 'FUNCTION' AND routine_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`,
		running: `SELECT pid, state, now() - query_start AS running_for, wait_event, left(query, 300) AS statement FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid() AND state <> 'idle' ORDER BY query_start`,
		definition: `SELECT pg_get_functiondef(%s::regproc)`,
		top:        func(o string) string { return "SELECT * FROM " + o + " LIMIT 100" },
		quote:      func(n string) string { return `"` + strings.ReplaceAll(n, `"`, `""`) + `"` },
	},
	"mysql": {
		tables:     `SELECT table_name AS name, table_rows AS rows, round((data_length+index_length)/1048576, 1) AS mb FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY 1`,
		views:      `SELECT table_name AS name FROM information_schema.views WHERE table_schema = DATABASE() ORDER BY 1`,
		procedures: `SELECT routine_name AS name FROM information_schema.routines WHERE routine_schema = DATABASE() AND routine_type = 'PROCEDURE' ORDER BY 1`,
		functions:  `SELECT routine_name AS name FROM information_schema.routines WHERE routine_schema = DATABASE() AND routine_type = 'FUNCTION' ORDER BY 1`,
		running:    `SELECT id, user, command, time AS seconds, state, left(info, 300) AS statement FROM information_schema.processlist WHERE db = DATABASE() AND id <> CONNECTION_ID() ORDER BY time DESC`,
		definition: `SELECT routine_definition FROM information_schema.routines WHERE routine_schema = DATABASE() AND routine_name = %s`,
		top:        func(o string) string { return "SELECT * FROM " + o + " LIMIT 100" },
		quote:      func(n string) string { return "`" + strings.ReplaceAll(n, "`", "``") + "`" },
	},
}

var objectKinds = []string{"tables", "views", "procedures", "functions", "running"}

// Browse walks the server: databases, then a database's object kinds, then the objects, then one
// object's first rows (tables, views) or definition (procedures, functions).
func (d *DB) Browse(ctx context.Context, path []string) (core.Table, bool, error) {
	b, ok := browsers[d.opt.Driver]
	if !ok {
		return core.Table{}, false, fmt.Errorf("no browsing for driver %q", d.opt.Driver)
	}
	switch len(path) {
	case 0:
		names, err := d.Databases(ctx)
		t := core.Table{Columns: []string{"database"}}
		for _, n := range names {
			t.Rows = append(t.Rows, []string{n})
		}
		return t, false, err
	case 1:
		t := core.Table{Columns: []string{"objects"}}
		for _, k := range objectKinds {
			t.Rows = append(t.Rows, []string{k})
		}
		return t, false, nil
	}
	db, kind := path[0], path[1]
	if len(path) == 2 {
		q := map[string]string{"tables": b.tables, "views": b.views, "procedures": b.procedures, "functions": b.functions, "running": b.running}[kind]
		if q == "" {
			return core.Table{}, false, fmt.Errorf("unknown object kind %q (have %s)", kind, strings.Join(objectKinds, ", "))
		}
		t, err := d.Query(ctx, db, q)
		return t, kind == "running", err
	}
	object := path[2]
	switch kind {
	case "tables", "views":
		var parts []string
		for _, p := range strings.Split(object, ".") {
			parts = append(parts, b.quote(p))
		}
		t, err := d.Query(ctx, db, b.top(strings.Join(parts, ".")))
		return t, true, err
	case "procedures", "functions":
		name := object
		if d.opt.Driver == "mysql" {
			name = object[strings.LastIndex(object, ".")+1:]
		}
		t, err := d.Query(ctx, db, fmt.Sprintf(b.definition, "'"+strings.ReplaceAll(name, "'", "''")+"'"))
		if err != nil || len(t.Rows) == 0 {
			return t, true, err
		}
		def := core.Table{Columns: []string{"definition"}}
		for _, l := range strings.Split(strings.ReplaceAll(t.Rows[0][0], "\r", ""), "\n") {
			def.Rows = append(def.Rows, []string{l})
		}
		return def, true, nil
	}
	return core.Table{}, true, fmt.Errorf("%s has nothing below it", strings.Join(path, "/"))
}

// QueryAt runs q in the database the path starts at.
func (d *DB) QueryAt(ctx context.Context, path []string, q string) (core.Table, error) {
	if len(path) == 0 {
		return core.Table{}, fmt.Errorf("no database in the path: give one, e.g. @db SELECT ...")
	}
	return d.Query(ctx, path[0], q)
}

// SuggestQuery reads a table or view's first rows, and calls a procedure or function with each
// parameter as NULL /* its type */ to fill in; elsewhere it leaves a SELECT to finish.
func (d *DB) SuggestQuery(ctx context.Context, path []string) string {
	b, ok := browsers[d.opt.Driver]
	if !ok || len(path) == 0 {
		return ""
	}
	if len(path) < 3 {
		return "SELECT "
	}
	switch path[1] {
	case "tables", "views":
		var parts []string
		for _, p := range strings.Split(path[2], ".") {
			parts = append(parts, b.quote(p))
		}
		return b.top(strings.Join(parts, "."))
	case "procedures", "functions":
		return d.callTemplate(ctx, path[0], path[1] == "functions", path[2])
	}
	return "SELECT "
}

// callTemplate is a runnable call of a routine: every argument NULL, its name and type beside it.
func (d *DB) callTemplate(ctx context.Context, db string, function bool, object string) string {
	lit := "'" + strings.ReplaceAll(object, "'", "''") + "'"
	name := object
	if d.opt.Driver == "mysql" {
		name = object[strings.LastIndex(object, ".")+1:]
		lit = "'" + strings.ReplaceAll(name, "'", "''") + "'"
	}
	var q string
	switch d.opt.Driver {
	case "mssql":
		q = `SELECT p.name, TYPE_NAME(p.user_type_id) + CASE WHEN TYPE_NAME(p.user_type_id) IN ('varchar','nvarchar','char','nchar','varbinary')
			THEN '(' + CASE WHEN p.max_length = -1 THEN 'max' ELSE CAST(CASE WHEN TYPE_NAME(p.user_type_id) LIKE 'n%' THEN p.max_length/2 ELSE p.max_length END AS varchar) END + ')' ELSE '' END,
			CASE WHEN p.is_output = 1 THEN 'OUTPUT' ELSE '' END, (SELECT type FROM sys.objects WHERE object_id = OBJECT_ID(` + lit + `))
			FROM sys.parameters p WHERE p.object_id = OBJECT_ID(` + lit + `) AND p.name <> '' ORDER BY p.parameter_id`
	case "postgres":
		q = `SELECT a.name, a.type, '', '' FROM (SELECT unnest(coalesce(p.proargnames, array_fill(''::text, array[p.pronargs]))) AS name,
			unnest(string_to_array(oidvectortypes(p.proargtypes), ', ')) AS type FROM pg_proc p WHERE p.oid = ` + lit + `::regproc) a`
	case "mysql":
		q = `SELECT parameter_name, dtd_identifier, parameter_mode, '' FROM information_schema.parameters
			WHERE specific_schema = DATABASE() AND specific_name = ` + lit + ` AND parameter_name IS NOT NULL ORDER BY ordinal_position`
	}
	t, err := d.Query(ctx, db, q)
	var args []string
	kind := ""
	for _, r := range t.Rows {
		if len(r) < 4 {
			continue
		}
		kind = r[3]
		hint := strings.TrimSpace(r[1] + " " + r[2])
		switch {
		case d.opt.Driver == "mssql":
			out := ""
			if r[2] == "OUTPUT" {
				out = " OUTPUT"
			}
			args = append(args, r[0]+" = NULL"+out+" /* "+r[1]+" */")
		case r[0] != "":
			args = append(args, "NULL /* "+r[0]+" "+hint+" */")
		default:
			args = append(args, "NULL /* "+hint+" */")
		}
	}
	if err != nil {
		args = []string{"/* parameters unknown: " + err.Error() + " */"}
	}
	list := strings.Join(args, ", ")
	switch {
	case d.opt.Driver == "mssql" && !function:
		return strings.TrimSpace("EXEC " + object + " " + list)
	case d.opt.Driver == "mssql" && kind == "FN":
		return "SELECT " + object + "(" + list + ")"
	case function && d.opt.Driver == "mysql":
		return "SELECT " + name + "(" + list + ")"
	case function:
		return "SELECT * FROM " + object + "(" + list + ")"
	case d.opt.Driver == "mysql":
		return "CALL " + name + "(" + list + ")"
	}
	return "CALL " + object + "(" + list + ")"
}
