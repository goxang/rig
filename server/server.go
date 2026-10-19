// Package server is `rig serve`: the Engine API over HTTPS for remote CLIs, MCP clients and the web
// inbox, with accounts, server-side policy, reauth, approvals, leases and one audit log per
// environment. Requests carry a bearer token; streams are newline-delimited JSON.
package server

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/kernel/policy"
	"github.com/goxang/rig/protocol"
)

type Server struct {
	// File is the project's rig.yaml.
	File  string
	Store *Store
	OIDC  *OIDC
	// Open opens the project on an environment; engine.Open when nil.
	Open func(file, env string) (*engine.App, error)
	// ReauthWindow is how long a reauth lasts (default 5m).
	ReauthWindow time.Duration
	// RatePerSecond caps each caller's requests (default 50).
	RatePerSecond int

	mu       sync.Mutex
	engines  map[string]*engine.Local
	reauthed map[string]time.Time
	hits     map[string]*window
}

type window struct {
	start time.Time
	n     int
}

// Close stops every environment's engine.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.engines {
		l.Close()
		l.App.Close()
	}
	s.engines = nil
}

// Engine is the engine for env ("" is the project's default), opened on first use.
func (s *Server) Engine(env string) (*engine.Local, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.engines[env]; ok {
		return l, nil
	}
	open := s.Open
	if open == nil {
		open = engine.Open
	}
	a, err := open(s.File, env)
	if err != nil {
		return nil, err
	}
	if a.Env == nil {
		a.Close()
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "no environment " + env}
	}
	// policy settles confirmation for each request, so the app's own protected check stands aside
	a.Confirmed, a.Ask = true, nil
	a.Spec.Policy = WithDefaults(a.Spec.Policy)
	l := engine.NewLocal(a, &jobs.Manager{})
	if s.engines == nil {
		s.engines = map[string]*engine.Local{}
	}
	s.engines[env] = l
	return l, nil
}

// WithDefaults adds the built-in roles a project does not define itself.
func WithDefaults(c *policy.Config) *policy.Config {
	out := &policy.Config{Roles: map[string]policy.Role{}, Bind: map[string]string{}}
	if c != nil {
		for k, v := range c.Roles {
			out.Roles[k] = v
		}
		for k, v := range c.Bind {
			out.Bind[k] = v
		}
	}
	for name, r := range map[string]policy.Role{
		"viewer":   {Allow: []string{"*:read"}},
		"dev":      {Extends: "viewer", Allow: []string{"*:write"}, Confirm: []string{"*:danger"}},
		"operator": {Extends: "dev", Reauth: []string{"*:danger"}},
		"admin":    {Allow: []string{"*"}, Reauth: []string{"*:danger"}},
		// the AI's writes wait in the inbox whoever it acts for
		"ai": {Allow: []string{"*:read"}, Approve: []string{"*:write", "*:danger"}},
	} {
		if _, ok := out.Roles[name]; !ok {
			out.Roles[name] = r
		}
	}
	return out
}

type caller struct {
	user  string
	token Token
	subj  identity.Subject
}

func (s *Server) subject(name string, u User, t Token, roles map[string]policy.Role) identity.Subject {
	sub := identity.Subject{Kind: identity.KindUser, Name: name, Groups: u.Groups}
	if !t.Session {
		sub.Kind, sub.Name = identity.KindToken, name+"/"+t.Name
	}
	for _, r := range u.Roles {
		if _, ok := roles[r]; ok {
			sub.Roles = append(sub.Roles, r)
		}
	}
	if len(sub.Roles) == 0 {
		sub.Roles = []string{"viewer"} // an account no known role binds may only look
	}
	return sub
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", s.info)
	mux.HandleFunc("POST /v1/login", s.login)
	mux.HandleFunc("GET /v1/oidc/start", s.oidcStart)
	mux.HandleFunc("GET /v1/oidc/callback", s.oidcCallback)
	mux.HandleFunc("POST /v1/oidc/poll", s.oidcPoll)
	mux.HandleFunc("POST /v1/logout", s.auth(s.logout))
	mux.HandleFunc("POST /v1/reauth", s.auth(s.reauth))
	mux.HandleFunc("GET /v1/tokens", s.auth(s.tokens))
	mux.HandleFunc("POST /v1/tokens", s.auth(s.newToken))
	mux.HandleFunc("DELETE /v1/tokens/{id}", s.auth(s.revoke))
	mux.HandleFunc("POST /v1/engine/{method}", s.auth(s.engine))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
		_, _ = w.Write(inboxPage)
	})
	return mux
}

