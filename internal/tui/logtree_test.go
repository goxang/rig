package tui

import (
	"strings"
	"testing"
)

func TestLogTree(t *testing.T) {
	paths := func(n *jnode) string {
		var out []string
		var walk func(n *jnode)
		walk = func(n *jnode) {
			if !n.container() {
				out = append(out, n.path()+"="+n.text())
			}
			for _, k := range n.kids {
				walk(k)
			}
		}
		walk(n)
		return strings.Join(out, " ")
	}
	for _, c := range []struct{ line, want string }{
		{`{"level":"info","service":"parsersvc","output":"{\"Transaction\":{\"Amount\":90000,\"RRN\":\"0100\"}} \n","message":"Parse"}`,
			`$.level=info $.service=parsersvc $.output.Transaction.Amount=90000 $.output.Transaction.RRN=0100 $.message=Parse`},
		{"\x1b[90m3:58PM\x1b[0m \x1b[32mINF\x1b[0m Parse  Executation Time 47ms output={\"A\":{\"B\":1}} service=parsersvc span=788e",
			`$.time=3:58PM $.level=INF $.message=Parse Executation Time 47ms $.output.A.B=1 $.service=parsersvc $.span=788e`},
		{`3:58PM INF save input="[{435642 InternetPackage 1 {0 63914796008 0xc00051a460} 502229  N}]" service=domainsvc`,
			`$.time=3:58PM $.level=INF $.message=save $.input[0][0]=435642 $.input[0][1]=InternetPackage $.input[0][2]=1 $.input[0][3][0]=0 $.input[0][3][1]=63914796008 $.input[0][3][2]=0xc00051a460 $.input[0][4]=502229 $.input[0][5]=N $.service=domainsvc`},
		{`{"input":"` + "`" + `&{Name:switch v2 Port:8080 Tags:[a b] Meta:map[k:v]}` + "`" + `"}`,
			`$.input.Name=switch v2 $.input.Port=8080 $.input.Tags[0]=a $.input.Tags[1]=b $.input.Meta.k=v`},
		{`plain text line`, `$.text=plain text line`},
		{`request body: {"a":1}`, `$.text.text=request body: $.text.value.a=1`},
	} {
		if got := paths(logTree(c.line)); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.line, got, c.want)
		}
	}
}
