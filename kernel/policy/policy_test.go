package policy

import (
	"testing"

	"github.com/goxang/rig/kernel/identity"
)

var envs = map[string]Env{"loadtest2": {Protected: true}, "prod": {Readonly: true}}

func TestLocalDefault(t *testing.T) {
	me := identity.Subject{Kind: identity.KindUser, Name: "me", Roles: []string{"owner"}}
	var c *Config
	for _, tc := range []struct {
		s      identity.Subject
		effect string
		env    string
		want   Decision
	}{
		{me, "read", "prod", Allow},
		{me, "write", "local", Allow},
		{me, "write", "prod", Deny},
		{me, "write", "loadtest2", Confirm},
		{me, "danger", "local", Confirm},
		{identity.AI(me), "write", "local", Approve},
		{identity.AI(me), "read", "local", Allow},
		{identity.Agent("fixer", []string{"x.*"}, me), "write", "local", Approve},
		{identity.Agent("fixer", nil, me), "write", "local", Allow},
		{identity.Agent("fixer", nil, me), "write", "loadtest2", Approve},
	} {
		if d, _ := c.Decide(Request{Subject: tc.s, Action: "x.y", Effect: tc.effect, Env: tc.env}, envs); d != tc.want {
			t.Errorf("%s %s@%s = %s, want %s", tc.s, tc.effect, tc.env, d, tc.want)
		}
	}
}

func TestRoles(t *testing.T) {
	c := &Config{
		Roles: map[string]Role{
			"viewer":   {Allow: []string{"*:read"}},
			"dev":      {Extends: "viewer", Allow: []string{"*:write@env/local"}},
			"operator": {Extends: "dev", Confirm: []string{"*:write@env/loadtest2"}, Reauth: []string{"*:danger"}, Allow: []string{"*:write@env/loadtest2"}},
			"ai":       {Allow: []string{"*:read"}, Approve: []string{"*:write"}},
		},
		Bind: map[string]string{"group:backend": "dev", "user:alice": "operator"},
	}
	alice := identity.Subject{Kind: identity.KindUser, Name: "alice"}
	bob := identity.Subject{Kind: identity.KindUser, Name: "bob", Groups: []string{"backend"}}
	for _, tc := range []struct {
		s      identity.Subject
		effect string
		env    string
		want   Decision
	}{
		{bob, "read", "loadtest2", Allow},
		{bob, "write", "local", Allow},
		{bob, "write", "loadtest2", Deny},
		{alice, "write", "loadtest2", Confirm},
		{alice, "danger", "local", Reauth},
		{alice, "write", "prod", Deny},
		{identity.AI(bob), "write", "local", Approve},
	} {
		if d, why := c.Decide(Request{Subject: tc.s, Action: "runtime.restart", Effect: tc.effect, Env: tc.env}, envs); d != tc.want {
			t.Errorf("%s %s@%s = %s (%s), want %s", tc.s, tc.effect, tc.env, d, why, tc.want)
		}
	}
}

// TestMatrix is every subject against every effect in every kind of environment.
func TestMatrix(t *testing.T) {
	roles := &Config{
		Roles: map[string]Role{
			"viewer":   {Allow: []string{"*:read"}},
			"dev":      {Extends: "viewer", Allow: []string{"*:write@env/local"}},
			"operator": {Extends: "dev", Confirm: []string{"*:write@env/loadtest2"}, Reauth: []string{"*:danger"}, Allow: []string{"*:write@env/loadtest2"}},
			"ai":       {Allow: []string{"*:read"}, Approve: []string{"*:write"}},
		},
		Bind: map[string]string{"user:vic": "viewer", "user:bob": "dev", "user:alice": "operator"},
	}
	me := identity.Subject{Kind: identity.KindUser, Name: "me"}
	user := func(n string) identity.Subject { return identity.Subject{Kind: identity.KindUser, Name: n} }
	A, D, C, R, P := Allow, Deny, Confirm, Reauth, Approve
	effects, envNames := []string{"read", "write", "danger"}, []string{"local", "loadtest2", "prod"}
	for _, tc := range []struct {
		name string
		c    *Config
		s    identity.Subject
		// by effect, then by environment: local, protected loadtest2, read-only prod
		want [3][3]Decision
	}{
		{"viewer", roles, user("vic"), [3][3]Decision{{A, A, A}, {D, D, D}, {D, D, D}}},
		{"dev", roles, user("bob"), [3][3]Decision{{A, A, A}, {A, D, D}, {D, D, D}}},
		{"operator", roles, user("alice"), [3][3]Decision{{A, A, A}, {A, C, D}, {R, R, D}}},
		{"ai role", roles, identity.AI(me), [3][3]Decision{{A, A, A}, {P, P, D}, {D, D, D}}},
		{"no role", roles, user("eve"), [3][3]Decision{{A, A, A}, {A, C, D}, {C, C, D}}},
		{"local user", nil, me, [3][3]Decision{{A, A, A}, {A, C, D}, {C, C, D}}},
		{"local ai", nil, identity.AI(me), [3][3]Decision{{A, A, A}, {P, P, D}, {P, P, D}}},
		{"local agent", nil, identity.Agent("fixer", nil, me), [3][3]Decision{{A, A, A}, {A, P, D}, {P, P, D}}},
	} {
		for i, effect := range effects {
			for j, env := range envNames {
				if d, why := tc.c.Decide(Request{Subject: tc.s, Action: "runtime.restart", Effect: effect, Env: env}, envs); d != tc.want[i][j] {
					t.Errorf("%s %s@%s = %s (%s), want %s", tc.name, effect, env, d, why, tc.want[i][j])
				}
			}
		}
	}
}
