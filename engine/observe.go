package engine

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

type streamFunc func(ctx context.Context, args protocol.Args) (<-chan protocol.StreamBatch, error)

var logFields = []protocol.Field{{Name: "time", Type: protocol.TypeTime}, {Name: "service", Type: protocol.TypeString},
	{Name: "instance", Type: protocol.TypeString}, {Name: "stream", Type: protocol.TypeString}, {Name: "text", Type: protocol.TypeString}}

// observe serves what the web UI reads: logs, metrics, scaling.
func (l *Local) observe() {
	logArgs := protocol.Schema{"type": "object", "properties": map[string]any{
		"services": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"tail":     map[string]any{"type": "integer"}, "instance": map[string]any{"type": "string"}}}
	l.about["services.logs"] = protocol.SourceSpec{Description: "the services' recent log lines; streamed, it follows them", Args: logArgs, Stream: true}
	l.sources["services.logs"] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		ch, err := l.logs(ctx, args, false)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for ln := range ch {
			rows = append(rows, logRow(ln))
		}
		return frames.FromRows(logFields, rows), nil
	}
	l.live["services.logs"] = func(ctx context.Context, args protocol.Args) (<-chan protocol.StreamBatch, error) {
		ch, err := l.logs(ctx, args, true)
		if err != nil {
			return nil, err
		}
		out := make(chan protocol.StreamBatch)
		go func() {
			defer close(out)
			seq := 0
			send := func(rows []map[string]any, done bool) bool {
				seq++
				b := protocol.StreamBatch{ID: "services.logs", Seq: seq, Done: done}
				if len(rows) > 0 {
					b.Frame = frames.FromRows(logFields, rows)
				}
				select {
				case out <- b:
					return true
				case <-ctx.Done():
					return false
				}
			}
			tick := time.NewTicker(150 * time.Millisecond)
			defer tick.Stop()
			var rows []map[string]any
			for {
				select {
				case ln, ok := <-ch:
					if !ok {
						send(rows, true)
						return
					}
					rows = append(rows, logRow(ln))
				case <-tick.C:
					if len(rows) > 0 {
						if !send(rows, false) {
							return
						}
						rows = nil
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		return out, nil
	}

	l.about["metrics.query"] = protocol.SourceSpec{Description: "a PromQL query over a range (range: 15m, step) or instant, on the metrics component named by source (the first by default); vars fill $variables",
		Args: protocol.Schema{"type": "object", "required": []any{"query"}, "properties": map[string]any{
			"query": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}, "range": map[string]any{"type": "string"},
			"step": map[string]any{"type": "string"}, "end": map[string]any{"type": "string", "description": "RFC 3339; the range ends here instead of now"}, "instant": map[string]any{"type": "boolean"}, "vars": map[string]any{"type": "object"}}}}
	l.sources["metrics.query"] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		src, _ := args["source"].(string)
		m, _, err := Get[core.Metrics](l.App, core.KindMetrics, src)
		if err != nil {
			return nil, err
		}
		text, _ := args["query"].(string)
		vars := map[string][]string{}
		if vs, ok := args["vars"].(map[string]any); ok {
			for k, v := range vs {
				vars[k] = strs(v)
			}
		}
		rng := dur(args["range"], 15*time.Minute)
		step := dur(args["step"], max(rng/240, time.Second))
		return metricsFrame(ctx, m, ExpandQuery(text, vars, rng, step), args)
	}

	_ = l.runner.Actions.Add(actions.Action{Name: "runtime.scale", Effect: protocol.EffectWrite, Help: "set a service's replica count",
		Args: protocol.Schema{"type": "object", "required": []any{"service", "replicas"}, "properties": map[string]any{
			"service": map[string]any{"type": "string"}, "replicas": map[string]any{"type": "integer"}}},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			n, _ := args["service"].(string)
			replicas, err := strconv.Atoi(fmt.Sprint(args["replicas"]))
			if err != nil {
				return nil, fmt.Errorf("replicas: %w", err)
			}
			if err := l.App.Scale(ctx, n, replicas); err != nil {
				return nil, err
			}
			return &protocol.RunResult{Message: fmt.Sprintf("scale %s to %d", n, replicas), Refs: []protocol.Ref{protocol.Ref("svc://" + n)}}, nil
		}})
}

// logs merges the named services' lines (every service when none is named).
func (l *Local) logs(ctx context.Context, args protocol.Args, follow bool) (<-chan core.LogLine, error) {
	names := strs(args["services"])
	if len(names) == 0 {
		names = l.App.Spec.ServiceNames()
	}
	tail := 200
	if n, err := strconv.Atoi(fmt.Sprint(args["tail"])); err == nil && n > 0 {
		tail = n
	}
	inst, _ := args["instance"].(string)
	out := make(chan core.LogLine, 256)
	var wg sync.WaitGroup
	var first error
	opened := 0
	for _, n := range names {
		ch, err := l.App.Logs(ctx, n, core.LogOptions{Follow: follow, Tail: tail, Instance: inst})
		if err != nil {
			first = fmt.Errorf("%s: %w", n, err)
			continue
		}
		opened++
		wg.Go(func() {
			for ln := range ch {
				if ln.Service == "" {
					ln.Service = n
				}
				select {
				case out <- ln:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	if opened == 0 && first != nil {
		return nil, first
	}
	go func() { wg.Wait(); close(out) }()
	return out, nil
}

func logRow(ln core.LogLine) map[string]any {
	return map[string]any{"time": ln.Time, "service": ln.Service, "instance": ln.Instance, "stream": ln.Stream, "text": ln.Text}
}
