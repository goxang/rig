package ai

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goxang/scrub"
	"github.com/goxang/scrub/packs"
)

// Redactor puts <secret:NAME> in place of secrets in text bound for a model: the values rig knows
// to be secret (Known), then whatever goxang/scrub's packs take for a credential (URL passwords,
// key=value pairs, JWTs, cloud tokens, bearer headers, private keys, card numbers, PINs).
// A nil Redactor redacts nothing.
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

// patterns find what looks like a credential: goxang/scrub's payment, secret and cloud packs, plus
// key=value pairs whose key only ends in a secret's name (DB_PASSWORD=x) and basic auth.
var patterns = func() *scrub.Scrubber {
	rules := append(packs.All(),
		scrub.Rule{
			ID:      "secret.suffixed",
			Pattern: `(?i)[a-z0-9][_.-](?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)["']?\s{0,8}[:=]\s{0,8}["']?(?P<secret>[^\s"',;&<]{1,256})`,
			Anchors: []string{"password", "passwd", "pwd", "secret", "token", "key"},
			Group:   "secret",
		},
		scrub.Rule{
			ID:      "secret.basic_auth",
			Pattern: `(?i)\bbasic\s+(?P<secret>[A-Za-z0-9+/=]{8,})`,
			Anchors: []string{"basic"},
			Group:   "secret",
		})
	for i := range rules {
		name := placeholder[rules[i].ID]
		if name == "" {
			_, name, _ = strings.Cut(rules[i].ID, ".")
			name = strings.ReplaceAll(name, "_", "-")
		}
		rules[i].Mask = func(match string) string {
			if strings.ContainsRune(match, sentinel) || strings.EqualFold(match, "bearer") || strings.EqualFold(match, "basic") {
				return match // named already, or the scheme of an Authorization header whose token has its own rule
			}
			return "<secret:" + name + ">"
		}
	}
	return scrub.New().Add(rules...).MustBuild()
}()

var placeholder = map[string]string{"secret.keyed": "value", "secret.suffixed": "value", "cloud.aws_access_key_id": "aws-access-key", "cloud.openai_key": "api-key"}

// sentinel marks the spans the patterns leave alone: known values and placeholders already there.
const sentinel = '\uE000'

var (
	placeholderRe = regexp.MustCompile(`<secret:[A-Za-z0-9_.-]+>`)
	heldRe        = regexp.MustCompile("\uE000[0-9]+\uE000")
)

// Redact is the one function every text bound for a model goes through.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	var held []string
	hold := func(text string) string {
		held = append(held, text)
		return string(sentinel) + strconv.Itoa(len(held)-1) + string(sentinel)
	}
	s = placeholderRe.ReplaceAllStringFunc(s, hold)
	for _, k := range r.known {
		if strings.Contains(s, k.value) {
			s = strings.ReplaceAll(s, k.value, hold("<secret:"+k.name+">"))
		}
	}
	s = patterns.Redact(s)
	if len(held) == 0 {
		return s
	}
	return heldRe.ReplaceAllStringFunc(s, func(m string) string {
		i, _ := strconv.Atoi(strings.Trim(m, string(sentinel)))
		return held[i]
	})
}

var secretName = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api_?key|access_?key|private_?key|credential)`)

// SecretName is whether a variable or field named n holds a secret, by its name.
func SecretName(n string) bool { return secretName.MatchString(n) }
