// Package audit appends every write to a JSONL log, secrets redacted.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/kernel/identity"
)

type Entry struct {
	Time     time.Time        `json:"time"`
	Subject  identity.Subject `json:"subject"`
	Action   string           `json:"action"`
	Args     map[string]any   `json:"args,omitempty"`
	Effect   string           `json:"effect"`
	Env      string           `json:"env,omitempty"`
	Decision string           `json:"decision"`
	Approver string           `json:"approver,omitempty"`
	Result   string           `json:"result,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type Log struct {
	path string
	mu   sync.Mutex
	// secrets are values masked wherever they appear
	secrets []string
}

// Open logs to path, usually .rig/<env>/audit.jsonl; secrets are masked in every entry.
func Open(path string, secrets ...string) *Log {
	var keep []string
	for _, s := range secrets {
		if len(s) >= 4 {
			keep = append(keep, s)
		}
	}
	return &Log{path: path, secrets: keep}
}

func (l *Log) Append(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Args = l.redact(e.Args)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line := string(b)
	for _, s := range l.secrets {
		line = strings.ReplaceAll(line, s, "***")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

var secretKey = regexp.MustCompile(`(?i)pass|secret|token|key|credential|auth`)

func (l *Log) redact(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch x := v.(type) {
		case map[string]any:
			out[k] = l.redact(x)
		default:
			if secretKey.MatchString(k) {
				out[k] = "***"
			} else {
				out[k] = v
			}
		}
	}
	return out
}

// Read is every entry of the log at path, oldest first; a missing log has none.
func Read(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var e Entry
		if json.Unmarshal(line, &e) == nil && e.Action != "" {
			out = append(out, e)
		}
	}
	return out, nil
}
