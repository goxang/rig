package host

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
	"golang.org/x/sync/errgroup"
)

// Installer adds, upgrades and restores a project's plugins, pinned in rig.lock.
type Installer struct {
	Dir      string
	Registry *Registry
	// Yes approves workspace trust and permissions without asking (--yes).
	Yes bool
	// Ask asks the person at the terminal; nil means nobody is there.
	Ask func(question string) bool
	Out io.Writer

	mu sync.Mutex
}

func (in *Installer) say(format string, a ...any) {
	if in.Out != nil {
		in.mu.Lock()
		fmt.Fprintf(in.Out, format+"\n", a...)
		in.mu.Unlock()
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

type source struct {
	kind           string // registry, tarball, git, path
	name, version  string
	url, sha       string
	repo, sub, ref string
	path           string
	original       string
}

func parseSource(s string) source {
	src := source{original: s}
	switch {
	case strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/"):
		src.kind, src.path = "path", s
	case (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "file://")) && (strings.Contains(s, ".tar.gz") || strings.Contains(s, ".tgz")):
		src.kind = "tarball"
		src.url, src.sha, _ = strings.Cut(s, "#sha256=")
	case strings.HasPrefix(s, "git+"):
		src.kind = "git"
		src.repo, src.ref = cutRef(strings.TrimPrefix(s, "git+"))
	case strings.Contains(strings.SplitN(s, "/", 2)[0], ".") && strings.Contains(s, "/"):
		src.kind = "git"
		rest, ref := cutRef(s)
		parts := strings.SplitN(rest, "/", 4)
		src.repo, src.ref = "https://"+strings.Join(parts[:min(3, len(parts))], "/"), ref
		if len(parts) == 4 {
			src.sub = parts[3]
		}
	default:
		src.kind = "registry"
		src.name, src.version, _ = strings.Cut(s, "@")
		if p, ok := strings.CutPrefix(src.name, "pack/"); ok {
			src.name = "pack-" + p
		}
	}
	return src
}

// cutRef splits a trailing @ref off, but not the user@ of an ssh URL.
func cutRef(s string) (string, string) {
	i := strings.LastIndex(s, "@")
	if i < 0 || strings.Contains(s[i:], "/") || strings.Contains(s[i:], ":") {
		return s, ""
	}
	return s[:i], s[i+1:]
}

func isPathSource(s string) bool { return parseSource(s).kind == "path" }

func storePath(name, version string) string { return filepath.Join(StoreDir(), name, version) }

func (in *Installer) approve(m *Manifest, old string) error {
	if old == m.Permissions.Hash() {
		return nil
	}
	in.say("%s %s asks for: %s", m.Name, m.Version, describePerms(m.Permissions))
	if in.Yes {
		return nil
	}
	if in.Ask != nil && in.Ask(fmt.Sprintf("Allow %s these permissions?", m.Name)) {
		return nil
	}
	return fmt.Errorf("plugin %s needs its permissions approved: rerun with --yes", m.Name)
}

func describePerms(p Permissions) string {
	var out []string
	add := func(k string, v []string) {
		if len(v) > 0 {
			out = append(out, k+" "+strings.Join(v, ", "))
		}
	}
	add("net", p.Net)
	add("secrets", p.Secrets)
	add("exec", p.Exec)
	add("uses", p.Uses)
	add("fs", p.FS)
	add("env", p.Env)
	if p.AI {
		out = append(out, "ai")
	}
	if len(out) == 0 {
		return "nothing beyond the project"
	}
	if !sandboxed {
		out = append(out, "(advisory here: no sandbox on this OS)")
	}
	return strings.Join(out, "; ")
}

// Add installs plugins from their sources and pins them in rig.lock, with their packs' plugins.
func (in *Installer) Add(ctx context.Context, srcs []string) error {
	lock, err := ReadLock(in.Dir)
	if err != nil {
		return err
	}
	roots := map[string]string{}
	var packs []string
	for _, s := range srcs {
		src := parseSource(s)
		if src.kind != "registry" {
			m, l, err := in.fetch(ctx, src, lock)
			if err != nil {
				return err
			}
			if err := in.approve(m, ""); err != nil {
				return err
			}
			lock.Plugins[m.Name] = l
			in.say("+ %s %s (%s)", m.Name, m.Version, s)
			if text := m.MissingText(false); text != "" {
				in.say("! %s %s", m.Name, text)
			}
			continue
		}
		roots[src.name] = src.version
		if strings.HasPrefix(src.name, "pack-") {
			packs = append(packs, src.name)
		}
	}
	if len(roots) > 0 {
		if err := in.resolve(ctx, lock, roots, packs, false); err != nil {
			return err
		}
	}
	if err := lock.Write(in.Dir); err != nil {
		return err
	}
	return Trust(in.Dir)
}

// resolve runs minimal version selection over the locked registry plugins plus roots, then installs
// whatever changed.
func (in *Installer) resolve(ctx context.Context, lock *Lock, roots map[string]string, packs []string, major bool) error {
	ix, err := in.Registry.Index(ctx, true)
	if err != nil {
		return err
	}
	want := map[string]string{}
	for n, l := range lock.Plugins {
		if l.Source == "registry" {
			want[n] = l.Version
		}
	}
	for n, v := range roots {
		e := ix.Plugins[n]
		if e == nil {
			return fmt.Errorf("plugin %s is not in the index (rig plugin search %s)", n, n)
		}
		switch {
		case v != "":
		case major || want[n] == "":
			v = e.Latest()
		default:
			v = e.pick(minVersion(want[n]))
			for _, c := range slices.Collect(maps.Keys(e.Versions)) {
				if semver.Major(canon(c)) == semver.Major(canon(v)) && semver.Prerelease(canon(c)) == "" && semver.Compare(canon(c), canon(v)) > 0 {
					v = c
				}
			}
		}
		want[n] = v
	}
	chosen, err := ix.Resolve(want)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	var mu sync.Mutex
	for n, v := range chosen {
		mu.Lock()
		old := lock.Plugins[n]
		mu.Unlock()
		if old != nil && old.Version == v && exists(filepath.Join(storePath(n, v), ManifestFile)) {
			continue
		}
		g.Go(func() error {
			m, l, err := in.installRegistry(gctx, ix, n, v, nil)
			if err != nil {
				return err
			}
			prev := ""
			if old != nil {
				prev = old.Permissions
				l.Pack = old.Pack
			}
			if err := in.approve(m, prev); err != nil {
				return err
			}
			if _, root := roots[n]; !root && old == nil && len(packs) > 0 {
				l.Pack = packs[0]
			}
			mu.Lock()
			lock.Plugins[n] = l
			mu.Unlock()
			in.say("+ %s %s", n, v)
			return nil
		})
	}
	return g.Wait()
}

func (in *Installer) installRegistry(ctx context.Context, ix *Index, name, version string, locked *Locked) (*Manifest, *Locked, error) {
	e := ix.Plugins[name]
	if e == nil || e.Versions[version] == nil {
		return nil, nil, fmt.Errorf("plugin %s %s is not in the index", name, version)
	}
	vv := e.Versions[version]
	if !HostSupports(vv.Rig) {
		return nil, nil, fmt.Errorf("plugin %s %s needs rig %s; this is %s (rig upgrade)", name, version, vv.Rig, Version)
	}
	plat := Platform()
	art, ok := vv.Artifacts[plat]
	if !ok {
		plat = "any"
		if art, ok = vv.Artifacts[plat]; !ok {
			return nil, nil, fmt.Errorf("plugin %s %s has no build for %s", name, version, Platform())
		}
	}
	l := &Locked{Version: version, Source: "registry", SHA256: map[string]string{}}
	for p, a := range vv.Artifacts {
		l.SHA256[p] = a.SHA256
		for f, fa := range a.Files {
			if l.Files == nil {
				l.Files = map[string]map[string]string{}
			}
			if l.Files[p] == nil {
				l.Files[p] = map[string]string{}
			}
			l.Files[p][f] = fa.SHA256
		}
	}
	if locked != nil && locked.SHA256[plat] != "" && locked.SHA256[plat] != art.SHA256 {
		return nil, nil, fmt.Errorf("plugin %s %s: the index's checksum differs from rig.lock; refusing", name, version)
	}
	for f, fa := range art.Files {
		if locked != nil && locked.Files[plat][f] != "" && locked.Files[plat][f] != fa.SHA256 {
			return nil, nil, fmt.Errorf("plugin %s %s: the index's checksum of %s differs from rig.lock; refusing", name, version, f)
		}
	}
	data, err := in.artifact(ctx, name, version, plat, in.Registry.artifactURL(name, version, art))
	if err != nil {
		return nil, nil, err
	}
	if err := checkSum(data, art.SHA256); err != nil {
		return nil, nil, fmt.Errorf("plugin %s %s: %w", name, version, err)
	}
	key, err := publisherKey(e.Publisher, e.Key)
	if err != nil {
		return nil, nil, err
	}
	if key != "" {
		if art.Sig == "" {
			return nil, nil, fmt.Errorf("plugin %s %s is not signed", name, version)
		}
		if err := verifyMinisign(key, art.Sig, data); err != nil {
			return nil, nil, fmt.Errorf("plugin %s %s: %w", name, version, err)
		}
	}
	m, err := installArchive(data, name, version)
	if err != nil {
		return nil, nil, err
	}
	for f, fa := range art.Files {
		if err := in.sharedFile(ctx, key, f, fa, storePath(name, version)); err != nil {
			return nil, nil, fmt.Errorf("plugin %s %s: %s: %w", name, version, f, err)
		}
	}
	l.Permissions = m.Permissions.Hash()
	return m, l, nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ponytail: GC leaves blobs; drop those with one link left if the store grows.
// ponytail: one lock for every blob; per-sha locks if downloads of different files should overlap
var blobMu sync.Mutex

func blobPath(sha string) string { return filepath.Join(StoreDir(), ".blobs", sha) }

func vendorBlob(dir, sha string) string {
	return filepath.Join(dir, ".rig", "vendor", "blobs", sha+".gz")
}

// sharedFile puts a shared file at path f in the plugin at dir: a hard link to the store's one copy,
// downloaded (or read from .rig/vendor), checked and unpacked the first time.
func (in *Installer) sharedFile(ctx context.Context, key, f string, a Artifact, dir string) error {
	if !sha256Hex.MatchString(a.SHA256) {
		return fmt.Errorf("bad sha256 %q", a.SHA256)
	}
	to := filepath.Join(dir, f)
	if !strings.HasPrefix(to, filepath.Clean(dir)+string(os.PathSeparator)) {
		return fmt.Errorf("path %q escapes the plugin", f)
	}
	blob := blobPath(a.SHA256)
	// plugins install in parallel; two writing one blob would leave each linked to its own copy
	blobMu.Lock()
	defer blobMu.Unlock()
	if !exists(blob) {
		data, err := os.ReadFile(vendorBlob(in.Dir, a.SHA256))
		if err != nil {
			body, err := in.Registry.get(ctx, in.Registry.fileURL(a))
			if err != nil {
				return err
			}
			data, err = io.ReadAll(body)
			body.Close()
			if err != nil {
				return err
			}
		}
		if err := checkSum(data, a.SHA256); err != nil {
			return err
		}
		if key != "" {
			if a.Sig == "" {
				return errors.New("not signed")
			}
			if err := verifyMinisign(key, a.Sig, data); err != nil {
				return err
			}
		}
		if err := unpackBlob(data, blob); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	_ = os.Remove(to)
	if os.Link(blob, to) == nil {
		return nil
	}
	b, err := os.ReadFile(blob)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o755)
}

// unpackBlob writes the gunzipped data at blob, keeping data beside it as blob.gz for vendor.
func unpackBlob(data []byte, blob string) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 1<<30))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(blob+".gz", data, 0o644); err != nil {
		return err
	}
	return writeAtomic(blob, raw, 0o755)
}

