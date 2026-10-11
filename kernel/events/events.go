// Package events is the bus: plugins, the engine and agents publish typed events such as
// deploy.finished, and hooks subscribe by topic pattern.
package events

import (
	"path"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/protocol"
)

type Event = protocol.Event

type sub struct {
	pattern string
	fn      func(Event)
}

type Bus struct {
	mu   sync.RWMutex
	subs map[int]sub
	next int
}

func NewBus() *Bus { return &Bus{subs: map[int]sub{}} }

// Match says whether topic matches pattern; * stands for one dot-separated part, and a lone * for any topic.
func Match(pattern, topic string) bool {
	if pattern == "*" {
		return true
	}
	ok, _ := path.Match(strings.ReplaceAll(pattern, ".", "/"), strings.ReplaceAll(topic, ".", "/"))
	return ok
}

// Subscribe calls fn for every event whose topic matches pattern, until the returned stop.
func (b *Bus) Subscribe(pattern string, fn func(Event)) (stop func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	b.subs[id] = sub{pattern, fn}
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, id)
	}
}

// Publish delivers e to its subscribers in turn, on the caller's goroutine; a slow subscriber
// goes async itself.
func (b *Bus) Publish(e Event) {
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339)
	}
	b.mu.RLock()
	var fns []func(Event)
	for _, s := range b.subs {
		if Match(s.pattern, e.Topic) {
			fns = append(fns, s.fn)
		}
	}
	b.mu.RUnlock()
	for _, fn := range fns {
		fn(e)
	}
}

// Hooks subscribes `on:` entries: each topic pattern's steps go to run with the event that fired them.
func (b *Bus) Hooks(on map[string][]string, run func(steps []string, e Event)) (stop func()) {
	var stops []func()
	for pattern, steps := range on {
		stops = append(stops, b.Subscribe(pattern, func(e Event) { run(steps, e) }))
	}
	return func() {
		for _, s := range stops {
			s()
		}
	}
}
