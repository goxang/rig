// Package sdk writes rig plugins in Go: register sources, streams, actions and the rest, then Serve.
// It has no dependencies outside the standard library; the protocol, not this package, is the contract.
package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
)

const (
	Read   = EffectRead
	Write  = EffectWrite
	Danger = EffectDanger
)

type Result = RunResult

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Errorf is a protocol error with a code, such as CodeNotFound.
func Errorf(code int, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Unavailable reports a missing tool or service, with what to do about it.
func Unavailable(message, hint string) *Error {
	return &Error{Code: CodeUnavailable, Message: message, Data: &ErrorData{Hint: hint}}
}

type Option func(*options)

type options struct {
	desc, cache, language string
	keys, on              []string
	args                  Schema
}

func Describe(s string) Option           { return func(o *options) { o.desc = s } }
func Cache(d string) Option              { return func(o *options) { o.cache = d } }
func Language(l string) Option           { return func(o *options) { o.language = l } }
func Keys(k ...string) Option            { return func(o *options) { o.keys = k } }
func On(schemesOrKinds ...string) Option { return func(o *options) { o.on = schemesOrKinds } }

// Arguments declares the arguments of a handler that takes sdk.Args, which has no fields to read them from.
func Arguments(s Schema) Option { return func(o *options) { o.args = s } }

type handler struct {
	fn  reflect.Value
	arg reflect.Type
}

var ctxType = reflect.TypeOf((*Ctx)(nil))

// typed checks fn is func(*Ctx, T[, extra]) (out, error) or func(*Ctx, T[, extra]) error.
func typed(name string, fn any, extra reflect.Type, out reflect.Type) handler {
	v := reflect.ValueOf(fn)
	t := v.Type()
	in := 2
	if extra != nil {
		in = 3
	}
	ok := t.Kind() == reflect.Func && t.NumIn() == in && t.In(0) == ctxType && (extra == nil || t.In(2) == extra)
	if out == nil {
		ok = ok && t.NumOut() == 1
	} else {
		ok = ok && t.NumOut() == 2 && t.Out(0) == out
	}
	if !ok {
		panic(fmt.Sprintf("sdk: %s: handler is %s, want func(*sdk.Ctx, Args%s) %s", name, t, map[bool]string{true: ", " + fmt.Sprint(extra)}[extra != nil], map[bool]string{true: "error", false: "(" + fmt.Sprint(out) + ", error)"}[out == nil]))
	}
	return handler{fn: v, arg: t.In(1)}
}

func (h handler) call(c *Ctx, args Args, extra ...reflect.Value) (any, error) {
	a := reflect.New(h.arg)
	if len(args) > 0 {
		b, _ := json.Marshal(args)
		if err := json.Unmarshal(b, a.Interface()); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	out := h.fn.Call(append([]reflect.Value{reflect.ValueOf(c), a.Elem()}, extra...))
	err, _ := out[len(out)-1].Interface().(error)
	if len(out) == 1 {
		return nil, err
	}
	return out[0].Interface(), err
}

type Plugin struct {
	info       InitializeResult
	sources    map[string]handler
	streams    map[string]handler
	actions    map[string]handler
	plans      map[string]handler
	importers  map[string]func(*Ctx, ImportParams) (*ImportResult, error)
	views      map[string]func(*Ctx, RenderParams) (*Node, error)
	widgets    map[string]func(*Ctx, WidgetRenderParams) (*WidgetRenderResult, error)
	completers map[string]func(*Ctx, CompleteParams) ([]Completion, error)
	viewEvent  func(*Ctx, ViewEventParams) (*Node, error)
	resolve    func(*Ctx, Ref) (*Resolved, error)
	search     func(*Ctx, SearchParams) ([]Hit, error)
	doctor     func(*Ctx) []Check
	event      func(*Ctx, Event) error
	configure  func(*Ctx, ConfigureParams) error

	cfgMu sync.RWMutex
	cfg   ConfigureParams

	w       io.Writer
	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[string]chan *message
	running map[string]context.CancelFunc
	acks    map[string]*window
}

func New(name string) *Plugin {
	return &Plugin{
		info:    InitializeResult{Name: name, Protocol: Version, Provides: &Provides{}},
		sources: map[string]handler{}, streams: map[string]handler{}, actions: map[string]handler{}, plans: map[string]handler{},
		importers: map[string]func(*Ctx, ImportParams) (*ImportResult, error){}, views: map[string]func(*Ctx, RenderParams) (*Node, error){},
		widgets: map[string]func(*Ctx, WidgetRenderParams) (*WidgetRenderResult, error){}, completers: map[string]func(*Ctx, CompleteParams) ([]Completion, error){},
		pending: map[string]chan *message{}, running: map[string]context.CancelFunc{}, acks: map[string]*window{},
	}
}

func (p *Plugin) Version(v string) *Plugin { p.info.Version = v; return p }

// Implements declares contracts the plugin follows, such as metrics/1.
func (p *Plugin) Implements(contracts ...string) *Plugin {
	p.info.Provides.Implements = append(p.info.Provides.Implements, contracts...)
	return p
}

// Emits declares the event topics the plugin sends with Ctx.Emit.
func (p *Plugin) Emits(topics ...string) *Plugin {
	p.info.Provides.Events = append(p.info.Provides.Events, topics...)
	return p
}

func apply(opts []Option) options {
	var o options
	for _, f := range opts {
		f(&o)
	}
	return o
}

func (p *Plugin) sourceSpec(name string, h handler, stream bool, opts []Option) {
	o := apply(opts)
	if p.info.Provides.Sources == nil {
		p.info.Provides.Sources = map[string]SourceSpec{}
	}
	s := p.info.Provides.Sources[name]
	s.Description, s.Cache, s.Language, s.Args = first(o.desc, s.Description), first(o.cache, s.Cache), first(o.language, s.Language), ArgsSchema(h.arg)
	if o.args != nil {
		s.Args = o.args
	}
	s.Stream = s.Stream || stream
	p.info.Provides.Sources[name] = s
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Source answers query: fn is func(*sdk.Ctx, T) (*sdk.Frame, error), T a struct or sdk.Args.
func (p *Plugin) Source(name string, fn any, opts ...Option) *Plugin {
	h := typed(name, fn, nil, reflect.TypeOf((*Frame)(nil)))
	p.sources[name] = h
	p.sourceSpec(name, h, false, opts)
	return p
}

// Stream answers stream: fn is func(*sdk.Ctx, T, *sdk.Out) error and returns when the stream ends.
func (p *Plugin) Stream(name string, fn any, opts ...Option) *Plugin {
	h := typed(name, fn, reflect.TypeOf((*Out)(nil)), nil)
	p.streams[name] = h
	p.sourceSpec(name, h, true, opts)
	return p
}

// Action answers run: fn is func(*sdk.Ctx, T) (*sdk.Result, error); effect is Read, Write or Danger.
func (p *Plugin) Action(name, effect string, fn any, opts ...Option) *Plugin {
	h := typed(name, fn, nil, reflect.TypeOf((*Result)(nil)))
	p.actions[name] = h
	o := apply(opts)
	if p.info.Provides.Actions == nil {
		p.info.Provides.Actions = map[string]ActionSpec{}
	}
	a := p.info.Provides.Actions[name]
	a.Effect, a.Description, a.Args, a.Keys, a.On = effect, o.desc, ArgsSchema(h.arg), o.keys, o.on
	if o.args != nil {
		a.Args = o.args
	}
	p.info.Provides.Actions[name] = a
	return p
}

// Plan is the dry run of an action registered with Action: it returns the changes the action would make.
func (p *Plugin) Plan(name string, fn any) *Plugin {
	p.plans[name] = typed(name, fn, nil, reflect.TypeOf((*Result)(nil)))
	a := p.info.Provides.Actions[name]
	a.Plan = true
	if p.info.Provides.Actions == nil {
		p.info.Provides.Actions = map[string]ActionSpec{}
	}
	p.info.Provides.Actions[name] = a
	return p
}

func (p *Plugin) Importer(name string, files []string, fn func(*Ctx, ImportParams) (*ImportResult, error)) *Plugin {
	p.importers[name] = fn
	if p.info.Provides.Importers == nil {
		p.info.Provides.Importers = map[string]ImporterSpec{}
	}
	p.info.Provides.Importers[name] = ImporterSpec{Files: files}
	return p
}

// View is a dynamic view: render returns its widget tree.
func (p *Plugin) View(name string, spec ViewSpec, fn func(*Ctx, RenderParams) (*Node, error)) *Plugin {
	p.views[name] = fn
	if p.info.Provides.Views == nil {
		p.info.Provides.Views = map[string]ViewSpec{}
	}
	p.info.Provides.Views[name] = spec
	return p
}

// OnViewEvent handles input in a dynamic view and returns its new tree.
func (p *Plugin) OnViewEvent(fn func(*Ctx, ViewEventParams) (*Node, error)) *Plugin {
	p.viewEvent = fn
	return p
}

// Widget is a custom widget: it draws lines of at most size.w cells.
func (p *Plugin) Widget(name string, fn func(*Ctx, WidgetRenderParams) (*WidgetRenderResult, error)) *Plugin {
	p.widgets[name] = fn
	p.info.Provides.Widgets = append(p.info.Provides.Widgets, name)
	return p
}

func (p *Plugin) Completer(language string, fn func(*Ctx, CompleteParams) ([]Completion, error)) *Plugin {
	p.completers[language] = fn
	p.info.Provides.Languages = append(p.info.Provides.Languages, language)
	return p
}

// Resolver opens refs of the given schemes.
func (p *Plugin) Resolver(schemes []string, fn func(*Ctx, Ref) (*Resolved, error)) *Plugin {
	p.resolve = fn
	p.info.Provides.Schemes = append(p.info.Provides.Schemes, schemes...)
	return p
}

func (p *Plugin) Searcher(fn func(*Ctx, SearchParams) ([]Hit, error)) *Plugin {
	p.search = fn
	p.info.Provides.Search = true
	return p
}

func (p *Plugin) Doctor(fn func(*Ctx) []Check) *Plugin { p.doctor = fn; return p }

func (p *Plugin) OnEvent(fn func(*Ctx, Event) error) *Plugin { p.event = fn; return p }

// OnConfigure sees the plugin's config, components and secrets; Ctx.Config has them later too.
func (p *Plugin) OnConfigure(fn func(*Ctx, ConfigureParams) error) *Plugin {
	p.configure = fn
	return p
}

// Serve speaks rig/1 on stdin and stdout until shutdown or EOF. Use stderr for logs.
func (p *Plugin) Serve() {
	if err := p.ServeIO(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, p.info.Name+":", err)
		os.Exit(1)
	}
}

func (p *Plugin) ServeIO(r io.Reader, w io.Writer) error {
	p.w = w
	br := bufio.NewReader(r)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var m message
			if jerr := json.Unmarshal(line, &m); jerr != nil {
				p.send(&message{Error: &Error{Code: CodeParseError, Message: jerr.Error()}, ID: json.RawMessage("null")})
			} else if m.Method == "" {
				p.mu.Lock()
				ch := p.pending[string(m.ID)]
				delete(p.pending, string(m.ID))
				p.mu.Unlock()
				if ch != nil {
					ch <- &m
				}
			} else if m.ID == nil {
				p.notification(&m)
			} else if m.Method == MethodShutdown {
				p.cancelAll()
				wg.Wait()
				p.reply(m.ID, Empty{}, nil)
				return nil
			} else {
				wg.Add(1)
				go func() { defer wg.Done(); p.request(&m) }()
			}
		}
		if err == io.EOF {
			p.cancelAll()
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (p *Plugin) cancelAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.running {
		c()
	}
	for id, ch := range p.pending {
		ch <- &message{Error: &Error{Code: CodeUnavailable, Message: "host gone"}}
		delete(p.pending, id)
	}
}

func (p *Plugin) send(m *message) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(&message{JSONRPC: "2.0", ID: m.ID, Error: &Error{Code: CodeInternal, Message: err.Error()}})
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, _ = p.w.Write(append(b, '\n'))
}

func (p *Plugin) reply(id json.RawMessage, result any, err error) {
	if err != nil {
		p.send(&message{ID: id, Error: asError(err)})
		return
	}
	b, merr := json.Marshal(result)
	if merr != nil || string(b) == "null" {
		b = []byte("{}")
	}
	p.send(&message{ID: id, Result: b})
}

func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var exit *exec.ExitError
	if errors.Is(err, exec.ErrNotFound) || errors.As(err, &exit) {
		// a tool that is missing or fails is the environment's doing, not the plugin's
		return &Error{Code: CodeUnavailable, Message: err.Error()}
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

func (p *Plugin) notify(method string, params any) {
	b, _ := json.Marshal(params)
	p.send(&message{Method: method, Params: b})
}

func (p *Plugin) notification(m *message) {
	switch m.Method {
	case MethodNotifyCancel:
		var c struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(m.Params, &c)
		key := string(c.ID)
		if s, err := strconv.Unquote(key); err == nil {
			key = s
		}
		p.mu.Lock()
		cancel := p.running[key]
		if cancel == nil {
			cancel = p.running[string(c.ID)]
		}
		p.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case MethodNotifyAck:
		var a Ack
		_ = json.Unmarshal(m.Params, &a)
		p.mu.Lock()
		w := p.acks[a.ID]
		p.mu.Unlock()
		if w != nil {
			w.ack(a.Seq)
		}
	}
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	return v, nil
}

func (p *Plugin) newCtx(ctx context.Context, call *Context, component, job string) *Ctx {
	c := &Ctx{Context: ctx, p: p, Component: component, job: job}
	if call != nil {
		c.Call = *call
	}
	return c
}

func (p *Plugin) request(m *message) {
	ctx, cancel := context.WithCancel(context.Background())
	key := string(m.ID)
	p.mu.Lock()
	p.running[key] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.running, key)
		p.mu.Unlock()
		cancel()
	}()
	var result any
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
			}
		}()
		result, err = p.dispatch(ctx, m)
	}()
	p.reply(m.ID, result, err)
}

