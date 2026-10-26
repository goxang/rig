package sdk

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

// Ctx is one call: its cancellation and deadline, the caller's context, and the way back to the host.
type Ctx struct {
	context.Context
	// Call is who called and where: project, env, user, scope.
	Call Context
	// Component is the rig.yaml component the call is for, if any.
	Component string
	// Cursor is the page a query asks for, from a previous frame's meta.next.
	Cursor string
	p      *Plugin
	job    string
}

// Config is what configure sent: the plugin's config, its components and its declared secrets.
func (c *Ctx) Config() ConfigureParams {
	c.p.cfgMu.RLock()
	defer c.p.cfgMu.RUnlock()
	return c.p.cfg
}

// Spec is the configured component the call is for.
func (c *Ctx) Spec() Args {
	return c.Config().Components[c.Component]
}

// Progress reports how far a run is; total 0 means unknown.
func (c *Ctx) Progress(done, total int, message string) {
	c.p.notify(MethodNotifyProgress, Progress{Job: c.job, Done: done, Total: total, Message: message})
}

// Line is one line of a run's output.
func (c *Ctx) Line(s string) {
	c.p.notify(MethodNotifyProgress, Progress{Job: c.job, Line: s})
}

func (c *Ctx) Log(level, message string) {
	c.p.notify(MethodHostLog, LogParams{Level: level, Message: message})
}

// Query reads another plugin's source through the host, as far as this plugin's permissions allow.
func (c *Ctx) Query(source string, args Args) (*Frame, error) {
	var f Frame
	return &f, c.p.call(c, MethodHostQuery, QueryParams{Source: source, Args: args, Context: &c.Call}, &f)
}

// Run runs another plugin's action through the host; policy applies as for any caller.
func (c *Ctx) Run(action string, args Args) (*Result, error) {
	var r Result
	return &r, c.p.call(c, MethodHostRun, RunParams{Action: action, Args: args, Context: &c.Call}, &r)
}

func (c *Ctx) Ask(q AskParams) (AskResult, error) {
	var r AskResult
	return r, c.p.call(c, MethodHostAsk, q, &r)
}

// AI completes a prompt with the user's configured backend.
func (c *Ctx) AI(prompt, system string) (string, error) {
	var r AIResult
	return r.Text, c.p.call(c, MethodHostAI, AIParams{Prompt: prompt, System: system}, &r)
}

// State reads per-plugin state, scoped to the project and environment.
func (c *Ctx) State(key string) (StateValue, error) {
	var r StateValue
	return r, c.p.call(c, MethodHostStateGet, StateGetParams{Key: key}, &r)
}

func (c *Ctx) SetState(key string, value any) error {
	return c.p.call(c, MethodHostStateSet, StateSetParams{Key: key, Value: value}, nil)
}

// Secret reads a secret the manifest declares.
func (c *Ctx) Secret(name string) (string, error) {
	var r SecretValue
	return r.Value, c.p.call(c, MethodHostSecret, SecretParams{Name: name}, &r)
}

// SetSecret stores a secret the manifest declares for this project, as rig secret set does; an
// empty value removes it.
func (c *Ctx) SetSecret(name, value string) error {
	return c.p.call(c, MethodHostSecretSet, SecretSetParams{Name: name, Value: value}, nil)
}

func (c *Ctx) Emit(e Event) error {
	return c.p.call(c, MethodHostEmit, e, nil)
}

const (
	flushEvery = 50 * time.Millisecond
	flushRows  = 500
	ackWindow  = 8
)

// window holds a stream back once ackWindow batches are unacknowledged.
type window struct {
	mu    sync.Mutex
	cond  *sync.Cond
	acked int
}

func (w *window) ack(seq int) {
	w.mu.Lock()
	if seq > w.acked {
		w.acked = seq
	}
	w.mu.Unlock()
	w.cond.Broadcast()
}

// Out is a stream's output: frames and lines, batched every 50 ms or 500 rows.
type Out struct {
	c     *Ctx
	id    string
	win   *window
	mu    sync.Mutex
	seq   int
	frame *Frame
	lines []string
	ended bool
}

func (o *Out) rows() int {
	n := len(o.lines)
	if o.frame != nil && len(o.frame.Columns) > 0 {
		n += len(o.frame.Columns[0])
	}
	return n
}

// Frame adds rows; frames with the same fields merge into one batch.
func (o *Out) Frame(f *Frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.frame != nil && !sameFields(o.frame, f) {
		if err := o.flushLocked(false, nil); err != nil {
			return err
		}
	}
	if o.frame == nil {
		o.frame = &Frame{Name: f.Name, Fields: f.Fields, Columns: make([][]any, len(f.Fields))}
	}
	for i := range f.Columns {
		o.frame.Columns[i] = append(o.frame.Columns[i], f.Columns[i]...)
	}
	if o.rows() >= flushRows {
		return o.flushLocked(false, nil)
	}
	return nil
}

func (o *Out) Line(s string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = append(o.lines, s)
	if o.rows() >= flushRows {
		return o.flushLocked(false, nil)
	}
	return nil
}

func sameFields(a, b *Frame) bool {
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for i := range a.Fields {
		if a.Fields[i] != b.Fields[i] {
			return false
		}
	}
	return true
}

func (o *Out) flushLocked(done bool, err *Error) error {
	if o.ended || !done && o.frame == nil && len(o.lines) == 0 {
		return nil
	}
	o.ended = done
	o.win.mu.Lock()
	for !done && o.seq-o.win.acked >= ackWindow && o.c.Err() == nil {
		o.win.cond.Wait()
	}
	o.win.mu.Unlock()
	if !done && o.c.Err() != nil {
		return o.c.Err()
	}
	o.seq++
	o.c.p.notify(MethodNotifyStream, StreamBatch{ID: o.id, Seq: o.seq, Frame: o.frame, Lines: o.lines, Done: done, Error: err})
	o.frame, o.lines = nil, nil
	return nil
}

func (p *Plugin) startStream(in StreamParams, h handler) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &window{}
	w.cond = sync.NewCond(&w.mu)
	c := p.newCtx(ctx, in.Context, in.Component, "")
	o := &Out{c: c, id: in.ID, win: w}
	p.mu.Lock()
	p.running[in.ID] = cancel
	p.acks[in.ID] = w
	p.mu.Unlock()
	stop := context.AfterFunc(ctx, w.cond.Broadcast)
	go func() {
		t := time.NewTicker(flushEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				o.mu.Lock()
				_ = o.flushLocked(false, nil)
				o.mu.Unlock()
			}
		}
	}()
	go func() {
		_, err := h.call(c, in.Args, reflect.ValueOf(o))
		cancel()
		stop()
		p.mu.Lock()
		delete(p.running, in.ID)
		delete(p.acks, in.ID)
		p.mu.Unlock()
		var e *Error
		if err != nil && !errors.Is(err, context.Canceled) {
			e = asError(err)
		}
		o.mu.Lock()
		_ = o.flushLocked(true, e)
		o.mu.Unlock()
	}()
}
