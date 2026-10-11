// Package host finds, starts and supervises plugins, and connects them to the engine.
package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/kernel/config"
)

const ManifestFile = "rig-plugin.yaml"

// Command is a program to run, as a list or a shell line.
type Command []string

func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*c = Command{"sh", "-c", n.Value}
		return nil
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*c = l
	return nil
}

// Server is how a plugin runs: a JSON-RPC process, or an MCP server under mcp:.
type Server struct {
	Command Command           `yaml:"command"`
	Env     map[string]string `yaml:"env,omitempty"`
}

type Permissions struct {
	// Net lists hosts or URLs a plugin connects to; on Landlock ABI 4+ their ports are enforced.
	Net []string `yaml:"net,omitempty" json:"net,omitempty"`
	// Secrets are the names host/secret answers.
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	// Exec lists programs the plugin runs (advisory; PATH stays).
	Exec []string `yaml:"exec,omitempty" json:"exec,omitempty"`
	// Uses are globs of other plugins' sources and actions it may call through the host.
	Uses []string `yaml:"uses,omitempty" json:"uses,omitempty"`
	// FS lists paths beyond the project, its own directory and temp it reads (ro:) or writes.
	FS []string `yaml:"fs,omitempty" json:"fs,omitempty"`
	// Env lists environment variables it gets besides PATH, HOME and LANG.
	Env []string `yaml:"env,omitempty" json:"env,omitempty"`
	// AI lets it call host/ai.
	AI bool `yaml:"ai,omitempty" json:"ai,omitempty"`
}

// Hash identifies a set of permissions, so an update that widens them asks again.
func (p Permissions) Hash() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

type SourceDecl struct {
	Help   string         `yaml:"help,omitempty"`
	Args   map[string]any `yaml:"args,omitempty"`
	Cache  string         `yaml:"cache,omitempty"`
	Stream bool           `yaml:"stream,omitempty"`
	// Run makes it a script source; Format overrides output detection.
	Run    Command `yaml:"run,omitempty"`
	Format string  `yaml:"format,omitempty"`
}

type ActionDecl struct {
	Help   string         `yaml:"help,omitempty"`
	Effect string         `yaml:"effect,omitempty"`
	Args   map[string]any `yaml:"args,omitempty"`
	Keys   []string       `yaml:"keys,omitempty"`
	On     []string       `yaml:"on,omitempty"`
	Plan   bool           `yaml:"plan,omitempty"`
	Run    Command        `yaml:"run,omitempty"`
}

type ImporterDecl struct {
	Help  string   `yaml:"help,omitempty"`
	Files []string `yaml:"files,omitempty"`
	Run   Command  `yaml:"run,omitempty"`
}

type Detect struct {
	GitRemote string   `yaml:"git_remote,omitempty"`
	Files     []string `yaml:"files,omitempty"`
}

type Manifest struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Rig         string            `yaml:"rig,omitempty"`
	Description string            `yaml:"description,omitempty"`
	Publisher   string            `yaml:"publisher,omitempty"`
	Requires    map[string]string `yaml:"requires,omitempty"`
	// Needs are programs on the machine the plugin's scripts run, each with how to install it
	// (needs: {jq: "apt install jq"}); permissions.exec ones are checked too.
	Needs       map[string]string `yaml:"needs,omitempty"`
	Implements  []string          `yaml:"implements,omitempty"`
	Detect      *Detect           `yaml:"detect,omitempty"`
	Server      *Server           `yaml:"server,omitempty"`
	MCP         *Server           `yaml:"mcp,omitempty"`
	Permissions Permissions       `yaml:"permissions,omitempty"`
	// Config is the JSON Schema of plugins.<name>: in rig.yaml, as properties.
	Config    map[string]map[string]any `yaml:"config,omitempty"`
	Types     map[string]yaml.Node      `yaml:"types,omitempty"`
	Schemes   []string                  `yaml:"schemes,omitempty"`
	Sources   map[string]SourceDecl     `yaml:"sources,omitempty"`
	Actions   map[string]ActionDecl     `yaml:"actions,omitempty"`
	Importers map[string]ImporterDecl   `yaml:"importers,omitempty"`
	Views     map[string]yaml.Node      `yaml:"views,omitempty"`
	Widgets   map[string]yaml.Node      `yaml:"widgets,omitempty"`
	On        map[string]yaml.Node      `yaml:"on,omitempty"`
	Events    []string                  `yaml:"events,omitempty"`
	Skills    []string                  `yaml:"skills,omitempty"`
	Search    bool                      `yaml:"search,omitempty"`
	Complete  []string                  `yaml:"complete,omitempty"`
	Doctor    bool                      `yaml:"doctor,omitempty"`
	Docs      string                    `yaml:"docs,omitempty"`
	// Build runs in the plugin's directory after a git or path install, such as go build -o bin/x .
	Build Command `yaml:"build,omitempty"`

	// Dir is where the manifest lives; Fragment the rig.yaml keys it contributes.
	Dir      string      `yaml:"-"`
	Fragment *config.Doc `yaml:"-"`
	Warnings []string    `yaml:"-"`
}

// fragmentKeys are the rig.yaml keys a manifest may carry as defaults for the project.
var fragmentKeys = map[string]bool{"tasks": true, "queries": true, "dashboards": true, "alerts": true, "reports": true,
	"tests": true, "agents": true, "templates": true, "defaults": true, "policy": true, "ai": true, "ui": true,
	"sections": true, "keys": true, "themes": true}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type parsedManifest struct {
	mod  time.Time
	size int64
	m    *Manifest
}