//go:embed inbox.html
var inboxPage []byte

type ctxCaller struct{}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		t, found := s.Store.Token(secret)
		if !ok || !found {
			writeErr(w, http.StatusUnauthorized, &protocol.Error{Code: protocol.CodeDenied, Message: "sign in first: rig login"})
			return
		}
		if !s.allow("u:"+t.User, s.rate()) {
			writeErr(w, http.StatusTooManyRequests, &protocol.Error{Code: protocol.CodeUnavailable, Message: "too many requests"})
			return
		}
		c := &caller{user: t.User, token: t}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxCaller{}, c)))
	}
}

func (s *Server) rate() int {
	if s.RatePerSecond > 0 {
		return s.RatePerSecond
	}
	return 50
}

// allow counts a request for key in a one-second window (logins: per minute, keyed by address).
func (s *Server) allow(key string, limit int) bool {
	per := time.Second
	if strings.HasPrefix(key, "login:") {
		per = time.Minute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hits == nil {
		s.hits = map[string]*window{}
	}
	now := time.Now()
	w := s.hits[key]
	if w == nil || now.Sub(w.start) >= per {
		if len(s.hits) > 10000 {
			clear(s.hits)
		}
		w = &window{start: now}
		s.hits[key] = w
	}
	w.n++
	return w.n <= limit
}

func callerOf(r *http.Request) *caller { return r.Context().Value(ctxCaller{}).(*caller) }

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	l, err := s.Engine("")
	if err != nil {
		writeErr(w, 0, err)
		return
	}
	writeJSON(w, map[string]any{"project": l.App.Spec.Name, "env": l.App.Env.Name, "envs": engine.SortedKeys(l.App.Spec.Environments), "oidc": s.OIDC != nil})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allow("login:"+clientIP(r), 10) {
		writeErr(w, http.StatusTooManyRequests, &protocol.Error{Code: protocol.CodeUnavailable, Message: "too many sign-in attempts; wait a minute"})
		return
	}
	var in struct{ User, Password, Code string }
	if !readJSON(w, r, &in) {
		return
	}
	if _, err := s.Store.Login(in.User, in.Password, in.Code); err != nil {
		writeErr(w, http.StatusUnauthorized, &protocol.Error{Code: protocol.CodeDenied, Message: err.Error()})
		return
	}
	s.issueSession(w, in.User)
}

func (s *Server) issueSession(w http.ResponseWriter, user string) {
	secret, t, err := s.Store.Issue(user, "session", 12*time.Hour, true)
	if err != nil {
		writeErr(w, 0, err)
		return
	}
	writeJSON(w, map[string]any{"token": secret, "user": user, "expires": t.Expires})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if err := s.Store.Revoke(c.user, c.token.ID); err != nil {
		writeErr(w, 0, err)
		return
	}
	writeJSON(w, map[string]any{})
}

func (s *Server) reauth(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if !s.allow("login:"+clientIP(r), 10) {
		writeErr(w, http.StatusTooManyRequests, &protocol.Error{Code: protocol.CodeUnavailable, Message: "too many attempts; wait a minute"})
		return
	}
	var in struct{ Password, Code string }
	if !readJSON(w, r, &in) {
		return
	}
	if !c.token.Session || !s.Store.Check(c.user, in.Password, in.Code) {
		writeErr(w, http.StatusUnauthorized, &protocol.Error{Code: protocol.CodeDenied, Message: ErrAuth.Error()})
		return
	}
	s.mu.Lock()
	if s.reauthed == nil {
		s.reauthed = map[string]time.Time{}
	}
	s.reauthed[c.token.ID] = time.Now()
	s.mu.Unlock()
	writeJSON(w, map[string]any{"until": time.Now().Add(s.window())})
}

func (s *Server) window() time.Duration {
	if s.ReauthWindow > 0 {
		return s.ReauthWindow
	}
	return 5 * time.Minute
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Store.Tokens(callerOf(r).user))
}

