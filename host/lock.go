package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"gopkg.in/yaml.v3"
)

const LockFile = "rig.lock"

// Locked pins one plugin: where it came from and what it was, so every machine gets the same bytes.
type Locked struct {
	Version string `yaml:"version"`
	// Source is registry, a git repo (host/owner/repo[/sub]@ref), a tarball URL or a local path.
	Source string `yaml:"source"`
	Commit string `yaml:"commit,omitempty"`
	// SHA256 is per os/arch, or "any" for a platform-independent artifact.
	SHA256 map[string]string `yaml:"sha256,omitempty"`
	// Files are the shared files' sha256, per os/arch and then by path in the plugin.
	Files map[string]map[string]string `yaml:"files,omitempty"`
	// Permissions is the hash of the permissions granted; a plugin asking for more needs approval again.
	Permissions string `yaml:"permissions"`
	// Pack names the pack that brought it in, if any.
	Pack string `yaml:"pack,omitempty"`
}

type Lock struct {
	Plugins map[string]*Locked `yaml:"plugins"`
}

func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

func ReadLock(dir string) (*Lock, error) {
	l := &Lock{Plugins: map[string]*Locked{}}
	b, err := os.ReadFile(filepath.Join(dir, LockFile))
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, l); err != nil {
		return nil, err
	}
	if l.Plugins == nil {
		l.Plugins = map[string]*Locked{}
	}
	remember(dir)
	return l, nil
}

func (l *Lock) Write(dir string) error {
	b, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	b = append([]byte("# written by rig plugin; commit it, do not edit it\n"), b...)
	remember(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, LockFile), b, 0o644)
}

func (l *Lock) Names() []string {
	var out []string
	for n := range l.Plugins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func projectsFile() string { return filepath.Join(filepath.Dir(StoreDir()), "projects.json") }

// remember notes a project using the store, so gc keeps what any of them locks.
func remember(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	ps := projects()
	if ps[abs] {
		return
	}
	ps[abs] = true
	b, _ := json.Marshal(ps)
	if os.MkdirAll(filepath.Dir(projectsFile()), 0o700) == nil {
		_ = writeAtomic(projectsFile(), b, 0o600)
	}
}

func projects() map[string]bool {
	ps := map[string]bool{}
	if b, err := os.ReadFile(projectsFile()); err == nil {
		_ = json.Unmarshal(b, &ps)
	}
	return ps
}
