package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const DefaultIndex = "https://raw.githubusercontent.com/goxang/rig-plugins/main/registry/index.json"

type Index struct {
	Plugins map[string]*IndexEntry `json:"plugins"`
}

type IndexEntry struct {
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Repo        string `json:"repo,omitempty"`
	// Key is the publisher's minisign public key; goxang's is built in.
	Key      string                   `json:"key,omitempty"`
	Detect   *Detect                  `json:"detect,omitempty"`
	Provides map[string]any           `json:"provides,omitempty"`
	Packs    []string                 `json:"packs,omitempty"`
	Versions map[string]*IndexVersion `json:"versions"`
}

type IndexVersion struct {
	Rig         string              `json:"rig,omitempty"`
	Requires    map[string]string   `json:"requires,omitempty"`
	Permissions Permissions         `json:"permissions"`
	Artifacts   map[string]Artifact `json:"artifacts"`
}

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Sig is the minisign signature file's text.
	Sig string `json:"sig,omitempty"`
	// Files are gzipped files the archive leaves out because several plugins share them (rig-std),
	// by their path in the plugin; each is downloaded once.
	Files map[string]Artifact `json:"files,omitempty"`
}

// Registry reads the plugin index, from a mirror when one is set.
type Registry struct {
	// Mirror replaces every download: <mirror>/index.json and <mirror>/<name>/<version>/<file>.
	Mirror string
	Client *http.Client
}

// UserRegistry is the registry the user config, or $RIG_REGISTRY, points at.
func UserRegistry() *Registry {
	// the default 10s TLS handshake is too short through a slow proxy
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSHandshakeTimeout = time.Minute
	r := &Registry{Mirror: os.Getenv("RIG_REGISTRY"), Client: &http.Client{Timeout: 5 * time.Minute, Transport: tr}}
	if r.Mirror == "" {
		var c struct {
			Registry string `yaml:"registry"`
		}
		if b, err := os.ReadFile(filepath.Join(ConfigDir(), "config.yaml")); err == nil && yaml.Unmarshal(b, &c) == nil {
			r.Mirror = c.Registry
		}
	}
	r.Mirror = strings.TrimSuffix(r.Mirror, "/")
	return r
}

func (r *Registry) indexURL() string {
	if r.Mirror != "" {
		return r.Mirror + "/index.json"
	}
	return DefaultIndex
}

func (r *Registry) artifactURL(name, version string, a Artifact) string {
	if r.Mirror != "" {
		return fmt.Sprintf("%s/%s/%s/%s", r.Mirror, name, version, path.Base(a.URL))
	}
	return a.URL
}

// fileURL is where a shared file is: <mirror>/files/<name> on a mirror.
func (r *Registry) fileURL(a Artifact) string {
	if r.Mirror != "" {
		return r.Mirror + "/files/" + path.Base(a.URL)
	}
	return a.URL
}

func (r *Registry) get(ctx context.Context, url string) (io.ReadCloser, error) {
	if strings.HasPrefix(url, "file://") || !strings.Contains(url, "://") {
		return os.Open(strings.TrimPrefix(url, "file://"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, res.Status)
	}
	return res.Body, nil
}

// Index is the cached index, refetched after an hour or when refresh is set.
func (r *Registry) Index(ctx context.Context, refresh bool) (*Index, error) {
	cache := filepath.Join(CacheDir(), "index-"+shortHash(r.indexURL())+".json")
	st, err := os.Stat(cache)
	if refresh || err != nil || time.Since(st.ModTime()) > time.Hour {
		if err := r.fetchIndex(ctx, cache); err != nil {
			if st == nil {
				return nil, fmt.Errorf("plugin index: %w", err)
			}
			// stale beats nothing when offline
		}
	}
	b, err := os.ReadFile(cache)
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, fmt.Errorf("plugin index: %w", err)
	}
	return &ix, nil
}

func (r *Registry) fetchIndex(ctx context.Context, cache string) error {
	body, err := r.get(ctx, r.indexURL())
	if err != nil {
		return err
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if !json.Valid(b) {
		return fmt.Errorf("%s: not JSON", r.indexURL())
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		return err
	}
	return writeAtomic(cache, b, 0o600)
}

// Latest is the highest published version, pre-releases only when nothing else exists.
func (e *IndexEntry) Latest() string {
	var vs []string
	for v := range e.Versions {
		vs = append(vs, v)
	}
	sortVersions(vs)
	for i := len(vs) - 1; i >= 0; i-- {
		if semver.Prerelease(canon(vs[i])) == "" {
			return vs[i]
		}
	}
	if len(vs) == 0 {
		return ""
	}
	return vs[len(vs)-1]
}

func sortVersions(vs []string) {
	sort.Slice(vs, func(i, j int) bool { return semver.Compare(canon(vs[i]), canon(vs[j])) < 0 })
}

// minVersion reads a requirement as minimal version selection does: ^1.2, ~1.2, >=1.2 and 1.2 all
// mean "1.2.0 or later of major 1".
func minVersion(c string) string {
	c = strings.TrimSpace(strings.TrimLeft(c, "^~>=v "))
	if c == "" || c == "*" {
		return "v0.0.0"
	}
	v := canon(c)
	switch strings.Count(v, ".") {
	case 0:
		v += ".0.0"
	case 1:
		v += ".0"
	}
	return v
}

// HostSupports says whether this rig satisfies a manifest's rig: constraint; dev builds take anything.
func HostSupports(c string) bool {
	if c == "" || !semver.IsValid(canon(Version)) {
		return true
	}
	return semver.Compare(canon(Version), minVersion(c)) >= 0
}

// Resolve picks a version for every plugin the roots need, by minimal version selection: each plugin
// gets the highest of the minimums asked of it, then the lowest published version of that major at
// or above it. The result is deterministic and needs no solver.
func (ix *Index) Resolve(roots map[string]string) (map[string]string, error) {
	want := map[string]string{}
	var queue []string
	for n, v := range roots {
		want[n] = minVersion(v)
		queue = append(queue, n)
	}
	chosen := map[string]string{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		e := ix.Plugins[n]
		if e == nil {
			return nil, fmt.Errorf("plugin %s is not in the index", n)
		}
		v := e.pick(want[n])
		if v == "" {
			return nil, fmt.Errorf("plugin %s has no version %s or later of that major", n, want[n])
		}
		if chosen[n] == v {
			continue
		}
		chosen[n] = v
		for dep, c := range e.Versions[v].Requires {
			m := minVersion(c)
			if cur, ok := want[dep]; ok && semver.Major(cur) != semver.Major(m) {
				return nil, fmt.Errorf("%s needs %s %s but %s is already required", n, dep, c, cur)
			}
			if cur, ok := want[dep]; !ok || semver.Compare(m, cur) > 0 {
				want[dep] = m
				queue = append(queue, dep)
			}
		}
	}
	return chosen, nil
}

func (e *IndexEntry) pick(min string) string {
	var vs []string
	for v := range e.Versions {
		if semver.Major(canon(v)) == semver.Major(min) && semver.Compare(canon(v), min) >= 0 {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return ""
	}
	sortVersions(vs)
	return vs[0]
}
