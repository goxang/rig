package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/goxang/rig/kernel/catalog"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/inbox"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
)

// Hints on a needs-input error that say what the server wants asked before the call is retried.
const (
	HintConfirm = "confirm"
	HintReauth  = "reauth"
)

// Job is a job as it travels: its output and how it ended.
type Job struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end,omitzero"`
	Output []string  `json:"output,omitempty"`
	Error  string    `json:"error,omitempty"`
	// Waiting is the prompt of a running job that waits for its input.
	Waiting string `json:"waiting,omitempty"`
	// Step is the task step running ("3/7").
	Step string `json:"step,omitempty"`
}

func WireJob(j *jobs.Job) *Job {
	if j == nil {
		return nil
	}
	end, _, err := j.State()
	out := &Job{ID: j.ID, Label: j.Label, Start: j.Start, End: end, Output: j.Output()}
	if err != nil {
		out.Error = err.Error()
	}
	if p, ok := j.Waiting(); ok {
		out.Waiting = p
	}
	out.Step, _ = j.StepTimer()
	return out
}

func (w *Job) job() *jobs.Job {
	if w == nil {
		return nil
	}
	var err error
	if w.Error != "" {
		err = errors.New(w.Error)
	}
	j := jobs.Restore(w.Label, w.Start, w.End, w.Output, err)
	j.ID = w.ID
	return j
}

// Request is the body of an engine call.
type Request struct {
	Source  string            `json:"source,omitempty"`
	Action  string            `json:"action,omitempty"`
	Args    protocol.Args     `json:"args,omitempty"`
	DryRun  bool              `json:"dry_run,omitempty"`
	As      *identity.Subject `json:"as,omitempty"`
	Pattern string            `json:"pattern,omitempty"`
	Query   string            `json:"query,omitempty"`
	Ref     string            `json:"ref,omitempty"`
	ID      string            `json:"id,omitempty"`
	Approve bool              `json:"approve,omitempty"`
	Lang    string            `json:"language,omitempty"`
	Text    string            `json:"text,omitempty"`
	Pos     int               `json:"pos,omitempty"`
}

// Client is an Engine served by `rig serve`.
type Client struct {
	URL   string
	Token string
	Env   string
	// Fingerprint pins a self-signed server certificate (its SHA-256); empty verifies it the usual way.
	Fingerprint string
	// Ask puts a server's question to the user: for a confirm, ok says go ahead; for a reauth,
	// answer is a password or TOTP code. Without it such calls fail.
	Ask func(hint, message string) (answer string, ok bool)

	http *http.Client
}

var _ Engine = (*Client)(nil)

func (c *Client) client() *http.Client {
	if c.http == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if c.Fingerprint != "" {
			want := c.Fingerprint
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(s tls.ConnectionState) error {
				if len(s.PeerCertificates) == 0 {
					return errors.New("no server certificate")
				}
				sum := sha256.Sum256(s.PeerCertificates[0].Raw)
				if hex.EncodeToString(sum[:]) != want {
					return errors.New("server certificate changed: rig login again only if you trust the new one")
				}
				return nil
			}}
		}
		c.http = &http.Client{Transport: tr}
	}
	return c.http
}

