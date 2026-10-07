package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/ai"
)

// An assistant's change shows its command and waits for a yes; "always" answers the same command
// for the rest of the session; reads never ask; every call lands in the audit log.
func TestAIChangesWaitForConfirmation(t *testing.T) {
	dir := t.TempDir()
	audit := filepath.Join(dir, "ai-audit.jsonl")
	sock := filepath.Join(dir, "s.sock")
	var asked []string
	answer := ai.Reply{OK: true, Text: "the user, in rig (always this session)"}
	always := map[string]bool{}
	stop, err := ai.Serve(sock, func(r ai.Request) ai.Reply {
		if r.Op != "approve" {
			return ai.Reply{OK: true}
		}
		if always[r.Command] {
			return ai.Reply{OK: true, Text: "allowed for this session"}
		}
		asked = append(asked, r.Command)
		always[r.Command] = true
		return answer
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for k, v := range map[string]string{ai.EnvLock: "dev", ai.EnvSock: sock, ai.EnvAudit: audit, ai.EnvDir: dir} {
		t.Setenv(k, v)
	}
	tool := mcpTool{Name: "rig_x", argv: func(a map[string]any) ([]string, error) { return []string{"restart", "api"}, nil }}
	read := mcpTool{Name: "rig_y", argv: func(a map[string]any) ([]string, error) { return []string{"status"}, nil }}
	runFake := func(t mcpTool) { callTool(context.Background(), t, map[string]any{}) }

	runFake(read)
	runFake(tool)
	runFake(tool)
	if len(asked) != 1 || asked[0] != "rig restart api" {
		t.Fatalf("asked %v, want one question for rig restart api", asked)
	}
	es, err := ai.ReadAudit(audit)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 {
		t.Fatalf("audit has %d entries: %+v", len(es), es)
	}
	if es[0].By != "" || es[1].By != answer.Text || es[2].By != "allowed for this session" {
		t.Fatalf("confirmed by: %q %q %q", es[0].By, es[1].By, es[2].By)
	}
	if es[1].Env != "dev" || es[1].Command != "rig restart api" {
		t.Fatalf("entry %+v", es[1])
	}

	_ = os.Remove(audit)
	answer, always = ai.Reply{Text: "declined in rig"}, map[string]bool{}
	if text, failed := callTool(context.Background(), tool, map[string]any{}); !failed {
		t.Fatalf("declined change ran: %s", text)
	}
	if es, _ := ai.ReadAudit(audit); len(es) != 1 || es[0].Result != "declined" {
		t.Fatalf("declined audit %+v", es)
	}
}
