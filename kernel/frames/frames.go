// Package frames reads and builds protocol frames: columnar, typed rows.
package frames

import (
	"fmt"
	"strconv"
	"time"

	"github.com/goxang/rig/kernel/refs"
	"github.com/goxang/rig/protocol"
)

func Len(f *protocol.Frame) int {
	if f == nil || len(f.Columns) == 0 {
		return 0
	}
	return len(f.Columns[0])
}

// Column is the values of the named field, or nil.
func Column(f *protocol.Frame, name string) []any {
	if f == nil {
		return nil
	}
	for i, fl := range f.Fields {
		if fl.Name == name && i < len(f.Columns) {
			return f.Columns[i]
		}
	}
	return nil
}

func Row(f *protocol.Frame, i int) map[string]any {
	if f == nil {
		return map[string]any{}
	}
	m := make(map[string]any, len(f.Fields))
	for j, fl := range f.Fields {
		if j < len(f.Columns) && i < len(f.Columns[j]) {
			m[fl.Name] = f.Columns[j][i]
		}
	}
	return m
}

func Rows(f *protocol.Frame) []map[string]any {
	out := make([]map[string]any, Len(f))
	for i := range out {
		out[i] = Row(f, i)
	}
	return out
}

func FromRows(fields []protocol.Field, rows []map[string]any) *protocol.Frame {
	f := &protocol.Frame{Fields: fields, Columns: make([][]any, len(fields))}
	for j, fl := range fields {
		col := make([]any, len(rows))
		for i, r := range rows {
			col[i] = r[fl.Name]
		}
		f.Columns[j] = col
	}
	return f
}

// RowRef is row i's ref from the first field with a ref template, or "".
func RowRef(f *protocol.Frame, i int) string {
	for _, fl := range f.Fields {
		if fl.Ref != "" {
			return refs.Expand(fl.Ref, Row(f, i))
		}
	}
	return ""
}

// Format writes one value for display, by its field's type and unit.
func Format(fl protocol.Field, v any) string {
	if v == nil {
		return ""
	}
	n, isNum := number(v)
	var s string
	switch {
	case fl.Type == protocol.TypeTime && isNum:
		s = time.Unix(int64(n), 0).Format("2006-01-02 15:04:05")
	case fl.Type == protocol.TypeDuration && isNum:
		s = (time.Duration(n * float64(time.Second))).Round(time.Millisecond).String()
	case fl.Type == protocol.TypeBytes && isNum:
		s = bytesize(n)
	case isNum:
		s = strconv.FormatFloat(n, 'f', -1, 64)
	default:
		s = fmt.Sprint(v)
	}
	if fl.Unit != "" && isNum && fl.Type != protocol.TypeTime && fl.Type != protocol.TypeDuration && fl.Type != protocol.TypeBytes {
		s += " " + fl.Unit
	}
	return s
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func bytesize(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", n)
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}
