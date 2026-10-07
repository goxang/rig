package engine

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/spec"
)

// WatchEvent is a step of a live rebuild: State is changed, building, restarting, ok or failed.
type WatchEvent struct {
	Service string
	State   string
	// Output is the build's output, on failed.
	Output string
	Err    error
	Took   time.Duration
}

var defaultIgnore = []string{"*_test.go", "*.md", ".git", ".rig", "node_modules", "*.swp", "*~", ".#*"}

// watchPoll and watchQuiet: how often sources are looked at, and how long they must stay unchanged
// after a change before the rebuild starts (an editor's save, a git checkout, come in bursts).
var watchPoll, watchQuiet = 700 * time.Millisecond, 500 * time.Millisecond

// Watch rebuilds and restarts each named service (every app service with a build or run, when none)
// when its sources change, until ctx ends. Events go to emit as they happen. A failed build leaves
// the running service as it was.
// ponytail: polls file mtimes (no fsnotify in the module); fine for a service's packages, not a monorepo.
func (a *App) Watch(ctx context.Context, names []string, emit func(WatchEvent)) error {
	if err := a.Guard(); err != nil {
		return err
	}
	if len(names) == 0 {
		for _, n := range a.Spec.ServiceNames() {
			s := a.Spec.Services[n]
			if s.Role == spec.RoleApp && !a.SharedElsewhere(n) && (s.Build != nil || s.Run != nil || s.Watch != nil) {
				names = append(names, n)
			}
		}
	}
	type watched struct {
		paths, ignore []string
		sum           uint64
		changed       time.Time
	}
	ws := map[string]*watched{}
	for _, n := range names {
		s, err := a.Service(n)
		if err != nil {
			return err
		}
		paths := a.watchPaths(ctx, s)
		if len(paths) == 0 {
			return fmt.Errorf("%s: nothing to watch: give it watch.paths", n)
		}
		w := &watched{paths: paths, ignore: defaultIgnore}
		if s.Watch != nil {
			w.ignore = slices.Concat(defaultIgnore, s.Watch.Ignore)
		}
		w.sum = a.sourceSum(ctx, w.paths, w.ignore)
		ws[n] = w
	}
	tick := time.NewTicker(watchPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		for _, n := range names {
			w := ws[n]
			if sum := a.sourceSum(ctx, w.paths, w.ignore); sum != w.sum {
				if w.changed.IsZero() {
					emit(WatchEvent{Service: n, State: "changed"})
				}
				w.sum, w.changed = sum, time.Now()
				continue
			}
			if w.changed.IsZero() || time.Since(w.changed) < watchQuiet {
				continue
			}
			w.changed = time.Time{}
			a.Rebuild(ctx, n, emit)
		}
	}
}

// Rebuild builds a service with its builder and restarts it through its runtime: a fresh image
// rolled out where the runtime runs images, a restart (after a compile check, for Go) for local
// processes. A build that fails leaves the service running what it ran.
func (a *App) Rebuild(ctx context.Context, name string, emit func(WatchEvent)) {
	start := time.Now()
	rt, s, err := a.changing(name)
	if err != nil {
		emit(WatchEvent{Service: name, State: "failed", Err: err})
		return
	}
	emit(WatchEvent{Service: name, State: "building"})
	var out bytes.Buffer
	rel := core.Release{}
	switch {
	case a.ImageBased() && s.Build != nil:
		img, err := a.BuildFrom(core.WithConfirmed(ctx), s, "watch-"+time.Now().Format("20060102-150405.000"), "", &out)
		if err != nil {
			emit(WatchEvent{Service: name, State: "failed", Err: err, Output: out.String()})
			return
		}
		rel.Image = img
	case s.Build != nil && s.Build.Go != "":
		// the local runtime builds again on start (from the cache, at once): this only proves it compiles
		b := sh.New("go", "build", "-o", os.DevNull, s.Build.Go)
		b.Dir = a.Spec.Dir
		if err := b.Attach(ctx, nil, &out, &out); err != nil {
			emit(WatchEvent{Service: name, State: "failed", Err: err, Output: out.String()})
			return
		}
	}
	emit(WatchEvent{Service: name, State: "restarting"})
	if rel.Image != "" {
		err = rt.Deploy(ctx, s, rel)
	} else {
		err = rt.Restart(ctx, s)
	}
	if err != nil {
		emit(WatchEvent{Service: name, State: "failed", Err: err})
		return
	}
	emit(WatchEvent{Service: name, State: "ok", Took: time.Since(start)})
}

