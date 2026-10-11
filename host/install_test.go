package host

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/goxang/rig/spec"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// fakeRegistry publishes plugins as a mirror directory: index.json and <name>/<version>/<file>.
func fakeRegistry(t *testing.T, plugins map[string]map[string]string) string {
	t.Helper()
	mirror := t.TempDir()
	ix := Index{Plugins: map[string]*IndexEntry{}}
	for name, versions := range plugins {
		e := &IndexEntry{Publisher: "someone", Versions: map[string]*IndexVersion{}}
		for v, extra := range versions {
			src := t.TempDir()
			manifest := "name: " + name + "\nversion: " + v + "\n" + extra
			os.WriteFile(filepath.Join(src, ManifestFile), []byte(manifest), 0o644)
			data, err := tarDir(src)
			if err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(filepath.Join(mirror, name, v), 0o755)
			os.WriteFile(filepath.Join(mirror, name, v, "p.tar.gz"), data, 0o644)
			sum := sha256.Sum256(data)
			m, _ := ParseManifest([]byte(manifest), "x/"+ManifestFile)
			e.Versions[v] = &IndexVersion{Requires: m.Requires, Permissions: m.Permissions,
				Artifacts: map[string]Artifact{"any": {URL: "https://example.invalid/p.tar.gz", SHA256: hex.EncodeToString(sum[:])}}}
		}
		ix.Plugins[name] = e
	}
	b, _ := json.Marshal(ix)
	os.WriteFile(filepath.Join(mirror, "index.json"), b, 0o644)
	return mirror
}

func isolate(t *testing.T) {
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
}

func TestInstallPackVendorGC(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	mirror := fakeRegistry(t, map[string]map[string]string{
		"git":      {"1.0.0": "", "1.1.0": "", "1.2.0": "", "2.0.0": ""},
		"pack-std": {"1.0.0": "requires: { git: ^1.1 }\ntasks: { hi: [echo hi] }\n"},
		"net":      {"1.0.0": "permissions: { net: [example.com] }\n"},
	})
	proj := t.TempDir()
	in := &Installer{Dir: proj, Registry: &Registry{Mirror: mirror}, Yes: true}
	if err := in.Add(ctx, []string{"pack/std"}); err != nil {
		t.Fatal(err)
	}
	lock, _ := ReadLock(proj)
	if g := lock.Plugins["git"]; g == nil || g.Version != "1.1.0" || g.Pack != "pack-std" {
		t.Fatalf("git locked as %+v, want 1.1.0 from the pack (minimal version selection)", g)
	}
	ms, warn := Discover(proj, nil)
	if len(ms) != 2 || len(warn) != 0 {
		t.Fatalf("discovered %d plugins, warnings %v", len(ms), warn)
	}

	if err := in.Up(ctx, []string{"git"}, false); err != nil {
		t.Fatal(err)
	}
	if lock, _ = ReadLock(proj); lock.Plugins["git"].Version != "1.2.0" {
		t.Fatalf("up went to %s, want 1.2.0 (same major)", lock.Plugins["git"].Version)
	}

	asked := ""
	strict := &Installer{Dir: proj, Registry: &Registry{Mirror: mirror}, Ask: func(q string) bool { asked = q; return false }}
	if err := strict.Add(ctx, []string{"net"}); err == nil || !strings.Contains(asked, "permissions") {
		t.Fatalf("new permissions went unasked: %v %q", err, asked)
	}

	if _, err := in.Vendor(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // a clean machine
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	offline := &Installer{Dir: proj, Registry: &Registry{Mirror: filepath.Join(mirror, "gone")}}
	if _, err := offline.Ensure(ctx); err == nil || !strings.Contains(err.Error(), "rig trust") {
		t.Fatalf("an untrusted, non-interactive install went ahead: %v", err)
	}
	offline.Yes = true
	if _, err := offline.Ensure(ctx); err != nil {
		t.Fatalf("offline install from vendor: %v", err)
	}
	if ms, _ := Discover(proj, nil); len(ms) != 2 {
		t.Fatalf("after the vendor install: %d plugins", len(ms))
	}

	if err := in.Remove([]string{"pack/std"}); err != nil {
		t.Fatal(err)
	}
	if lock, _ = ReadLock(proj); len(lock.Plugins) != 0 {
		t.Fatalf("pack removal left %v", lock.Names())
	}
	removed, err := GC()
	if err != nil || len(removed) != 2 {
		t.Fatalf("gc removed %v, %v", removed, err)
	}
}

func TestChecksumMismatchRefused(t *testing.T) {
	isolate(t)
	mirror := fakeRegistry(t, map[string]map[string]string{"x": {"1.0.0": ""}})
	os.WriteFile(filepath.Join(mirror, "x/1.0.0/p.tar.gz"), []byte("tampered"), 0o644)
	in := &Installer{Dir: t.TempDir(), Registry: &Registry{Mirror: mirror}, Yes: true}
	if err := in.Add(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered artifact: %v", err)
	}
}

func TestResolve(t *testing.T) {
	v := func(req map[string]string) *IndexVersion { return &IndexVersion{Requires: req} }
	ix := &Index{Plugins: map[string]*IndexEntry{
		"a": {Versions: map[string]*IndexVersion{"1.0.0": v(map[string]string{"c": "^1.1"})}},
		"b": {Versions: map[string]*IndexVersion{"1.0.0": v(map[string]string{"c": "1.3"})}},
		"c": {Versions: map[string]*IndexVersion{"1.1.0": v(nil), "1.3.0": v(nil), "1.4.0": v(nil), "2.0.0": v(nil)}},
		"d": {Versions: map[string]*IndexVersion{"1.0.0": v(map[string]string{"c": "^2"})}},
	}}
	got, err := ix.Resolve(map[string]string{"a": "1.0.0", "b": "1.0.0"})
	if err != nil || got["c"] != "1.3.0" {
		t.Fatalf("c = %s, %v; want 1.3.0, the highest minimum", got["c"], err)
	}
	if _, err := ix.Resolve(map[string]string{"a": "1.0.0", "d": "1.0.0"}); err == nil {
		t.Fatal("two majors of c resolved")
	}
}

func TestParseSource(t *testing.T) {
	for in, want := range map[string]string{
		"gitlab":                          "registry gitlab ",
		"pack/product@1.2.0":              "registry pack-product 1.2.0",
		"github.com/o/r/sub@v1.2.0":       "git https://github.com/o/r sub v1.2.0",
		"git+ssh://git@host/grp/r.git@v1": "git ssh://git@host/grp/r.git  v1",
		"./tools/rig-foo":                 "path ./tools/rig-foo",
		"https://x/p.tar.gz#sha256=abc":   "tarball https://x/p.tar.gz abc",
	} {
		s := parseSource(in)
		var got string
		switch s.kind {
		case "registry":
			got = strings.Join([]string{s.kind, s.name, s.version}, " ")
		case "git":
			got = strings.Join([]string{s.kind, s.repo, s.sub, s.ref}, " ")
		case "path":
			got = s.kind + " " + s.path
		case "tarball":
			got = strings.Join([]string{s.kind, s.url, s.sha}, " ")
		}
		if got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestMinisign(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := []byte("12345678")
	pk := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id...), pub...))
	data := []byte("artifact bytes")
	h := blake2b.Sum512(data)
	sig := ed25519.Sign(priv, h[:])
	trusted := "timestamp:1 file:p.tar.gz"
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), trusted...))
	text := "untrusted comment: x\n" + base64.StdEncoding.EncodeToString(append(append([]byte("ED"), id...), sig...)) +
		"\ntrusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n"
	if err := verifyMinisign(pk, text, data); err != nil {
		t.Fatal(err)
	}
	if err := verifyMinisign(pk, text, []byte("other bytes")); err == nil {
		t.Fatal("a signature over other bytes verified")
	}
}

