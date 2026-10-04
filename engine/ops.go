package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

type UpOptions struct {
	Build bool
	// Tag names the images: what builds produce, or without Build, the tag to deploy from the registry.
	Tag string
	// Ref builds from a git ref (branch, tag, commit) instead of the working tree.
	Ref    string
	NoDeps bool
	Wait   time.Duration
	// Settle is how long up watches what it deployed after the last phase: a service that turned
	// ready and then crashed (a missing config key, say) fails the up instead of passing silently.
	Settle time.Duration
	// DryRun prints the phases and what each service would get, changing nothing.
	DryRun bool
	Out    io.Writer
}

// Targets expands names, groups and roles; unknown targets are an error.
func (a *App) Targets(targets []string, deps bool) ([]string, error) {
	if bad := a.Spec.Unknown(targets); len(bad) > 0 {
		return nil, fmt.Errorf("unknown service or group: %v", bad)
	}
	names := a.Spec.Select(targets)
	if deps {
		names = a.Spec.WithDeps(names)
	}
	return names, nil
}

// ImageBased reports whether the runtime runs images (and so needs builds) rather than local processes.
func (a *App) ImageBased() bool {
	_, reg := a.runtime.(core.Registrar)
	_, load := a.runtime.(core.ImageLoader)
	return reg || load
}

// infraLike is what up leaves alone once it runs, unless named: infrastructure changes rarely.
func (a *App) infraLike(n string) bool {
	return a.Spec.Services[n].Role == spec.RoleInfra || a.SharedElsewhere(n)
}

