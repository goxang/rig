package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// mcpCommand serves rig to AI agents over the Model Context Protocol (JSON-RPC on stdio). Every tool
// runs this binary's own CLI, so agents get exactly what a person at the terminal gets, protections included.
func mcpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "serve rig to AI agents over MCP (stdio): status, up, deploy, build, logs, queries, load, kv, tasks",
		Long: `Add to an agent's MCP config:

  { "mcpServers": { "rig": { "command": "rig", "args": ["mcp"], "cwd": "/path/to/project" } } }

Changes to a protected environment, and pushes over an existing image tag, need "confirm": true.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveMCP(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	argv        func(a map[string]any) ([]string, error)
}

func serveMCP(ctx context.Context, in io.Reader, out io.Writer) error {
	tools := mcpTools()
	byName := map[string]mcpTool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var req rpcReq
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{Code: -32700, Message: err.Error()}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification
		}
		resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2024-11-05"
			}
			resp.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "rig", "version": Version},
				"instructions": "rig controls this project's services and infrastructure in any environment (local, docker, kind, kubernetes). " +
					"Start with rig_envs and rig_status. Every tool takes an optional env. Changes to protected environments need confirm: true.",
			}
		case "ping":
			resp.Result = map[string]any{}
		case "tools/list":
			resp.Result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp.Error = &rpcErr{Code: -32602, Message: err.Error()}
				break
			}
			t, ok := byName[p.Name]
			if !ok {
				resp.Error = &rpcErr{Code: -32602, Message: "unknown tool " + p.Name}
				break
			}
			text, failed := runTool(ctx, t, p.Arguments)
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": failed}
		default:
			resp.Error = &rpcErr{Code: -32601, Message: "method not found: " + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func runTool(ctx context.Context, t mcpTool, args map[string]any) (string, bool) {
	if args == nil {
		args = map[string]any{}
	}
	argv, err := t.argv(args)
	if err != nil {
		return err.Error(), true
	}
	var global []string
	if g.file != "" {
		global = append(global, "-f", g.file)
	}
	if env := str(args, "env"); env != "" {
		global = append(global, "-e", env)
	}
	if b, _ := args["confirm"].(bool); b {
		global = append(global, "--yes")
	}
	self, err := os.Executable()
	if err != nil {
		return err.Error(), true
	}
	cmd := exec.CommandContext(ctx, self, append(global, argv...)...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "RIG_YES=")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	text := strings.TrimSpace(buf.String())
	if len(text) > 200_000 {
		text = "…" + text[len(text)-200_000:]
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		if strings.Contains(text, "protected") || strings.Contains(text, "--yes") {
			text += "\n(repeat with \"confirm\": true if the user agreed to this change)"
		}
		return text, true
	}
	if text == "" {
		text = "done"
	}
	return text, false
}

func str(a map[string]any, k string) string {
	switch v := a[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func list(a map[string]any, k string) []string {
	switch v := a[k].(type) {
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case string:
		return strings.Fields(v)
	}
	return nil
}

func need(a map[string]any, keys ...string) error {
	for _, k := range keys {
		if str(a, k) == "" && len(list(a, k)) == 0 {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}

func schema(props map[string]any, required ...string) map[string]any {
	props["env"] = map[string]any{"type": "string", "description": "environment (default: the project's default)"}
	props["confirm"] = map[string]any{"type": "boolean", "description": "the user agreed to change a protected environment or overwrite an image tag"}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	pTargets = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "services, groups (core, infra, ...) or roles (app, infra, load)"}
	pString  = func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	pBool    = func(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
)

func flag(a map[string]any, k, f string) []string {
	if v := str(a, k); v != "" && v != "false" {
		if v == "true" {
			return []string{f}
		}
		return []string{f, v}
	}
	return nil
}

func mcpTools() []mcpTool {
	return []mcpTool{
		{Name: "rig_envs", Description: "environments of the project, their runtimes, and the components of the current one",
			InputSchema: schema(map[string]any{}),
			argv:        func(map[string]any) ([]string, error) { return []string{"env"}, nil }},
		{Name: "rig_status", Description: "state, readiness, restarts and image of services",
			InputSchema: schema(map[string]any{"targets": pTargets}),
			argv: func(a map[string]any) ([]string, error) {
				return append([]string{"status"}, list(a, "targets")...), nil
			}},
		{Name: "rig_up", Description: "deploy targets and their dependencies in dependency order, waiting for readiness. Infrastructure that runs is left alone. build: build images first (from ref when given); tag: deploy that registry tag without building.",
			InputSchema: schema(map[string]any{"targets": pTargets, "build": pBool("build images first"), "tag": pString("image tag"), "ref": pString("git ref to build from")}),
			argv: func(a map[string]any) ([]string, error) {
				argv := append([]string{"up"}, list(a, "targets")...)
				argv = append(argv, flag(a, "build", "--build")...)
				argv = append(argv, flag(a, "tag", "--tag")...)
				return append(argv, flag(a, "ref", "--ref")...), nil
			}},
		{Name: "rig_down", Description: "stop services in reverse dependency order (every app when no targets; infrastructure only when named)",
			InputSchema: schema(map[string]any{"targets": pTargets}),
			argv:        func(a map[string]any) ([]string, error) { return append([]string{"down"}, list(a, "targets")...), nil }},
		{Name: "rig_service", Description: "start, stop or restart services (restart reloads their configuration)",
			InputSchema: schema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"start", "stop", "restart"}}, "targets": pTargets}, "action", "targets"),
			argv: func(a map[string]any) ([]string, error) {
				if err := need(a, "action", "targets"); err != nil {
					return nil, err
				}
				return append([]string{str(a, "action")}, list(a, "targets")...), nil
			}},
		{Name: "rig_scale", Description: "set replica counts, or move them: replicas \"3\", \"+1\", \"-2\"",
			InputSchema: schema(map[string]any{"targets": pTargets, "replicas": pString("count, or +n / -n")}, "targets", "replicas"),
			argv: func(a map[string]any) ([]string, error) {
				if err := need(a, "targets", "replicas"); err != nil {
					return nil, err
				}
				return append(append([]string{"scale"}, list(a, "targets")...), str(a, "replicas")), nil
			}},
		{Name: "rig_build", Description: "build images and push them to the environment's registry; returns the tag. An existing tag is not overwritten without confirm.",
			InputSchema: schema(map[string]any{"targets": pTargets, "tag": pString("image tag (default: <branch>-<date>-<time>)"), "ref": pString("git branch, tag or commit to build (default: the working tree)")}, "targets"),
			argv: func(a map[string]any) ([]string, error) {
				if err := need(a, "targets"); err != nil {
					return nil, err
				}
				argv := append([]string{"build"}, list(a, "targets")...)
				argv = append(argv, flag(a, "tag", "--tag")...)
				return append(argv, flag(a, "ref", "--ref")...), nil
			}},
		{Name: "rig_deploy", Description: "roll out services: with tag, that image tag from the registry (apps separately from infra: targets [\"app\"]); with build, a fresh build first",
			InputSchema: schema(map[string]any{"targets": pTargets, "tag": pString("image tag to deploy"), "build": pBool("build first"), "ref": pString("git ref to build from")}, "targets"),
			argv: func(a map[string]any) ([]string, error) {
				if err := need(a, "targets"); err != nil {
					return nil, err
				}
				argv := append([]string{"deploy"}, list(a, "targets")...)
				argv = append(argv, flag(a, "tag", "--tag")...)
				argv = append(argv, flag(a, "build", "--build")...)
				return append(argv, flag(a, "ref", "--ref")...), nil
			}},
		{Name: "rig_logs", Description: "recent log lines of services (merged), optionally one instance, filtered by text",
			InputSchema: schema(map[string]any{"targets": pTargets, "tail": pString("lines per service (default 100)"), "since": pString("e.g. 10m"), "grep": pString("only lines containing this"), "instance": pString("one pod or container")}, "targets"),
			argv: func(a map[string]any) ([]string, error) {
				argv := append([]string{"logs"}, list(a, "targets")...)
				argv = append(argv, flag(a, "tail", "--tail")...)
				argv = append(argv, flag(a, "since", "--since")...)
				argv = append(argv, flag(a, "grep", "--grep")...)
				return append(argv, flag(a, "instance", "--instance")...), nil
			}},
		{Name: "rig_query", Description: "run a saved query (name, with params) or an ad hoc one (component + query: SQL with optional @db prefix, PromQL, redis, kubectl, rabbitmq, kv). With no arguments lists saved queries and queryable components.",
			InputSchema: schema(map[string]any{"saved": pString("saved query name"), "params": map[string]any{"type": "object", "description": "values for the saved query's {{placeholders}}"},
				"component": pString("component for an ad hoc query"), "query": pString("ad hoc query text")}),
			argv: func(a map[string]any) ([]string, error) {
				if n := str(a, "saved"); n != "" {
					argv := []string{"query", n}
					if p, ok := a["params"].(map[string]any); ok {
						for k, v := range p {
							argv = append(argv, k+"="+fmt.Sprint(v))
						}
					}
					return argv, nil
				}
				if str(a, "component") == "" {
					return []string{"query"}, nil
				}
				return []string{"query", str(a, "component"), str(a, "query")}, nil
			}},
		{Name: "rig_load", Description: "load generators: status (all), start, stop, rate (requests/s per instance), scale (instances: n, +n, -n), up/down (one step)",
			InputSchema: schema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"status", "start", "stop", "rate", "scale", "up", "down"}},
				"generator": pString("generator component"), "value": pString("rate or replicas"), "force": pBool("allow a rate above the generator's max")}, "action"),
			argv: func(a map[string]any) ([]string, error) {
				act := str(a, "action")
				if act == "status" || act == "" {
					return []string{"load", "ls"}, nil
				}
				if err := need(a, "generator"); err != nil {
					return nil, err
				}
				argv := []string{"load", act, str(a, "generator")}
				if v := str(a, "value"); v != "" {
					argv = append(argv, v)
				}
				return append(argv, flag(a, "force", "--force")...), nil
			}},
		{Name: "rig_kv", Description: "key-value store (Consul): list keys under a prefix, get, put or delete a key. After changing a service's configuration, restart it with rig_service.",
			InputSchema: schema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "get", "put", "delete"}}, "key": pString("key or prefix"), "value": pString("value for put")}, "action"),
			argv: func(a map[string]any) ([]string, error) {
				switch str(a, "action") {
				case "list":
					return []string{"kv", "ls", str(a, "key")}, nil
				case "get":
					return []string{"kv", "get", str(a, "key")}, need(a, "key")
				case "put":
					return []string{"kv", "put", str(a, "key"), str(a, "value")}, need(a, "key", "value")
				case "delete":
					return []string{"kv", "rm", str(a, "key")}, need(a, "key")
				}
				return nil, fmt.Errorf("action is list, get, put or delete")
			}},
		{Name: "rig_infra", Description: "infrastructure where it runs (shared by local, docker and kind): status, up, down, restart",
			InputSchema: schema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"status", "up", "down", "restart"}}, "targets": pTargets}, "action"),
			argv: func(a map[string]any) ([]string, error) {
				return append([]string{"infra", str(a, "action")}, list(a, "targets")...), need(a, "action")
			}},
		{Name: "rig_task", Description: "run a named task from rig.yaml (e.g. bootstrap); without name lists tasks",
			InputSchema: schema(map[string]any{"name": pString("task name")}),
			argv:        func(a map[string]any) ([]string, error) { return append([]string{"task"}, list(a, "name")...), nil }},
		{Name: "rig", Description: "any rig command line, for what the other tools do not cover (e.g. [\"do\", \"runtime\", \"events\"], [\"traces\", \"--min\", \"500ms\"])",
			InputSchema: schema(map[string]any{"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "args"),
			argv: func(a map[string]any) ([]string, error) {
				args := list(a, "args")
				if len(args) == 0 {
					return nil, fmt.Errorf("args is required")
				}
				if args[0] == "mcp" || len(args) == 0 {
					return nil, fmt.Errorf("not from inside mcp")
				}
				return args, nil
			}},
	}
}
