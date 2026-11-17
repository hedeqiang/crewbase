package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Protocol versions served, newest first. initialize echoes the client's
// version when it is one of these, else answers the newest.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Limits.
const (
	maxBody      = 4 << 20
	maxImage     = 8 << 20
	serverTitle  = "Crewbase"
	serverVer    = "1"
	instructions = "Crewbase's own tools: operating the person's apps (computer_*) and Crewbase's browser (browser_*). " +
		"Every call goes through Crewbase's policy and may wait for the person's approval. " +
		"A denied call is final: do not retry it or work around it with other tools; tell the person instead."
)

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Content is one MCP content block (text or image).
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallResult is the result of tools/call.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type handler struct {
	s    *Service
	host *kernel.Host
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// loopbackOrigin accepts browser origins on this machine only.
func loopbackOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg}})
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if o := r.Header.Get("Origin"); o != "" && !loopbackOrigin(o) {
		httpError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	tok := bearer(r)
	g := h.s.lookup(tok)
	if g == nil {
		if tok != "" && h.host != nil {
			gateway.AuthFailed.Emit(h.host, gateway.AuthFailure{Remote: r.RemoteAddr, Path: r.URL.Path})
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="crewbase-mcp"`)
		httpError(w, http.StatusUnauthorized, "missing, invalid or expired token")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpError(w, http.StatusMethodNotAllowed, "only POST is served (no SSE stream)")
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(protocolVersions, v) {
		httpError(w, http.StatusBadRequest, "unsupported MCP-Protocol-Version "+v)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) > maxBody {
		httpError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	body = bytes.TrimSpace(body)
	var msgs []json.RawMessage
	batch := len(body) > 0 && body[0] == '['
	if batch {
		if json.Unmarshal(body, &msgs) != nil || len(msgs) == 0 {
			writeJSON(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error"}})
			return
		}
	} else {
		msgs = []json.RawMessage{body}
	}
	var out []rpcResponse
	for _, raw := range msgs {
		if resp, ok := h.handle(r.Context(), g, raw); ok {
			out = append(out, resp)
		}
	}
	switch {
	case len(out) == 0:
		w.WriteHeader(http.StatusAccepted) // notifications and responses only
	case batch:
		writeJSON(w, out)
	default:
		writeJSON(w, out[0])
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// handle processes one message; ok is false when there is nothing to answer.
func (h *handler) handle(ctx context.Context, g *Grant, raw json.RawMessage) (rpcResponse, bool) {
	var m rpcMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error"}}, true
	}
	isNull := len(m.ID) == 0 || string(m.ID) == "null"
	if m.Method == "" {
		if isNull {
			return rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeInvalidRequest, "invalid request"}}, true
		}
		return rpcResponse{}, false // a response to a request we never sent
	}
	if isNull {
		h.notify(g, m)
		return rpcResponse{}, false
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: m.ID}
	res, rerr := h.call(ctx, g, m)
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = res
	}
	return resp, true
}

func (h *handler) notify(g *Grant, m rpcMessage) {
	if m.Method != "notifications/cancelled" {
		return // notifications/initialized and others need nothing
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) == nil && len(p.RequestID) > 0 {
		g.cancel(string(p.RequestID))
	}
}

func (h *handler) call(ctx context.Context, g *Grant, m rpcMessage) (any, *rpcError) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		v := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]bool{"listChanged": false}},
			"serverInfo":      map[string]string{"name": ServerName, "title": serverTitle, "version": serverVer},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return h.list(g), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{codeInvalidParams, "tools/call needs a tool name"}
		}
		if !g.Allows(p.Name) {
			return nil, &rpcError{codeInvalidParams, "Unknown tool: " + p.Name}
		}
		args := bytes.TrimSpace(p.Arguments)
		if len(args) == 0 || string(args) == "null" {
			args = []byte("{}")
		}
		if args[0] != '{' {
			return nil, &rpcError{codeInvalidParams, "arguments must be a JSON object"}
		}
		return h.run(ctx, g, string(m.ID), p.Name, args), nil
	}
	return nil, &rpcError{codeMethodNotFound, "Method not found: " + m.Method}
}

func (h *handler) list(g *Grant) map[string]any {
	ts := h.s.visible(g.b.Agent, g.kind())
	out := make([]map[string]any, 0, len(ts))
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": inputSchema(t)})
		names = append(names, t.Name)
	}
	g.mu.Lock()
	g.listed = names
	g.mu.Unlock()
	return map[string]any{"tools": out}
}

// inputSchema is t's parameters as an MCP inputSchema (always an object).
func inputSchema(t *tools.Tool) map[string]any {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	if t.Params != nil {
		schema = make(map[string]any, len(t.Params)+1)
		for k, v := range t.Params {
			schema[k] = v
		}
		if _, ok := schema["type"]; !ok {
			schema["type"] = "object"
		}
	}
	return schema
}

func errResult(msg string) CallResult {
	return CallResult{Content: []Content{{Type: "text", Text: msg}}, IsError: true}
}

// run executes a call through the tools pipeline as the grant's agent.
func (h *handler) run(ctx context.Context, g *Grant, id, name string, args []byte) CallResult {
	ws, mode, err := g.effective()
	if err != nil {
		return errResult(err.Error())
	}
	ss := g.session()
	if ss == nil {
		return errResult("the Crewbase session is not available")
	}
	cctx, done, err := g.begin(ctx, id)
	if err != nil {
		return errResult(err.Error())
	}
	defer done()
	exe := h.s.reg.Run(cctx, h.s.c, tools.Call{
		ID: session.NewID("mcp"), Name: name, Args: args,
		Agent: g.b.Agent, Session: ss, Mode: mode, Workspace: ws,
	})
	return toResult(ws, exe.Result)
}

// toResult converts a tool result: its text, then its images.
func toResult(workspace string, r tools.Result) CallResult {
	out := CallResult{IsError: r.IsError}
	if r.Content != "" || len(r.Attachments) == 0 {
		out.Content = append(out.Content, Content{Type: "text", Text: r.Content})
	}
	for _, at := range r.Attachments {
		if !at.IsImage() {
			out.Content = append(out.Content, Content{Type: "text", Text: fmt.Sprintf("[attached %s: %s in the agent workspace]", at.Mime, at.Path)})
			continue
		}
		data, err := imageData(workspace, at)
		if err != nil {
			out.Content = append(out.Content, Content{Type: "text", Text: fmt.Sprintf("[image %s not available: %v]", at.Path, err)})
			continue
		}
		out.Content = append(out.Content, Content{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: at.Mime})
	}
	return out
}

func imageData(workspace string, at llm.Attachment) ([]byte, error) {
	if len(at.Data) > 0 {
		return at.Data, nil
	}
	p, err := resolveIn(workspace, at.Path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() || st.Size() > maxImage {
		return nil, errors.New("not a file or too large")
	}
	return os.ReadFile(p)
}

// resolveIn joins a workspace-relative path, refusing escapes (also
// through symlinks).
func resolveIn(workspace, rel string) (string, error) {
	if workspace == "" {
		return "", errors.New("no workspace")
	}
	c := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || path.IsAbs(c) || strings.ContainsRune(c, 0) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(workspace, filepath.FromSlash(c)))
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves the workspace", rel)
	}
	return real, nil
}