func (s *Server) newToken(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if !c.token.Session {
		writeErr(w, http.StatusForbidden, &protocol.Error{Code: protocol.CodeDenied, Message: "a token cannot make tokens; sign in"})
		return
	}
	var in struct {
		Name string
		TTL  string
	}
	if !readJSON(w, r, &in) {
		return
	}
	ttl := 30 * 24 * time.Hour
	if in.TTL != "" {
		d, err := time.ParseDuration(in.TTL)
		if err != nil || d <= 0 || d > 366*24*time.Hour {
			writeErr(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "ttl: a duration up to 8784h"})
			return
		}
		ttl = d
	}
	secret, t, err := s.Store.Issue(c.user, cmp.Or(in.Name, "api"), ttl, false)
	if err != nil {
		writeErr(w, 0, err)
		return
	}
	writeJSON(w, map[string]any{"token": secret, "id": t.ID, "expires": t.Expires})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Revoke(callerOf(r).user, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, &protocol.Error{Code: protocol.CodeNotFound, Message: err.Error()})
		return
	}
	writeJSON(w, map[string]any{})
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeErr(w, http.StatusNotFound, &protocol.Error{Code: protocol.CodeNotFound, Message: "this server has no OIDC sign-in"})
		return
	}
	u, err := s.OIDC.start(r.Context(), r.URL.Query().Get("state"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if e := q.Get("error"); e != "" {
		s.OIDC.finish(state, "", "sign-in refused: "+e)
		http.Error(w, "sign-in refused: "+e, http.StatusUnauthorized)
		return
	}
	f, id, err := s.OIDC.callback(r.Context(), state, q.Get("code"))
	if err == nil {
		u, ok := s.Store.User(id.User)
		if ok && !u.OIDC {
			err = fmt.Errorf("%s is a local account; sign in with its password", id.User)
		} else {
			if !ok {
				u = User{OIDC: true, Roles: s.OIDC.Roles}
			}
			u.Groups = id.Groups
			err = s.Store.PutUser(id.User, u, "")
		}
	}
	if err != nil {
		if f != nil {
			s.OIDC.finish(state, "", err.Error())
		}
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	secret, _, err := s.Store.Issue(id.User, "session", 12*time.Hour, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.OIDC.finish(state, secret, "")
	_, _ = fmt.Fprintf(w, "Signed in to rig as %s. You can close this tab and go back to the terminal.\n", id.User)
}

func (s *Server) oidcPoll(w http.ResponseWriter, r *http.Request) {
	var in struct{ State string }
	if s.OIDC == nil || !readJSON(w, r, &in) {
		return
	}
	tok, done, err := s.OIDC.poll(in.State)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, &protocol.Error{Code: protocol.CodeDenied, Message: err.Error()})
		return
	}
	writeJSON(w, map[string]any{"done": done, "token": tok})
}

func (s *Server) engine(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	l, err := s.Engine(r.Header.Get("X-Rig-Env"))
	if err != nil {
		writeErr(w, 0, err)
		return
	}
	c.subj = s.subject(c.user, mustUser(s.Store, c.user), c.token, l.App.Spec.Policy.Roles)
	Call(w, r, l, c.subj, func() bool { return s.reauthedWithin(c.token) })
}

// Call answers the Engine call r names (POST .../{method}) on e for subj. reauthed settles a
// policy reauth; nil asks it as a confirm, for a local client.
func Call(w http.ResponseWriter, r *http.Request, e api.Engine, subj identity.Subject, reauthed func() bool) {
	var in api.Request
	if !readJSON(w, r, &in) {
		return
	}
	ctx := identity.With(r.Context(), subj)
	confirmed := r.Header.Get("X-Rig-Confirm") == "1"
	if confirmed {
		ctx = core.WithConfirmed(ctx)
	}
	ctx = actions.WithAsk(ctx, func(_ context.Context, d policy.Decision, why string) error {
		switch {
		case (d == policy.Confirm || reauthed == nil) && confirmed:
			return nil
		case d == policy.Confirm || reauthed == nil:
			return &protocol.Error{Code: protocol.CodeNeedsInput, Message: "confirm: " + why, Data: &protocol.ErrorData{Hint: api.HintConfirm}}
		case reauthed():
			return nil
		}
		return &protocol.Error{Code: protocol.CodeNeedsInput, Message: "reauth: " + why, Data: &protocol.ErrorData{Hint: api.HintReauth}}
	})
	switch r.PathValue("method") {
	case "catalog":
		cat, err := e.Catalog(ctx)
		if err != nil {
			writeErr(w, 0, err)
			return
		}
		writeJSON(w, cat.List(""))
	case "query":
		f, err := e.Query(ctx, in.Source, in.Args)
		reply(w, f, err)
	case "stream":
		ch, err := e.Stream(ctx, in.Source, in.Args)
		if err != nil {
			writeErr(w, 0, err)
			return
		}
		streamOut(w, r, ch)
	case "events":
		ch, err := e.Events(ctx, in.Pattern)
		if err != nil {
			writeErr(w, 0, err)
			return
		}
		streamOut(w, r, ch)
	case "run":
		o := api.RunOpts{DryRun: in.DryRun}
		if in.As != nil {
			// a client may only act as its own AI or agents, which carry its roles
			switch in.As.Kind {
			case identity.KindAI:
				as := identity.AI(subj)
				o.As = &as
			case identity.KindAgent:
				as := identity.Agent(in.As.Name, in.As.Approve, subj)
				o.As = &as
			default:
				writeErr(w, http.StatusForbidden, &protocol.Error{Code: protocol.CodeDenied, Message: "cannot run as " + in.As.String()})
				return
			}
		}
		j, res, err := e.Run(ctx, in.Action, in.Args, o)
		if err != nil {
			writeErr(w, 0, err)
			return
		}
		writeJSON(w, map[string]any{"job": api.WireJob(j), "result": res})
	case "jobs":
		var out []*api.Job
		for _, j := range e.Jobs(ctx) {
			out = append(out, api.WireJob(j))
		}
		writeJSON(w, out)
	case "search":
		hits, err := e.Search(ctx, in.Query)
		reply(w, hits, err)
	case "resolve":
		res, err := e.Resolve(ctx, in.Ref)
		reply(w, res, err)
	case "complete":
		cs, err := e.Complete(ctx, in.Lang, in.Text, in.Pos)
		reply(w, cs, err)
	case "session":
		writeJSON(w, e.Session(ctx))
	case "provides":
		writeJSON(w, e.Provides(ctx))
	case "inbox":
		writeJSON(w, e.Inbox(ctx))
	case "decide":
		res, err := e.Decide(ctx, in.ID, in.Approve)
		reply(w, res, err)
	default:
		writeErr(w, http.StatusNotFound, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "no method " + r.PathValue("method")})
	}
}