// artifact reads a vendored artifact when there is one, else downloads it.
func (in *Installer) artifact(ctx context.Context, name, version, plat, url string) ([]byte, error) {
	if b, err := os.ReadFile(vendorFile(in.Dir, name, version, plat)); err == nil {
		return b, nil
	}
	body, err := in.Registry.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("download %s %s: %w", name, version, err)
	}
	defer body.Close()
	return io.ReadAll(body)
}

func vendorFile(dir, name, version, plat string) string {
	return filepath.Join(dir, ".rig", "vendor", fmt.Sprintf("%s-%s-%s.tar.gz", name, version, strings.ReplaceAll(plat, "/", "-")))
}

func checkSum(data []byte, want string) error {
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); want != "" && got != want {
		return fmt.Errorf("checksum %s, want %s", got, want)
	}
	return nil
}

// installArchive unpacks a plugin tarball into the store, keeping the archive for vendor.
func installArchive(data []byte, name, version string) (*Manifest, error) {
	tmp, err := stageDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := untar(data, tmp); err != nil {
		return nil, err
	}
	root := tmp
	if !exists(filepath.Join(root, ManifestFile)) {
		ents, _ := os.ReadDir(tmp)
		if len(ents) == 1 && ents[0].IsDir() {
			root = filepath.Join(tmp, ents[0].Name())
		}
	}
	m, err := LoadManifest(root)
	if err != nil {
		return nil, fmt.Errorf("the archive has no usable %s: %w", ManifestFile, err)
	}
	if name == "" {
		name, version = m.Name, cmpVersion(m.Version, "0.0.0")
	}
	if m.Name != name {
		return nil, fmt.Errorf("the archive holds plugin %s, not %s", m.Name, name)
	}
	if err := place(root, storePath(name, version)); err != nil {
		return nil, err
	}
	_ = os.WriteFile(storePath(name, version)+".tar.gz", data, 0o600)
	return LoadManifest(storePath(name, version))
}

