package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/host"
	"github.com/goxang/rig/spec"
)

func purgeCommand() *cobra.Command {
	var days, maxLog int
	var dry bool
	c := &cobra.Command{
		Use:   "purge",
		Short: "free space: saved sessions, reports, logs, profiles, builds and backups older than --days, and plugin versions no project uses",
		Long: `Deletes what rig wrote and no longer needs, untouched for --days (default 30):
saved sessions (not the pinned one), reports, AI and test runs of every project rig has opened;
this project's .rig/<env> logs, profiles and built binaries (rebuilt on the next run);
backups, keeping the newest of each kind; the plugin description cache; and installed plugin
versions no project's or your own rig.lock pins. Secrets, config and locks are never touched.
A service log above --max-log MB is emptied whatever its age, since a running service keeps filling it.
Disk full? rig purge --days 0 drops every log and built binary of this project at once.`,
		Example: "  rig purge --dry-run      # list it first\n  rig purge --days 7",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := &purger{cutoff: time.Now().AddDate(0, 0, -days), dry: dry}
			if root, err := spec.DataDir("/"); err == nil {
				p.projects(filepath.Dir(root))
			}
			dir := projectDir()
			envs, _ := os.ReadDir(filepath.Join(dir, ".rig"))
			for _, e := range envs {
				if !e.IsDir() {
					continue
				}
				d := filepath.Join(dir, ".rig", e.Name())
				p.truncateBig(filepath.Join(d, "logs"), int64(maxLog)<<20)
				for _, sub := range []string{"logs", "profiles", "bin"} {
					p.old(filepath.Join(d, sub))
				}
				p.backups(d, e.Name() == "cluster-backups")
			}
			if cache, err := os.UserCacheDir(); err == nil {
				p.old(filepath.Join(cache, "rig", "provides"))
			}
			verb := "freed"
			if dry {
				verb = "would free"
			}
			if !dry {
				removed, err := host.GC()
				for _, r := range removed {
					fmt.Println("  - plugin " + r)
				}
				if err != nil {
					return err
				}
			}
			fmt.Printf("  %s %s in %d items (older than %d days)\n", verb, bytesText(p.freed), p.n, days)
			return nil
		},
	}
	c.Flags().IntVar(&days, "days", 30, "only what has not changed in this many days")
	c.Flags().IntVar(&maxLog, "max-log", 64, "empty any service log above this many MB, even a running service's (it keeps writing to the same file)")
	c.Flags().BoolVar(&dry, "dry-run", false, "list what would go, delete nothing")
	return c
}

type purger struct {
	cutoff time.Time
	dry    bool
	freed  int64
	n      int
}

// projects purges every project's saved state, then drops the project folders left empty.
func (p *purger) projects(root string) {
	ds, _ := os.ReadDir(root)
	for _, d := range ds {
		dir := filepath.Join(root, d.Name())
		sessions := filepath.Join(dir, "sessions")
		pinned, _ := os.ReadFile(filepath.Join(sessions, "pinned"))
		p.old(sessions, "pinned", strings.TrimSpace(string(pinned)))
		for _, sub := range []string{"reports", "ai", "tests"} {
			p.old(filepath.Join(dir, sub))
		}
		if !p.dry {
			for _, sub := range []string{"sessions", "reports", "ai", "tests", ""} {
				_ = os.Remove(filepath.Join(dir, sub))
			}
		}
	}
}

// truncateBig empties the files in dir above limit bytes: removing one a running service has open would free nothing.
func (p *purger) truncateBig(dir string, limit int64) {
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		path := filepath.Join(dir, e.Name())
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() <= limit {
			continue
		}
		p.freed += st.Size()
		p.n++
		fmt.Printf("  - %s emptied %s\n", tildePath(path), dim(bytesText(st.Size())))
		if !p.dry {
			_ = os.Truncate(path, 0)
		}
	}
}

// old removes dir's entries not changed since the cutoff, except those named in keep.
func (p *purger) old(dir string, keep ...string) {
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if !slices.Contains(keep, e.Name()) {
			p.removeIfOld(filepath.Join(dir, e.Name()))
		}
	}
}

// backups removes old *backup* files (all of dir's entries when all is set), keeping the newest:
// a backup folder's own entries count as backups.
func (p *purger) backups(dir string, all bool) {
	es, _ := os.ReadDir(dir)
	var files []string
	for _, e := range es {
		if !all && !strings.Contains(e.Name(), "backup") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			p.backups(path, true)
			continue
		}
		files = append(files, path)
	}
	slices.SortFunc(files, func(a, b string) int { return newest(b).Compare(newest(a)) })
	for _, f := range files[min(1, len(files)):] {
		p.removeIfOld(f)
	}
}

func (p *purger) removeIfOld(path string) {
	if newest(path).After(p.cutoff) {
		return
	}
	size := sizeOf(path)
	p.freed += size
	p.n++
	fmt.Printf("  - %s %s\n", tildePath(path), dim(bytesText(size)))
	if !p.dry {
		_ = os.RemoveAll(path)
	}
}

// newest is the latest change of a file under path (or of path itself when it holds none): a
// session folder written to yesterday stays.
func newest(path string) time.Time {
	var t time.Time
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil && info.ModTime().After(t) {
				t = info.ModTime()
			}
		}
		return nil
	})
	if info, err := os.Stat(path); t.IsZero() && err == nil {
		t = info.ModTime()
	}
	return t
}

func sizeOf(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return p
}
