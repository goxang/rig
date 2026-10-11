// Package rpc is the host's end of rig/1: newline-delimited JSON-RPC 2.0 to one plugin process.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/goxang/rig/protocol"
)

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *protocol.Error `json:"error,omitempty"`
}

type Options struct {
	// OnRequest answers the plugin's host/* requests; nil answers MethodNotFound.
	OnRequest func(ctx context.Context, method string, params json.RawMessage) (any, error)
	// OnNotify sees the plugin's notifications ($/progress, $/stream, host/log).
	OnNotify func(method string, params json.RawMessage)
	// Tap sees every line: dir '>' host to plugin, '<' plugin to host. Transcripts record through it.
	Tap func(dir byte, line []byte)
}

type Conn struct {
	opts    Options
	w       io.Writer
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[string]chan *Message
	running map[string]context.CancelFunc
	done    chan struct{}
	err     error
}

var ErrClosed = errors.New("plugin connection closed")

func New(r io.Reader, w io.Writer, opts Options) *Conn {
	c := &Conn{opts: opts, w: w, pending: map[string]chan *Message{}, running: map[string]context.CancelFunc{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) Err() error {
	<-c.done
	return c.err
}

func (c *Conn) read(r io.Reader) {
	br := bufio.NewReader(r)
	var err error
	for {
		var line []byte
		line, err = br.ReadBytes('\n')
		if len(line) > 0 {
			if c.opts.Tap != nil {
				c.opts.Tap('<', line)
			}
			c.handle(line)
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	if err == io.EOF {
		err = ErrClosed
	}
	c.err = err
	for id, ch := range c.pending {
		ch <- &Message{Error: &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}}
		delete(c.pending, id)
	}
	for _, cancel := range c.running {
		cancel()
	}
	c.mu.Unlock()
	close(c.done)
}

func (c *Conn) handle(line []byte) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return
	}
	switch {
	case m.Method == "":
		c.mu.Lock()
		ch := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	case m.ID == nil:
		if m.Method == protocol.MethodNotifyCancel {
			var p struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(m.Params, &p)
			c.mu.Lock()
			if cancel := c.running[string(p.ID)]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
		}
		if c.opts.OnNotify != nil {
			c.opts.OnNotify(m.Method, m.Params)
		}
	default:
		go c.serve(&m)
	}
}

func (c *Conn) serve(m *Message) {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[string(m.ID)] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.running, string(m.ID))
		c.mu.Unlock()
		cancel()
	}()
	var result any
	err := error(&protocol.Error{Code: protocol.CodeMethodNotFound, Message: "the host does not answer " + m.Method})
	if c.opts.OnRequest != nil {
		result, err = c.opts.OnRequest(ctx, m.Method, m.Params)
	}
	if err != nil {
		_ = c.write(&Message{ID: m.ID, Error: AsError(err)})
		return
	}
	b, merr := json.Marshal(result)
	if merr != nil || string(b) == "null" {
		b = []byte("{}")
	}
	_ = c.write(&Message{ID: m.ID, Result: b})
}

// AsError keeps a protocol error as it is and makes anything else an internal error.
func AsError(err error) *protocol.Error {
	var e *protocol.Error
	if errors.As(err, &e) {
		return e
	}
	return &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
}

func (c *Conn) write(m *Message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.opts.Tap != nil {
		c.opts.Tap('>', b)
	}
	_, err = c.w.Write(b)
	return err
}

// WriteRaw sends a line as it is, for tests that send what no well-behaved host would.
func (c *Conn) WriteRaw(line []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.opts.Tap != nil {
		c.opts.Tap('>', line)
	}
	_, err := c.w.Write(line)
	return err
}

// Call sends a request and waits for the answer; cancelling ctx sends $/cancel.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan *Message, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := json.RawMessage(strconv.Itoa(c.next))
	c.pending[string(id)] = ch
	c.mu.Unlock()
	// a plugin that stopped reading fills the pipe and blocks the write: the caller still honours ctx
	sent := make(chan error, 1)
	go func() { sent <- c.write(&Message{ID: id, Method: method, Params: b}) }()
	select {
	case err := <-sent:
		if err != nil {
			c.mu.Lock()
			delete(c.pending, string(id))
			c.mu.Unlock()
			return err
		}
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
		return ctx.Err()
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result != nil {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
		go func() { _ = c.Notify(protocol.MethodNotifyCancel, protocol.CancelParams{ID: id}) }()
		return ctx.Err()
	}
}

func (c *Conn) Notify(method string, params any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(&Message{Method: method, Params: b})
}

// Process is a plugin started as a child process, speaking on its stdin and stdout.
type Process struct {
	*Conn
	cmd   *exec.Cmd
	stdin io.Closer
}

// Start runs cmd and connects to it; stderr goes wherever cmd.Stderr points.
func Start(cmd *exec.Cmd, opts Options) (*Process, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Process{Conn: New(out, in, opts), cmd: cmd, stdin: in}, nil
}

func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Close asks the plugin to shut down, waits up to grace for it to exit, then kills it.
func (p *Process) Close(grace time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	callErr := p.Call(ctx, protocol.MethodShutdown, protocol.Empty{}, nil)
	_ = p.stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- p.cmd.Wait() }()
	select {
	case err := <-exited:
		if callErr != nil && !errors.Is(callErr, ErrClosed) {
			return fmt.Errorf("shutdown: %w", callErr)
		}
		return err
	case <-time.After(grace):
		_ = p.cmd.Process.Kill()
		<-exited
		return fmt.Errorf("plugin did not exit within %s of shutdown", grace)
	}
}