// manifests caches parsed manifests by file; a command discovers plugins several times.
var manifests sync.Map

// LoadManifest reads dir/rig-plugin.yaml. Unknown keys are warnings, so an older rig loads a newer plugin.
// Callers share the result and must not change it.
func LoadManifest(dir string) (*Manifest, error) {
	file := filepath.Join(dir, ManifestFile)
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if c, ok := manifests.Load(file); ok {
		if c := c.(parsedManifest); c.mod.Equal(st.ModTime()) && c.size == st.Size() {
			return c.m, nil
		}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(raw, file)
	if err == nil {
		manifests.Store(file, parsedManifest{st.ModTime(), st.Size(), m})
	}
	return m, err
}

func ParseManifest(raw []byte, file string) (*Manifest, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a mapping", file)
	}
	m := &Manifest{Dir: filepath.Dir(file)}
	if err := root.Content[0].Decode(m); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if !nameRE.MatchString(m.Name) {
		return nil, fmt.Errorf("%s: name %q: want lower-case letters, digits and dashes", file, m.Name)
	}
	known := knownKeys()
	frag := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		k := top.Content[i].Value
		switch {
		case fragmentKeys[k]:
			frag.Content = append(frag.Content, top.Content[i], top.Content[i+1])
		case !known[k]:
			m.Warnings = append(m.Warnings, fmt.Sprintf("%s:%d: unknown key %q (a newer rig may know it)", file, top.Content[i].Line, k))
		}
	}
	m.Fragment = config.FromNode(frag, file, "plugin "+m.Name)
	for n, a := range m.Actions {
		switch a.Effect {
		case "":
			a.Effect = "write"
			m.Actions[n] = a
		case "read", "write", "danger":
		default:
			return nil, fmt.Errorf("%s: action %s: effect %q: want read, write or danger", file, n, a.Effect)
		}
	}
	return m, nil
}

var knownKeys = sync.OnceValue(func() map[string]bool {
	known := map[string]bool{}
	for _, k := range structKeys() {
		known[k] = true
	}
	return known
})

func structKeys() []string {
	var out []string
	b, _ := yaml.Marshal(Manifest{Server: &Server{}, MCP: &Server{}, Detect: &Detect{}})
	var m map[string]any
	_ = yaml.Unmarshal(b, &m)
	for k := range m {
		out = append(out, k)
	}
	// omitempty hides these from the probe above
	out = append(out, "rig", "description", "publisher", "requires", "implements", "permissions", "config", "types",
		"schemes", "sources", "actions", "importers", "views", "widgets", "on", "events", "skills", "search",
		"complete", "doctor", "docs", "build")
	sort.Strings(out)
	return out
}

// Kind says how the plugin runs.
func (m *Manifest) Kind() string {
	switch {
	case m.Server != nil:
		return "server"
	case m.MCP != nil:
		return "mcp"
	}
	for _, s := range m.Sources {
		if len(s.Run) > 0 {
			return "script"
		}
	}
	for _, a := range m.Actions {
		if len(a.Run) > 0 {
			return "script"
		}
	}
	for _, im := range m.Importers {
		if len(im.Run) > 0 {
			return "script"
		}
	}
	return "declarative"
}

// Path resolves a manifest-relative path.
func (m *Manifest) Path(p string) string {
	if filepath.IsAbs(p) || !strings.HasPrefix(p, ".") && !strings.Contains(p, "/") {
		return p
	}
	return filepath.Join(m.Dir, p)
}

func (p Permissions) String() string {
	var out []string
	for _, kv := range []struct {
		k string
		v []string
	}{{"net", p.Net}, {"secrets", p.Secrets}, {"exec", p.Exec}, {"uses", p.Uses}, {"fs", p.FS}, {"env", p.Env}} {
		if len(kv.v) > 0 {
			out = append(out, kv.k+": "+strings.Join(kv.v, ", "))
		}
	}
	if p.AI {
		out = append(out, "ai")
	}
	if len(out) == 0 {
		return "none beyond the project"
	}
	return strings.Join(out, "; ")
}

// Lacking is a program a plugin needs that is not on PATH, and what to do about it.
type Lacking struct{ Bin, Fix string }

// Missing lists the programs the plugin needs (needs:, and unless needsOnly permissions.exec) that are not on PATH.
func (m *Manifest) Missing(needsOnly bool) []Lacking {
	bins := map[string]bool{}
	for b := range m.Needs {
		bins[b] = true
	}
	if !needsOnly {
		for _, b := range m.Permissions.Exec {
			bins[b] = true
		}
	}
	var out []Lacking
	for b := range bins {
		if _, err := exec.LookPath(b); err == nil {
			continue
		}
		fix := m.Needs[b]
		if fix == "" {
			fix = sh.Hint(`exec: "` + b + `": executable file not found`)
		}
		out = append(out, Lacking{b, fix})
	}
	slices.SortFunc(out, func(a, b Lacking) int { return strings.Compare(a.Bin, b.Bin) })
	return out
}

// MissingText is Missing as one sentence, "" when nothing is missing.
func (m *Manifest) MissingText(needsOnly bool) string {
	var parts []string
	for _, l := range m.Missing(needsOnly) {
		t := l.Bin
		if l.Fix != "" {
			t += " (" + l.Fix + ")"
		}
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		return ""
	}
	return "needs " + strings.Join(parts, ", ") + " installed first"
}
