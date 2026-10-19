package engine

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

// serviceExtras are what the TUI's service screen reads and changes beyond start, stop and scale:
// a service's env vars, its containers' requests and limits, and its autoscaler.
func (l *Local) serviceExtras() {
	a := l.App
	one := protocol.Schema{"type": "object", "required": []any{"service"}, "properties": map[string]any{"service": map[string]any{"type": "string"}}}
	l.about["services.env"] = protocol.SourceSpec{Description: "the env vars a service runs with (the live workload's where the runtime reads it)", Args: one}
	l.sources["services.env"] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		env, over, err := a.ServiceEnv(ctx, str(args["service"]))
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		for _, k := range SortedKeys(env) {
			rows = append(rows, map[string]any{"name": k, "value": env[k], "overridden": over[k]})
		}
		return frames.FromRows([]protocol.Field{{Name: "name", Type: protocol.TypeString}, {Name: "value", Type: protocol.TypeString}, {Name: "overridden", Type: protocol.TypeBool}}, rows), nil
	}
	l.about["services.resources"] = protocol.SourceSpec{Description: "requests and limits of a service's containers", Args: one}
	l.sources["services.resources"] = func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		rs, err := a.Resources(ctx, str(args["service"]))
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		for _, r := range rs {
			rows = append(rows, map[string]any{"container": r.Container, "cpu_request": r.CPURequest, "cpu_limit": r.CPULimit, "mem_request": r.MemRequest, "mem_limit": r.MemLimit})
		}
		return frames.FromRows(strFields("container", "cpu_request", "cpu_limit", "mem_request", "mem_limit"), rows), nil
	}

	reg := l.runner.Actions
	prop := func(names ...string) map[string]any {
		m := map[string]any{"service": map[string]any{"type": "string"}}
		for _, n := range names {
			m[n] = map[string]any{"type": "string"}
		}
		return m
	}
	_ = reg.Add(actions.Action{Name: "runtime.set-env", Effect: protocol.EffectWrite, Help: "set env vars of a service (an empty value removes one) and redeploy it",
		Args: protocol.Schema{"type": "object", "required": []any{"service", "env"}, "properties": map[string]any{
			"service": map[string]any{"type": "string"}, "env": map[string]any{"type": "object"}}},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			n := str(args["service"])
			in, _ := args["env"].(map[string]any)
			kv := map[string]string{}
			for k, v := range in {
				kv[k] = fmt.Sprint(v)
			}
			if len(kv) == 0 {
				return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "env is empty"}
			}
			if err := a.SetEnv(ctx, []string{n}, kv); err != nil {
				return nil, err
			}
			keys := SortedKeys(kv)
			return &protocol.RunResult{Message: fmt.Sprintf("set %v of %s", keys, n), Refs: []protocol.Ref{protocol.Ref("svc://" + n)}}, nil
		}})
	_ = reg.Add(actions.Action{Name: "runtime.set-resources", Effect: protocol.EffectWrite, Help: "change one container's requests and limits (restarts its pods)",
		Args: protocol.Schema{"type": "object", "required": []any{"service"}, "properties": prop("container", "cpu_request", "cpu_limit", "mem_request", "mem_limit")},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			n := str(args["service"])
			r := core.Resources{Container: str(args["container"]), CPURequest: str(args["cpu_request"]), CPULimit: str(args["cpu_limit"]),
				MemRequest: str(args["mem_request"]), MemLimit: str(args["mem_limit"])}
			if err := a.SetResources(ctx, n, r); err != nil {
				return nil, err
			}
			return &protocol.RunResult{Message: "set requests and limits of " + n, Refs: []protocol.Ref{protocol.Ref("svc://" + n)}}, nil
		}})
	_ = reg.Add(actions.Action{Name: "runtime.autoscale", Effect: protocol.EffectWrite, Help: "move a service's autoscaler range, or create one on cpu/memory utilization",
		Args: protocol.Schema{"type": "object", "required": []any{"service", "min", "max"}, "properties": prop("min", "max", "cpu", "memory")},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			n := str(args["service"])
			num := func(k string) (int, error) {
				if args[k] == nil || str(args[k]) == "" {
					return 0, nil
				}
				return strconv.Atoi(str(args[k]))
			}
			var b core.Bounds
			var err error
			for k, p := range map[string]*int{"min": &b.Min, "max": &b.Max, "cpu": &b.CPU, "memory": &b.Memory} {
				if *p, err = num(k); err != nil {
					return nil, fmt.Errorf("%s: %w", k, err)
				}
			}
			if b.Min < 1 || b.Max < b.Min {
				return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "want 1 <= min <= max"}
			}
			if err := a.SetAutoscale(ctx, n, b); err != nil {
				return nil, err
			}
			return &protocol.RunResult{Message: fmt.Sprintf("autoscale %s %d-%d", n, b.Min, b.Max), Refs: []protocol.Ref{protocol.Ref("svc://" + n)}}, nil
		}})
	_ = reg.Add(actions.Action{Name: "runtime.kill-ports", Effect: protocol.EffectDanger, Help: "kill the local processes listening on the ports the services declare (a crashed run left behind)",
		Args: protocol.Schema{"type": "object", "required": []any{"services"}, "properties": map[string]any{"services": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			var ports []int
			for _, n := range strs(args["services"]) {
				if s := a.Spec.Services[n]; s != nil {
					for _, p := range s.Ports {
						if !slices.Contains(ports, p) {
							ports = append(ports, p)
						}
					}
				}
			}
			if len(ports) == 0 {
				return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "no ports declared"}
			}
			slices.Sort(ports)
			list := strings.Trim(fmt.Sprint(ports), "[]")
			var pids []int
			for _, p := range ports {
				found, err := sh.ListenerPIDs(ctx, p)
				if err != nil {
					return nil, err
				}
				pids = append(pids, found...)
			}
			if len(pids) == 0 {
				return &protocol.RunResult{Message: "nothing listens on port " + list}, nil
			}
			sh.Kill(pids, 3*time.Second)
			return &protocol.RunResult{Message: fmt.Sprintf("killed %d processes on port %s", len(pids), list)}, nil
		}})
}
