package spec

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:generate go test -run TestSchemaUpToDate -update .

// SchemaJSON is the JSON Schema of rig.yaml, generated from these types (Schema) by go generate.
//
//go:embed rig.schema.json
var SchemaJSON []byte

// SchemaURL is where editors fetch SchemaJSON (the yaml-language-server header rig init writes).
const SchemaURL = "https://raw.githubusercontent.com/goxang/rig/main/spec/rig.schema.json"

// SchemaHeader is the first line of a rig.yaml that gives editors its schema.
const SchemaHeader = "# yaml-language-server: $schema=" + SchemaURL

// Schema builds rig.yaml's JSON Schema from Project by reflection; describe gives a key of a type
// its description ("" for none).
func Schema(describe func(typ, key string) string) ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}, describe: describe}
	root := g.object(reflect.TypeOf(Project{}))
	root["$schema"] = "http://json-schema.org/draft-07/schema#"
	root["$id"] = SchemaURL
	root["title"] = "rig.yaml"
	// x-… keys hold YAML anchors (&name) for the rest of the file to reuse
	root["patternProperties"] = map[string]any{"^x-": map[string]any{}}
	root["definitions"] = g.defs
	out, err := json.MarshalIndent(root, "", "  ")
	return append(out, '\n'), err
}

type schemaGen struct {
	defs     map[string]any
	describe func(typ, key string) string
}

var (
	durationType  = reflect.TypeOf(time.Duration(0))
	nodeType      = reflect.TypeOf(yaml.Node{})
	componentType = reflect.TypeOf(Component{})
	taskType      = reflect.TypeOf(Task{})
	dashboardType = reflect.TypeOf(Dashboard{})
)

// ${VAR} expands before a value is decoded, so numbers and booleans may be written as strings; and
// any scalar decodes into a string
func (g *schemaGen) of(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case durationType:
		return map[string]any{"type": "string", "description": "a duration: 500ms, 30s, 5m, 1h"}
	case nodeType:
		return map[string]any{}
	case componentType:
		g.define(t, func() any {
			return map[string]any{"oneOf": []any{
				map[string]any{"type": "string", "description": "the adapter type alone"},
				map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "description": g.describe("Component", "kind")},
						"type": map[string]any{"type": "string", "description": g.describe("Component", "type")},
					},
					// the rest are the adapter's own options
					"additionalProperties": true,
				},
			}}
		})
		return ref(t)
	case taskType:
		g.define(t, func() any {
			return map[string]any{"oneOf": []any{map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, g.object(t)}}
		})
		return ref(t)
	case dashboardType:
		g.define(t, func() any {
			return map[string]any{"oneOf": []any{map[string]any{"type": "array", "items": g.of(reflect.TypeOf(Panel{}))}, g.object(t)}}
		})
		return ref(t)
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": []string{"string", "number", "boolean"}}
	case reflect.Bool:
		return map[string]any{"type": []string{"boolean", "string"}}
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Uint, reflect.Uint64:
		return map[string]any{"type": []string{"integer", "string"}}
	case reflect.Float64, reflect.Float32:
		return map[string]any{"type": []string{"number", "string"}}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": g.of(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.of(t.Elem())}
	case reflect.Struct:
		g.define(t, func() any { return g.object(t) })
		return ref(t)
	}
	return map[string]any{}
}

func ref(t reflect.Type) map[string]any {
	return map[string]any{"$ref": "#/definitions/" + t.Name()}
}

func (g *schemaGen) define(t reflect.Type, build func() any) {
	if _, ok := g.defs[t.Name()]; ok {
		return
	}
	g.defs[t.Name()] = nil // a placeholder, so a type that refers to itself ends
	g.defs[t.Name()] = build()
}

func (g *schemaGen) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	open := false
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			open = true
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		p := g.of(f.Type)
		if t.Name() == "Environment" && name == "services" {
			// an environment's service patches take a service's keys
			p = map[string]any{"type": "object", "additionalProperties": g.of(reflect.TypeOf(Service{}))}
		}
		if d := g.describe(t.Name(), name); d != "" {
			p = withDescription(p, d)
		}
		props[name] = p
	}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": open}
}

// withDescription adds d to p; a $ref is wrapped, since draft-07 ignores a $ref's siblings.
func withDescription(p map[string]any, d string) map[string]any {
	if _, ok := p["$ref"]; ok {
		return map[string]any{"allOf": []any{p}, "description": d}
	}
	out := map[string]any{"description": d}
	for k, v := range p {
		if k != "description" {
			out[k] = v
		}
	}
	return out
}
