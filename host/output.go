package host

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

// parseOutput turns a script's stdout into a frame: json, ndjson, csv, tsv, yaml or lines, detected
// unless format names one.
func parseOutput(out []byte, format string) (*protocol.Frame, error) {
	out = bytes.TrimSpace(out)
	if format == "" {
		format = detect(out)
	}
	switch format {
	case "json", "yaml":
		var n yaml.Node
		if err := yaml.Unmarshal(out, &n); err != nil {
			return nil, fmt.Errorf("%s output: %w", format, err)
		}
		if len(n.Content) == 0 {
			return &protocol.Frame{}, nil
		}
		return nodeFrame(n.Content[0])
	case "ndjson":
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, l := range bytes.Split(out, []byte("\n")) {
			if len(bytes.TrimSpace(l)) == 0 {
				continue
			}
			var n yaml.Node
			if err := yaml.Unmarshal(l, &n); err != nil {
				return nil, fmt.Errorf("ndjson output: %w", err)
			}
			seq.Content = append(seq.Content, n.Content[0])
		}
		return nodeFrame(seq)
	case "csv", "tsv":
		r := csv.NewReader(bytes.NewReader(out))
		if format == "tsv" {
			r.Comma, r.LazyQuotes = '\t', true
		}
		r.FieldsPerRecord = -1
		recs, err := r.ReadAll()
		if err != nil || len(recs) == 0 {
			return nil, fmt.Errorf("%s output: %v", format, err)
		}
		var rows []map[string]any
		for _, rec := range recs[1:] {
			row := map[string]any{}
			for i, h := range recs[0] {
				if i < len(rec) {
					row[h] = scalarValue(rec[i])
				}
			}
			rows = append(rows, row)
		}
		return frames.FromRows(fieldsFor(recs[0], rows), rows), nil
	case "lines":
		var col []any
		for _, l := range strings.Split(string(out), "\n") {
			if l != "" {
				col = append(col, l)
			}
		}
		return &protocol.Frame{Fields: []protocol.Field{{Name: "line", Type: protocol.TypeString}}, Columns: [][]any{col}}, nil
	}
	return nil, fmt.Errorf("format %q: want json, ndjson, csv, tsv, yaml or lines", format)
}

func detect(out []byte) string {
	first, rest, multi := bytes.Cut(out, []byte("\n"))
	switch {
	case len(out) == 0:
		return "lines"
	case out[0] == '{' && multi && bytes.HasPrefix(bytes.TrimSpace(rest), []byte("{")) && !json.Valid(out):
		return "ndjson"
	case out[0] == '[' || out[0] == '{':
		return "json"
	case bytes.Contains(first, []byte("\t")):
		return "tsv"
	case multi && bytes.Count(first, []byte(",")) > 0 && bytes.Count(first, []byte(",")) == bytes.Count(bytes.SplitN(rest, []byte("\n"), 2)[0], []byte(",")):
		return "csv"
	case bytes.HasPrefix(out, []byte("- ")) || bytes.HasPrefix(out, []byte("---")) || isYAMLMap(first):
		return "yaml"
	}
	return "lines"
}

func isYAMLMap(line []byte) bool {
	k, _, ok := bytes.Cut(line, []byte(":"))
	return ok && len(k) > 0 && !bytes.ContainsAny(k, " \t\"'")
}

// nodeFrame takes a frame as written, a list of objects (a row each) or one object (one row).
func nodeFrame(n *yaml.Node) (*protocol.Frame, error) {
	if n.Kind == yaml.MappingNode && mapKey(n, "fields") != nil && mapKey(n, "columns") != nil {
		var f protocol.Frame
		b, _ := json.Marshal(nodeValue(n))
		return &f, json.Unmarshal(b, &f)
	}
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
	}
	var names []string
	seen := map[string]bool{}
	var rows []map[string]any
	for _, it := range items {
		row := map[string]any{}
		if it.Kind != yaml.MappingNode {
			row["value"] = nodeValue(it)
			if !seen["value"] {
				seen["value"], names = true, append(names, "value")
			}
		}
		for i := 0; it.Kind == yaml.MappingNode && i+1 < len(it.Content); i += 2 {
			k := it.Content[i].Value
			if !seen[k] {
				seen[k], names = true, append(names, k)
			}
			row[k] = nodeValue(it.Content[i+1])
		}
		rows = append(rows, row)
	}
	return frames.FromRows(fieldsFor(names, rows), rows), nil
}

func mapKey(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func nodeValue(n *yaml.Node) any {
	var v any
	_ = n.Decode(&v)
	return v
}

func scalarValue(s string) any {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// fieldsFor types each column by its values: int, number or bool when all agree, json for nesting.
func fieldsFor(names []string, rows []map[string]any) []protocol.Field {
	var out []protocol.Field
	for _, n := range names {
		typ := ""
		for _, r := range rows {
			t := valueType(r[n])
			switch {
			case t == "":
			case typ == "":
				typ = t
			case typ == protocol.TypeInt && t == protocol.TypeNumber || typ == protocol.TypeNumber && t == protocol.TypeInt:
				typ = protocol.TypeNumber
			case typ != t:
				typ = protocol.TypeString
			}
		}
		out = append(out, protocol.Field{Name: n, Type: cmp.Or(typ, protocol.TypeString)})
	}
	return out
}

func valueType(v any) string {
	switch v.(type) {
	case nil:
		return ""
	case int, int64:
		return protocol.TypeInt
	case float64:
		return protocol.TypeNumber
	case bool:
		return protocol.TypeBool
	case string:
		return protocol.TypeString
	}
	return protocol.TypeJSON
}