// Do sends in to path and decodes the answer into out; it returns the protocol error the server sent.
func (c *Client) Do(ctx context.Context, path string, headers map[string]string, in, out any) error {
	resp, err := c.send(ctx, path, headers, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) send(ctx context.Context, path string, headers map[string]string, in any) (*http.Response, error) {
	method := "POST"
	var body io.Reader
	if in == nil {
		method = "GET"
	} else {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	if m, p, ok := strings.Cut(path, " "); ok {
		method, path = m, p
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Env != "" {
		req.Header.Set("X-Rig-Env", c.Env)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var e struct{ Error *protocol.Error }
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != nil {
			return nil, e.Error
		}
		return nil, fmt.Errorf("%s: %s", c.URL, resp.Status)
	}
	return resp, nil
}

// call runs an engine method, settling a confirm or reauth the server asks for and retrying once each.
func (c *Client) call(ctx context.Context, method string, in Request) (*http.Response, error) {
	headers := map[string]string{}
	for range 3 {
		resp, err := c.send(ctx, "/v1/engine/"+method, headers, in)
		var pe *protocol.Error
		if err == nil || !errors.As(err, &pe) || pe.Code != protocol.CodeNeedsInput || pe.Data == nil || c.Ask == nil {
			return resp, err
		}
		answer, ok := c.Ask(pe.Data.Hint, pe.Message)
		if !ok {
			return nil, err
		}
		switch pe.Data.Hint {
		case HintConfirm:
			headers["X-Rig-Confirm"] = "1"
		case HintReauth:
			body := map[string]string{"password": answer}
			if totpCode.MatchString(answer) {
				body = map[string]string{"code": answer}
			}
			if err := c.Do(ctx, "/v1/reauth", nil, body, nil); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	return nil, errors.New("the server kept asking")
}

var totpCode = regexp.MustCompile(`^\d{6}$`)

func (c *Client) get(ctx context.Context, method string, in Request, out any) error {
	resp, err := c.call(ctx, method, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func stream[T any](ctx context.Context, c *Client, method string, in Request) (<-chan T, error) {
	resp, err := c.call(ctx, method, in)
	if err != nil {
		return nil, err
	}
	ch := make(chan T, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		go func() { <-ctx.Done(); resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		for sc.Scan() {
			var v T
			if json.Unmarshal(sc.Bytes(), &v) != nil {
				continue
			}
			select {
			case ch <- v:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (c *Client) Catalog(ctx context.Context) (*catalog.Catalog, error) {
	var rs []catalog.Resource
	if err := c.get(ctx, "catalog", Request{}, &rs); err != nil {
		return nil, err
	}
	cat := catalog.New()
	for _, r := range rs {
		cat.Add(r)
	}
	return cat, nil
}

func (c *Client) Query(ctx context.Context, source string, args protocol.Args) (*protocol.Frame, error) {
	var f protocol.Frame
	if err := c.get(ctx, "query", Request{Source: source, Args: args}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (c *Client) Stream(ctx context.Context, source string, args protocol.Args) (<-chan protocol.StreamBatch, error) {
	return stream[protocol.StreamBatch](ctx, c, "stream", Request{Source: source, Args: args})
}

func (c *Client) Run(ctx context.Context, action string, args protocol.Args, o RunOpts) (*jobs.Job, *protocol.RunResult, error) {
	var out struct {
		Job    *Job
		Result *protocol.RunResult
	}
	err := c.get(ctx, "run", Request{Action: action, Args: args, DryRun: o.DryRun, As: o.As}, &out)
	return out.Job.job(), out.Result, err
}

func (c *Client) Jobs(ctx context.Context) []*jobs.Job {
	var ws []*Job
	_ = c.get(ctx, "jobs", Request{}, &ws)
	out := make([]*jobs.Job, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.job())
	}
	return out
}

func (c *Client) Events(ctx context.Context, pattern string) (<-chan protocol.Event, error) {
	return stream[protocol.Event](ctx, c, "events", Request{Pattern: pattern})
}

func (c *Client) Search(ctx context.Context, q string) ([]protocol.Hit, error) {
	var hs []protocol.Hit
	return hs, c.get(ctx, "search", Request{Query: q}, &hs)
}

func (c *Client) Resolve(ctx context.Context, ref string) (*protocol.Resolved, error) {
	var r protocol.Resolved
	if err := c.get(ctx, "resolve", Request{Ref: ref}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) Complete(ctx context.Context, lang, text string, pos int) ([]protocol.Completion, error) {
	var cs []protocol.Completion
	return cs, c.get(ctx, "complete", Request{Lang: lang, Text: text, Pos: pos}, &cs)
}

func (c *Client) Session(ctx context.Context) identity.Subject {
	var s identity.Subject
	_ = c.get(ctx, "session", Request{}, &s)
	return s
}

func (c *Client) Provides(ctx context.Context) protocol.Provides {
	var p protocol.Provides
	_ = c.get(ctx, "provides", Request{}, &p)
	return p
}

func (c *Client) Inbox(ctx context.Context) []inbox.Item {
	var items []inbox.Item
	_ = c.get(ctx, "inbox", Request{}, &items)
	return items
}

func (c *Client) Decide(ctx context.Context, id string, approve bool) (*protocol.RunResult, error) {
	var r *protocol.RunResult
	return r, c.get(ctx, "decide", Request{ID: id, Approve: approve}, &r)
}
