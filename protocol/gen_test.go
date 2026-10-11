package protocol

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode"
)

var update = flag.Bool("update", false, "rewrite the generated protocol files")

// generated maps each file the schema generates, relative to the repo root, to its content.
func generated(t *testing.T) map[string][]byte {
	root, err := parseOrdered(SchemaJSON)
	if err != nil {
		t.Fatal(err)
	}
	s := loadSchema(t, root)
	host, err := goFile(s, "protocol")
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := goFile(s, "sdk")
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"protocol/types_gen.go":        host,
		"sdk/protocol_gen.go":          sdk,
		"sdk/python/rig/protocol.py":   pyFile(s),
		"sdk/typescript/protocol.d.ts": tsTypes(s),
		"sdk/typescript/protocol.js":   jsConsts(s),
	}
}

func TestGenerated(t *testing.T) {
	for path, want := range generated(t) {
		if *update {
			if err := os.WriteFile("../"+path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile("../" + path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is out of date with rig1.schema.json: go generate ./protocol", path)
		}
	}
}

// ordered is a JSON object that keeps its keys' order, so generated code follows the schema.
type ordered struct {
	keys []string
	vals map[string]any
}

func parseOrdered(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return decodeValue(d)
}

func decodeValue(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := &ordered{vals: map[string]any{}}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			o.keys = append(o.keys, k.(string))
			o.vals[k.(string)] = v
		}
		_, err := d.Token()
		return o, err
	case json.Delim('['):
		var a []any
		for d.More() {
			v, err := decodeValue(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := d.Token()
		return a, err
	}
	return tok, nil
}

func (o *ordered) obj(k string) *ordered {
	v, _ := o.vals[k].(*ordered)
	return v
}

func (o *ordered) str(k string) string {
	v, _ := o.vals[k].(string)
	return v
}

type method struct{ name, from, params, result string }

type prop struct {
	name, doc string
	required  bool
	schema    *ordered
}

type def struct {
	name, doc string
	schema    *ordered
	props     []prop
}

type schemaModel struct {
	defs    []def
	methods []method
	errors  []struct {
		name string
		code string
	}
}

func loadSchema(t *testing.T, root any) *schemaModel {
	r := root.(*ordered)
	m := &schemaModel{}
	defs := r.obj("$defs")
	for _, name := range defs.keys {
		d := def{name: name, schema: defs.obj(name), doc: defs.obj(name).str("description")}
		req := map[string]bool{}
		if a, ok := d.schema.vals["required"].([]any); ok {
			for _, k := range a {
				req[k.(string)] = true
			}
		}
		if ps := d.schema.obj("properties"); ps != nil {
			for _, k := range ps.keys {
				d.props = append(d.props, prop{name: k, doc: ps.obj(k).str("description"), required: req[k], schema: ps.obj(k)})
			}
		}
		m.defs = append(m.defs, d)
	}
	ms := r.obj("methods")
	for _, name := range ms.keys {
		o := ms.obj(name)
		m.methods = append(m.methods, method{name, o.str("from"), o.str("params"), o.str("result")})
	}
	es := r.obj("errors")
	for _, name := range es.keys {
		m.errors = append(m.errors, struct {
			name string
			code string
		}{name, es.vals[name].(json.Number).String()})
	}
	if len(m.defs) == 0 || len(m.methods) == 0 {
		t.Fatal("schema has no $defs or methods")
	}
	return m
}

func refName(s *ordered) string {
	r := s.str("$ref")
	return r[strings.LastIndexByte(r, '/')+1:]
}

func goName(s string) string {
	var b strings.Builder
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		switch strings.ToLower(w) {
		case "id", "url", "ai", "ui", "json":
			b.WriteString(strings.ToUpper(w))
		default:
			b.WriteString(strings.ToUpper(w[:1]) + w[1:])
		}
	}
	return b.String()
}

func (m *schemaModel) isObject(name string) bool {
	for _, d := range m.defs {
		if d.name == name {
			return d.props != nil || d.schema.vals["additionalProperties"] == nil && d.schema.str("type") == "object"
		}
	}
	return false
}

func (m *schemaModel) goType(s *ordered, optional bool) string {
	if s.str("$ref") != "" {
		n := refName(s)
		if optional && m.isObject(n) {
			return "*" + n
		}
		return n
	}
	switch s.str("type") {
	case "string":
		return "string"
	case "integer":
		return "int"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		return "[]" + m.goType(s.obj("items"), false)
	case "object":
		if ap := s.obj("additionalProperties"); ap != nil {
			return "map[string]" + m.goType(ap, false)
		}
	}
	return "any"
}

func comment(b *bytes.Buffer, indent, doc string) {
	if doc != "" {
		fmt.Fprintf(b, "%s// %s\n", indent, doc)
	}
}

func goFile(m *schemaModel, pkg string) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.\n\npackage %s\n\n", pkg)
	b.WriteString("// Version is the protocol this package speaks.\nconst Version = \"rig/1\"\n\n")
	b.WriteString("const (\n")
	for _, me := range m.methods {
		fmt.Fprintf(&b, "\tMethod%s = %q\n", goName(strings.Replace(me.name, "$/", "notify/", 1)), me.name)
	}
	b.WriteString(")\n\nconst (\n")
	for _, e := range m.errors {
		fmt.Fprintf(&b, "\tCode%s = %s\n", e.name, e.code)
	}
	b.WriteString(")\n\n")
	enums := map[string]string{}
	for _, d := range m.defs {
		comment(&b, "", d.doc)
		switch {
		case d.props != nil:
			fmt.Fprintf(&b, "type %s struct {\n", d.name)
			for _, p := range d.props {
				comment(&b, "\t", p.doc)
				tag := p.name
				if !p.required {
					tag += ",omitempty"
				}
				fmt.Fprintf(&b, "\t%s %s `json:%q`\n", goName(p.name), m.goType(p.schema, !p.required), tag)
				if e, ok := p.schema.vals["enum"].([]any); ok && len(e) > 1 {
					for _, v := range e {
						enums[goName(p.name)+goName(v.(string))] = v.(string)
					}
				}
			}
			b.WriteString("}\n\n")
		case d.schema.str("type") == "object" && d.schema.vals["additionalProperties"] == nil:
			fmt.Fprintf(&b, "type %s struct{}\n\n", d.name)
		default:
			fmt.Fprintf(&b, "type %s = %s\n\n", d.name, m.goType(d.schema, false))
		}
	}
	names := make([]string, 0, len(enums))
	for k := range enums {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("const (\n")
	for _, k := range names {
		fmt.Fprintf(&b, "\t%s = %q\n", k, enums[k])
	}
	b.WriteString(")\n")
	return format.Source(b.Bytes())
}

func (m *schemaModel) pyType(s *ordered) string {
	if s.str("$ref") != "" {
		return refName(s)
	}
	switch s.str("type") {
	case "string":
		return "str"
	case "integer":
		return "int"
	case "number":
		return "float"
	case "boolean":
		return "bool"
	case "array":
		return "list[" + m.pyType(s.obj("items")) + "]"
	case "object":
		if ap := s.obj("additionalProperties"); ap != nil {
			return "dict[str, " + m.pyType(ap) + "]"
		}
		return "dict[str, Any]"
	}
	return "Any"
}

func pyFile(m *schemaModel) []byte {
	var b bytes.Buffer
	b.WriteString("# Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.\nfrom __future__ import annotations\n\nfrom typing import Any, TypedDict\n\nVERSION = \"rig/1\"\n\n")
	b.WriteString("METHODS = {\n")
	for _, me := range m.methods {
		fmt.Fprintf(&b, "    %q: %q,\n", me.name, me.from)
	}
	b.WriteString("}\n\n")
	for _, e := range m.errors {
		fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(snake(e.name)), e.code)
	}
	for _, d := range m.defs {
		b.WriteString("\n\n")
		if d.props == nil {
			fmt.Fprintf(&b, "%s = %s\n", d.name, m.pyType(d.schema))
			continue
		}
		if slices.ContainsFunc(d.props, func(p prop) bool { return pyKeywords[p.name] }) {
			var fs []string
			for _, p := range d.props {
				fs = append(fs, fmt.Sprintf("%q: %s", p.name, m.pyType(p.schema)))
			}
			fmt.Fprintf(&b, "%s = TypedDict(%q, {%s}, total=False)\n", d.name, d.name, strings.Join(fs, ", "))
			continue
		}
		fmt.Fprintf(&b, "class %s(TypedDict, total=False):\n", d.name)
		if d.doc != "" {
			fmt.Fprintf(&b, "    %q\n\n", d.doc)
		}
		for _, p := range d.props {
			fmt.Fprintf(&b, "    %s: %s\n", p.name, m.pyType(p.schema))
		}
	}
	return b.Bytes()
}