// Up builds (when asked) and deploys targets layer by layer in dependency order, waiting for each
// layer to be ready before the next starts. Dependencies pulled in by the targets, and infrastructure
// when no target is given, are only started when not already running, never redeployed.
func (a *App) Up(ctx context.Context, targets []string, o UpOptions) error {
	if err := a.Guard(); err != nil && !o.DryRun {
		return err
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	names, err := a.Targets(targets, !o.NoDeps)
	if err != nil {
		return err
	}
	layers, err := a.Spec.Order(names)
	if err != nil {
		return err
	}
	named := map[string]bool{}
	if len(targets) > 0 {
		own, err := a.Targets(targets, false)
		if err != nil {
			return err
		}
		for _, n := range own {
			named[n] = true
		}
	}
	leaveRunning := func(n string) bool {
		// running infrastructure is redeployed only when named itself, never through a group
		if a.infraLike(n) {
			_, direct := a.Spec.Services[n]
			return !direct || !slices.Contains(targets, n)
		}
		return len(targets) > 0 && !named[n]
	}
	if o.Tag == "" && o.Build {
		o.Tag = a.DefaultTag(ctx, o.Ref)
	}
	if o.Wait == 0 {
		o.Wait = 3 * time.Minute
	}
	deployed := map[string]int{} // service → restarts when it turned ready
	var dmu sync.Mutex
	if o.DryRun {
		return a.plan(ctx, layers, leaveRunning, o, out)
	}
	for i, layer := range layers {
		fmt.Fprintf(out, "▸ phase %d/%d: %v\n", i+1, len(layers), layer)
		// parked services exist but were scaled to 0 on purpose; a redeploy keeps them there
		skipped, parked := map[string]bool{}, map[string]bool{}
		var mu sync.Mutex
		err := parallel(layer, func(n string) error {
			rt, s, err := a.Owner(n)
			if err != nil {
				return err
			}
			st, stErr := rt.Status(ctx, s)
			if leaveRunning(n) && stErr == nil && Ready(st) {
				mu.Lock()
				skipped[n] = true
				mu.Unlock()
				return a.bridge(ctx, n)
			}
			if stErr == nil && st.State == core.StateStopped && st.Desired == 0 && st.Image != "" {
				mu.Lock()
				parked[n] = true
				mu.Unlock()
			}
			if s.Delay > 0 {
				fmt.Fprintf(out, "  … %s starts in %s\n", n, s.Delay)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(s.Delay):
				}
			}
			rel := core.Release{}
			switch {
			case o.Build && s.Build != nil && a.ImageBased() && !a.SharedElsewhere(n):
				img, err := a.BuildFrom(ctx, s, o.Tag, o.Ref, out)
				if err != nil {
					return err
				}
				rel.Image = img
			case o.Tag != "" && s.Build != nil && a.ImageBased() && !a.SharedElsewhere(n):
				rel.Image = a.TagImage(s, o.Tag)
			case s.Build != nil:
				rel.Image = a.DefaultImage(ctx, s)
			}
			if err := rt.Deploy(ctx, s, rel); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			return a.bridge(ctx, n)
		})
		if err != nil {
			return err
		}
		err = parallel(layer, func(n string) error {
			if skipped[n] {
				fmt.Fprintf(out, "  = %-24s already running\n", n)
				return nil
			}
			if parked[n] {
				if st, err := a.Status(ctx, n); err == nil && st.Desired == 0 {
					fmt.Fprintf(out, "  ○ %-24s scaled to 0 before, left at 0\n", n)
					return nil
				}
			}
			if s := a.Spec.Services[n]; s != nil && s.Replicas != nil && *s.Replicas == 0 {
				fmt.Fprintf(out, "  ○ %-24s deployed at 0 replicas\n", n)
				return nil
			}
			st, err := a.Wait(ctx, n, o.Wait)
			if err != nil {
				return err
			}
			dmu.Lock()
			deployed[n] = restartsOf(st)
			dmu.Unlock()
			fmt.Fprintf(out, "  ✓ %-24s %d/%d ready\n", n, st.Ready, max(st.Desired, 1))
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := a.settle(ctx, deployed, o.Settle, out); err != nil {
		return err
	}
	if o.Tag != "" && !o.Build {
		return a.SetState(ctx, map[string]string{"tag": o.Tag})
	}
	return nil
}

// plan is what Up would do, from the services' current state.
func (a *App) plan(ctx context.Context, layers [][]string, leaveRunning func(string) bool, o UpOptions, out io.Writer) error {
	for i, layer := range layers {
		fmt.Fprintf(out, "▸ phase %d/%d\n", i+1, len(layers))
		for _, st := range a.StatusAll(ctx, layer) {
			n, s := st.Service, a.Spec.Services[st.Service]
			what := "deploy"
			switch {
			case leaveRunning(n) && Ready(st):
				what = "leave running"
			case o.Build && s.Build != nil && a.ImageBased() && !a.SharedElsewhere(n):
				what = "build and deploy"
			case o.Tag != "" && s.Build != nil && a.ImageBased() && !a.SharedElsewhere(n):
				what = "deploy " + a.TagImage(s, o.Tag)
			}
			if a.SharedElsewhere(n) {
				what += " (in " + a.Env.Infra + ")"
			}
			if s.Delay > 0 && what != "leave running" {
				what = fmt.Sprintf("wait %s, then %s", s.Delay, what)
			}
			fmt.Fprintf(out, "  %-40s %-9s %s\n", n, st.State, what)
		}
	}
	return nil
}

func restartsOf(st core.Status) int {
	n := 0
	for _, in := range st.Instances {
		n += in.Restarts
	}
	return n
}

// settle waits d and fails when a deployed service is no longer ready or restarted meanwhile.
func (a *App) settle(ctx context.Context, deployed map[string]int, d time.Duration, out io.Writer) error {
	if d <= 0 || len(deployed) == 0 {
		return nil
	}
	fmt.Fprintf(out, "▸ watching for %s that nothing crashes\n", d)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
	}
	names := SortedKeys(deployed)
	var bad []string
	for _, st := range a.StatusAll(ctx, names) {
		if r := restartsOf(st); !Ready(st) || r > deployed[st.Service] {
			bad = append(bad, fmt.Sprintf("%s (%s %d/%d, %d restarts) %s", st.Service, st.State, st.Ready, st.Desired, r, st.Message))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("ready, then failing: %s; see rig logs <service>", strings.Join(bad, "; "))
	}
	fmt.Fprintln(out, "  ✓ stable")
	return nil
}

// Down stops targets in reverse dependency order. With no targets it stops every app and load service:
// infrastructure, and services shared from another environment, stop only when named.
func (a *App) Down(ctx context.Context, targets []string, out io.Writer) error {
	if err := a.Guard(); err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	var names []string
	if len(targets) > 0 {
		var err error
		if names, err = a.Targets(targets, false); err != nil {
			return err
		}
	} else {
		for _, n := range a.Spec.ServiceNames() {
			if !a.infraLike(n) {
				names = append(names, n)
			}
		}
	}
	layers, err := a.Spec.Order(names)
	if err != nil {
		return err
	}
	for i := len(layers) - 1; i >= 0; i-- {
		err := parallel(layers[i], func(n string) error {
			if err := a.Stop(ctx, n); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			fmt.Fprintf(out, "  ■ %s stopped\n", n)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Build builds one service's image from the working tree and returns the reference.
func (a *App) Build(ctx context.Context, s *spec.Service, tag string, out io.Writer) (string, error) {
	return a.BuildFrom(ctx, s, tag, "", out)
}

// BuildFrom builds one service's image with the builder its build section asks for, from a git ref
// when given, and returns the reference. Pushing over a tag the registry already has needs confirmation.
func (a *App) BuildFrom(ctx context.Context, s *spec.Service, tag, ref string, out io.Writer) (string, error) {
	if s.Build == nil {
		return s.Image, nil
	}
	typ := "command"
	switch {
	case s.Build.Go != "":
		typ = "go"
	case s.Build.Dockerfile != "":
		typ = "docker"
	}
	b, err := a.builder(typ)
	if err != nil {
		return "", err
	}
	reg := ""
	if r, ok := a.runtime.(core.Registrar); ok {
		reg = r.Registry()
	}
	if out == nil {
		out = io.Discard
	}
	dir := ""
	if ref != "" {
		if dir, err = a.Source(ctx, ref); err != nil {
			return "", err
		}
	}
	// services sharing one build (same source, same image name) are built once per tag
	key := fmt.Sprintf("%s|%+v|%s|%s|%s", typ, *s.Build, core.ImageRef(s, reg, tag), reg, ref)
	a.mu.Lock()
	once, ok := a.builds[key]
	if !ok {
		once = &buildOnce{}
		if a.builds == nil {
			a.builds = map[string]*buildOnce{}
		}
		a.builds[key] = once
	}
	a.mu.Unlock()
	once.Do(func() {
		buildSlots <- struct{}{}
		defer func() { <-buildSlots }()
		if reg != "" && !core.Confirmed(ctx) && imageExists(ctx, core.ImageRef(s, reg, tag)) {
			once.err = fmt.Errorf("%s: %w", core.ImageRef(s, reg, tag), core.ErrTagExists)
			return
		}
		once.img, once.err = b.Build(ctx, s, core.BuildOptions{Tag: tag, Registry: reg, Push: reg != "", Out: out, Dir: dir})
	})
	img, err := once.img, once.err
	if err != nil {
		return "", fmt.Errorf("build %s: %w", s.Name, err)
	}
	if l, ok := a.runtime.(core.ImageLoader); ok && reg == "" {
		if err := l.LoadImage(ctx, img); err != nil {
			return "", err
		}
	}
	if err := a.SetState(ctx, map[string]string{"image." + s.Name: img, "tag": tag}); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "  ⚒ %-24s %s\n", s.Name, img)
	return img, nil
}

// buildSlots caps concurrent builds: a Go build of a large service takes gigabytes of memory.
var buildSlots = make(chan struct{}, buildJobs())

func buildJobs() int {
	if n, err := strconv.Atoi(os.Getenv("RIG_BUILD_JOBS")); err == nil && n > 0 {
		return n
	}
	return 2
}

type buildOnce struct {
	sync.Once
	img string
	err error
}

func (a *App) builder(typ string) (core.Builder, error) {
	for _, n := range a.Names(core.KindBuilder) {
		if _, t, _ := a.Kind(n); t == typ {
			b, _, err := Get[core.Builder](a, core.KindBuilder, n)
			return b, err
		}
	}
	v, err := a.defaultComponent(core.KindBuilder, typ)
	if err != nil {
		return nil, err
	}
	return v.(core.Builder), nil
}

// Ready is the one readiness rule every front end uses.
func Ready(st core.Status) bool {
	return st.State == core.StateRunning && st.Ready >= max(st.Desired, 1)
}

// Wait polls the service until it is ready or timeout passes.
func (a *App) Wait(ctx context.Context, name string, timeout time.Duration) (core.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last core.Status
	for {
		st, err := a.Status(ctx, name)
		if err == nil {
			last = st
			if Ready(st) {
				return st, nil
			}
		}
		select {
		case <-ctx.Done():
			msg := last.Message
			if msg == "" && err != nil {
				msg = err.Error()
			}
			return last, fmt.Errorf("%s not ready after %s (%s %d/%d) %s", name, timeout, last.State, last.Ready, last.Desired, msg)
		case <-time.After(time.Second):
		}
	}
}

// ---- load generators ----

type LoadLimits struct {
	Max  float64 `yaml:"max"`
	Step float64 `yaml:"step"`
}

func (a *App) LoadLimits(name string) LoadLimits {
	l := LoadLimits{}
	if c, ok := a.Spec.Components[name]; ok {
		_ = c.Decode(&l)
	}
	if l.Step <= 0 {
		l.Step = 10
	}
	return l
}

var ErrOverMax = errors.New("rate above the generator's max (pass force to exceed it)")

// SetRate sets a generator's rate within its max, unless force, and records it in the run state.
func (a *App) SetRate(ctx context.Context, name string, rps float64, force bool) error {
	if err := a.Guard(); err != nil {
		return err
	}
	g, _, err := Get[core.LoadGenerator](a, core.KindLoad, name)
	if err != nil {
		return err
	}
	if rps < 0 {
		rps = 0
	}
	if l := a.LoadLimits(name); l.Max > 0 && rps > l.Max && !force {
		return fmt.Errorf("%s: %.0f/s: %w (max %.0f)", name, rps, ErrOverMax, l.Max)
	}
	if err := g.SetRate(ctx, rps); err != nil {
		return err
	}
	return a.SetState(ctx, map[string]string{"load." + name + ".rate": strconv.FormatFloat(rps, 'f', -1, 64)})
}

// Nudge moves a generator's rate one step up (dir > 0) or down and returns the new rate.
func (a *App) Nudge(ctx context.Context, name string, dir int) (float64, error) {
	g, _, err := Get[core.LoadGenerator](a, core.KindLoad, name)
	if err != nil {
		return 0, err
	}
	st, err := g.Status(ctx)
	if err != nil {
		return 0, err
	}
	l := a.LoadLimits(name)
	next := st.Rate + float64(dir)*l.Step
	if l.Max > 0 && next > l.Max {
		next = l.Max
	}
	return max(next, 0), a.SetRate(ctx, name, max(next, 0), false)
}

func parallel(names []string, f func(string) error) error {
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f(n)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// SortedKeys is a small helper front ends use for stable output.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
