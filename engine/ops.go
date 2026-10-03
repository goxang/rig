package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

type UpOptions struct {
	Build  bool
	Tag    string
	NoDeps bool
	Wait   time.Duration
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

// Up builds (when asked) and deploys targets layer by layer in dependency order,
// waiting for each layer to be ready before the next starts.
func (a *App) Up(ctx context.Context, targets []string, o UpOptions) error {
	if err := a.Guard(); err != nil {
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
	if o.Tag == "" {
		o.Tag = time.Now().Format("20060102-150405")
	}
	if o.Wait == 0 {
		o.Wait = 3 * time.Minute
	}
	for i, layer := range layers {
		fmt.Fprintf(out, "▸ phase %d/%d: %v\n", i+1, len(layers), layer)
		err := parallel(layer, func(n string) error {
			s := a.Spec.Services[n]
			rel := core.Release{}
			if o.Build && s.Build != nil && a.ImageBased() {
				img, err := a.Build(ctx, s, o.Tag, out)
				if err != nil {
					return err
				}
				rel.Image = img
			} else if s.Build != nil {
				rel.Image = a.DefaultImage(ctx, s)
			}
			if err := a.runtime.Deploy(ctx, s, rel); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		err = parallel(layer, func(n string) error {
			st, err := a.Wait(ctx, a.Spec.Services[n], o.Wait)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "  ✓ %-24s %d/%d ready\n", n, st.Ready, max(st.Desired, 1))
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Down stops targets in reverse dependency order.
func (a *App) Down(ctx context.Context, targets []string, out io.Writer) error {
	if err := a.Guard(); err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	names := a.Spec.ServiceNames()
	if len(targets) > 0 {
		var err error
		if names, err = a.Targets(targets, false); err != nil {
			return err
		}
	}
	layers, err := a.Spec.Order(names)
	if err != nil {
		return err
	}
	for i := len(layers) - 1; i >= 0; i-- {
		err := parallel(layers[i], func(n string) error {
			if err := a.runtime.Stop(ctx, a.Spec.Services[n]); err != nil {
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

// Build builds one service's image with the builder its build section asks for and returns the reference.
func (a *App) Build(ctx context.Context, s *spec.Service, tag string, out io.Writer) (string, error) {
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
	// services sharing one build (same source, same image name) are built once per tag
	key := fmt.Sprintf("%s|%+v|%s|%s", typ, *s.Build, core.ImageRef(s, reg, tag), reg)
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
		once.img, once.err = b.Build(ctx, s, core.BuildOptions{Tag: tag, Registry: reg, Push: reg != "", Out: out})
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
func (a *App) Wait(ctx context.Context, s *spec.Service, timeout time.Duration) (core.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last core.Status
	for {
		st, err := a.runtime.Status(ctx, s)
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
			return last, fmt.Errorf("%s not ready after %s (%s %d/%d) %s", s.Name, timeout, last.State, last.Ready, last.Desired, msg)
		case <-time.After(time.Second):
		}
	}
}

// StatusAll asks the runtime for every named service in parallel; errors become StateUnknown rows.
func (a *App) StatusAll(ctx context.Context, names []string) []core.Status {
	out := make([]core.Status, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := a.runtime.Status(ctx, a.Spec.Services[n])
			if err != nil {
				st = core.Status{State: core.StateUnknown, Message: err.Error()}
			}
			st.Service = n
			out[i] = st
		}()
	}
	wg.Wait()
	return out
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
