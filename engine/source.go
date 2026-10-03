package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/goxang/rig/internal/sh"
)

var sourceMu sync.Mutex

// Source exports a git ref of the project's repository into the user cache (rig/src/<commit>, once
// per commit) and returns the directory that corresponds to the project directory inside it.
func (a *App) Source(ctx context.Context, ref string) (string, error) {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	git := func(args ...string) (string, error) {
		c := sh.New("git", args...)
		c.Dir = a.Spec.Dir
		out, err := c.Output(ctx)
		return strings.TrimSpace(string(out)), err
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("--ref needs a git repository: %w", err)
	}
	commit, err := git("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		if _, ferr := git("fetch", "--quiet", "origin", ref); ferr == nil {
			commit, err = git("rev-parse", "--verify", "FETCH_HEAD^{commit}")
		}
		if err != nil {
			return "", fmt.Errorf("unknown git ref %q", ref)
		}
	}
	rel, _ := filepath.Rel(top, a.Spec.Dir)
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	root := filepath.Join(cache, "rig", "src", commit[:12])
	if _, err := os.Stat(filepath.Join(root, ".complete")); err == nil {
		return filepath.Join(root, rel), nil
	}
	_ = os.RemoveAll(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	c := sh.New("sh", "-c", "git archive --format=tar "+commit+" | tar -x -C "+shellQuote(root))
	c.Dir = top
	if err := c.Run(ctx); err != nil {
		return "", fmt.Errorf("export %s: %w", ref, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".complete"), []byte(ref+"\n"), 0o644); err != nil {
		return "", err
	}
	return filepath.Join(root, rel), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// imageExists reports whether the registry already has ref; unreachable registries count as "no".
func imageExists(ctx context.Context, ref string) bool {
	r, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = remote.Head(r, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	return err == nil
}