func mustUser(st *Store, name string) User {
	u, _ := st.User(name)
	return u
}

func (s *Server) reauthedWithin(t Token) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.reauthed[t.ID]
	return ok && t.Session && time.Since(at) < s.window()
}

func streamOut[T any](w http.ResponseWriter, r *http.Request, ch <-chan T) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case v, ok := <-ch:
			if !ok {
				return
			}
			if enc.Encode(v) != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, 0, err)
		return
	}
	writeJSON(w, v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil && !errors.Is(err, http.ErrBodyNotAllowed) && err.Error() != "EOF" {
		writeErr(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeInvalidRequest, Message: err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr sends err as a protocol error, so a client gets back the code its exit status and
// prompts depend on.
func writeErr(w http.ResponseWriter, status int, err error) {
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		pe = &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
		switch {
		case errors.Is(err, engine.ErrProtected), errors.Is(err, core.ErrReadOnly), errors.Is(err, actions.ErrDeclined):
			pe.Code = protocol.CodeDenied
		case errors.Is(err, engine.ErrFailed):
			pe.Code = CodeFailed
		}
	}
	if status == 0 {
		status = map[int]int{protocol.CodeDenied: 403, protocol.CodeNotFound: 404, protocol.CodeNeedsInput: 409,
			protocol.CodeInvalidParams: 400, protocol.CodeMethodNotFound: 404, protocol.CodeConflict: 409, CodeFailed: 422}[pe.Code]
		if status == 0 {
			status = 500
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": pe})
}

// CodeFailed carries engine.ErrFailed (a failed check) across the wire.
const CodeFailed = -32010

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
