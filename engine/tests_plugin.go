package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/spec"
)

// runPluginTests streams the suite's runner plugin's tests.events, each line a go test -json event.
func (a *App) runPluginTests(ctx context.Context, s *spec.TestSuite, run *TestRun, o TestOptions, dir string, env []string) error {
	vars := map[string]any{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	source := s.Runner + ".tests.events"
	run.mu.Lock()
	run.Commands = append(run.Commands, []string{"plugin", source, o.Run})
	run.mu.Unlock()
	ch, err := a.Host().Stream(ctx, source, protocol.Args{"run": o.Run, "dir": dir, "env": vars, "with": s.With})
	if err != nil {
		return fmt.Errorf("suite %s: runner %s: %w", s.Name, s.Runner, err)
	}
	for b := range ch {
		if b.Error != nil {
			return fmt.Errorf("suite %s: %s", s.Name, b.Error.Message)
		}
		for _, line := range b.Lines {
			var e testEvent
			if json.Unmarshal([]byte(line), &e) != nil {
				run.mu.Lock()
				run.Stderr = append(run.Stderr, line)
				run.mu.Unlock()
				continue
			}
			run.apply(e)
		}
	}
	return nil
}
