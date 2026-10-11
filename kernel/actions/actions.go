// Package actions runs named verbs: each goes through policy, runs as a job and, unless it only
// reads, lands in the audit log.
package actions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/goxang/rig/kernel/audit"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/inbox"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/kernel/policy"
	"github.com/goxang/rig/protocol"
)

type Func func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error)

type Action struct {
	Name   string
	Effect string
	Help   string
	Args   protocol.Schema
	Run    Func
	// Plan, when set, says what Run would do without doing it.
	Plan Func
}

type Registry struct {
	mu sync.RWMutex
	m  map[string]Action
}

func NewRegistry() *Registry { return &Registry{m: map[string]Action{}} }

func (r *Registry) Add(a Action) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[a.Name]; ok {
		return fmt.Errorf("action %s is already registered", a.Name)
	}
	r.m[a.Name] = a
	return nil
}

func (r *Registry) Get(name string) (Action, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.m[name]
	return a, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ErrDeclined is a confirm or reauth no one was asked about.
var ErrDeclined = errors.New("declined")

type Runner struct {
	Actions *Registry
	Policy  *policy.Config
	Envs    map[string]policy.Env
	Jobs    *jobs.Manager
	// Audit, when set, logs every action that is not a read.
	Audit *audit.Log
	// Ask settles a confirm or reauth decision: nil to go ahead, else why not.
	Ask AskFunc
	// Inbox, when set, keeps writes that need approval until another person decides.
	Inbox *inbox.Inbox
}

type AskFunc func(ctx context.Context, d policy.Decision, why string) error

type askKey struct{}

// WithAsk settles confirm and reauth for calls run with ctx instead of the runner's Ask: a server
// request answers for its own caller.
func WithAsk(ctx context.Context, f AskFunc) context.Context {
	return context.WithValue(ctx, askKey{}, f)
}

type Call struct {
	Subject identity.Subject
	Env     string
	Action  string
	Args    protocol.Args
	DryRun  bool
}

// Run runs c as a job and waits for it.
func (r *Runner) Run(ctx context.Context, c Call) (_ *jobs.Job, _ *protocol.RunResult, err error) {
	a, ok := r.Actions.Get(c.Action)
	if !ok {
		return nil, nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no action " + c.Action}
	}
	if c.DryRun {
		if a.Plan == nil {
			return nil, nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: c.Action + " has no plan"}
		}
		res, err := a.Plan(ctx, c.Args)
		return nil, res, err
	}
	d, why := r.Policy.Decide(policy.Request{Subject: c.Subject, Action: c.Action, Effect: a.Effect, Env: c.Env}, r.Envs)
	entry := audit.Entry{Subject: c.Subject, Action: c.Action, Args: c.Args, Effect: a.Effect, Env: c.Env, Decision: string(d)}
	switch d {
	case policy.Approve:
		if r.Inbox != nil {
			return nil, nil, r.hold(ctx, a, c, why)
		}
		fallthrough
	case policy.Deny:
		err = &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("%s: %s (%s)", c.Action, d, why)}
	case policy.Confirm, policy.Reauth:
		err = ErrDeclined
		if ask, ok := ctx.Value(askKey{}).(AskFunc); ok {
			err = ask(ctx, d, why)
		} else if r.Ask != nil {
			err = r.Ask(ctx, d, why)
		}
	}
	if err != nil {
		entry.Error = err.Error()
		return nil, nil, errors.Join(err, r.log(a, entry))
	}
	// a caller already running a job (the TUI's act) keeps it, and finishes it itself
	j := jobs.From(ctx)
	if j == nil {
		ctx, j = r.Jobs.Start(ctx, c.Action)
		defer func() { j.Finish(err) }()
	}
	res, err := a.Run(ctx, c.Args)
	if err != nil {
		entry.Error = err.Error()
	} else if res != nil {
		entry.Result = res.Message
	}
	err = errors.Join(err, r.log(a, entry))
	return j, res, err
}

// hold puts c in the inbox with its plan, so the approver sees what it would do.
func (r *Runner) hold(ctx context.Context, a Action, c Call, why string) error {
	it := inbox.Item{Subject: c.Subject, Env: c.Env, Action: c.Action, Args: c.Args, Why: why}
	if a.Plan != nil {
		it.Plan, _ = a.Plan(ctx, c.Args)
	}
	it, err := r.Inbox.Add(it)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("%s waits for approval (%s): rig inbox approve %s", c.Action, why, it.ID)
	entry := audit.Entry{Subject: c.Subject, Action: c.Action, Args: c.Args, Effect: a.Effect, Env: c.Env, Decision: string(policy.Approve), Error: msg}
	return errors.Join(&protocol.Error{Code: protocol.CodeNeedsInput, Message: msg}, r.log(a, entry))
}

// Decide approves or rejects a pending item as by, who must be a person other than whoever asked.
// An approved item runs as its subject, audited with the approver.
func (r *Runner) Decide(ctx context.Context, id string, approve bool, by identity.Subject) (_ *protocol.RunResult, err error) {
	if r.Inbox == nil {
		return nil, errors.New("no inbox")
	}
	it, err := r.Inbox.Get(id)
	if err != nil {
		return nil, err
	}
	if it.Status != inbox.Pending {
		return nil, fmt.Errorf("%s is already %s", id, it.Status)
	}
	if by.Kind != identity.KindUser || by.String() == it.Subject.String() {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "only another person can decide " + id}
	}
	a, ok := r.Actions.Get(it.Action)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no action " + it.Action}
	}
	if d, why := r.Policy.Decide(policy.Request{Subject: by, Action: it.Action, Effect: a.Effect, Env: it.Env}, r.Envs); d == policy.Deny || d == policy.Approve {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: by.String() + " may not approve " + it.Action + " (" + why + ")"}
	}
	it.By = by.String()
	entry := audit.Entry{Subject: it.Subject, Action: it.Action, Args: it.Args, Effect: a.Effect, Env: it.Env, Decision: string(policy.Approve), Approver: it.By}
	if !approve {
		it.Status = inbox.Rejected
		entry.Error = "rejected by " + it.By
		return nil, errors.Join(r.Inbox.Put(it), r.log(a, entry))
	}
	j := jobs.From(ctx)
	if j == nil {
		ctx, j = r.Jobs.Start(ctx, it.Action)
		defer func() { j.Finish(err) }()
	}
	res, err := a.Run(ctx, it.Args)
	it.Status = inbox.Approved
	if err != nil {
		it.Status, it.Result, entry.Error = inbox.Failed, err.Error(), err.Error()
	} else if res != nil {
		it.Result, entry.Result = res.Message, res.Message
	}
	return res, errors.Join(err, r.Inbox.Put(it), r.log(a, entry))
}

func (r *Runner) log(a Action, e audit.Entry) error {
	if r.Audit == nil || a.Effect == protocol.EffectRead {
		return nil
	}
	if err := r.Audit.Append(e); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}
