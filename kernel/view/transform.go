package view

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

// Apply runs a node's transform: ops in order, each one of filter, sort, limit, select, rename,
// group or join. join fetches the other frame through other.
func Apply(f *protocol.Frame, ops any, other func(protocol.Binding) (*protocol.Frame, error)) (*protocol.Frame, error) {
	list, ok := ops.([]any)
	if ops == nil || f == nil {
		return f, nil
	}
	if !ok {
		list = []any{ops}
	}
	for _, o := range list {
		m, ok := o.(map[string]any)
		if !ok || len(m) != 1 {
			return nil, fmt.Errorf("transform: each step is one of filter, sort, limit, select, rename, group, join")
		}
		for k, v := range m {
			var err error
			if f, err = step(f, k, v, other); err != nil {
				return nil, fmt.Errorf("transform %s: %w", k, err)
			}
		}
	}
	return f, nil
}

func step(f *protocol.Frame, op string, v any, other func(protocol.Binding) (*protocol.Frame, error)) (*protocol.Frame, error) {
	rows := frames.Rows(f)
	switch op {
	case "filter":
		terms := strings.Fields(fmt.Sprint(v))
		rows = slices.DeleteFunc(rows, func(r map[string]any) bool { return !matchAll(r, f.Fields, terms) })
		return frames.FromRows(f.Fields, rows), nil
	case "sort":
		keys := strs(v)
		slices.SortStableFunc(rows, func(a, b map[string]any) int {
			for _, k := range keys {
				name, desc := strings.CutPrefix(k, "-")
				if c := compare(a[name], b[name]); c != 0 {
					return map[bool]int{true: -c, false: c}[desc]
				}
			}
			return 0
		})
		return frames.FromRows(f.Fields, rows), nil
	case "limit":
		n, err := strconv.Atoi(fmt.Sprint(v))
		if err != nil {
			return nil, err
		}
		return frames.FromRows(f.Fields, rows[:min(n, len(rows))]), nil
	case "select":
		var fields []protocol.Field
		for _, n := range strs(v) {
			if fl, ok := field(f, n); ok {
				fields = append(fields, fl)
			}
		}
		return frames.FromRows(fields, rows), nil
	case "rename":
		m, _ := v.(map[string]any)
		fields := slices.Clone(f.Fields)
		for i, fl := range fields {
			if to, ok := m[fl.Name]; ok {
				fields[i].Name = fmt.Sprint(to)
			}
		}
		return &protocol.Frame{Name: f.Name, Fields: fields, Columns: f.Columns, Meta: f.Meta}, nil
	case "group":
		return group(f, rows, v)
	case "join":
		return join(f, rows, v, other)
	}
	return nil, fmt.Errorf("unknown step")
}

func strs(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = fmt.Sprint(x)
		}
		return out
	case nil:
		return nil
	}
	return strings.Split(strings.ReplaceAll(fmt.Sprint(v), " ", ""), ",")
}

func field(f *protocol.Frame, name string) (protocol.Field, bool) {
	for _, fl := range f.Fields {
		if fl.Name == name {
			return fl, true
		}
	}
	return protocol.Field{}, false
}

// matchAll keeps a row when every term holds: col=v, col!=v, col~text, col>n, col<n, col>=n,
// col<=n, or a bare word found in any cell.
func matchAll(r map[string]any, fields []protocol.Field, terms []string) bool {
	for _, t := range terms {
		if !match(r, fields, t) {
			return false
		}
	}
	return true
}

