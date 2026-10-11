package tui

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/internal/sh"
)

const maxHits = 500

// fileHit is a file whose name matched, or a line of one whose content did (line 0: the name).
type fileHit struct {
	path string
	line int
	text string
}

// fileIndex is the project's file list, read once and kept for a while: names are searched in
// memory, contents by git grep (which reads its own index) or, outside git, a parallel scan.
type fileIndex struct {
	mu    sync.Mutex
	root  string
	files []string
	at    time.Time
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".rig": true, "dist": true, "__pycache__": true}

func (ix *fileIndex) list(ctx context.Context) []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if time.Since(ix.at) < 30*time.Second && ix.files != nil {
		return ix.files
	}
	var files []string
	if out, err := (&sh.Cmd{Name: "git", Args: []string{"ls-files", "-co", "--exclude-standard", "-z"}, Dir: ix.root}).Output(ctx); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	} else {
		_ = filepath.WalkDir(ix.root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return nil
			case d.IsDir() && skipDirs[d.Name()]:
				return filepath.SkipDir
			case !d.IsDir():
				rel, _ := filepath.Rel(ix.root, p)
				files = append(files, rel)
			}
			return nil
		})
	}
	ix.files, ix.at = files, time.Now()
	return files
}

func (ix *fileIndex) forget() { ix.mu.Lock(); ix.at = time.Time{}; ix.mu.Unlock() }

// searchNames keeps the files whose path has every word of q in order.
func searchNames(files []string, q string) []fileHit {
	var hits []fileHit
	for _, f := range files {
		if fuzzy(f, q) {
			if hits = append(hits, fileHit{path: f}); len(hits) >= maxHits {
				break
			}
		}
	}
	return hits
}

// searchContent finds q (case-insensitive, literal) inside the files.
func (ix *fileIndex) searchContent(ctx context.Context, q string) []fileHit {
	if _, err := os.Stat(filepath.Join(ix.root, ".git")); err == nil && sh.Have("git") {
		out, err := (&sh.Cmd{Name: "git", Args: []string{"grep", "-I", "-n", "-i", "-F", "--untracked", "-m", "5", "-e", q}, Dir: ix.root}).Output(ctx)
		if err == nil || len(out) > 0 {
			var hits []fileHit
			for _, l := range strings.Split(string(out), "\n") {
				p, rest, ok := strings.Cut(l, ":")
				n, text, ok2 := strings.Cut(rest, ":")
				line, _ := strconv.Atoi(n)
				if ok && ok2 && line > 0 {
					if hits = append(hits, fileHit{p, line, strings.TrimSpace(text)}); len(hits) >= maxHits {
						break
					}
				}
			}
			return hits
		}
	}
	return scanContent(ctx, ix.root, ix.list(ctx), q)
}

// scanContent reads files in parallel (up to 1 MB each, binaries skipped) for the text outside git.
func scanContent(ctx context.Context, root string, files []string, q string) []fileHit {
	needle := bytes.ToLower([]byte(q))
	var (
		mu   sync.Mutex
		hits []fileHit
		wg   sync.WaitGroup
		jobs = make(chan string)
	)
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				raw, err := os.ReadFile(filepath.Join(root, f))
				if err != nil || len(raw) > 1<<20 || bytes.IndexByte(raw[:min(len(raw), 8000)], 0) >= 0 {
					continue
				}
				for i, l := range bytes.Split(raw, []byte("\n")) {
					if bytes.Contains(bytes.ToLower(l), needle) {
						mu.Lock()
						hits = append(hits, fileHit{f, i + 1, strings.TrimSpace(string(l))})
						mu.Unlock()
						break
					}
				}
			}
		}()
	}
	for _, f := range files {
		if ctx.Err() != nil || func() bool { mu.Lock(); defer mu.Unlock(); return len(hits) >= maxHits }() {
			break
		}
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	return hits
}
