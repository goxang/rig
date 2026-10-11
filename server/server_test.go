package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

type stubRuntime struct{ core.Runtime }

func init() {
	plugin.Register(core.KindRuntime, "stub", "test runtime", func(core.Env, *spec.Component) (any, error) { return stubRuntime{}, nil })
}

const project = `
project: s
default: dev
environments:
  dev: { runtime: { type: stub } }
  shared: { runtime: { type: stub }, protected: true }
tasks:
  hello: { steps: ['echo hello'] }
  wipe: { steps: ['echo wiped'] }
policy:
  roles:
    tester: { extends: viewer, allow: ["task.hello:write", "env.lock:write"], approve: ["task.wipe:*"] }
    boss:   { extends: viewer, allow: ["task.*:write"], reauth: ["task.wipe:*"] }
`

func newServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "rig.yaml")
	if err := os.WriteFile(file, []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{File: file, Store: st}
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, ts, Fingerprint(ts.Certificate().Raw)
}

func login(t *testing.T, ts *httptest.Server, fp, user, pw, code string) *api.Client {
	t.Helper()
	c := &api.Client{URL: ts.URL, Fingerprint: fp}
	var out struct{ Token string }
	if err := c.Do(context.Background(), "/v1/login", nil, map[string]string{"user": user, "password": pw, "code": code}, &out); err != nil {
		t.Fatalf("login %s: %v", user, err)
	}
	c.Token = out.Token
	return c
}

func code(err error) int {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return 0
}