var pyKeywords = map[string]bool{"from": true, "import": true, "class": true, "def": true, "global": true, "in": true, "is": true, "not": true, "or": true, "and": true, "pass": true, "return": true, "with": true, "as": true, "if": true, "else": true, "for": true, "while": true, "try": true, "lambda": true, "yield": true, "async": true, "await": true, "None": true, "True": true, "False": true}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func (m *schemaModel) tsType(s *ordered) string {
	if s.str("$ref") != "" {
		return refName(s)
	}
	if e, ok := s.vals["enum"].([]any); ok {
		var vs []string
		for _, v := range e {
			vs = append(vs, fmt.Sprintf("%q", v))
		}
		return strings.Join(vs, " | ")
	}
	switch s.str("type") {
	case "string":
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		t := m.tsType(s.obj("items"))
		if strings.Contains(t, " ") {
			t = "(" + t + ")"
		}
		return t + "[]"
	case "object":
		if ap := s.obj("additionalProperties"); ap != nil {
			return "Record<string, " + m.tsType(ap) + ">"
		}
		return "Record<string, unknown>"
	}
	return "unknown"
}

func tsTypes(m *schemaModel) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.\n\nexport const VERSION: \"rig/1\";\nexport const METHODS: Record<string, \"host\" | \"plugin\" | \"any\">;\nexport const ERRORS: {\n")
	for _, e := range m.errors {
		fmt.Fprintf(&b, "  readonly %s: %s;\n", e.name, e.code)
	}
	b.WriteString("};\n")
	for _, d := range m.defs {
		b.WriteString("\n")
		if d.doc != "" {
			fmt.Fprintf(&b, "/** %s */\n", d.doc)
		}
		if d.props == nil {
			fmt.Fprintf(&b, "export type %s = %s;\n", d.name, m.tsType(d.schema))
			continue
		}
		fmt.Fprintf(&b, "export interface %s {\n", d.name)
		for _, p := range d.props {
			if p.doc != "" {
				fmt.Fprintf(&b, "  /** %s */\n", p.doc)
			}
			opt := "?"
			if p.required {
				opt = ""
			}
			fmt.Fprintf(&b, "  %s%s: %s;\n", p.name, opt, m.tsType(p.schema))
		}
		b.WriteString("}\n")
	}
	return b.Bytes()
}

func jsConsts(m *schemaModel) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.\n\nexport const VERSION = \"rig/1\";\n\nexport const METHODS = {\n")
	for _, me := range m.methods {
		fmt.Fprintf(&b, "  %q: %q,\n", me.name, me.from)
	}
	b.WriteString("};\n\nexport const ERRORS = {\n")
	for _, e := range m.errors {
		fmt.Fprintf(&b, "  %s: %s,\n", e.name, e.code)
	}
	b.WriteString("};\n")
	return b.Bytes()
}
