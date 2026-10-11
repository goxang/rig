// Package conformance checks a plugin speaks rig/1: what every plugin must do, and what each
// capability it declares promises. `rig plugin test` and the SDKs' tests run it.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/rpc"
)

type Result struct {
	Check string
	Err   error
}

type Options struct {
	// Wait bounds every call; zero is 5s.
	Wait time.Duration
	Tap  func(dir byte, line []byte)
}

type run struct {
	opts    Options
	p       *rpc.Process
	results []Result
	mu      sync.Mutex
	batches map[string]chan protocol.StreamBatch
}

func (r *run) check(name string, f func() error) {
	r.results = append(r.results, Result{Check: name, Err: f()})
}

func (r *run) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.opts.Wait)
}

func (r *run) call(method string, params, result any) error {
	ctx, cancel := r.ctx()
	defer cancel()
	return r.p.Call(ctx, method, params, result)
}

// callRaw answers the raw result, validated against the method's result definition.
func (r *run) callValid(method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := r.call(method, params, &raw); err != nil {
		return nil, err
	}
	spec, _ := protocol.Method(method)
	if errs := protocol.Validate(spec.Result, raw); errs != nil {
		return raw, fmt.Errorf("invalid %s: %v", spec.Result, errs)
	}
	return raw, nil
}

func wantCode(err error, codes ...int) error {
	var e *protocol.Error
	if !errors.As(err, &e) {
		return fmt.Errorf("want error %v, got %v", codes, err)
	}
	for _, c := range codes {
		if e.Code == c {
			return nil
		}
	}
	return fmt.Errorf("want error %v, got %d %s", codes, e.Code, e.Message)
}

// tolerated are the errors a source may answer in a bare environment: a missing tool, needed input.
func tolerated(err error) error {
	if err == nil {
		return nil
	}
	return wantCode(err, protocol.CodeUnavailable, protocol.CodeNeedsInput)
}

func required(s protocol.Schema) bool {
	r, _ := s["required"].([]any)
	return len(r) > 0
}

// Run starts the plugin from cmd, runs every check that applies and shuts it down.
func Run(cmd *exec.Cmd, opts Options) ([]Result, error) {
	if opts.Wait == 0 {
		opts.Wait = 5 * time.Second
	}
	r := &run{opts: opts, batches: map[string]chan protocol.StreamBatch{}}
	p, err := rpc.Start(cmd, rpc.Options{
		Tap: opts.Tap,
		OnNotify: func(method string, params json.RawMessage) {
			if method != protocol.MethodNotifyStream {
				return
			}
			var b protocol.StreamBatch
			_ = json.Unmarshal(params, &b)
			r.mu.Lock()
			ch := r.batches[b.ID]
			r.mu.Unlock()
			if ch != nil {
				ch <- b
			}
		},
	})
	if err != nil {
		return nil, err
	}
	r.p = p
	var info protocol.InitializeResult
	r.check("initialize", func() error {
		params := map[string]any{
			"host":         protocol.HostInfo{Name: "rig-conformance", Version: "0"},
			"protocols":    []string{"rig/0", protocol.Version},
			"capabilities": map[string]any{"widgets": map[string]int{"table": 1}, "x-future": true},
			"x-future":     "unknown fields are ignored",
		}
		raw, err := r.callValid(protocol.MethodInitialize, params)
		if err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &info)
		if info.Protocol != protocol.Version {
			return fmt.Errorf("protocol %q, want %s", info.Protocol, protocol.Version)
		}
		if info.Name == "" {
			return errors.New("no name")
		}
		return nil
	})
	if r.results[0].Err != nil {
		_ = p.Close(opts.Wait)
		return r.results, nil
	}
	if info.Provides == nil {
		info.Provides = &protocol.Provides{}
	}
	caps := map[string]bool{}
	for _, c := range info.Capabilities {
		caps[c] = true
	}
	r.check("configure", func() error {
		_, err := r.callValid(protocol.MethodConfigure, protocol.ConfigureParams{Config: protocol.Args{}})
		return err
	})
	r.check("unknown method", func() error {
		return wantCode(r.call("x-conformance.nothing", protocol.Empty{}, nil), protocol.CodeMethodNotFound)
	})
	r.check("unknown source", func() error {
		return wantCode(r.call(protocol.MethodQuery, protocol.QueryParams{Source: "x-conformance.nothing"}, nil), protocol.CodeNotFound)
	})
	r.check("invalid params", func() error {
		return wantCode(r.call(protocol.MethodQuery, map[string]any{"source": 7}, nil), protocol.CodeInvalidParams)
	})
	r.check("cancel of an unknown request", func() error {
		if err := p.Notify(protocol.MethodNotifyCancel, protocol.CancelParams{ID: 987654321}); err != nil {
			return err
		}
		return wantCode(r.call("x-conformance.still-there", protocol.Empty{}, nil), protocol.CodeMethodNotFound)
	})
	for _, name := range sortedKeys(info.Provides.Sources) {
		s := info.Provides.Sources[name]
		if required(s.Args) {
			continue
		}
		if s.Stream {
			r.check("stream "+name, func() error { return r.stream(name) })
			continue
		}
		r.check("query "+name, func() error {
			raw, err := r.callValid(protocol.MethodQuery, protocol.QueryParams{Source: name, Args: protocol.Args{}})
			if err != nil {
				return tolerated(err)
			}
			var f protocol.Frame
			_ = json.Unmarshal(raw, &f)
			return protocol.CheckFrame(&f)
		})
	}
	if caps["doctor"] {
		r.check("doctor", func() error {
			_, err := r.callValid(protocol.MethodDoctor, protocol.DoctorParams{})
			return err
		})
	}
	if info.Provides.Search {
		r.check("search", func() error {
			_, err := r.callValid(protocol.MethodSearch, protocol.SearchParams{Query: "x", Limit: 5})
			return err
		})
	}
	r.check("shutdown", func() error { return p.Close(opts.Wait) })
	return r.results, nil
}

// stream starts a stream, takes its first batch, cancels it and expects the final batch.
func (r *run) stream(name string) error {
	id := "conformance-" + name
	ch := make(chan protocol.StreamBatch, 64)
	r.mu.Lock()
	r.batches[id] = ch
	r.mu.Unlock()
	if err := r.call(protocol.MethodStream, protocol.StreamParams{ID: id, Source: name, Args: protocol.Args{}}, nil); err != nil {
		return tolerated(err)
	}
	cancelled, seq := false, 0
	deadline := time.After(2 * r.opts.Wait)
	for {
		select {
		case b := <-ch:
			raw, _ := json.Marshal(b)
			if errs := protocol.Validate("StreamBatch", raw); errs != nil {
				return fmt.Errorf("invalid batch: %v", errs)
			}
			if b.Seq != seq+1 {
				return fmt.Errorf("batch seq %d after %d", b.Seq, seq)
			}
			seq = b.Seq
			if b.Frame != nil {
				if err := protocol.CheckFrame(b.Frame); err != nil {
					return err
				}
			}
			_ = r.p.Notify(protocol.MethodNotifyAck, protocol.Ack{ID: id, Seq: b.Seq})
			if b.Done {
				if b.Error != nil {
					return tolerated(b.Error)
				}
				return nil
			}
			if !cancelled {
				cancelled = true
				_ = r.p.Notify(protocol.MethodNotifyCancel, protocol.CancelParams{ID: id})
			}
		case <-deadline:
			if !cancelled {
				return errors.New("no batch")
			}
			return errors.New("no final batch (done) after $/cancel")
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