func TestServer(t *testing.T) {
	s, ts, fp := newServer(t)
	ctx := context.Background()
	totp := NewTOTP()
	must(t, s.Store.PutUser("alice", User{Roles: []string{"tester"}}, "pw-alice"))
	must(t, s.Store.PutUser("bob", User{Roles: []string{"boss"}, TOTP: totp}, "pw-bob"))
	must(t, s.Store.PutUser("eve", User{Roles: []string{"nonsense"}}, "pw-eve"))

	if err := (&api.Client{URL: ts.URL, Fingerprint: "00"}).Do(ctx, "/v1/info", nil, nil, nil); err == nil {
		t.Fatal("a wrong pin must fail")
	}
	if err := (&api.Client{URL: ts.URL, Fingerprint: fp}).Do(ctx, "/v1/login", nil, map[string]string{"user": "bob", "password": "pw-bob"}, nil); code(err) != protocol.CodeDenied {
		t.Fatalf("bob without a TOTP code: %v", err)
	}
	alice := login(t, ts, fp, "alice", "pw-alice", "")
	bob := login(t, ts, fp, "bob", "pw-bob", TOTPCode(totp, time.Now()))
	eve := login(t, ts, fp, "eve", "pw-eve", "")

	if err := alice.Do(ctx, "/v1/engine/session", nil, map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if me := alice.Session(ctx); me.String() != "user:alice" || me.Roles[0] != "tester" {
		t.Fatalf("session %+v", me)
	}
	if me := eve.Session(ctx); me.Roles[0] != "viewer" {
		t.Fatalf("an unknown role must fall back to viewer: %+v", me)
	}
	if _, res, err := alice.Run(ctx, "task.hello", nil, api.RunOpts{}); err != nil || res != nil && res.Message != "" {
		t.Fatalf("alice hello: %v", err)
	}
	if _, _, err := eve.Run(ctx, "task.hello", nil, api.RunOpts{}); code(err) != protocol.CodeDenied {
		t.Fatalf("eve may only read: %v", err)
	}

	// approve: alice's wipe waits; she cannot approve it herself, bob can after reauth
	_, _, err := alice.Run(ctx, "task.wipe", nil, api.RunOpts{})
	if code(err) != protocol.CodeNeedsInput {
		t.Fatalf("alice wipe should wait for approval: %v", err)
	}
	items := bob.Inbox(ctx)
	if len(items) != 1 || items[0].Subject.String() != "user:alice" {
		t.Fatalf("inbox %+v", items)
	}
	if _, err := alice.Decide(ctx, items[0].ID, true); code(err) != protocol.CodeDenied {
		t.Fatalf("self-approval: %v", err)
	}
	if _, err := eve.Decide(ctx, items[0].ID, true); code(err) != protocol.CodeDenied {
		t.Fatalf("a viewer approving: %v", err)
	}
	if _, err := bob.Decide(ctx, items[0].ID, true); err != nil {
		t.Fatalf("bob approves: %v", err)
	}

	// reauth: bob's own wipe asks again, and the client answers with a fresh code
	if _, _, err := bob.Run(ctx, "task.wipe", nil, api.RunOpts{}); code(err) != protocol.CodeNeedsInput {
		t.Fatalf("bob wipe without reauth: %v", err)
	}
	asked := ""
	bob.Ask = func(hint, _ string) (string, bool) { asked = hint; return TOTPCode(totp, time.Now()), true }
	if _, _, err := bob.Run(ctx, "task.wipe", nil, api.RunOpts{}); err != nil || asked != api.HintReauth {
		t.Fatalf("bob wipe after reauth: %v (asked %q)", err, asked)
	}

	// a protected environment asks to confirm even an allowed write
	alice.Env = "shared"
	if _, _, err := alice.Run(ctx, "task.hello", nil, api.RunOpts{}); code(err) != protocol.CodeNeedsInput {
		t.Fatalf("protected without confirm: %v", err)
	}
	alice.Ask = func(hint, _ string) (string, bool) { return "", hint == api.HintConfirm }
	if _, _, err := alice.Run(ctx, "task.hello", nil, api.RunOpts{}); err != nil {
		t.Fatalf("protected after confirm: %v", err)
	}

	// leases: alice locks shared, bob cannot change it
	if _, _, err := alice.Run(ctx, "env.lock", protocol.Args{"for": "1h"}, api.RunOpts{}); err != nil {
		t.Fatal(err)
	}
	bob.Env = "shared"
	bob.Ask = func(string, string) (string, bool) { return TOTPCode(totp, time.Now()), true }
	if _, _, err := bob.Run(ctx, "task.hello", nil, api.RunOpts{}); err == nil || !strings.Contains(err.Error(), "locked by user:alice") {
		t.Fatalf("lease: %v", err)
	}

	// API tokens: made from a session, cannot make more, cannot reauth or approve
	var tok struct{ Token, ID string }
	if err := alice.Do(ctx, "/v1/tokens", nil, map[string]string{"name": "ci", "ttl": "1h"}, &tok); err != nil {
		t.Fatal(err)
	}
	ci := &api.Client{URL: ts.URL, Fingerprint: fp, Token: tok.Token}
	if me := ci.Session(ctx); me.String() != "token:alice/ci" {
		t.Fatalf("token subject %v", me)
	}
	if err := ci.Do(ctx, "/v1/tokens", nil, map[string]string{"name": "x"}, nil); code(err) != protocol.CodeDenied {
		t.Fatalf("token minting a token: %v", err)
	}
	if err := alice.Do(ctx, "DELETE /v1/tokens/"+tok.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := ci.Do(ctx, "/v1/engine/session", nil, map[string]string{}, nil); code(err) != protocol.CodeDenied {
		t.Fatalf("revoked token still works: %v", err)
	}

	// audit: the server's log names who did what, and who approved
	bob.Env = ""
	f, err := bob.Query(ctx, "audit", protocol.Args{"action": "task.wipe"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if !strings.Contains(string(b), "user:bob") {
		t.Fatalf("audit %s", b)
	}
}

func TestOIDC(t *testing.T) {
	s, ts, fp := newServer(t)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var idp *httptest.Server
	var nonce string
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": idp.URL, "authorization_endpoint": idp.URL + "/auth",
				"token_endpoint": idp.URL + "/token", "jwks_uri": idp.URL + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/auth":
			q := r.URL.Query()
			nonce = q.Get("nonce")
			http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "c1" || r.Form.Get("code_verifier") == "" {
				http.Error(w, "bad", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": jwt(key, map[string]any{"iss": idp.URL, "aud": "rig",
				"exp": time.Now().Add(time.Minute).Unix(), "nonce": nonce, "preferred_username": "carol", "groups": []string{"/backend"}})})
		}
	}))
	defer idp.Close()
	s.OIDC = &OIDC{Issuer: idp.URL, ClientID: "rig", ClientSecret: "x", RedirectURL: ts.URL + "/v1/oidc/callback", Roles: []string{"dev"}}

	c := &api.Client{URL: ts.URL, Fingerprint: fp}
	state := "state-0123456789abcdef"
	jar, _ := cookiejar.New(nil)
	browser := ts.Client()
	browser.Jar = jar
	resp, err := browser.Get(ts.URL + "/v1/oidc/start?state=" + state)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("browser flow: %v %v", err, resp.Status)
	}
	var out struct {
		Done  bool
		Token string
	}
	if err := c.Do(context.Background(), "/v1/oidc/poll", nil, map[string]string{"state": state}, &out); err != nil || out.Token == "" {
		t.Fatalf("poll: %v %+v", err, out)
	}
	c.Token = out.Token
	if me := c.Session(context.Background()); me.String() != "user:carol" || me.Roles[0] != "dev" || me.Groups[0] != "backend" {
		t.Fatalf("oidc session %+v", me)
	}
}

func jwt(key *rsa.PrivateKey, claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	in := enc(map[string]string{"alg": "RS256", "kid": "k1"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
