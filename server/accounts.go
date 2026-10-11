package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

type User struct {
	Hash   string   `json:"hash,omitempty"`
	TOTP   string   `json:"totp,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	Groups []string `json:"groups,omitempty"`
	// OIDC users sign in through the identity provider only.
	OIDC bool `json:"oidc,omitempty"`
}

type Token struct {
	ID      string    `json:"id"`
	User    string    `json:"user"`
	Name    string    `json:"name"`
	Expires time.Time `json:"expires"`
	// Session tokens come from a login and may reauth; API tokens never can.
	Session bool `json:"session,omitempty"`
}

// Store keeps accounts and tokens in one 0600 JSON file; tokens are kept as their SHA-256.
type Store struct {
	path string
	mu   sync.Mutex
	data struct {
		Users  map[string]*User  `json:"users"`
		Tokens map[string]*Token `json:"tokens"`
	}
}

var ErrAuth = errors.New("wrong user, password or code")

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if s.data.Users == nil {
		s.data.Users = map[string]*User{}
	}
	if s.data.Tokens == nil {
		s.data.Tokens = map[string]*Token{}
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) User(name string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.data.Users[name]
	if !ok {
		return User{}, false
	}
	return *u, true
}

func (s *Store) Users() map[string]User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]User{}
	for n, u := range s.data.Users {
		out[n] = *u
	}
	return out
}

// PutUser adds or changes name; a non-empty password is hashed with argon2id.
func (s *Store) PutUser(name string, u User, password string) error {
	if name == "" || strings.ContainsAny(name, ": \t\n") {
		return fmt.Errorf("bad user name %q", name)
	}
	if password != "" {
		u.Hash = hashPassword(password)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Users[name] = &u
	return s.save()
}

func (s *Store) RemoveUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Users[name]; !ok {
		return fmt.Errorf("no user %s", name)
	}
	delete(s.data.Users, name)
	for h, t := range s.data.Tokens {
		if t.User == name {
			delete(s.data.Tokens, h)
		}
	}
	return s.save()
}

// Login checks a password and, when the user has one, the current TOTP code.
func (s *Store) Login(name, password, code string) (User, error) {
	u, ok := s.User(name)
	if !ok || u.Hash == "" {
		hashPassword(password) // the same work either way, so timing does not tell which names exist
		return User{}, ErrAuth
	}
	if !checkPassword(u.Hash, password) || u.TOTP != "" && !checkTOTP(u.TOTP, code, time.Now()) {
		return User{}, ErrAuth
	}
	return u, nil
}

// Check verifies a password or TOTP code again, for reauth.
func (s *Store) Check(name, password, code string) bool {
	u, ok := s.User(name)
	if !ok {
		return false
	}
	if u.TOTP != "" && code != "" {
		return checkTOTP(u.TOTP, code, time.Now())
	}
	return u.Hash != "" && checkPassword(u.Hash, password)
}

// Issue makes a token for user and returns its secret, shown once.
func (s *Store) Issue(user, name string, ttl time.Duration, session bool) (string, Token, error) {
	secret := "rig_" + randomString(24)
	t := Token{ID: randomString(6), User: user, Name: name, Expires: time.Now().Add(ttl), Session: session}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for h, old := range s.data.Tokens {
		if now.After(old.Expires) {
			delete(s.data.Tokens, h)
		}
	}
	s.data.Tokens[tokenHash(secret)] = &t
	return secret, t, s.save()
}

// Token is the live token secret names.
func (s *Store) Token(secret string) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Tokens[tokenHash(secret)]
	if !ok || time.Now().After(t.Expires) {
		return Token{}, false
	}
	if _, ok := s.data.Users[t.User]; !ok {
		return Token{}, false
	}
	return *t, true
}

func (s *Store) Tokens(user string) []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Token
	for _, t := range s.data.Tokens {
		if (user == "" || t.User == user) && !t.Session && time.Now().Before(t.Expires) {
			out = append(out, *t)
		}
	}
	return out
}

// Revoke drops the token with id; user, when set, must own it.
func (s *Store) Revoke(user, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, t := range s.data.Tokens {
		if t.ID == id && (user == "" || t.User == user) {
			delete(s.data.Tokens, h)
			return s.save()
		}
	}
	return fmt.Errorf("no token %s", id)
}

func tokenHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

const argonTime, argonMem, argonThreads = 2, 64 << 10, 2

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMem, argonThreads, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMem, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func checkPassword(hash, pw string) bool {
	var mem, t uint32
	var p uint8
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewTOTP is a fresh base32 secret for an authenticator app.
func NewTOTP() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// TOTPURL is what an authenticator app scans.
func TOTPURL(issuer, user, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s", issuer, user, secret, issuer)
}

// TOTPCode is the RFC 6238 code (SHA-1, 30s, 6 digits) for t.
func TOTPCode(secret string, t time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	n := binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1_000_000)
}

// checkTOTP accepts the code for now and one step either side, for clock drift.
func checkTOTP(secret, code string, now time.Time) bool {
	for _, d := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		if c := TOTPCode(secret, now.Add(d)); c != "" && subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
