// Package runtime serves logs straight from the runtime (kubectl logs, docker logs, local log files),
// merged across services. It is the default log source when none is configured.
package runtime

import (
	"context"
	"strings"
	"sync"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
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
	for _, n := range names {
		s, ok := p.Services[n]
		if !ok {
			continue
		}
		ch, err := l.env.Runtime().Logs(ctx, s, core.LogOptions{Follow: q.Follow, Tail: q.Tail, Since: q.Since})
		if err != nil {
			continue // one service without logs must not hide the others
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for line := range ch {
				if q.Match != "" && !strings.Contains(line.Text, q.Match) {
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
	go func() {
		wg.Wait()
		close(out)
	}()
	return out, nil
}
