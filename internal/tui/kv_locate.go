package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/goxang/rig/internal/sh"
)

// kvSourceFiles are the files a kv component names in files: (globs from the project directory):
// the config files its keys are loaded from.
func kvSourceFiles(m *model, comp string) []string {
	c, ok := m.app.Spec.Components[comp]
	if !ok {
		return nil
	}
	var opt struct {
		Files []string `yaml:"files"`
	}
	if c.Node.Decode(&opt) != nil {
		return nil
	}
	var out []string
	for _, g := range opt.Files {
		if !filepath.IsAbs(g) {
			g = filepath.Join(m.app.Spec.Dir, g)
		}
		matches, _ := filepath.Glob(g)
		out = append(out, matches...)
	}
	sort.Strings(out)
	return out
}

var lastField = regexp.MustCompile(`([^.\[\]$]+)(\[\d+\])*$`)

// locateKey picks the files that hold key (and, given, the field at path $.a.b): a file named after
// the key's last segment that has the field, else any file that has the field, else the named one.
// line is where the field's name first appears in the first file (1 when unknown).
func locateKey(files []string, key, path string) (found []string, line int) {
	name := key[strings.LastIndex(key, "/")+1:]
	var named, withPath []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		has := path == "" || jsonHasPath(raw, path)
		if strings.EqualFold(base, name) {
			named = append(named, f)
			if has {
				return []string{f}, fieldLine(raw, path)
			}
		}
		if has && path != "" {
			withPath = append(withPath, f)
		}
	}
	found = withPath
	if len(found) == 0 {
		found = named
	}
	if len(found) == 1 {
		raw, _ := os.ReadFile(found[0])
		line = fieldLine(raw, path)
	}
	return found, max(line, 1)
}

func jsonHasPath(raw []byte, path string) bool {
	root, err := parseJSON(bytes.TrimSpace(raw))
	if err != nil {
		return false
	}
	has := false
	walkNodes(root, func(n *jnode) bool {
		if n.path() == path {
			has = true
		}
		return !has
	})
	return has
}

func fieldLine(raw []byte, path string) int {
	m := lastField.FindStringSubmatch(path)
	if m == nil {
		return 1
	}
	if i := bytes.Index(raw, []byte(`"`+m[1]+`"`)); i >= 0 {
		return bytes.Count(raw[:i], []byte("\n")) + 1
	}
	return 1
}

// openSource opens the config file behind the selected key or field, asking first when several
// files fit.
func (t *kvTab) openSource(m *model, key, path string) tea.Cmd {
	files := kvSourceFiles(m, t.comp)
	if len(files) == 0 {
		m.setStatus("no source files: give the "+t.comp+" component files: [globs] in rig.yaml", true)
		return nil
	}
	found, line := locateKey(files, key, path)
	rel := func(f string) string { return relTo(m.app.Spec.Dir, f) }
	switch len(found) {
	case 0:
		m.setStatus("no config file holds "+key+" "+strings.TrimPrefix(path, "$."), true)
		return nil
	case 1:
		m.setStatus(rel(found[0])+":"+strconv.Itoa(line), false)
		return openFileAt(found[0], line)
	}
	items := make([]string, len(found))
	for i, f := range found {
		items[i] = rel(f)
	}
	m.pick("config files holding "+strings.TrimPrefix(path, "$."), items, nil, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		f := filepath.Join(m.app.Spec.Dir, c[0])
		raw, _ := os.ReadFile(f)
		return openFileAt(f, fieldLine(raw, path))
	})
	return nil
}

// openFileAt opens file in the editor, at line for editors that take +N.
func openFileAt(file string, line int) tea.Cmd {
	ed := editorCmd()
	arg := ""
	switch filepath.Base(strings.Fields(ed + " x")[0]) {
	case "vi", "vim", "nvim", "nano", "emacs", "micro", "hx", "kak":
		arg = " +" + strconv.Itoa(line)
	case "code", "cursor", "codium":
		file += ":" + strconv.Itoa(line)
		arg = " -g"
	}
	c := exec.Command(sh.Shell(), "-c", ed+arg+` "$1"`, "rig-edit", file)
	return execProcess(c, func(error) tea.Msg { return nil })
}
