package sdk

import (
	"reflect"
	"strings"
	"time"
)

// NewFrame starts an empty frame with these columns.
func NewFrame(fields ...Field) *Frame {
	return &Frame{Fields: fields, Columns: make([][]any, len(fields))}
}

// Col is a field: Col("p95", TypeNumber, "ms"), the unit optional.
func Col(name, typ string, unit ...string) Field {
	f := Field{Name: name, Type: typ}
	if len(unit) > 0 {
		f.Unit = unit[0]
	}
	return f
}

// Append adds one row, a value per field; times become Unix seconds and durations seconds.
func (f *Frame) Append(row ...any) *Frame {
	for i := range f.Fields {
		var v any
		if i < len(row) {
			v = row[i]
		}
		switch t := v.(type) {
		case time.Time:
			v = float64(t.UnixNano()) / 1e9
		case time.Duration:
			v = t.Seconds()
		}
		f.Columns[i] = append(f.Columns[i], v)
	}
	return f
}

func (f *Frame) Len() int {
	if len(f.Columns) == 0 {
		return 0
	}
	return len(f.Columns[0])
}

// ArgsSchema is the JSON Schema of a handler's argument type: a struct's json fields, with the
// `desc:"..."` and `rig:"required"` tags. Anything else takes any arguments.
func ArgsSchema(t reflect.Type) Schema {
	if t.Kind() != reflect.Struct {
		return nil
	}
	props := map[string]any{}
	var req []string
	for _, sf := range reflect.VisibleFields(t) {
		if !sf.IsExported() || sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		p := map[string]any{}
		if typ := jsonType(sf.Type); typ != "" {
			p["type"] = typ
		}
		if d := sf.Tag.Get("desc"); d != "" {
			p["description"] = d
		}
		props[name] = p
		if sf.Tag.Get("rig") == "required" {
			req = append(req, name)
		}
	}
	s := Schema{"type": "object", "properties": props}
	if req != nil {
		s["required"] = req
	}
	return s
}

func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	}
	return ""
}