func match(r map[string]any, fields []protocol.Field, t string) bool {
	for _, op := range []string{"!=", ">=", "<=", "=", "~", ">", "<"} {
		k, want, ok := strings.Cut(t, op)
		if !ok || k == "" {
			continue
		}
		got, have := r[k]
		if !have {
			return false
		}
		c := compare(got, want)
		switch op {
		case "=":
			return c == 0
		case "!=":
			return c != 0
		case "~":
			return strings.Contains(strings.ToLower(fmt.Sprint(got)), strings.ToLower(want))
		case ">":
			return c > 0
		case "<":
			return c < 0
		case ">=":
			return c >= 0
		case "<=":
			return c <= 0
		}
	}
	t = strings.ToLower(t)
	for _, fl := range fields {
		if strings.Contains(strings.ToLower(fmt.Sprint(r[fl.Name])), t) {
			return true
		}
	}
	return false
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// compare orders numbers as numbers, everything else as text; nil sorts first.
func compare(a, b any) int {
	if a == nil || b == nil {
		return cmp.Compare(map[bool]int{true: 0, false: 1}[a == nil], map[bool]int{true: 0, false: 1}[b == nil])
	}
	x, ok1 := number(a)
	y, ok2 := number(b)
	if ok1 && ok2 {
		return cmp.Compare(x, y)
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

// group: { by: [cols], agg: { out: count | sum(col) | avg(col) | min(col) | max(col) | first(col) | last(col) } }
func group(f *protocol.Frame, rows []map[string]any, v any) (*protocol.Frame, error) {
	m, _ := v.(map[string]any)
	by := strs(m["by"])
	aggs, _ := m["agg"].(map[string]any)
	if len(aggs) == 0 {
		aggs = map[string]any{"count": "count"}
	}
	var fields []protocol.Field
	for _, b := range by {
		fl, ok := field(f, b)
		if !ok {
			return nil, fmt.Errorf("no column %s", b)
		}
		fields = append(fields, fl)
	}
	outs := make([]string, 0, len(aggs))
	for k := range aggs {
		outs = append(outs, k)
	}
	slices.Sort(outs)
	type agg struct{ fn, col string }
	specs := map[string]agg{}
	for _, o := range outs {
		s := fmt.Sprint(aggs[o])
		fn, col, _ := strings.Cut(strings.TrimSuffix(s, ")"), "(")
		specs[o] = agg{fn, col}
		typ := "number"
		if fn == "first" || fn == "last" {
			if fl, ok := field(f, col); ok {
				typ = fl.Type
			}
		}
		fields = append(fields, protocol.Field{Name: o, Type: typ})
	}
	var order []string
	groups := map[string][]map[string]any{}
	for _, r := range rows {
		parts := make([]string, len(by))
		for i, b := range by {
			parts[i] = fmt.Sprint(r[b])
		}
		k := strings.Join(parts, "\x00")
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []map[string]any
	for _, k := range order {
		g := groups[k]
		row := map[string]any{}
		for _, b := range by {
			row[b] = g[0][b]
		}
		for o, s := range specs {
			row[o] = aggregate(s.fn, s.col, g)
		}
		out = append(out, row)
	}
	return frames.FromRows(fields, out), nil
}

func aggregate(fn, col string, g []map[string]any) any {
	switch fn {
	case "count":
		return float64(len(g))
	case "first":
		return g[0][col]
	case "last":
		return g[len(g)-1][col]
	}
	var vals []float64
	for _, r := range g {
		if x, ok := number(r[col]); ok {
			vals = append(vals, x)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	switch fn {
	case "min":
		return slices.Min(vals)
	case "max":
		return slices.Max(vals)
	}
	var sum float64
	for _, x := range vals {
		sum += x
	}
	if fn == "avg" {
		return sum / float64(len(vals))
	}
	return sum
}

// join: { source: S, query|args: ..., on: col (or {left: right}) } adds the other frame's columns
// to each row whose key matches; rows without a match keep empty cells.
func join(f *protocol.Frame, rows []map[string]any, v any, other func(protocol.Binding) (*protocol.Frame, error)) (*protocol.Frame, error) {
	m, _ := v.(map[string]any)
	b := protocol.Binding{Source: fmt.Sprint(m["source"])}
	if a, ok := m["args"].(map[string]any); ok {
		b.Args = protocol.Args(a)
	}
	if q, ok := m["query"]; ok {
		if b.Args == nil {
			b.Args = protocol.Args{}
		}
		b.Args["query"] = q
	}
	left, right := fmt.Sprint(m["on"]), fmt.Sprint(m["on"])
	if on, ok := m["on"].(map[string]any); ok && len(on) == 1 {
		for l, r := range on {
			left, right = l, fmt.Sprint(r)
		}
	}
	if other == nil || m["source"] == nil {
		return nil, errors.New("needs a source and an on")
	}
	o, err := other(b)
	if err != nil {
		return nil, err
	}
	index := map[string]map[string]any{}
	for _, r := range frames.Rows(o) {
		index[fmt.Sprint(r[right])] = r
	}
	fields := slices.Clone(f.Fields)
	for _, fl := range o.Fields {
		if _, dup := field(f, fl.Name); !dup {
			fields = append(fields, fl)
		}
	}
	for _, r := range rows {
		for k, x := range index[fmt.Sprint(r[left])] {
			if _, have := r[k]; !have {
				r[k] = x
			}
		}
	}
	return frames.FromRows(fields, rows), nil
}
