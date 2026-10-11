package view

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/goxang/rig/protocol"
)

// Fetcher shares fetches: one request per distinct source and args however many widgets bind to
// it, a TTL per binding, and backoff after errors. Renderers ask only for what is visible and
// cancel with the context when it hides.
type Fetcher struct {
	Query func(ctx context.Context, source string, args protocol.Args) (*protocol.Frame, error)
	// TTL is how long a frame stays fresh when the binding gives no every:.
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]*entry
	sf    singleflight.Group
	now   func() time.Time
}

type entry struct {
	frame *protocol.Frame
	err   error
	at    time.Time
	fails int
}

// Key is what makes two bindings the same fetch.
func Key(b protocol.Binding) string {
	raw, _ := json.Marshal(b.Args)
	return b.Source + "\x00" + string(raw)
}

func (f *Fetcher) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *Fetcher) ttl(b protocol.Binding) time.Duration {
	if d, err := time.ParseDuration(b.Every); err == nil && d > 0 {
		return d
	}
	if f.TTL > 0 {
		return f.TTL
	}
	return 10 * time.Second
}

// Get is b's frame from the cache while fresh, else fetched once for every caller waiting on it.
func (f *Fetcher) Get(ctx context.Context, b protocol.Binding) (*protocol.Frame, error) {
	k := Key(b)
	f.mu.Lock()
	if f.cache == nil {
		f.cache = map[string]*entry{}
	}
	e := f.cache[k]
	f.mu.Unlock()
	if e != nil {
		wait := f.ttl(b)
		if e.err != nil {
			wait = min(time.Second<<min(e.fails, 6), time.Minute)
		}
		if f.clock().Sub(e.at) < wait {
			return e.frame, e.err
		}
	}
	v, err, _ := f.sf.Do(k, func() (any, error) {
		fr, err := f.Query(ctx, b.Source, b.Args)
		f.mu.Lock()
		defer f.mu.Unlock()
		n := &entry{frame: fr, err: err, at: f.clock()}
		if err != nil && ctx.Err() == nil {
			if old := f.cache[k]; old != nil {
				n.frame, n.fails = old.frame, old.fails
			}
			n.fails++
		}
		if ctx.Err() == nil {
			f.cache[k] = n
		}
		return fr, err
	})
	fr, _ := v.(*protocol.Frame)
	return fr, err
}

// Forget drops cached frames so the next Get fetches again, as a manual refresh does.
func (f *Fetcher) Forget() {
	f.mu.Lock()
	f.cache = nil
	f.mu.Unlock()
}

// Fresh says whether b's cached frame is still inside its TTL, so asking again would not fetch.
func (f *Fetcher) Fresh(b protocol.Binding) bool {
	f.mu.Lock()
	e := f.cache[Key(b)]
	f.mu.Unlock()
	return e != nil && e.err == nil && f.clock().Sub(e.at) < f.ttl(b)
}