func cmpVersion(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func stageDir() (string, error) {
	if err := os.MkdirAll(StoreDir(), 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(StoreDir(), ".stage-")
}

// place moves a staged plugin into the store, replacing what was there.
func place(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(to)
	return os.Rename(from, to)
}

func untar(data []byte, dir string) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, h.Name)
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("archive path %q escapes the plugin", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, 1<<30))
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}

func tarDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		h, _ := tar.FileInfoHeader(info, "")
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fetch installs a non-registry source: a tarball, a git repo or a local path.
func (in *Installer) fetch(ctx context.Context, src source, lock *Lock) (*Manifest, *Locked, error) {
	switch src.kind {
	case "path":
		p := src.path
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, rest)
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(in.Dir, p)
		}
		m, err := LoadManifest(p)
		if err != nil {
			return nil, nil, err
		}
		if err := in.build(ctx, m); err != nil {
			return nil, nil, err
		}
		return m, &Locked{Version: m.Version, Source: src.original, Permissions: m.Permissions.Hash()}, nil
	case "tarball":
		body, err := in.Registry.get(ctx, src.url)
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			return nil, nil, err
		}
		if err := checkSum(data, src.sha); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", src.url, err)
		}
		m, err := installArchive(data, "", "")
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(data)
		return m, &Locked{Version: m.Version, Source: src.original, SHA256: map[string]string{"any": hex.EncodeToString(sum[:])}, Permissions: m.Permissions.Hash()}, nil
	case "git":
		commit := ""
		for _, l := range lock.Plugins {
			if l.Source == src.original {
				commit = l.Commit
			}
		}
		return in.gitInstall(ctx, src, commit)
	}
	return nil, nil, fmt.Errorf("unknown plugin source %q", src.original)
}

