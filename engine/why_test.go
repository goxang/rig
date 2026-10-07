package engine

import (
	"bytes"
	"strings"
	"testing"
)

// The shop with its database killed: the api only fails, postgres is down, unreachable and named in
// the api's errors. Postgres must come first, citing its status, reach and the api's log line.
func TestRankBlamesTheDependency(t *testing.T) {
	ev := []Evidence{
		{ID: "E1", Kind: "logs", Service: "api", Bad: true, Summary: "api logged 40 error lines", Blames: []string{"postgres"}},
		{ID: "E2", Kind: "traces", Service: "api", Bad: true, Summary: "12 of 12 traces failed"},
		{ID: "E3", Kind: "status", Service: "postgres", Bad: true, Summary: "postgres is stopped"},
		{ID: "E4", Kind: "reach", Service: "postgres", Bad: true, Summary: "component db does not answer"},
		{ID: "E5", Kind: "health", Service: "api", Summary: "api health /healthz: 200 OK"},
	}
	got := Rank("api", ev)
	if len(got) != 2 || got[0].Service != "postgres" {
		t.Fatalf("ranking %+v, want postgres first", got)
	}
	if strings.Join(got[0].Evidence, " ") != "E1 E3 E4" {
		t.Fatalf("postgres cites %v", got[0].Evidence)
	}
	inc := &Incident{Service: "api", Env: "docker", Window: "15m", Checked: []string{"api", "postgres"}, Evidence: ev, Suspects: got}
	if p := inc.Prompt(); !strings.Contains(p, "[E4] BAD reach") || !strings.Contains(p, "postgres (score 12") {
		t.Fatalf("prompt:\n%s", p)
	}
	var b bytes.Buffer
	inc.Markdown(&b, "")
	if md := b.String(); !strings.Contains(md, "1. **postgres**") || !strings.Contains(md, "`rig restart postgres") {
		t.Fatalf("report:\n%s", md)
	}
}

func TestRankTieGoesToTheDependency(t *testing.T) {
	got := Rank("api", []Evidence{
		{ID: "E1", Kind: "health", Service: "api", Bad: true},
		{ID: "E2", Kind: "health", Service: "cache", Bad: true},
	})
	if got[0].Service != "cache" {
		t.Fatalf("%+v", got)
	}
}

func TestMentions(t *testing.T) {
	for line, want := range map[string]bool{
		`dial tcp: lookup postgres on 127.0.0.11:53: no such host`: true,
		`failed to connect to host=postgres user=postgres`:         true,
		`postgresql driver error`:                                  false,
		`pg-postgres-2 refused`:                                    false,
	} {
		if mentions(line, "postgres") != want {
			t.Errorf("mentions(%q) != %v", line, want)
		}
	}
}
