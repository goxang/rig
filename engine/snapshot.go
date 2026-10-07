package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/goxang/rig/core"
)

// Snapshot is one saved copy of a data component, in the environment's state dir.
type Snapshot struct {
	Name string
	File string
	Size int64
	At   time.Time
}

var snapshotName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (a *App) snapshotDir(comp string) string { return filepath.Join(a.StateDir(), "snapshots", comp) }

func (a *App) snapshotter(comp string) (core.Snapshotter, error) {
	v, err := a.Component(comp)
	if err != nil {
		return nil, err
	}
	s, ok := v.(core.Snapshotter)
	if !ok {
		if _, cache := v.(core.Cache); cache {
			return nil, fmt.Errorf("%s: cache snapshots are not supported (redis-cli --rdb <file> takes one by hand): %w", comp, core.ErrUnsupported)
		}
		return nil, fmt.Errorf("%s cannot be snapshotted: %w", comp, core.ErrUnsupported)
	}
	return s, nil
}

// Snapshots lists comp's snapshots, newest first.
func (a *App) Snapshots(comp string) ([]Snapshot, error) {
	ents, err := os.ReadDir(a.snapshotDir(comp))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || e.IsDir() || filepath.Ext(e.Name()) != ".dump" {
			continue
		}
		name := e.Name()[:len(e.Name())-len(".dump")]
		out = append(out, Snapshot{Name: name, File: filepath.Join(a.snapshotDir(comp), e.Name()), Size: info.Size(), At: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// TakeSnapshot saves comp's data as name (a timestamp when ""); it changes nothing, so read-only
// environments can take one too.
func (a *App) TakeSnapshot(ctx context.Context, comp, name string, log io.Writer) (Snapshot, error) {
	s, err := a.snapshotter(comp)
	if err != nil {
		return Snapshot{}, err
	}
	if name == "" {
		name = time.Now().Format("20060102-150405")
	}
	if !snapshotName.MatchString(name) {
		return Snapshot{}, fmt.Errorf("snapshot name %q: letters, digits, '.', '_' and '-'", name)
	}
	if err := os.MkdirAll(a.snapshotDir(comp), 0o755); err != nil {
		return Snapshot{}, err
	}
	file := filepath.Join(a.snapshotDir(comp), name+".dump")
	part := file + ".part"
	defer os.Remove(part)
	if err := s.Snapshot(ctx, part, log); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot %s: %w", comp, err)
	}
	if err := os.Rename(part, file); err != nil {
		return Snapshot{}, err
	}
	info, err := os.Stat(file)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Name: name, File: file, Size: info.Size(), At: info.ModTime()}, nil
}

// RestoreSnapshot puts snapshot name back into comp, replacing what it holds.
func (a *App) RestoreSnapshot(ctx context.Context, comp, name string, log io.Writer) error {
	if err := a.Guard(); err != nil {
		return err
	}
	s, err := a.snapshotter(comp)
	if err != nil {
		return err
	}
	file := filepath.Join(a.snapshotDir(comp), name+".dump")
	if !snapshotName.MatchString(name) {
		return fmt.Errorf("snapshot name %q: letters, digits, '.', '_' and '-'", name)
	}
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("no snapshot %s of %s (rig data snapshots %s lists them)", name, comp, comp)
	}
	if err := s.Restore(ctx, file, log); err != nil {
		return fmt.Errorf("restore %s: %w", comp, err)
	}
	return nil
}

// SeedData runs comp's seed.
func (a *App) SeedData(ctx context.Context, comp string, log io.Writer) error {
	if err := a.Guard(); err != nil {
		return err
	}
	v, err := a.Component(comp)
	if err != nil {
		return err
	}
	s, ok := v.(core.Seeder)
	if !ok {
		return fmt.Errorf("%s has no seed: %w", comp, core.ErrUnsupported)
	}
	return s.Seed(ctx, log)
}

// seedFresh seeds each database that runs in one of services and has no tables yet: rig up on a
// fresh database leaves it filled.
func (a *App) seedFresh(ctx context.Context, services map[string]bool, out io.Writer) error {
	for _, n := range a.Names(core.KindDatabase) {
		v, err := a.Component(n)
		if err != nil {
			continue
		}
		s, ok := v.(core.Seeder)
		if !ok || !services[s.Host()] {
			continue
		}
		fresh, err := s.Fresh(ctx)
		if err != nil {
			fmt.Fprintf(out, "  seed %s: cannot tell whether it is empty: %v\n", n, err)
			continue
		}
		if !fresh {
			continue
		}
		fmt.Fprintf(out, "▸ seeding %s, which started empty\n", n)
		if err := s.Seed(ctx, out); err != nil {
			return fmt.Errorf("seed %s: %w", n, err)
		}
	}
	return nil
}