func notFound(what, name string) error { return Errorf(CodeNotFound, "no %s %q", what, name) }

func (p *Plugin) dispatch(ctx context.Context, m *message) (any, error) {
	switch m.Method {
	case MethodInitialize:
		in, err := decode[InitializeParams](m.Params)
		if err != nil {
			return nil, err
		}
		for _, v := range in.Protocols {
			if v == Version {
				return p.initialize(), nil
			}
		}
		return nil, Errorf(CodeInvalidRequest, "%s speaks %s, the host %v", p.info.Name, Version, in.Protocols)
	case MethodConfigure:
		in, err := decode[ConfigureParams](m.Params)
		if err != nil {
			return nil, err
		}
		p.cfgMu.Lock()
		p.cfg = in
		p.cfgMu.Unlock()
		if p.configure != nil {
			return Empty{}, p.configure(p.newCtx(ctx, in.Context, "", ""), in)
		}
		return Empty{}, nil
	case MethodQuery:
		in, err := decode[QueryParams](m.Params)
		if err != nil {
			return nil, err
		}
		h, ok := p.sources[in.Source]
		if !ok {
			return nil, notFound("source", in.Source)
		}
		c := p.newCtx(ctx, in.Context, in.Component, "")
		c.Cursor = in.Cursor
		return h.call(c, in.Args)
	case MethodStream:
		in, err := decode[StreamParams](m.Params)
		if err != nil {
			return nil, err
		}
		h, ok := p.streams[in.Source]
		if !ok {
			return nil, notFound("stream", in.Source)
		}
		p.startStream(in, h)
		return Empty{}, nil
	case MethodRun, MethodPlan:
		in, err := decode[RunParams](m.Params)
		if err != nil {
			return nil, err
		}
		hs := p.actions
		if m.Method == MethodPlan {
			hs = p.plans
		}
		h, ok := hs[in.Action]
		if !ok {
			return nil, notFound(m.Method+" action", in.Action)
		}
		return h.call(p.newCtx(ctx, in.Context, in.Component, in.Job), in.Args)
	case MethodImport:
		in, err := decode[ImportParams](m.Params)
		if err != nil {
			return nil, err
		}
		f, ok := p.importers[in.Importer]
		if !ok {
			return nil, notFound("importer", in.Importer)
		}
		return f(p.newCtx(ctx, nil, "", ""), in)
	case MethodComplete:
		in, err := decode[CompleteParams](m.Params)
		if err != nil {
			return nil, err
		}
		f, ok := p.completers[in.Language]
		if !ok {
			return nil, notFound("language", in.Language)
		}
		items, err := f(p.newCtx(ctx, in.Context, "", ""), in)
		return CompleteResult{Items: nonNil(items)}, err
	case MethodRender:
		in, err := decode[RenderParams](m.Params)
		if err != nil {
			return nil, err
		}
		f, ok := p.views[in.View]
		if !ok {
			return nil, notFound("view", in.View)
		}
		return f(p.newCtx(ctx, in.Context, "", ""), in)
	case MethodViewEvent:
		in, err := decode[ViewEventParams](m.Params)
		if err != nil {
			return nil, err
		}
		if p.viewEvent == nil {
			return nil, notFound("view event handler for", in.View)
		}
		return p.viewEvent(p.newCtx(ctx, in.Context, "", ""), in)
	case MethodWidgetRender:
		in, err := decode[WidgetRenderParams](m.Params)
		if err != nil {
			return nil, err
		}
		f, ok := p.widgets[in.Widget]
		if !ok {
			return nil, notFound("widget", in.Widget)
		}
		return f(p.newCtx(ctx, nil, "", ""), in)
	case MethodResolve:
		in, err := decode[ResolveParams](m.Params)
		if err != nil {
			return nil, err
		}
		if p.resolve == nil {
			return nil, notFound("resolver for", in.Ref)
		}
		return p.resolve(p.newCtx(ctx, in.Context, "", ""), in.Ref)
	case MethodSearch:
		in, err := decode[SearchParams](m.Params)
		if err != nil {
			return nil, err
		}
		if p.search == nil {
			return SearchResult{Hits: []Hit{}}, nil
		}
		hits, err := p.search(p.newCtx(ctx, in.Context, "", ""), in)
		return SearchResult{Hits: nonNil(hits)}, err
	case MethodDoctor:
		in, err := decode[DoctorParams](m.Params)
		if err != nil {
			return nil, err
		}
		var checks []Check
		if p.doctor != nil {
			checks = p.doctor(p.newCtx(ctx, in.Context, "", ""))
		}
		return DoctorResult{Checks: nonNil(checks)}, nil
	case MethodEvent:
		in, err := decode[Event](m.Params)
		if err != nil {
			return nil, err
		}
		if p.event != nil {
			return Empty{}, p.event(p.newCtx(ctx, nil, "", ""), in)
		}
		return Empty{}, nil
	}
	return nil, Errorf(CodeMethodNotFound, "%s does not implement %s", p.info.Name, m.Method)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (p *Plugin) initialize() InitializeResult {
	info := p.info
	var caps []string
	for c, on := range map[string]bool{"stream": len(p.streams) > 0, "plan": len(p.plans) > 0, "render": len(p.views) > 0,
		"widget.render": len(p.widgets) > 0, "complete": len(p.completers) > 0, "resolve": p.resolve != nil,
		"search": p.search != nil, "doctor": p.doctor != nil, "event": p.event != nil, "import": len(p.importers) > 0} {
		if on {
			caps = append(caps, c)
		}
	}
	sort.Strings(caps)
	info.Capabilities = caps
	return info
}

// call sends a host/* request and waits for its answer.
func (p *Plugin) call(ctx context.Context, method string, params, result any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan *message, 1)
	p.mu.Lock()
	p.nextID++
	id := json.RawMessage(strconv.Itoa(p.nextID))
	p.pending[string(id)] = ch
	p.mu.Unlock()
	p.send(&message{ID: id, Method: method, Params: b})
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, string(id))
		p.mu.Unlock()
		p.notify(MethodNotifyCancel, CancelParams{ID: id})
		return ctx.Err()
	}
}
