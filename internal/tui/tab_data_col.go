package tui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/core"
)

var fromRe = regexp.MustCompile(`(?is)\bFROM\s+(\S+)(?:.*?\bWHERE\s+(.+?))?;?\s*$`)

// columnMenu offers queries built on one column of the selected row: the table and the row's key
// come from the adapter's own row query, so quoting is the adapter's.
func (t *dataTab) columnMenu(m *model, c dataComp) tea.Cmd {
	r, ok := t.right.current()
	if !ok || !t.leaf || len(t.table.Columns) == 0 || c.kind != string(core.KindDatabase) {
		return nil
	}
	v, err := m.app.Component(c.name)
	rq, isRQ := core.As[core.RowQuerier](v)
	if err != nil || !isRQ {
		return nil
	}
	at := append([]string{}, t.paths[c.name]...)
	row, tb := rowIndex(r.id), t.table
	ctx := m.work()
	m.setStatus("preparing column queries…", false)
	return func() tea.Msg {
		c2, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		q := rq.SuggestRowQuery(c2, at, tb, row)
		return thenMsg(func() tea.Cmd {
			m.setStatus("", false)
			sm := fromRe.FindStringSubmatch(q)
			if sm == nil {
				m.setStatus("no table found for this row", true)
				return nil
			}
			t.pickColumn(m, c, at, sm[1], sm[2], tb.Rows[row])
			return nil
		})
	}
}

func (t *dataTab) pickColumn(m *model, c dataComp, at []string, tbl, where string, row []string) {
	cols := t.table.Columns
	m.pick("column", cols, nil, 0, false, func(got []string) tea.Cmd {
		if len(got) == 0 {
			return nil
		}
		i := 0
		for j, n := range cols {
			if n == got[0] {
				i = j
			}
		}
		col, val := quoteLike(tbl, cols[i]), ""
		if i < len(row) {
			val = sqlValue(row[i])
		}
		acts := []string{"select the column", "where it equals this row's", "order by it", "count by it"}
		qs := []string{
			fmt.Sprintf("SELECT %s FROM %s", col, tbl),
			fmt.Sprintf("SELECT * FROM %s WHERE %s = %s", tbl, col, val),
			fmt.Sprintf("SELECT * FROM %s ORDER BY %s DESC", tbl, col),
			fmt.Sprintf("SELECT %s, COUNT(*) FROM %s GROUP BY %s ORDER BY 2 DESC", col, tbl, col),
		}
		if where != "" {
			acts = append(acts, "update it on this row")
			qs = append(qs, fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s", tbl, col, val, where))
		}
		m.pick(cols[i], acts, nil, 0, false, func(a []string) tea.Cmd {
			for j, n := range acts {
				if len(a) > 0 && a[0] == n {
					t.remember(c)
					t.askQuery(m, c, at, "", qs[j])
				}
			}
			return nil
		})
		return nil
	})
}

// quoteLike quotes name the way the table reference is quoted.
func quoteLike(tbl, name string) string {
	switch {
	case strings.HasPrefix(tbl, `"`):
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	case strings.HasPrefix(tbl, "`"):
		return "`" + name + "`"
	case strings.HasPrefix(tbl, "["):
		return "[" + name + "]"
	}
	return name
}

func sqlValue(s string) string {
	if s == "" || s == "NULL" || s == "<nil>" {
		return "NULL"
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
