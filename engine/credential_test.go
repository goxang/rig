package engine

import (
	"testing"

	"github.com/goxang/rig/spec"
)

func TestCredentialSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a := &App{Spec: &spec.Project{Name: "p", Plugins: map[string]*spec.Plugin{"jira": {Config: map[string]any{"token": "x"}}}}}
	if !a.CredentialSet("jira", []string{"JIRA_TOKEN"}) {
		t.Error("a token in the plugin config is a credential")
	}
	if a.CredentialSet("gitlab", []string{"RIG_TEST_UNSET_TOKEN"}) {
		t.Error("no value anywhere is not a credential")
	}
	t.Setenv("RIG_TEST_UNSET_TOKEN", "v")
	if !a.CredentialSet("gitlab", []string{"RIG_TEST_UNSET_TOKEN"}) {
		t.Error("an environment value is a credential")
	}
	if !a.CredentialSet("gitlab", nil) {
		t.Error("a plugin that asks for nothing is always set")
	}
}
