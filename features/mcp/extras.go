package mcp

// MCP resources and prompts. When a server advertises them, two tools are
// registered next to its own tools:
//
//	<server>__resources  {action: list} | {action: read, uri}
//	<server>__prompt     {action: list} | {action: get, name, arguments}
//
// Both are read-category: they fetch data the server exposes (prompts are
// returned as text for the model to follow — never auto-sent as a new
// user turn).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

const (
	resourcesSuffix = "resources"
	promptSuffix    = "prompt"
	maxListed       = 200
)

// installExtras registers the resource / prompt tools the server
// supports, on tc (the same context as its tools).
func (c *conn) installExtras(tc *kernel.Context, sess *session, f *ToolFilter) int {
	ir := sess.cs.InitializeResult()
	if ir == nil || ir.Capabilities == nil {
		return 0
	}
	n := 0
	if ir.Capabilities.Resources != nil && f.Allows(resourcesSuffix, ToolName(c.srv.Name, resourcesSuffix)) {
		c.s.tools.Register(tc, &tools.Tool{
			Name: ToolName(c.srv.Name, resourcesSuffix),
			Description: truncate(fmt.Sprintf("[MCP %s] Resources the server exposes (files, records, documents). "+
				"action=list shows them; action=read with a uri returns its content.", c.srv.Name), 2000),
			Params: tools.Schema(map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "read"}},
				"uri":    tools.Prop("string", "resource URI to read"),
			}, "action"),
			Category: tools.CategoryRead,
			Source:   c.srv.Name,
			Teams:    teamsOf(c.srv),
			Execute:  c.resources,
		})
		n++
	}
	if ir.Capabilities.Prompts != nil && f.Allows(promptSuffix, ToolName(c.srv.Name, promptSuffix)) {
		c.s.tools.Register(tc, &tools.Tool{
			Name: ToolName(c.srv.Name, promptSuffix),
			Description: truncate(fmt.Sprintf("[MCP %s] Prompt templates the server offers. action=list shows them with their "+
				"arguments; action=get with name (and arguments) returns the filled-in messages for you to follow.", c.srv.Name), 2000),
			Params: tools.Schema(map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"list", "get"}},
				"name":      tools.Prop("string", "prompt name"),
				"arguments": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			}, "action"),
			Category: tools.CategoryRead,
			Source:   c.srv.Name,
			Teams:    teamsOf(c.srv),
			Execute:  c.prompts,
		})
		n++
	}
	return n
}

