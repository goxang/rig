package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/goxang/rig/spec"
)

func init() { spec.PluginImport = pluginImport }

// pluginImport runs the importer through the plugin that provides it. Fragments are cached in
// .rig/cache/import by the sha256 of the imported files and the plugin, so warm loads start no plugin.
// ponytail: files an importer reads beside path (a compose .env) are not in the key; use ImportResult.Watch when one bites.
func pluginImport(dir, name, path string) (map[string]any, error) {
	ms, _ := Discover(dir, nil)
	var m *Manifest
	for _, c := range ms {
		if _, ok := c.Importers[name]; ok {
			m = c
			break
		}
	}
	if m == nil {
		return nil, fmt.Errorf("%q: %w", name, spec.ErrNoImporter)
	}
	cache := filepath.Join(dir, ".rig", "cache", "import", importKey(m, name, path)+".json")
	if b, err := os.ReadFile(cache); err == nil {
		var frag map[string]any
		if json.Unmarshal(b, &frag) == nil {
			return frag, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h := New(Options{Dir: dir}, nil)
	defer h.Close()
	r, err := h.Import(ctx, m.Name+"."+name, []string{path})
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(r.Fragment); err == nil && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		_ = os.WriteFile(cache, b, 0o644)
	}
	return r.Fragment, nil
}

func importKey(m *Manifest, name, path string) string {
	h := sha256.New()
	fmt.Fprintln(h, m.Name, m.Version, name, path)
	if m.Server != nil && len(m.Server.Command) > 0 {
		if st, err := os.Stat(m.Server.Command[0]); err == nil {
			fmt.Fprintln(h, st.ModTime().UnixNano())
		}
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		fmt.Fprintln(h, p)
		_, _ = io.Copy(h, f)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}
