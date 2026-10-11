package api

import (
	"context"
	"strings"
	"sync"

	"github.com/goxang/rig/kernel/catalog"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/inbox"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
)

// Fake is an Engine of canned answers that records what it was asked to run.
type Fake struct {
	Cat     *catalog.Catalog
	Frames  map[string]*protocol.Frame
	Results map[string]*protocol.RunResult
	Errs    map[string]error
	Subject identity.Subject

	mu   sync.Mutex
	Ran  []string
	jobs jobs.Manager
}

var _ Engine = (*Fake)(nil)

func (f *Fake) Catalog(context.Context) (*catalog.Catalog, error) {
	if f.Cat == nil {
		return catalog.New(), nil
	}
	return f.Cat, nil
}

func (f *Fake) Query(_ context.Context, source string, _ protocol.Args) (*protocol.Frame, error) {
	if err := f.Errs[source]; err != nil {
		return nil, err
	}
	if fr, ok := f.Frames[source]; ok {
		return fr, nil
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no source " + source}
}

func (f *Fake) Stream(ctx context.Context, source string, args protocol.Args) (<-chan protocol.StreamBatch, error) {
	fr, err := f.Query(ctx, source, args)
	if err != nil {
		return nil, err
	}
	ch := make(chan protocol.StreamBatch, 2)
	ch <- protocol.StreamBatch{ID: source, Seq: 1, Frame: fr}
	ch <- protocol.StreamBatch{ID: source, Seq: 2, Done: true}
	close(ch)
	return ch, nil
}

func (f *Fake) Run(ctx context.Context, action string, _ protocol.Args, _ RunOpts) (*jobs.Job, *protocol.RunResult, error) {
	f.mu.Lock()
	f.Ran = append(f.Ran, action)
	f.mu.Unlock()
	_, j := f.jobs.Start(ctx, action)
	err := f.Errs[action]
	j.Finish(err)
	return j, f.Results[action], err
}

func (f *Fake) Jobs(context.Context) []*jobs.Job { return f.jobs.List() }

func (f *Fake) Events(context.Context, string) (<-chan protocol.Event, error) {
	return make(chan protocol.Event), nil
}

func (f *Fake) Search(ctx context.Context, q string) ([]protocol.Hit, error) {
	c, _ := f.Catalog(ctx)
	var hits []protocol.Hit
	for _, r := range c.List("") {
		if strings.Contains(r.Name, q) {
			hits = append(hits, protocol.Hit{Title: r.Name, Kind: r.Kind, Ref: r.Ref()})
		}
	}
	return hits, nil
}

func (f *Fake) Resolve(_ context.Context, ref string) (*protocol.Resolved, error) {
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: ref}
}

func (f *Fake) Complete(context.Context, string, string, int) ([]protocol.Completion, error) {
	return nil, nil
}

func (f *Fake) Session(context.Context) identity.Subject { return f.Subject }

func (f *Fake) Inbox(context.Context) []inbox.Item { return nil }

func (f *Fake) Decide(_ context.Context, id string, _ bool) (*protocol.RunResult, error) {
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no inbox item " + id}
}

func (f *Fake) Provides(context.Context) protocol.Provides {
	out := protocol.Provides{Sources: map[string]protocol.SourceSpec{}, Actions: map[string]protocol.ActionSpec{}}
	for n := range f.Frames {
		out.Sources[n] = protocol.SourceSpec{}
	}
	for n := range f.Results {
		out.Actions[n] = protocol.ActionSpec{Effect: protocol.EffectWrite}
	}
	return out
}
