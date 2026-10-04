package ai

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session is one conversation: rig keeps the transcript to show and resume it, the backend keeps
// its own (BackendID) so a resumed turn carries the whole context.
type Session struct {
	ID        string    `json:"id"`
	Backend   string    `json:"backend"`
	BackendID string    `json:"backend_id,omitempty"`
	Env       string    `json:"env"`
	Title     string    `json:"title"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Messages  []Message `json:"messages"`

	dir string
}

type Message struct {
	Role  string    `json:"role"` // user, assistant, tool, error
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	Tools []string  `json:"tools,omitempty"`
}

func sessionsDir(dataDir string) string { return filepath.Join(dataDir, "ai") }

func NewSession(dataDir, backend, env string) *Session {
	now := time.Now()
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return &Session{ID: now.Format("0102-1504") + fmt.Sprintf("-%x", b), Backend: backend, Env: env, Created: now, Updated: now, dir: sessionsDir(dataDir)}
}

// Dir holds what the session's tools read: the last user message and the bridge address.
func (s *Session) Dir() string { return filepath.Join(s.dir, s.ID) }

func (s *Session) Save() error {
	s.Updated = time.Now()
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, s.ID+".json"), raw, 0o600)
}

func (s *Session) Add(role, text string, tools ...string) {
	s.Messages = append(s.Messages, Message{Role: role, Text: text, At: time.Now(), Tools: tools})
	if s.Title == "" && role == "user" {
		s.Title = firstLine(text, 60)
	}
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

func LoadSession(dataDir, id string) (*Session, error) {
	if id == "" || id == "last" {
		ss, err := ListSessions(dataDir)
		if err != nil {
			return nil, err
		}
		if len(ss) == 0 {
			return nil, fmt.Errorf("no AI sessions yet")
		}
		return ss[0], nil
	}
	raw, err := os.ReadFile(filepath.Join(sessionsDir(dataDir), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("AI session %s: %w", id, err)
	}
	s := &Session{dir: sessionsDir(dataDir)}
	return s, json.Unmarshal(raw, s)
}

// ListSessions are the project's conversations, newest first.
func ListSessions(dataDir string) ([]*Session, error) {
	files, err := filepath.Glob(filepath.Join(sessionsDir(dataDir), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Session
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := &Session{dir: sessionsDir(dataDir)}
		if json.Unmarshal(raw, s) == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Close forgets a session: its transcript and its tool state.
func CloseSession(dataDir, id string) error {
	dir := sessionsDir(dataDir)
	if err := os.Remove(filepath.Join(dir, id+".json")); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, id))
}

func (s *Session) Summary() string {
	return fmt.Sprintf("%s · %d messages · %s", s.Env, len(s.Messages), s.Title)
}
