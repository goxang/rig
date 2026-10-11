package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/spec"
)

func TestRelevant(t *testing.T) {
	user := filepath.Join(ConfigDir(), "plugins")
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(proj, ".git", "config"), []byte(`[remote "origin"]`+"\n\turl = git@gitlab.example.com:g/p.git\n"), 0o644)
	_ = os.WriteFile(filepath.Join(proj, "main.tf"), nil, 0o644)
	for _, c := range []struct {
		manifest string
		cfg      map[string]*spec.Plugin
		want     bool
	}{
		{"name: arcade\nversion: 1.0.0\n", nil, true},
		{"name: jira\nversion: 1.0.0\nconfig: {url: {type: string}}\n", nil, false},
		{"name: jira\nversion: 1.0.0\nconfig: {url: {type: string}}\n", map[string]*spec.Plugin{"jira": {}}, true},
		{"name: gitlab\nversion: 1.0.0\ndetect: {git_remote: gitlab}\n", nil, true},
		{"name: github\nversion: 1.0.0\ndetect: {git_remote: github}\n", nil, false},
		{"name: terraform\nversion: 1.0.0\ndetect: {files: ['*.tf']}\n", nil, true},
		{"name: helm\nversion: 1.0.0\ndetect: {files: [Chart.yaml]}\n", nil, false},
	} {
		m, err := LoadManifest(plugin(t, user, c.manifest))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Relevant(proj, c.cfg); got != c.want {
			t.Errorf("%s with %v: relevant %v", m.Name, c.cfg, got)
		}
	}
	m, _ := LoadManifest(plugin(t, filepath.Join(proj, ".rig", "plugins"), "name: helm\nversion: 1.0.0\ndetect: {files: [Chart.yaml]}\n"))
	if !m.Relevant(proj, nil) {
		t.Error("a project plugin is always relevant")
	}
}
