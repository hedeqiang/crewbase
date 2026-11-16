//go:build !windows

package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/bridge/jsonrpc"
)

// The test binary doubles as a fake ACP agent (TestMain re-exec pattern):
// with FAKE_ACP_AGENT=1 it speaks ACP on stdio instead of running tests.
//
// Prompt commands (anywhere in the prompt text, last one wins):
//
//	/quota   JSON-RPC error with a Claude-style usage limit message
//	/banner  replies with a limit banner as normal text
//	/auth    JSON-RPC -32000 Authentication required
//	/crash   writes to stderr and exits 3
//	/slow    streams one chunk, then waits for session/cancel
//	/perm    requests permission and reports the outcome
//	/read P  fs/read_text_file P
//	/write P fs/write_text_file P
//	/spawn   starts a grandchild `sleep 300` and reports its pid
//	/cwd     replies with the session's cwd
//	/meta    replies with the session/new _meta
//	/cfgperm P requests permission to edit P (raw input file_path)
//
// Otherwise it replies "[s<n>] echo: <prompt>" in two chunks with a tool call.
const fakeEnv = "FAKE_ACP_AGENT"

func runFakeAgent() {
	var (
		mu      sync.Mutex
		nsess   int
		cancels = map[string]chan struct{}{}
		cwds    = map[string]string{}
		metas   = map[string]string{}
		conn    *jsonrpc.Conn
	)
	update := func(sid string, u map[string]any) {
		_ = conn.Notify("session/update", map[string]any{"sessionId": sid, "update": u})
	}
	chunk := func(sid, text string) {
		update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}})
	}
	arg := func(text, cmd string) (string, bool) {
		i := strings.LastIndex(text, cmd+" ")
		if i < 0 {
			return "", false
		}
		f := strings.Fields(text[i+len(cmd)+1:])
		if len(f) == 0 {
			return "", false
		}
		return f[0], true
	}
	prompt := func(ctx context.Context, params json.RawMessage) (any, error) {
		var p promptParams
		_ = json.Unmarshal(params, &p)
		text := ""
		for _, b := range p.Prompt {
			text += b.Text
		}
		mu.Lock()
		cancel := cancels[p.SessionID]
		mu.Unlock()
		sid := p.SessionID
		switch {
		case strings.Contains(text, "/quota"):
			return nil, jsonrpc.NewError(-32603, "Internal error: Claude AI usage limit reached|4102444800", nil)
		case strings.Contains(text, "/banner"):
			chunk(sid, "You've hit your usage limit. Try again in 2 hours.")
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/auth"):
			return nil, jsonrpc.NewError(-32000, "Authentication required", nil)
		case strings.Contains(text, "/crash"):
			fmt.Fprintln(os.Stderr, "fatal: segmentation fault in agent")
			os.Exit(3)
		case strings.Contains(text, "/slow"):
			chunk(sid, "working...")
			select {
			case <-cancel:
				return map[string]string{"stopReason": "cancelled"}, nil
			case <-time.After(30 * time.Second):
				return map[string]string{"stopReason": "end_turn"}, nil
			}
		case strings.Contains(text, "/perm"):
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "rm -rf build", "kind": "execute", "status": "pending"})
			var res permissionResult
			err := conn.Call(ctx, "session/request_permission", map[string]any{
				"sessionId": sid,
				"toolCall":  map[string]string{"toolCallId": "t1", "title": "rm -rf build", "kind": "execute"},
				"options": []map[string]string{
					{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
					{"optionId": "no", "name": "Reject", "kind": "reject_once"},
				},
			}, &res)
			if err != nil {
				return nil, err
			}
			chunk(sid, fmt.Sprintf("permission: %s/%s", res.Outcome.Outcome, res.Outcome.OptionID))
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/read "):
			path, _ := arg(text, "/read")
			var res struct{ Content string }
			if err := conn.Call(ctx, "fs/read_text_file", map[string]any{"sessionId": sid, "path": path, "line": 2, "limit": 1}, &res); err != nil {
				chunk(sid, "read error: "+err.Error())
			} else {
				chunk(sid, "read: "+res.Content)
			}
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/write "):
			path, _ := arg(text, "/write")
			if err := conn.Call(ctx, "fs/write_text_file", map[string]any{"sessionId": sid, "path": path, "content": "written"}, nil); err != nil {
				chunk(sid, "write error: "+err.Error())
			} else {
				chunk(sid, "write ok")
			}
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/edit"):
			// the CLI edits files with its own tools (no fs/write_text_file)
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "e1", "title": "Edit main.go", "kind": "edit", "status": "pending",
				"locations": []map[string]any{{"path": "main.go", "line": 3}}})
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "e1", "status": "completed"})
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "e2", "title": "Patch", "kind": "other", "status": "pending"})
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "e2", "status": "completed",
				"content": []map[string]any{{"type": "diff", "path": "docs/readme.md", "oldText": "a", "newText": "b"}}})
			update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "e3", "title": "Read go.mod", "kind": "read", "status": "pending",
				"locations": []map[string]any{{"path": "go.mod"}}})
			update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "e3", "status": "completed"})
			chunk(sid, "edited")
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/meta"):
			mu.Lock()
			meta := metas[sid]
			mu.Unlock()
			chunk(sid, "["+sid+"] meta="+meta)
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/cfgperm "):
			path, _ := arg(text, "/cfgperm")
			var res permissionResult
			err := conn.Call(ctx, "session/request_permission", map[string]any{
				"sessionId": sid,
				"toolCall": map[string]any{"toolCallId": "w1", "title": "Write settings", "kind": "edit",
					"rawInput": map[string]string{"file_path": path}},
				"options": []map[string]string{
					{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
					{"optionId": "no", "name": "Reject", "kind": "reject_once"},
				},
			}, &res)
			if err != nil {
				return nil, err
			}
			chunk(sid, fmt.Sprintf("permission: %s/%s", res.Outcome.Outcome, res.Outcome.OptionID))
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/cwd"):
			mu.Lock()
			cwd := cwds[sid]
			mu.Unlock()
			chunk(sid, "["+sid+"] cwd="+cwd)
			return map[string]string{"stopReason": "end_turn"}, nil
		case strings.Contains(text, "/spawn"):
			c := exec.Command("sleep", "300")
			if err := c.Start(); err != nil {
				return nil, err
			}
			chunk(sid, fmt.Sprintf("pid=%d", c.Process.Pid))
			return map[string]string{"stopReason": "end_turn"}, nil
		}
		update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "thinking"}})
		update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "r1", "title": "Read README.md", "kind": "read", "status": "pending"})
		update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "r1", "status": "completed"})
		chunk(sid, "["+sid+"] ")
		chunk(sid, "echo: "+text)
		return map[string]string{"stopReason": "end_turn"}, nil
	}
	conn = jsonrpc.NewConn(os.Stdin, os.Stdout, jsonrpc.Options{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "initialize":
				if os.Getenv("FAKE_ACP_INIT_FAIL") != "" {
					fmt.Fprintln(os.Stderr, "Error: You've hit your weekly limit.")
					os.Exit(1)
				}
				return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{},
					"agentInfo": map[string]string{"name": "fake", "version": "9.9.9"}, "authMethods": []any{}}, nil
			case "session/new":
				mu.Lock()
				nsess++
				sid := fmt.Sprintf("s%d", nsess)
				cancels[sid] = make(chan struct{})
				var np struct {
					Cwd  string
					Meta json.RawMessage `json:"_meta"`
				}
				_ = json.Unmarshal(params, &np)
				cwds[sid] = np.Cwd
				metas[sid] = string(np.Meta)
				mu.Unlock()
				return map[string]any{"sessionId": sid, "models": map[string]any{
					"currentModelId":  "fast",
					"availableModels": []map[string]string{{"modelId": "fast", "name": "Fast"}, {"modelId": "smart", "name": "Smart"}},
				}}, nil
			case "session/set_model":
				return map[string]any{}, nil
			case "session/prompt":
				return prompt(ctx, params)
			}
			return nil, jsonrpc.ErrMethodNotFound
		},
		OnNotification: func(method string, params json.RawMessage) {
			if method != "session/cancel" {
				return
			}
			var p struct{ SessionID string }
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			if ch := cancels[p.SessionID]; ch != nil {
				close(ch)
				cancels[p.SessionID] = make(chan struct{})
			}
			mu.Unlock()
		},
	})
	_ = conn.Serve(context.Background())
}
