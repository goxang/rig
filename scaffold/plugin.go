package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

// The Python and TypeScript SDKs are copied into each plugin, so a new plugin needs no package registry.
//go:generate sh -c "rm -rf plugin/python/rig plugin/ts/rig && mkdir -p plugin/python/rig plugin/ts/rig && cp ../sdk/python/rig/__init__.py ../sdk/python/rig/protocol.py plugin/python/rig/ && cp ../sdk/typescript/index.js ../sdk/typescript/index.d.ts ../sdk/typescript/protocol.js ../sdk/typescript/protocol.d.ts plugin/ts/rig/"

//go:embed all:plugin
var pluginFS embed.FS

// Langs are the plugin templates: a server in Go, Python or TypeScript, scripts, or YAML alone.
var Langs = []string{"go", "python", "ts", "bash", "yaml"}

var pluginName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// NewPlugin writes a plugin named name in lang into dir, which must be empty or missing, and returns
// the files it wrote.
func NewPlugin(dir, name, lang string) ([]string, error) {
	if !pluginName.MatchString(name) {
		return nil, fmt.Errorf("a plugin name is lowercase letters, digits and dashes: %q", name)
	}
	if !slices.Contains(Langs, lang) {
		return nil, fmt.Errorf("--lang is one of %s", strings.Join(Langs, ", "))
	}
	if ents, _ := os.ReadDir(dir); len(ents) > 0 {
		return nil, fmt.Errorf("%s is not empty", dir)
	}
	data := map[string]string{"Name": name, "Lang": lang, "Title": strings.ToUpper(name[:1]) + strings.ReplaceAll(name[1:], "-", " ")}
	var wrote []string
	for _, root := range []string{"plugin/common", "plugin/" + lang} {
		err := fs.WalkDir(pluginFS, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			// .tmpl keeps go.mod and main.go out of rig's own build
			rel := strings.TrimSuffix(strings.ReplaceAll(strings.TrimPrefix(p, root+"/"), "NAME", name), ".tmpl")
			b, err := pluginFS.ReadFile(p)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(rel, "rig/") {
				t, err := template.New(rel).Delims("{%", "%}").Parse(string(b))
				if err != nil {
					return err
				}
				var out bytes.Buffer
				if err := t.Execute(&out, data); err != nil {
					return err
				}
				b = out.Bytes()
			}
			mode := os.FileMode(0o644)
			if ext := path.Ext(rel); ext == ".sh" || rel == "plugin.py" {
				mode = 0o755
			}
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			wrote = append(wrote, rel)
			return os.WriteFile(full, b, mode)
		})
		if err != nil {
			return wrote, err
		}
	}
	return wrote, nil
}
