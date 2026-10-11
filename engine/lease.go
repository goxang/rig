package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/audit"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/protocol"
)

// Lease holds an environment for one person: while it lasts, nobody else changes it.
type Lease struct {
	By    string    `json:"by"`
	Until time.Time `json:"until"`
	Note  string    `json:"note,omitempty"`
}

func (l *Local) leasePath() string { return filepath.Join(l.App.StateDir(), "lease.json") }

// Lease is the environment's live lease, if any.
func (l *Local) Lease() (Lease, bool) {
	var ls Lease
	b, err := os.ReadFile(l.leasePath())
	if err != nil || json.Unmarshal(b, &ls) != nil || time.Now().After(ls.Until) {
		return Lease{}, false
	}
	return ls, true
}

func holds(ls Lease, who identity.Subject) bool {
	_, name, _ := strings.Cut(ls.By, ":")
	return ls.By == who.String() || who.Name == "ai@"+name
}

// leased refuses a change to an environment someone else holds.
func (l *Local) leased(action string, who identity.Subject) error {
	a, ok := l.runner.Actions.Get(action)
	if !ok || a.Effect == protocol.EffectRead || action == "env.unlock.force" {
		return nil
	}
	if ls, ok := l.Lease(); ok && !holds(ls, who) {
		return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("%s is locked by %s until %s %s",
			l.envName(), ls.By, ls.Until.Format("15:04"), ls.Note)}
	}
	return nil
}

func (l *Local) leases() {
	reg := l.runner.Actions
	_ = reg.Add(actions.Action{Name: "env.lock", Effect: protocol.EffectWrite, Help: "hold the environment so nobody else changes it (for: 2h, note)",
		Args: protocol.Schema{"type": "object", "properties": map[string]any{"for": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"}}},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			d := 2 * time.Hour
			if s, _ := args["for"].(string); s != "" {
				var err error
				if d, err = time.ParseDuration(s); err != nil {
					return nil, err
				}
			}
			note, _ := args["note"].(string)
			ls := Lease{By: l.Session(ctx).String(), Until: time.Now().Add(d), Note: note}
			b, _ := json.Marshal(ls)
			if err := os.MkdirAll(filepath.Dir(l.leasePath()), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(l.leasePath(), b, 0o644); err != nil {
				return nil, err
			}
			return &protocol.RunResult{Message: l.envName() + " locked by " + ls.By + " until " + ls.Until.Format("15:04")}, nil
		}})
	unlock := func(force bool) actions.Func {
		return func(ctx context.Context, _ protocol.Args) (*protocol.RunResult, error) {
			ls, ok := l.Lease()
			if !ok {
				return &protocol.RunResult{Message: l.envName() + " is not locked"}, nil
			}
			if !force && !holds(ls, l.Session(ctx)) {
				return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "locked by " + ls.By + " (env.unlock.force breaks it)"}
			}
			if err := os.Remove(l.leasePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			return &protocol.RunResult{Message: l.envName() + " unlocked"}, nil
		}
	}
	_ = reg.Add(actions.Action{Name: "env.unlock", Effect: protocol.EffectWrite, Help: "release your lock on the environment", Run: unlock(false)})
	_ = reg.Add(actions.Action{Name: "env.unlock.force", Effect: protocol.EffectDanger, Help: "break someone else's lock on the environment", Run: unlock(true)})

	l.about["env.lease"] = protocol.SourceSpec{Description: "who holds the environment, until when"}
	l.sources["env.lease"] = func(context.Context, protocol.Args) (*protocol.Frame, error) {
		var rows []map[string]any
		if ls, ok := l.Lease(); ok {
			rows = append(rows, map[string]any{"by": ls.By, "until": ls.Until.Format(time.RFC3339), "note": ls.Note})
		}
		return frames.FromRows(stringFields("by", "until", "note"), rows), nil
	}

	l.about["audit"] = protocol.SourceSpec{Description: "the environment's audit log, newest first (who, action: substring filters; limit)",
		Args: protocol.Schema{"type": "object", "properties": map[string]any{"who": map[string]any{"type": "string"}, "action": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}}}
	l.sources["audit"] = func(_ context.Context, args protocol.Args) (*protocol.Frame, error) {
		es, err := audit.Read(filepath.Join(l.App.StateDir(), "audit.jsonl"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		who, _ := args["who"].(string)
		act, _ := args["action"].(string)
		limit := 100
		if n, ok := args["limit"].(float64); ok {
			limit = int(n)
		}
		var rows []map[string]any
		for i := len(es) - 1; i >= 0 && len(rows) < limit; i-- {
			e := es[i]
			if !strings.Contains(e.Subject.String(), who) || !strings.Contains(e.Action, act) {
				continue
			}
			b, _ := json.Marshal(e.Args)
			rows = append(rows, map[string]any{"time": e.Time.Format(time.RFC3339), "who": e.Subject.String(), "action": e.Action,
				"args": string(b), "decision": e.Decision, "approver": e.Approver, "result": e.Result, "error": e.Error})
		}
		return frames.FromRows(stringFields("time", "who", "action", "args", "decision", "approver", "result", "error"), rows), nil
	}
}

func stringFields(names ...string) []protocol.Field {
	out := make([]protocol.Field, len(names))
	for i, n := range names {
		out[i] = protocol.Field{Name: n, Type: protocol.TypeString}
	}
	return out
}
