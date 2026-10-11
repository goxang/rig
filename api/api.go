// Package api is the Engine seam: CLI, TUI, MCP, the AI and the server are all clients of
// Engine. engine.Local runs it in-process; Fake drives tests.
package api

import (
	"context"

	"github.com/goxang/rig/kernel/catalog"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/inbox"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
)

type Engine interface {
	Catalog(ctx context.Context) (*catalog.Catalog, error)
	Query(ctx context.Context, source string, args protocol.Args) (*protocol.Frame, error)
	// Stream sends batches until one is done; cancel ctx to stop it early.
	Stream(ctx context.Context, source string, args protocol.Args) (<-chan protocol.StreamBatch, error)
	// Run goes through policy (confirm and approval happen here), runs as a job and is audited.
	Run(ctx context.Context, action string, args protocol.Args, o RunOpts) (*jobs.Job, *protocol.RunResult, error)
	Jobs(ctx context.Context) []*jobs.Job
	Events(ctx context.Context, pattern string) (<-chan protocol.Event, error)
	Search(ctx context.Context, query string) ([]protocol.Hit, error)
	Resolve(ctx context.Context, ref string) (*protocol.Resolved, error)
	Complete(ctx context.Context, language, text string, pos int) ([]protocol.Completion, error)
	Session(ctx context.Context) identity.Subject
	// Provides is every source and action the engine serves, with descriptions and argument schemas.
	Provides(ctx context.Context) protocol.Provides
	// Inbox lists writes waiting for approval; Decide approves (running it) or rejects one.
	Inbox(ctx context.Context) []inbox.Item
	Decide(ctx context.Context, id string, approve bool) (*protocol.RunResult, error)
}

type RunOpts struct {
	DryRun bool
	// As runs the action for another subject, such as the AI; the session's own when empty.
	As *identity.Subject
}