func (in *Installer) gitInstall(ctx context.Context, src source, commit string) (*Manifest, *Locked, error) {
	tmp, err := stageDir()
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = tmp
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, bytes.TrimSpace(out))
		}
		return strings.TrimSpace(string(out)), nil
	}
	args := []string{"clone", "--quiet"}
	if commit == "" {
		args = append(args, "--depth", "1")
		if src.ref != "" {
			args = append(args, "--branch", src.ref)
		}
	}
	if _, err := git(append(args, src.repo, "src")...); err != nil {
		return nil, nil, err
	}
	tmpSrc := filepath.Join(tmp, "src")
	if commit != "" {
		cmd := exec.CommandContext(ctx, "git", "checkout", "--quiet", commit)
		cmd.Dir = tmpSrc
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, nil, fmt.Errorf("git checkout %s: %s", commit, bytes.TrimSpace(out))
		}
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = tmpSrc
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, err
	}
	head := strings.TrimSpace(string(out))
	dir := filepath.Join(tmpSrc, src.sub)
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, nil, err
	}
	version := cmpVersion(m.Version, cmpVersion(src.ref, "0.0.0-"+head[:12]))
	if err := in.build(ctx, m); err != nil {
		return nil, nil, err
	}
	if err := place(dir, storePath(m.Name, version)); err != nil {
		return nil, nil, err
	}
	m, err = LoadManifest(storePath(m.Name, version))
	if err != nil {
		return nil, nil, err
	}
	return m, &Locked{Version: version, Source: src.original, Commit: head, Permissions: m.Permissions.Hash()}, nil
}

