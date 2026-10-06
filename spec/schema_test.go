package spec

import (
	"bytes"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite rig.schema.json from the types")

// docTables maps docs/config.md's headings to the type whose keys their tables list.
var docTables = map[string]string{"Top level": "Project", "services.<name>": "Service", "environments.<name>": "Environment", "ui.logs": "LogsUI"}

var docRow = regexp.MustCompile("^\\| `([a-z_]+)` \\| (.+) \\|$")

// schemaDescriptions are the keys' descriptions: docs/config.md's tables first, then the fields'
// doc comments.
func schemaDescriptions(t *testing.T) func(typ, key string) string {
	descs := map[string]string{}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]reflect.Type{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t.PkgPath() != reflect.TypeOf(Project{}).PkgPath() || types[t.Name()] != nil {
			return
		}
		types[t.Name()] = t
		for i := 0; i < t.NumField(); i++ {
			walk(t.Field(i).Type)
		}
	}
	walk(reflect.TypeOf(Project{}))
	for _, f := range pkgs["spec"].Files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			rt := types[ts.Name.Name]
			if !ok || rt == nil {
				return true
			}
			for _, field := range st.Fields.List {
				if field.Doc == nil || len(field.Names) == 0 {
					continue
				}
				sf, _ := rt.FieldByName(field.Names[0].Name)
				key, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
				if key == "" || key == "-" {
					continue
				}
				doc := strings.Join(strings.Fields(field.Doc.Text()), " ")
				for _, verb := range []string{" is ", " are ", " "} {
					if rest, ok := strings.CutPrefix(doc, field.Names[0].Name+verb); ok {
						doc = strings.ToUpper(rest[:1]) + rest[1:]
						break
					}
				}
				descs[ts.Name.Name+"."+key] = doc
			}
			return true
		})
	}
	raw, err := os.ReadFile("../docs/config.md")
	if err != nil {
		t.Fatal(err)
	}
	typ := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			typ = docTables[h]
			continue
		}
		if m := docRow.FindStringSubmatch(line); m != nil && typ != "" {
			d := strings.TrimSuffix(strings.ReplaceAll(strings.ReplaceAll(m[2], "`", ""), " (see below)", ""), "below")
			d = strings.TrimPrefix(strings.TrimPrefix(d, "below; "), "below: ")
			if d != "" {
				descs[typ+"."+m[1]] = d
			}
		}
	}
	return func(typ, key string) string { return descs[typ+"."+key] }
}

func TestSchemaUpToDate(t *testing.T) {
	got, err := Schema(schemaDescriptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("rig.schema.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(got, SchemaJSON) {
		t.Fatal("spec/rig.schema.json is out of date with the types: go generate ./spec")
	}
}
