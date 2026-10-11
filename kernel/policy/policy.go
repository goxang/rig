// Package policy decides whether a subject may run an action: allow, deny, confirm (the same
// person again), reauth (password or TOTP again) or approve (another person).
package policy

import (
	"path"
	"slices"
	"strings"

	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/protocol"
)

type Decision string

const (
	Allow   Decision = "allow"
	Deny    Decision = "deny"
	Confirm Decision = "confirm"
	Reauth  Decision = "reauth"
	Approve Decision = "approve"
)

type Request struct {
	Subject identity.Subject
	// Action is the verb, such as runtime.restart.
	Action string
	// Effect is the action's protocol effect: read, write or danger.
	Effect string
	Env    string
}

// Env is what policy needs from an environment.
type Env struct {
	Protected bool
	Readonly  bool
}

// Role lists rules as action:effect[@env/name] globs, such as *:write@env/local.
type Role struct {
	Extends string   `yaml:"extends,omitempty" json:"extends,omitempty"`
	Allow   []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	Deny    []string `yaml:"deny,omitempty" json:"deny,omitempty"`
	Confirm []string `yaml:"confirm,omitempty" json:"confirm,omitempty"`
	Reauth  []string `yaml:"reauth,omitempty" json:"reauth,omitempty"`
	Approve []string `yaml:"approve,omitempty" json:"approve,omitempty"`
}

// Config is rig.yaml's `policy:`. Bind maps user:<name>, group:<name> or ai to a role.
type Config struct {
	Roles map[string]Role   `yaml:"roles,omitempty" json:"roles,omitempty"`
	Bind  map[string]string `yaml:"bind,omitempty" json:"bind,omitempty"`
}

// Decide answers req. For a subject no role binds, the local default applies: reads are allowed; a readonly
// environment takes no writes; the AI's writes need approval; an agent's writes are allowed
// unless dangerous, protected or matching its approve globs; danger and writes to a protected
// environment need confirming. With roles, the most restrictive matching rule wins, an allowed write to a
// protected environment still needs confirming, and nothing matching is denied.
func (c *Config) Decide(req Request, envs map[string]Env) (Decision, string) {
	env := envs[req.Env]
	if env.Readonly && req.Effect != protocol.EffectRead {
		return Deny, "environment " + req.Env + " is read-only"
	}
	var roles []Role
	for _, name := range c.roleNames(req.Subject) {
		for seen := []string{}; name != "" && !slices.Contains(seen, name); {
			seen = append(seen, name)
			r, ok := c.Roles[name]
			if !ok {
				break
			}
			roles = append(roles, r)
			name = r.Extends
		}
	}
	if len(roles) == 0 {
		switch {
		case req.Effect == protocol.EffectRead:
			return Allow, ""
		case req.Subject.Kind == identity.KindAI:
			return Approve, "the AI's writes need approval"
		case req.Subject.Kind == identity.KindAgent && (req.Effect == protocol.EffectDanger || env.Protected ||
			slices.ContainsFunc(req.Subject.Approve, func(g string) bool { return glob(g, req.Action) })):
			return Approve, "agent " + req.Subject.Name + " needs approval for " + req.Action
		case req.Effect == protocol.EffectDanger:
			return Confirm, req.Action + " is dangerous"
		case env.Protected:
			return Confirm, "environment " + req.Env + " is protected"
		}
		return Allow, ""
	}
	for _, d := range []Decision{Deny, Approve, Reauth, Confirm, Allow} {
		for _, r := range roles {
			for _, rule := range r.rules(d) {
				if matches(rule, req) {
					if d == Allow && env.Protected && req.Effect != protocol.EffectRead {
						return Confirm, "environment " + req.Env + " is protected"
					}
					return d, rule
				}
			}
		}
	}
	return Deny, "no rule allows " + req.Action
}

func (c *Config) roleNames(s identity.Subject) []string {
	if c == nil {
		return nil
	}
	var out []string
	if s.Kind == identity.KindAI {
		if r, ok := c.Bind["ai"]; ok {
			return []string{r}
		}
		if _, ok := c.Roles["ai"]; ok {
			return []string{"ai"}
		}
	}
	if r, ok := c.Bind[s.String()]; ok {
		out = append(out, r)
	}
	for _, g := range s.Groups {
		if r, ok := c.Bind["group:"+g]; ok {
			out = append(out, r)
		}
	}
	return append(out, s.Roles...)
}

func (r Role) rules(d Decision) []string {
	switch d {
	case Allow:
		return r.Allow
	case Deny:
		return r.Deny
	case Confirm:
		return r.Confirm
	case Reauth:
		return r.Reauth
	}
	return r.Approve
}

func matches(rule string, req Request) bool {
	rule, obj, scoped := strings.Cut(rule, "@")
	verb, effect, ok := strings.Cut(rule, ":")
	if !ok {
		effect = "*"
	}
	if scoped && !glob(obj, "env/"+req.Env) {
		return false
	}
	return glob(verb, req.Action) && glob(effect, req.Effect)
}

func glob(pattern, s string) bool {
	ok, _ := path.Match(pattern, s)
	return ok
}
