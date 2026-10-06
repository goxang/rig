package ai

import (
	"regexp"
	"sort"
	"strings"
)

// Redactor puts <secret:NAME> in place of secrets in text bound for a model: the values rig knows
// to be secret (Known), then whatever looks like a credential (URL passwords, key=value pairs,
// JWTs, cloud and GitHub tokens, bearer headers, private keys). A nil Redactor redacts nothing.
type Redactor struct {
	known []knownSecret
}

type knownSecret struct{ name, value string }

// minKnown is the shortest known value replaced wherever it appears: shorter ones ("shop", "test")
// are words too, and the patterns still catch them where they stand as credentials.
const minKnown = 6

// NewRedactor redacts the values of known (name → value) wherever they appear.
func NewRedactor(known map[string]string) *Redactor {
	r := &Redactor{}
	seen := map[string]bool{}
	for n, v := range known {
		v = strings.TrimSpace(v)
		if len(v) < minKnown || strings.Contains(v, "${") || seen[v] {
			continue
		}
		seen[v] = true
		r.known = append(r.known, knownSecret{n, v})
	}
	// longest first, so a value inside another is not cut out of it first
	sort.Slice(r.known, func(i, j int) bool {
		if len(r.known[i].value) != len(r.known[j].value) {
			return len(r.known[i].value) > len(r.known[j].value)
		}
		return r.known[i].name < r.known[j].name
	})
	return r
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "<secret:private-key>"},
	// scheme://user:password@host
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):([^\s@/<][^\s@/]*)@`), "$1:<secret:url-password>@"},
	{regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 <secret:bearer>"},
	{regexp.MustCompile(`(?i)\b(authorization:\s*basic)\s+[A-Za-z0-9+/=]{8,}`), "$1 <secret:basic-auth>"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "<secret:jwt>"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "<secret:aws-access-key>"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})\b`), "<secret:github-token>"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "<secret:slack-token>"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "<secret:api-key>"},
	// "password": "x" in JSON, password=x / password: x in logs, DSNs and env dumps
	{regexp.MustCompile(`(?i)("(?:[a-z0-9_-]*?)(?:password|passwd|secret|token|api_?key|access_?key|private_?key)"\s*:\s*")([^"<]+)(")`), "$1<secret:value>$3"},
	// not after "<": a placeholder already put in (<secret:x>) has the word secret in it
	{regexp.MustCompile(`(?i)(^|[^<a-z0-9_.-])([a-z0-9_.-]*(?:password|passwd|secret|token|api_?key|access_?key|private_?key))(\s*[=:]\s*)([^\s;,"'&<]+)`), "$1$2$3<secret:$2>"},
}

// Redact is the one function every text bound for a model goes through.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	for _, k := range r.known {
		s = strings.ReplaceAll(s, k.value, "<secret:"+k.name+">")
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

var secretName = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api_?key|access_?key|private_?key|credential)`)

// SecretName is whether a variable or field named n holds a secret, by its name.
func SecretName(n string) bool { return secretName.MatchString(n) }
