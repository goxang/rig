package tui

import (
	"strings"
	"testing"

	"github.com/goxang/rig/core"
)

func TestJSONTreeEditKeepsOrder(t *testing.T) {
	tree, err := newJSONTree("t", []byte(`{"z":1,"a":{"port":8080,"tags":["x","y"]},"name":"svc","on":true,"none":null}`))
	if err != nil {
		t.Fatal(err)
	}
	find := func(path string) *jnode {
		for _, r := range tree.rows {
			if r.n.path() == path {
				return r.n
			}
		}
		t.Fatalf("no row %s", path)
		return nil
	}
	find("$.a.port").set("9090")
	find("$.name").set("42") // a string stays a string
	find("$.a.tags[1]").remove()
	tree.flatten()
	want := `{
  "z": 1,
  "a": {
    "port": 9090,
    "tags": [
      "x"
    ]
  },
  "name": "42",
  "on": true,
  "none": null
}`
	if got := string(tree.root.bytes()); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if n := find("$.a.tags[0]"); n.text() != "x" {
		t.Fatalf("text = %q", n.text())
	}
}

func TestLogWrapRows(t *testing.T) {
	v := newLogView("log", 10, nil)
	v.wrap = true
	v.add([]core.LogLine{{Text: "short"}, {Text: strings.Repeat("a", 25)}})
	m := &model{}
	out := v.render(m, 0, 0, 10, 4, func(l core.LogLine) string { return l.Text })
	rows := strings.Split(out, "\n")
	if len(rows) != 4 || rows[0] != "short" || rows[3] != "aaaaa" {
		t.Fatalf("rows %q", rows)
	}
	if v.rowLine[3] != 1 || v.rowCol[3] != 20 {
		t.Fatalf("row 3 maps to line %d col %d", v.rowLine[3], v.rowCol[3])
	}
}

func TestLogFormatHides(t *testing.T) {
	f := newLogFormat(nil)
	f.hidden["trace"] = true
	got := stripANSI(f.render(`{"level":"error","msg":"boom","trace":"abc","code":500}`))
	if strings.Contains(got, "trace") || !strings.Contains(got, "code=500") || !strings.HasPrefix(got, "ERROR boom") {
		t.Fatalf("got %q", got)
	}
}
