// Package protocol is rig/1, the plugin protocol: its schema, the types generated from it, and a
// validator. The schema is the contract; sdk/ and the Python and TypeScript SDKs are generated from it.
package protocol

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:generate go test -run TestGenerated -update .

//go:embed rig1.schema.json
var SchemaJSON []byte

var schema = sync.OnceValue(func() map[string]any {
	var s map[string]any
	if err := json.Unmarshal(SchemaJSON, &s); err != nil {
		panic(err)
	}
	return s
})

// Def is the schema of one $defs entry, such as Frame.
func Def(name string) map[string]any {
	d, _ := schema()["$defs"].(map[string]any)[name].(map[string]any)
	return d
}

// MethodSpec is one entry of the schema's method table.
type MethodSpec struct {
	From   string `json:"from"`
	Params string `json:"params"`
	Result string `json:"result"`
	Notify bool   `json:"notify"`
}

var methods = sync.OnceValue(func() map[string]MethodSpec {
	var s struct {
		Methods map[string]MethodSpec `json:"methods"`
	}
	if err := json.Unmarshal(SchemaJSON, &s); err != nil {
		panic(err)
	}
	return s.Methods
})

func Method(name string) (MethodSpec, bool) {
	m, ok := methods()[name]
	return m, ok
}

func Methods() []string {
	out := make([]string, 0, len(methods()))
	for k := range methods() {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate checks a JSON value against a $defs entry. Unknown fields pass: the protocol is append-only.
func Validate(def string, raw []byte) []string {
	d := Def(def)
	if d == nil {
		return []string{"no definition " + def}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []string{err.Error()}
	}
	var errs []string
	check(d, v, def, &errs)
	return errs
}

func check(s map[string]any, v any, path string, errs *[]string) {
	if r, ok := s["$ref"].(string); ok {
		check(Def(r[strings.LastIndexByte(r, '/')+1:]), v, path, errs)
		return
	}
	bad := func(f string, a ...any) { *errs = append(*errs, path+": "+fmt.Sprintf(f, a...)) }
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			found = found || x == v
		}
		if !found {
			bad("%v is not one of %v", v, e)
		}
	}
	switch s["type"] {
	case "object":
		o, ok := v.(map[string]any)
		if !ok {
			bad("want an object, got %s", kind(v))
			return
		}
		req, _ := s["required"].([]any)
		for _, k := range req {
			if _, ok := o[k.(string)]; !ok {
				bad("missing %s", k)
			}
		}
		props, _ := s["properties"].(map[string]any)
		extra, _ := s["additionalProperties"].(map[string]any)
		for k, x := range o {
			if ps, ok := props[k].(map[string]any); ok {
				check(ps, x, path+"."+k, errs)
			} else if extra != nil {
				check(extra, x, path+"."+k, errs)
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			bad("want an array, got %s", kind(v))
			return
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, x := range a {
				check(items, x, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			bad("want a string, got %s", kind(v))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			bad("want a boolean, got %s", kind(v))
		}
	case "number":
		if _, ok := v.(float64); !ok {
			bad("want a number, got %s", kind(v))
		}
	case "integer":
		if f, ok := v.(float64); !ok || f != float64(int64(f)) {
			bad("want an integer, got %v", v)
		}
	}
}

func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	}
	return "a number"
}

// CheckFrame validates a frame beyond its schema: one column per field, all of one length.
func CheckFrame(f *Frame) error {
	if len(f.Columns) != len(f.Fields) {
		return fmt.Errorf("frame: %d fields but %d columns", len(f.Fields), len(f.Columns))
	}
	for i, c := range f.Columns {
		if len(c) != len(f.Columns[0]) {
			return fmt.Errorf("frame: column %s has %d rows, %s has %d", f.Fields[i].Name, len(c), f.Fields[0].Name, len(f.Columns[0]))
		}
	}
	return nil
}

func (e *Error) Error() string { return e.Message }

// Unmet says err is an answer that the call cannot work here: a missing tool, service or setting.
func Unmet(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == CodeUnavailable || e.Code == CodeNeedsInput)
}