func TestSignVerifies(t *testing.T) {
	pub, secret, err := Keygen()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(secret, "checksums.txt", []byte("sums"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMinisign(pub, sig, []byte("sums")); err != nil {
		t.Fatal(err)
	}
}

func TestSharedFileOnceVendored(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	mirror := fakeRegistry(t, map[string]map[string]string{"a": {"1.0.0": ""}, "b": {"1.0.0": ""}})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("#!/bin/sh\n"))
	zw.Close()
	os.MkdirAll(filepath.Join(mirror, "files"), 0o755)
	os.WriteFile(filepath.Join(mirror, "files", "std.gz"), gz.Bytes(), 0o644)
	sum := sha256.Sum256(gz.Bytes())
	var ix Index
	b, _ := os.ReadFile(filepath.Join(mirror, "index.json"))
	json.Unmarshal(b, &ix)
	for _, e := range ix.Plugins {
		a := e.Versions["1.0.0"].Artifacts["any"]
		a.Files = map[string]Artifact{"bin/std": {URL: "https://example.invalid/std.gz", SHA256: hex.EncodeToString(sum[:])}}
		e.Versions["1.0.0"].Artifacts["any"] = a
	}
	b, _ = json.Marshal(ix)
	os.WriteFile(filepath.Join(mirror, "index.json"), b, 0o644)

	proj := t.TempDir()
	in := &Installer{Dir: proj, Registry: &Registry{Mirror: mirror}, Yes: true}
	if err := in.Add(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	fa, err := os.Stat(filepath.Join(storePath("a", "1.0.0"), "bin/std"))
	if err != nil {
		t.Fatal(err)
	}
	fb, _ := os.Stat(filepath.Join(storePath("b", "1.0.0"), "bin/std"))
	if !os.SameFile(fa, fb) || fa.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the shared file is not one executable copy: %v %v", fa.Mode(), fb)
	}

	if _, err := in.Vendor(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	offline := &Installer{Dir: proj, Registry: &Registry{Mirror: filepath.Join(mirror, "gone")}, Yes: true}
	if _, err := offline.Ensure(ctx); err != nil {
		t.Fatalf("offline install from vendor: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(storePath("a", "1.0.0"), "bin/std")); string(got) != "#!/bin/sh\n" {
		t.Fatalf("vendored shared file: %q", got)
	}
}

func TestUserPlugins(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	mirror := fakeRegistry(t, map[string]map[string]string{"arcade": {"1.0.0": ""}})
	proj := t.TempDir()
	mine := &Installer{Dir: ConfigDir(), Registry: &Registry{Mirror: mirror}, Yes: true}
	if err := mine.Add(ctx, []string{"arcade"}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(proj, LockFile)) {
		t.Fatal("a user plugin wrote the project's rig.lock")
	}
	ms, _ := Discover(proj, nil)
	if len(ms) != 1 || ms[0].Name != "arcade" || !ms[0].Relevant(proj, nil) {
		t.Fatalf("user plugin not discovered in a project: %v", ms)
	}
	if ms, _ := Discover(proj, map[string]*spec.Plugin{"arcade": {Off: true}}); len(ms) != 0 {
		t.Fatal("plugins.arcade.off does not hide a user plugin")
	}
	if removed, _ := GC(); len(removed) != 0 {
		t.Fatalf("gc removed a user-locked plugin: %v", removed)
	}
}
