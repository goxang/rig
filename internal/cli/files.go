package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/spec"
)

const maxRead = 256 << 10

// fileTool is rig_file: the project directory's files, and nothing outside it nor under the paths
// kept from assistants (credentials, rig.yaml ai.deny, .git). Deleting asks the user first.
func fileTool(args map[string]any) (string, bool) {
	root, deny, err := projectRoot()
	if err != nil {
		return err.Error(), true
	}
	out, err := fileAction(root, deny, args)
	if err != nil {
		return err.Error(), true
	}
	return out, false
}

func projectRoot() (string, []string, error) {
	file := g.file
	if file == "" {
		f, err := spec.Find(".")
		if err != nil {
			return "", nil, err
		}
		file = f
	}
	file, err := filepath.Abs(file)
	if err != nil {
		return "", nil, err
	}
	var deny []string
	if p, _, err := spec.Load(file, g.env); err == nil && p.AI != nil {
		deny = p.AI.Deny
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(file))
	return root, deny, err
}

func fileAction(root string, deny []string, args map[string]any) (string, error) {
	action := str(args, "action")
	path, rel, err := inProject(root, deny, str(args, "path"))
	if err != nil {
		return "", err
	}
	switch action {
	case "list":
		return listDir(root, deny, path, str(args, "recursive") == "true")
	case "read":
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if len(raw) > maxRead {
			return string(raw[:maxRead]) + fmt.Sprintf("\n… (%d bytes in all; the first %d shown)", len(raw), maxRead), nil
		}
		return string(raw), nil
	case "write":
		if _, ok := args["content"]; !ok {
			return "", errors.New("write needs content")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		mode := fs.FileMode(0o644)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		return "wrote " + rel, os.WriteFile(path, []byte(str(args, "content")), mode)
	case "edit":
		old, repl := str(args, "old"), str(args, "new")
		if old == "" {
			return "", errors.New("edit needs old (the exact text to replace) and new")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		n := strings.Count(string(raw), old)
		switch {
		case n == 0:
			return "", fmt.Errorf("old is not in %s: read it again and copy the text exactly", rel)
		case n > 1 && str(args, "all") != "true":
			return "", fmt.Errorf("old is %d times in %s: give more context, or all: true", n, rel)
		}
		st, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("replaced %d in %s", n, rel), os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), old, repl)), st.Mode().Perm())
	case "move":
		to, toRel, err := inProject(root, deny, str(args, "to"))
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(to); err == nil {
			return "", fmt.Errorf("%s exists: move elsewhere, or delete it first", toRel)
		}
		if kept := keptInside(root, deny, path); kept != "" || path == root {
			return "", fmt.Errorf("refused: %s holds %s, which is kept from assistants", rel, kept)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return "", err
		}
		return "moved " + rel + " to " + toRel, os.Rename(path, to)
	case "delete":
		st, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if path == root {
			return "", errors.New("refused: that is the project itself")
		}
		what := rel
		if st.IsDir() {
			what += "/ and everything in it"
			if kept := keptInside(root, deny, path); kept != "" {
				return "", fmt.Errorf("refused: %s holds %s, which is kept from assistants", rel, kept)
			}
		}
		if err := deleteGoAhead(args, what); err != nil {
			return "", err
		}
		return "deleted " + what, os.RemoveAll(path)
	}
	return "", fmt.Errorf("action is list, read, write, edit, move or delete")
}

// inProject resolves p (relative to the project, or absolute inside it) and refuses anything
// that leaves it, symlinks included, or that is kept from assistants.
func inProject(root string, deny []string, p string) (string, string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	// the deepest part that exists decides where a symlink leads
	real, rest := p, ""
	for {
		if r, err := filepath.EvalSymlinks(real); err == nil {
			real = filepath.Join(r, rest)
			break
		}
		parent := filepath.Dir(real)
		if parent == real {
			break
		}
		rest = filepath.Join(filepath.Base(real), rest)
		real = parent
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("refused: %s is outside the project (%s)", p, root)
	}
	if rel != "." && (ai.DeniedPath(rel, deny) || rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator))) {
		return "", "", fmt.Errorf("refused: %s is kept from assistants (credentials, rig.yaml ai.deny, .git)", rel)
	}
	return real, rel, nil
}

func listDir(root string, deny []string, dir string, recursive bool) (string, error) {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.Name() == ".git" || ai.DeniedPath(rel, deny) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			lines = append(lines, rel+"/")
			if !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			lines = append(lines, fmt.Sprintf("%s  %d", rel, info.Size()))
		}
		if len(lines) > 5000 {
			return errors.New("stop")
		}
		return nil
	})
	if err != nil && err.Error() != "stop" {
		return "", err
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

func keptInside(root string, deny []string, dir string) string {
	kept := ""
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		if err == nil && (d.Name() == ".git" || ai.DeniedPath(rel, deny)) {
			kept = rel
			return filepath.SkipAll
		}
		return nil
	})
	return kept
}

// deleteGoAhead is the user's yes to a delete: quoted in user_request, approved in the rig UI the
// assistant runs in, or, for an agent of its own, confirm: true.
func deleteGoAhead(args map[string]any, what string) error {
	if os.Getenv(ai.EnvLock) == "" {
		if str(args, "confirm") == "true" {
			return nil
		}
		return fmt.Errorf("not deleted: deleting %s needs the user's go-ahead; ask them, then call again with confirm: true", what)
	}
	if ai.Quoted(ai.LastPrompt(os.Getenv(ai.EnvDir)), str(args, "user_request")) {
		return nil
	}
	sock := os.Getenv(ai.EnvSock)
	if sock == "" {
		return fmt.Errorf("not deleted: deleting %s needs the user's go-ahead. Ask them; when they agree, call again with user_request set to their words, verbatim", what)
	}
	r, err := ai.Call(sock, ai.Request{Op: "approve", Text: "AI: delete " + what}, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("not deleted: could not ask the user (%v); ask them in the chat", err)
	}
	if !r.OK {
		return fmt.Errorf("not deleted: the user declined%s", note(r.Text))
	}
	return nil
}