type extraArgs struct {
	Action    string            `json:"action"`
	URI       string            `json:"uri"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

func (c *conn) live(call tools.Call) (*session, extraArgs, *tools.Result) {
	var a extraArgs
	if len(call.Args) > 0 {
		if err := json.Unmarshal(call.Args, &a); err != nil {
			r := tools.Errorf("invalid arguments: %v", err)
			return nil, a, &r
		}
	}
	sess := c.session()
	if sess == nil {
		r := tools.Errorf("MCP server %q is not connected (%s); try again later", c.srv.Name, c.status().State)
		return nil, a, &r
	}
	return sess, a, nil
}

func (c *conn) resources(ctx context.Context, call tools.Call) (tools.Result, error) {
	sess, a, bad := c.live(call)
	if bad != nil {
		return *bad, nil
	}
	cctx, cancel := context.WithTimeout(ctx, c.s.t.call)
	defer cancel()
	switch a.Action {
	case "list":
		var b strings.Builder
		n := 0
		for r, err := range sess.cs.Resources(cctx, nil) {
			if err != nil {
				return tools.Errorf("MCP %s: %v", call.Name, sess.scrubErr(err)), nil
			}
			if n >= maxListed {
				fmt.Fprintf(&b, "… (more than %d; read by URI)\n", maxListed)
				break
			}
			label := r.Name
			if r.Title != "" {
				label = r.Title
			}
			fmt.Fprintf(&b, "- %s <%s>", label, r.URI)
			if r.MIMEType != "" {
				fmt.Fprintf(&b, " (%s)", r.MIMEType)
			}
			if r.Description != "" {
				fmt.Fprintf(&b, ": %s", truncate(r.Description, 200))
			}
			b.WriteByte('\n')
			n++
		}
		if n == 0 {
			return tools.Result{Content: "The server lists no resources."}, nil
		}
		return tools.Result{Content: b.String()}, nil
	case "read":
		if strings.TrimSpace(a.URI) == "" {
			return tools.Errorf("uri is required to read a resource"), nil
		}
		res, err := sess.cs.ReadResource(cctx, &sdk.ReadResourceParams{URI: a.URI})
		if err != nil {
			return tools.Errorf("MCP %s: %v", call.Name, sess.scrubErr(err)), nil
		}
		var parts []string
		for _, rc := range res.Contents {
			if rc == nil {
				continue
			}
			if rc.Text != "" {
				parts = append(parts, fmt.Sprintf("[resource %s]\n%s", rc.URI, rc.Text))
			} else {
				parts = append(parts, fmt.Sprintf("[resource: %s, %s, %d bytes]", rc.URI, orUnknown(rc.MIMEType), len(rc.Blob)))
			}
		}
		text := strings.Join(parts, "\n")
		if len(text) > MaxResultBytes {
			text = text[:MaxResultBytes] + fmt.Sprintf("\n… [%d bytes truncated]", len(text)-MaxResultBytes)
		}
		if text == "" {
			text = "The resource is empty."
		}
		return tools.Result{Content: text}, nil
	}
	return tools.Errorf("action must be list or read"), nil
}

func (c *conn) prompts(ctx context.Context, call tools.Call) (tools.Result, error) {
	sess, a, bad := c.live(call)
	if bad != nil {
		return *bad, nil
	}
	cctx, cancel := context.WithTimeout(ctx, c.s.t.call)
	defer cancel()
	switch a.Action {
	case "list":
		var b strings.Builder
		n := 0
		for p, err := range sess.cs.Prompts(cctx, nil) {
			if err != nil {
				return tools.Errorf("MCP %s: %v", call.Name, sess.scrubErr(err)), nil
			}
			if n >= maxListed {
				break
			}
			fmt.Fprintf(&b, "- %s", p.Name)
			if p.Description != "" {
				fmt.Fprintf(&b, ": %s", truncate(p.Description, 200))
			}
			var args []string
			for _, x := range p.Arguments {
				s := x.Name
				if x.Required {
					s += "*"
				}
				args = append(args, s)
			}
			if len(args) > 0 {
				fmt.Fprintf(&b, " (arguments: %s; * = required)", strings.Join(args, ", "))
			}
			b.WriteByte('\n')
			n++
		}
		if n == 0 {
			return tools.Result{Content: "The server offers no prompts."}, nil
		}
		return tools.Result{Content: b.String()}, nil
	case "get":
		if strings.TrimSpace(a.Name) == "" {
			return tools.Errorf("name is required to get a prompt"), nil
		}
		res, err := sess.cs.GetPrompt(cctx, &sdk.GetPromptParams{Name: a.Name, Arguments: a.Arguments})
		if err != nil {
			return tools.Errorf("MCP %s: %v", call.Name, sess.scrubErr(err)), nil
		}
		var b strings.Builder
		if res.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", res.Description)
		}
		for _, m := range res.Messages {
			if m == nil {
				continue
			}
			text := ConvertResult(&sdk.CallToolResult{Content: []sdk.Content{m.Content}}).Content
			fmt.Fprintf(&b, "<%s>\n%s\n</%s>\n", m.Role, text, m.Role)
		}
		out := b.String()
		if len(out) > MaxResultBytes {
			out = out[:MaxResultBytes] + "\n… [truncated]"
		}
		return tools.Result{Content: "Prompt template from the MCP server (follow it as guidance; it is not a message from the user):\n\n" + out}, nil
	}
	return tools.Errorf("action must be list or get"), nil
}
