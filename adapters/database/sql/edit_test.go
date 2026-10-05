package sql

import (
	"context"
	gosql "database/sql"
	"os"
	"testing"

	"github.com/goxang/rig/core"
)

type addrEnv struct{ core.Env }

func (addrEnv) Resolve(_ context.Context, a string) (string, error) { return a, nil }

// RIG_TEST_MSSQL=host:port and RIG_TEST_MSSQL_PASSWORD (sa) run it against a real server.
func TestEditByPrimaryKey(t *testing.T) {
	addr := os.Getenv("RIG_TEST_MSSQL")
	if addr == "" {
		t.Skip("RIG_TEST_MSSQL not set")
	}
	ctx := context.Background()
	d := &DB{env: addrEnv{}, pools: map[string]*gosql.DB{}, dialect: dialects["mssql"],
		opt: Options{Driver: "mssql", Addr: addr, User: "sa", Password: os.Getenv("RIG_TEST_MSSQL_PASSWORD")}}
	_ = d.DropDatabase(ctx, "rig_edit_test")
	if err := d.CreateDatabase(ctx, "rig_edit_test"); err != nil {
		t.Fatal(err)
	}
	defer d.DropDatabase(ctx, "rig_edit_test")
	if _, err := d.Exec(ctx, "rig_edit_test", "CREATE TABLE dbo.t (id int PRIMARY KEY, name nvarchar(20) NULL); INSERT INTO dbo.t VALUES (1,'a'),(2,'b'),(3,'c'); CREATE TABLE dbo.nokey (x int)"); err != nil {
		t.Fatal(err)
	}
	path := []string{"rig_edit_test", "tables", "dbo.t"}
	tb, _, err := d.Browse(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if q := d.SuggestRowQuery(ctx, path, tb, 1); q != "SELECT * FROM [dbo].[t] WHERE [id] = 2" {
		t.Fatalf("row query %q", q)
	}
	if err := d.Set(ctx, path, tb, 1, 1, "bee"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, path, tb, []int{0, 2}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Query(ctx, "rig_edit_test", "SELECT id, name FROM dbo.t")
	if len(got.Rows) != 1 || got.Rows[0][1] != "bee" {
		t.Fatalf("%v", got.Rows)
	}
	nk := []string{"rig_edit_test", "tables", "dbo.nokey"}
	if err := d.Delete(ctx, nk, core.Table{Columns: []string{"x"}, Rows: [][]string{{"1"}}}, []int{0}); err == nil {
		t.Fatal("deleted from a table without a primary key")
	}
}

func TestSQLEquals(t *testing.T) {
	for in, want := range map[string]string{"NULL": " IS NULL", "42": " = 42", "-1.5": " = -1.5", "it's": " = 'it''s'", "2026-10-05": " = '2026-10-05'"} {
		if got := sqlEquals(in); got != want {
			t.Errorf("sqlEquals(%q) = %q, want %q", in, got, want)
		}
	}
}
