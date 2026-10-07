package ai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvAudit is where rig's MCP server appends what the assistant ran (AuditEntry lines).
const EnvAudit = "RIG_AI_AUDIT"

// AuditEntry is one action the assistant ran, or tried to, on an environment.
type AuditEntry struct {
	At      time.Time `json:"at"`
	Env     string    `json:"env"`
	Command string    `json:"command"`
	Risk    string    `json:"risk"`
	// Result is ok, failed, declined (by the user) or refused (by rig).
	Result string `json:"result"`
	By     string `json:"confirmed_by,omitempty"`
	Output string `json:"output,omitempty"`
}

func (e AuditEntry) String() string {
	mark := map[string]string{"ok": "✓", "failed": "✖", "declined": "⊘", "refused": "⊘"}[e.Result]
	s := fmt.Sprintf("%s %s  (%s", mark, e.Command, e.Result)
	if e.By != "" {
		s += ", " + e.By
	}
	return s + ")"
}

// AppendAudit adds e to file as one JSON line; the output is cut to its last 400 bytes.
func AppendAudit(file string, e AuditEntry) error {
	if file == "" {
		return nil
	}
	e.Output = tail(e.Output, 400)
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReadAudit returns file's entries, oldest first; a missing file has none.
func ReadAudit(file string) ([]AuditEntry, error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if strings.TrimSpace(sc.Text()) != "" && json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
