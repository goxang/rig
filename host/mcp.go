package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/rpc"
)

// The MCP bridge: an MCP server's tools become actions, its resources two sources, resources and resource.

const mcpVersion = "2025-06-18"

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema protocol.Schema `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
	} `json:"annotations"`
}

func (t mcpTool) effect() string {
	switch {
	case t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint:
		return protocol.EffectRead
	case t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint:
		return protocol.EffectDanger
	}
	return protocol.EffectWrite
}

func (p *Plugin) mcpHandshake(ctx context.Context, c *rpc.Conn) error {
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
	}
	err := c.Call(ctx, "initialize", map[string]any{"protocolVersion": mcpVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "rig", "version": Version}}, &init)
	if err != nil {
		return err
	}
	if err := c.Notify("notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	pr := &protocol.Provides{Actions: map[string]protocol.ActionSpec{}, Sources: map[string]protocol.SourceSpec{}}
	if _, ok := init.Capabilities["tools"]; ok {
		cursor := ""
		for {
			var page struct {
				Tools      []mcpTool `json:"tools"`
				NextCursor string    `json:"nextCursor"`
			}
			params := map[string]any{}
			if cursor != "" {
				params["cursor"] = cursor
			}
			if err := c.Call(ctx, "tools/list", params, &page); err != nil {
				return fmt.Errorf("tools/list: %w", err)
			}
			for _, t := range page.Tools {
				pr.Actions[t.Name] = protocol.ActionSpec{Description: t.Description, Effect: t.effect(), Args: t.InputSchema}
			}
			if cursor = page.NextCursor; cursor == "" {
				break
			}
		}
	}
	if _, ok := init.Capabilities["resources"]; ok {
		pr.Sources["resources"] = protocol.SourceSpec{Description: "the server's resources"}
		pr.Sources["resource"] = protocol.SourceSpec{Description: "one resource's contents",
			Args: protocol.Schema{"type": "object", "required": []any{"uri"}, "properties": map[string]any{"uri": map[string]any{"type": "string"}}}}
	}
	p.ready = &protocol.InitializeResult{Name: p.Name, Protocol: "mcp/" + init.ProtocolVersion, Provides: pr}
	p.saveProvides(pr)
	return nil
}

func (p *Plugin) mcpCall(ctx context.Context, c *rpc.Conn, method string, params, result any) error {
	switch method {
	case protocol.MethodRun:
		r := params.(protocol.RunParams)
		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		}
		args := r.Args
		if args == nil {
			args = protocol.Args{}
		}
		if err := c.Call(ctx, "tools/call", map[string]any{"name": r.Action, "arguments": args}, &out); err != nil {
			return err
		}
		var text []string
		for _, ct := range out.Content {
			if ct.Type == "text" {
				text = append(text, ct.Text)
			}
		}
		msg := strings.Join(text, "\n")
		if out.IsError {
			return fmt.Errorf("%s.%s: %s", p.Name, r.Action, msg)
		}
		fmt.Fprintln(jobs.Out(ctx), msg)
		*result.(*protocol.RunResult) = protocol.RunResult{Message: msg, Outputs: out.Structured}
		return nil
	case protocol.MethodQuery:
		q := params.(protocol.QueryParams)
		switch q.Source {
		case "resources":
			var out struct {
				Resources []map[string]any `json:"resources"`
			}
			if err := c.Call(ctx, "resources/list", map[string]any{}, &out); err != nil {
				return err
			}
			b, _ := json.Marshal(out.Resources)
			f, err := parseOutput(b, "json")
			if err == nil {
				*result.(*protocol.Frame) = *f
			}
			return err
		case "resource":
			var out struct {
				Contents []struct {
					Text     string `json:"text"`
					MimeType string `json:"mimeType"`
				} `json:"contents"`
			}
			if err := c.Call(ctx, "resources/read", map[string]any{"uri": q.Args["uri"]}, &out); err != nil {
				return err
			}
			var all []string
			for _, ct := range out.Contents {
				all = append(all, ct.Text)
			}
			f, err := parseOutput([]byte(strings.Join(all, "\n")), "")
			if err == nil {
				*result.(*protocol.Frame) = *f
			}
			return err
		}
		return notFound("source", p.Name+"."+q.Source)
	}
	return errNoMethod
}