func (in *Installer) build(ctx context.Context, m *Manifest) error {
	if len(m.Build) == 0 {
		return nil
	}
	in.say("  building %s: %s", m.Name, strings.Join(m.Build, " "))
	cmd := exec.CommandContext(ctx, m.Build[0], m.Build[1:]...)
	cmd.Dir = m.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build %s: %v\n%s", m.Name, err, out)
	}
	return nil
}

// ErrUntrusted stops a run nobody can answer the workspace trust question for.
var ErrUntrusted = errors.New("this project's plugins need workspace trust: run rig trust (or pass --yes, or set RIG_TRUST=project)")

// Ensure installs what rig.lock pins and this machine lacks, after workspace trust; changed says the
// project's plugins differ from before, so a loaded project is stale.
func (in *Installer) Ensure(ctx context.Context) (changed bool, err error) {
	lock, err := ReadLock(in.Dir)
	if err != nil {
		return false, err
	}
	var missing []string
	for _, n := range lock.Names() {
		l := lock.Plugins[n]
		if !isPathSource(l.Source) && !exists(filepath.Join(storePath(n, l.Version), ManifestFile)) {
			missing = append(missing, n)
		}
	}
	local := exists(filepath.Join(in.Dir, ".rig", "plugins"))
	if Trusted(in.Dir) && len(missing) == 0 || len(lock.Plugins) == 0 && !local {
		return false, nil
	}
	if !Trusted(in.Dir) {
		ok, err := in.trust(ctx, lock, missing)
		if !ok || err != nil {
			return false, err
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	var ix *Index
	var ixErr error
	var once sync.Once
	index := func() (*Index, error) {
		once.Do(func() { ix, ixErr = in.Registry.Index(gctx, false) })
		return ix, ixErr
	}
	for _, n := range missing {
		l := lock.Plugins[n]
		g.Go(func() error {
			m, err := in.restore(gctx, n, l, index)
			if err != nil {
				return err
			}
			if m.Permissions.Hash() != l.Permissions {
				return fmt.Errorf("plugin %s %s asks for other permissions than rig.lock grants; rig plugin up %s to review", n, l.Version, n)
			}
			in.say("  installed %s %s", n, l.Version)
			return nil
		})
	}
	return true, g.Wait()
}

func (in *Installer) restore(ctx context.Context, name string, l *Locked, index func() (*Index, error)) (*Manifest, error) {
	if b, err := os.ReadFile(vendorFile(in.Dir, name, l.Version, "any")); err == nil && l.Source != "registry" {
		return installArchive(b, name, l.Version)
	}
	switch src := parseSource(l.Source); src.kind {
	case "registry":
		plat := Platform()
		if l.SHA256[plat] == "" {
			plat = "any"
		}
		if b, err := os.ReadFile(vendorFile(in.Dir, name, l.Version, plat)); err == nil {
			if err := checkSum(b, l.SHA256[plat]); err != nil {
				return nil, fmt.Errorf("vendored %s: %w", name, err)
			}
			m, err := installArchive(b, name, l.Version)
			if err != nil {
				return nil, err
			}
			// the lock's checksum stands in for the signature, as for the vendored archive
			for f, sha := range l.Files[plat] {
				if err := in.sharedFile(ctx, "", f, Artifact{SHA256: sha}, storePath(name, l.Version)); err != nil {
					return nil, fmt.Errorf("vendored %s: %s: %w", name, f, err)
				}
			}
			return m, nil
		}
		ix, err := index()
		if err != nil {
			return nil, err
		}
		m, _, err := in.installRegistry(ctx, ix, name, l.Version, l)
		return m, err
	case "tarball":
		src.sha = l.SHA256["any"]
		m, _, err := in.fetch(ctx, src, &Lock{})
		return m, err
	case "git":
		m, _, err := in.gitInstall(ctx, src, l.Commit)
		return m, err
	}
	return nil, fmt.Errorf("plugin %s: unknown source %q in rig.lock", name, l.Source)
}

// trust shows what the workspace would run and asks once; nobody to ask fails with the command to run.
func (in *Installer) trust(ctx context.Context, lock *Lock, missing []string) (bool, error) {
	in.say("This project runs plugins:")
	// the index only describes permissions here: one short try, not a wait per plugin on a slow network
	index := sync.OnceValue(func() *Index {
		ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ix, _ := in.Registry.Index(ictx, false)
		return ix
	})
	for _, n := range lock.Names() {
		l := lock.Plugins[n]
		perms := "(permissions in rig.lock)"
		dir := storePath(n, l.Version)
		if src := parseSource(l.Source); src.kind == "path" && filepath.IsAbs(src.path) {
			dir = src.path
		} else if src.kind == "path" {
			dir = filepath.Join(in.Dir, src.path)
		}
		if m, err := LoadManifest(dir); err == nil {
			perms = describePerms(m.Permissions)
		} else if ix := index(); ix != nil && ix.Plugins[n] != nil && ix.Plugins[n].Versions[l.Version] != nil {
			perms = describePerms(ix.Plugins[n].Versions[l.Version].Permissions)
		}
		in.say("  %s %s from %s: %s", n, l.Version, l.Source, perms)
	}
	ents, _ := os.ReadDir(filepath.Join(in.Dir, ".rig", "plugins"))
	for _, e := range ents {
		if m, err := LoadManifest(filepath.Join(in.Dir, ".rig", "plugins", e.Name())); err == nil {
			in.say("  %s (in .rig/plugins): %s", m.Name, describePerms(m.Permissions))
		}
	}
	switch {
	case in.Yes:
	case in.Ask != nil:
		if !in.Ask("Trust this workspace" + map[bool]string{true: " and install them", false: ""}[len(missing) > 0] + "?") {
			in.say("Plugins stay off; rig trust turns them on.")
			return false, nil
		}
	default:
		return false, ErrUntrusted
	}
	return true, Trust(in.Dir)
}

// Remove unpins plugins, and the plugins a removed pack brought that nothing else needs.
func (in *Installer) Remove(names []string) error {
	lock, err := ReadLock(in.Dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		if p, ok := strings.CutPrefix(n, "pack/"); ok {
			n = "pack-" + p
		}
		if lock.Plugins[n] == nil {
			return fmt.Errorf("plugin %s is not in rig.lock", n)
		}
		delete(lock.Plugins, n)
		in.say("- %s", n)
		for d, l := range lock.Plugins {
			if l.Pack == n {
				delete(lock.Plugins, d)
				in.say("- %s (from %s)", d, n)
			}
		}
	}
	return lock.Write(in.Dir)
}

// Up moves plugins (all when names is empty) to their newest version of the same major, or the newest
// at all with major.
func (in *Installer) Up(ctx context.Context, names []string, major bool) error {
	lock, err := ReadLock(in.Dir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = lock.Names()
	}
	roots := map[string]string{}
	for _, n := range names {
		l := lock.Plugins[n]
		if l == nil {
			return fmt.Errorf("plugin %s is not in rig.lock", n)
		}
		switch src := parseSource(l.Source); src.kind {
		case "registry":
			roots[n] = ""
		case "git", "tarball", "path":
			m, nl, err := in.fetch(ctx, src, &Lock{})
			if err != nil {
				return err
			}
			if err := in.approve(m, l.Permissions); err != nil {
				return err
			}
			nl.Pack = l.Pack
			lock.Plugins[n] = nl
		}
	}
	if len(roots) > 0 {
		if err := in.resolve(ctx, lock, roots, nil, major); err != nil {
			return err
		}
	}
	return lock.Write(in.Dir)
}

// Vendor copies the locked plugins into .rig/vendor so an offline machine installs from the repo.
func (in *Installer) Vendor() ([]string, error) {
	lock, err := ReadLock(in.Dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range lock.Names() {
		l := lock.Plugins[n]
		if isPathSource(l.Source) {
			continue
		}
		plat := Platform()
		if l.SHA256[plat] == "" {
			plat = "any"
		}
		data, err := os.ReadFile(storePath(n, l.Version) + ".tar.gz")
		if err != nil || l.Source != "registry" {
			if data, err = tarDir(storePath(n, l.Version)); err != nil {
				return out, fmt.Errorf("vendor %s: %w (rig plugin up first?)", n, err)
			}
			plat = "any"
		}
		f := vendorFile(in.Dir, n, l.Version, plat)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return out, err
		}
		if err := os.WriteFile(f, data, 0o644); err != nil {
			return out, err
		}
		out = append(out, f)
		for _, sha := range l.Files[plat] {
			b, err := os.ReadFile(blobPath(sha) + ".gz")
			if err != nil {
				return out, fmt.Errorf("vendor %s: %w (rig plugin up first?)", n, err)
			}
			vf := vendorBlob(in.Dir, sha)
			if err := os.MkdirAll(filepath.Dir(vf), 0o755); err != nil {
				return out, err
			}
			if err := os.WriteFile(vf, b, 0o644); err != nil {
				return out, err
			}
			out = append(out, vf)
		}
	}
	return out, nil
}

// GC removes installed versions no known project locks.
func GC() ([]string, error) {
	keep := map[string]bool{}
	ps := projects()
	ps[ConfigDir()] = true
	for dir := range ps {
		if !exists(filepath.Join(dir, LockFile)) {
			continue
		}
		lock, err := ReadLock(dir)
		if err != nil {
			continue
		}
		for n, l := range lock.Plugins {
			keep[filepath.Join(n, l.Version)] = true
		}
	}
	var removed []string
	names, _ := os.ReadDir(StoreDir())
	for _, n := range names {
		if !n.IsDir() || strings.HasPrefix(n.Name(), ".") {
			continue
		}
		vs, _ := os.ReadDir(filepath.Join(StoreDir(), n.Name()))
		for _, v := range vs {
			if !v.IsDir() || keep[filepath.Join(n.Name(), v.Name())] {
				continue
			}
			p := filepath.Join(StoreDir(), n.Name(), v.Name())
			if err := os.RemoveAll(p); err != nil {
				return removed, err
			}
			os.Remove(p + ".tar.gz")
			removed = append(removed, n.Name()+"@"+v.Name())
		}
	}
	return removed, nil
}