// watchPaths are a service's watch.paths, else what its build or run reads from the project.
func (a *App) watchPaths(ctx context.Context, s *spec.Service) []string {
	if s.Watch != nil && len(s.Watch.Paths) > 0 {
		return s.Watch.Paths
	}
	if s.Build != nil && s.Build.Go != "" {
		// the project's own packages the main imports, as go list sees them
		l := sh.New("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", s.Build.Go)
		l.Dir = a.Spec.Dir
		raw, err := l.Output(ctx)
		if err == nil {
			var out []string
			for _, d := range strings.Fields(string(raw)) {
				if rel, err := filepath.Rel(a.Spec.Dir, d); err == nil && !strings.HasPrefix(rel, "..") {
					out = append(out, rel)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		return []string{s.Build.Go}
	}
	if s.Build != nil && s.Build.Context != "" {
		return []string{s.Build.Context}
	}
	if s.Build != nil && s.Build.Dockerfile != "" {
		return []string{filepath.Dir(s.Build.Dockerfile)}
	}
	if s.Run != nil && s.Run.Dir != "" {
		return []string{s.Run.Dir}
	}
	if s.Run != nil {
		return []string{"."}
	}
	return nil
}

// sourceSum fingerprints the files under paths (their names, sizes and times) that git does not
// ignore and that match no ignore glob.
func (a *App) sourceSum(ctx context.Context, paths, ignore []string) uint64 {
	h := fnv.New64a()
	for _, f := range a.sourceFiles(ctx, paths) {
		if ignored(f, ignore) {
			continue
		}
		fi, err := os.Stat(filepath.Join(a.Spec.Dir, f))
		if err != nil || fi.IsDir() {
			continue
		}
		fmt.Fprintf(h, "%s %d %d\n", f, fi.Size(), fi.ModTime().UnixNano())
	}
	return h.Sum64()
}

// sourceFiles lists the files under paths: git's tracked and untracked-but-not-ignored ones in a
// repository (so .gitignore holds), else every file walked.
func (a *App) sourceFiles(ctx context.Context, paths []string) []string {
	git := sh.New("git", append([]string{"ls-files", "-co", "--exclude-standard", "--"}, paths...)...)
	git.Dir = a.Spec.Dir
	if raw, err := git.Output(ctx); err == nil {
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	var out []string
	for _, p := range paths {
		_ = filepath.WalkDir(filepath.Join(a.Spec.Dir, p), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(a.Spec.Dir, path)
			if d.IsDir() && ignored(rel, defaultIgnore) {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				out = append(out, rel)
			}
			return nil
		})
	}
	return out
}

// ignored matches a glob against the file's name and each of its path's directories; a glob with a
// slash, or ending in /**, against the path itself.
func ignored(file string, globs []string) bool {
	file = filepath.ToSlash(file)
	parts := strings.Split(file, "/")
	for _, g := range globs {
		if dir, ok := strings.CutSuffix(g, "/**"); ok && (file == dir || strings.HasPrefix(file, dir+"/")) {
			return true
		}
		if strings.Contains(g, "/") {
			if ok, _ := filepath.Match(g, file); ok {
				return true
			}
			continue
		}
		for _, p := range parts {
			if ok, _ := filepath.Match(g, p); ok {
				return true
			}
		}
	}
	return false
}
