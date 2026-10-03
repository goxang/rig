// Package runtime serves logs straight from the runtime (kubectl logs, docker logs, local log files),
// merged across services. It is the default log source when none is configured.
package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindLogs, "runtime", "the runtime's own logs (stdout/stderr), merged across services", New)
}

type Logs struct{ env core.Env }

func New(env core.Env, _ *spec.Component) (any, error) { return &Logs{env: env}, nil }

func (l *Logs) Logs(ctx context.Context, q core.LogQuery) (<-chan core.LogLine, error) {
	p := l.env.Project()
	names := q.Services
	if len(names) == 0 {
		names = p.ServiceNames()
	}
	out := make(chan core.LogLine, 512)
	var wg sync.WaitGroup
	var errs []error
	for _, n := range names {
		rt, s, err := l.env.Owner(n)
		if err == nil {
			var ch <-chan core.LogLine
			if ch, err = rt.Logs(ctx, s, core.LogOptions{Follow: q.Follow, Tail: q.Tail, Since: q.Since, Instance: q.Instance}); err == nil {
				l.pump(ctx, &wg, ch, q.Match, out)
				continue
			}
		}
		// one service without logs must not hide the others
		errs = append(errs, err)
	}
	if len(errs) == len(names) && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out, nil
}

func (l *Logs) pump(ctx context.Context, wg *sync.WaitGroup, ch <-chan core.LogLine, match string, out chan<- core.LogLine) {
	{
		wg.Add(1)
		go func() {
			defer wg.Done()
			for line := range ch {
				if match != "" && !strings.Contains(line.Text, match) {
					continue
				}
				select {
				case out <- line:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
}
