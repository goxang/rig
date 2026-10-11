// Package identity says who is calling: a person, the AI, an agent or an API token.
package identity

import (
	"context"
	"os"
	"os/user"
)

const (
	KindUser   = "user"
	KindAI     = "ai"
	KindAgent  = "agent"
	KindToken  = "token"
	KindPlugin = "plugin"
)

type Subject struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Groups []string `json:"groups,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	// Approve lists action globs an agent must get approved even where its writes are otherwise allowed.
	Approve []string `json:"approve,omitempty"`
}

func (s Subject) String() string { return s.Kind + ":" + s.Name }

// Local is the person at this terminal; locally they own everything.
func Local() Subject {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	return Subject{Kind: KindUser, Name: name, Roles: []string{"owner"}}
}

// AI is the assistant acting for on, with on's roles.
func AI(on Subject) Subject {
	return Subject{Kind: KindAI, Name: "ai@" + on.Name, Roles: on.Roles}
}

// Agent is a rig.yaml agent acting for on, with on's roles.
func Agent(name string, approve []string, on Subject) Subject {
	return Subject{Kind: KindAgent, Name: name, Roles: on.Roles, Approve: approve}
}

type ctxKey struct{}

// With makes s the caller for everything run with ctx: a server request's account.
func With(ctx context.Context, s Subject) context.Context { return context.WithValue(ctx, ctxKey{}, s) }

func From(ctx context.Context) (Subject, bool) {
	s, ok := ctx.Value(ctxKey{}).(Subject)
	return s, ok
}
