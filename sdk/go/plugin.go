// Package crewplugin is the Go SDK for Crewbase out-of-process plugins.
// The host starts the plugin binary and speaks JSON-RPC 2.0 over stdio
// (newline-delimited, protocol version 1); see bridge/process.
//
//	func main() {
//		p := crewplugin.New("hello-go", "0.1.0")
//		p.Tool(crewplugin.Tool{Name: "go_hello", Description: "Say hello",
//			Execute: func(ctx context.Context, call crewplugin.Call, h *crewplugin.Host) (crewplugin.Result, error) {
//				return crewplugin.Text("hello from go"), nil
//			}})
//		if err := p.Run(context.Background()); err != nil {
//			log.Fatal(err)
//		}
//	}
package crewplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/hedeqiang/crewbase/bridge/jsonrpc"
)

// ProtocolVersion is the bridge protocol this SDK speaks.
const ProtocolVersion = 1

// Call is one tool invocation.
type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args"`
	Agent     string          `json:"agent,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Workspace string          `json:"workspace,omitempty"`
}

// Decode unmarshals the arguments.
func (c Call) Decode(v any) error {
	if len(c.Args) == 0 {
		return nil
	}
	return json.Unmarshal(c.Args, v)
}

// Result is a tool outcome.
type Result struct {
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// Text is a success result.
func Text(s string) Result { return Result{Content: s} }

// Execution is a finalized tool execution (tools/result).
type Execution struct {
	Call     Call   `json:"call"`
	Category string `json:"category"`
	Result   Result `json:"result"`
	Denied   bool   `json:"denied,omitempty"`
}

// Decision is the pre-execute decision as seen by a plugin.
type Decision struct {
	Call     Call   `json:"call"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Verdict  string `json:"verdict"`
	Reason   string `json:"reason,omitempty"`
}

// Verdict is a pre-execute answer: deny (with reason) or annotate (summary).
// A nil *Verdict leaves the decision unchanged.
type Verdict struct {
	Verdict string `json:"verdict,omitempty"` // "" or "deny"
	Reason  string `json:"reason,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Tool is a model-callable tool implemented by the plugin.
type Tool struct {
	Name        string
	Description string
	Params      map[string]any
	Category    string
	TimeoutMs   int
	Execute     func(ctx context.Context, call Call, h *Host) (Result, error)
}

// Plugin collects registrations and serves the host.
type Plugin struct {
	Name, Version string

	tools    map[string]Tool
	order    []string
	prompts  []map[string]any
	onResult func(Execution)
	pre      func(context.Context, Decision) (*Verdict, error)
	host     *Host
	init     json.RawMessage
}

// New creates a plugin.
func New(name, version string) *Plugin {
	return &Plugin{Name: name, Version: version, tools: map[string]Tool{}}
}

// Tool registers a tool.
func (p *Plugin) Tool(t Tool) {
	if _, ok := p.tools[t.Name]; !ok {
		p.order = append(p.order, t.Name)
	}
	p.tools[t.Name] = t
}

// Prompt adds a static system prompt fragment.
func (p *Plugin) Prompt(name, text string, order int) {
	p.prompts = append(p.prompts, map[string]any{"name": name, "text": text, "order": order})
}

// OnToolResult observes tools/result.
func (p *Plugin) OnToolResult(fn func(Execution)) { p.onResult = fn }

// PreExecute listens on tools/pre-execute (2 s host timeout).
func (p *Plugin) PreExecute(fn func(context.Context, Decision) (*Verdict, error)) { p.pre = fn }

// Host gives access to host services; every call is permission-checked.
type Host struct{ conn *jsonrpc.Conn }

// Log writes to the host log.
func (h *Host) Log(level, msg string) {
	_ = h.conn.Notify("log", map[string]string{"level": level, "message": msg})
}

// ResolveCredential resolves a credential ref listed in the manifest.
func (h *Host) ResolveCredential(ctx context.Context, ref string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	err := h.conn.Call(ctx, "credentials.resolve", map[string]string{"ref": ref}, &out)
	return out.Value, err
}

// Notify sends a notification through the host's notify service.
func (h *Host) Notify(ctx context.Context, kind, title, body string) error {
	return h.conn.Call(ctx, "notify.send", map[string]any{"kind": kind, "title": title, "body": body}, nil)
}

// InitParams returns the raw initialize params (permissions, config).
func (p *Plugin) InitParams() json.RawMessage { return p.init }

// Run serves stdin/stdout until the host closes the stream.
func (p *Plugin) Run(ctx context.Context) error {
	return p.Serve(ctx, os.Stdin, os.Stdout)
}

// Serve runs the plugin over an arbitrary stream (tests).
func (p *Plugin) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var conn *jsonrpc.Conn
	conn = jsonrpc.NewConn(r, w, jsonrpc.Options{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			return p.handle(ctx, method, params)
		},
		OnNotification: func(method string, params json.RawMessage) {
			switch method {
			case "tools/result":
				if p.onResult != nil {
					var e Execution
					if json.Unmarshal(params, &e) == nil {
						p.onResult(e)
					}
				}
			case "shutdown":
				_ = conn.Close()
			}
		},
	})
	p.host = &Host{conn: conn}
	return conn.Serve(ctx)
}

func (p *Plugin) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		p.init = append(json.RawMessage(nil), params...)
		events := []string{}
		if p.onResult != nil {
			events = append(events, "tools/result")
		}
		if p.pre != nil {
			events = append(events, "tools/pre-execute")
		}
		tools := []map[string]any{}
		for _, n := range p.order {
			t := p.tools[n]
			ps := t.Params
			if ps == nil {
				ps = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			cat := t.Category
			if cat == "" {
				cat = "none"
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "params": ps, "category": cat, "timeoutMs": t.TimeoutMs})
		}
		prompts := p.prompts
		if prompts == nil {
			prompts = []map[string]any{}
		}
		return map[string]any{"protocolVersion": ProtocolVersion, "name": p.Name, "version": p.Version,
			"tools": tools, "prompts": prompts, "events": events, "notify": []any{}}, nil
	case "tool/execute":
		var in struct {
			Tool string `json:"tool"`
			Call Call   `json:"call"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error(), nil)
		}
		t, ok := p.tools[in.Tool]
		if !ok {
			return nil, jsonrpc.NewError(jsonrpc.CodeMethodNotFound, fmt.Sprintf("unknown tool %s", in.Tool), nil)
		}
		return t.Execute(ctx, in.Call, p.host)
	case "tools/pre-execute":
		if p.pre == nil {
			return nil, nil
		}
		var d Decision
		if err := json.Unmarshal(params, &d); err != nil {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error(), nil)
		}
		return p.pre(ctx, d)
	}
	return nil, jsonrpc.ErrMethodNotFound
}
