package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLogQuery(t *testing.T) {
	line := `{"level":"info","message":"sent","input":"Transaction:{GormID:202604  Type:\"Purchase\"  HeaderInfo:{Key:\"a\"}  HeaderInfo:{Key:\"b c\"}}","output":"{\"Transaction\":{\"ID\":202604,\"Type\":\"Purchase\"}} \n"}`
	root := logTree(line)
	for _, c := range []struct {
		q    string
		want bool
	}{
		{"output.Transaction.ID=202604", true},
		{"$.output.Transaction.ID=202604", true},
		{"Transaction.ID=202604", true},
		{"output.Transaction.ID=1", false},
		{"input.Transaction.GormID=202604 level=INFO", true},
		{"input.Transaction.HeaderInfo.Key=a", true},
		{`input.Transaction.HeaderInfo.Key="b c"`, true},
		{"input.Transaction.HeaderInfo[1].Key=a", false},
		{"level!=error", true},
		{"missing!=x", true},
		{"level!=info", false},
		{"message~SEN", true},
		{"output.Transaction~", true},
	} {
		q, ok := parseLogQuery(c.q)
		if !ok {
			t.Errorf("%s: not read as a query", c.q)
			continue
		}
		if got := q.match(root); got != c.want {
			t.Errorf("%s: got %v, want %v", c.q, got, c.want)
		}
	}
	for _, grep := range []string{"timeout", "error: bad", "(?i)fail.*", "a=b c"} {
		if _, ok := parseLogQuery(grep); ok {
			t.Errorf("%q read as a field query", grep)
		}
	}
	n := resolve(root, []string{"output", "Transaction", "ID"})[0]
	if q := queryFor(n); q != "output.Transaction.ID=202604" {
		t.Errorf("queryFor = %s", q)
	}
}

func TestProtoText(t *testing.T) {
	got := string(logTree(`3:58PM INF save input="Transaction:{GormID:1  Serial:\"a/b\"  Tags:[\"x\", \"y\"]  Pan:\"\\x01\\x02\"}  Kind:PURCHASE"`).bytes())
	for _, want := range []string{`"GormID": 1`, `"Serial": "a/b"`, `"Kind": "PURCHASE"`, `"x",`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if parseProtoText("error: bad thing") != nil || parseProtoText("a:b c") != nil {
		t.Error("plain text read as prototext")
	}
}

func TestKVHits(t *testing.T) {
	hits := kvHits("switch_v2/domainsvc", []byte(`{"Server":{"GrpcMaxConcurrentStreams":100,"Tags":["a"]}}`))
	var got []string
	for _, h := range hits {
		got = append(got, h.key+"|"+h.path+"|"+h.value)
	}
	want := "switch_v2/domainsvc|| switch_v2/domainsvc|$.Server.GrpcMaxConcurrentStreams|100 switch_v2/domainsvc|$.Server.Tags[0]|a"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	tree, _ := newJSONTree("z", []byte(`{"a":{"b":[1,{"c":2}]}}`))
	tree.fold(1)
	tree.selectPath("$.a.b[1].c")
	if p := tree.current().path(); p != "$.a.b[1].c" {
		t.Errorf("selectPath landed on %s", p)
	}
}

func TestLogRowShowsFields(t *testing.T) {
	line := "\x1b[90m1:02AM\x1b[0m \x1b[32mINF\x1b[0m sent \x1b[36minput=\x1b[0m\"Transaction:{GormID:202604 Type:\\\"Purchase\\\"}\" \x1b[36moutput=\x1b[0m\"{\\\"Transaction\\\":{\\\"ID\\\":7}} \\n\""
	f := newLogFormat(nil)
	got := ansi.Strip(f.render(line))
	for _, want := range []string{`INF   sent`, `input={"Transaction":{"GormID":202604,"Type":"Purchase"}}`, `output={"Transaction":{"ID":7}}`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	f.hidden["input"] = true
	if strings.Contains(ansi.Strip(f.render(line)), "input=") {
		t.Error("hidden field still shown")
	}
}
