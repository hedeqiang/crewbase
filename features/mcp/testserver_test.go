package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The test binary doubles as a tiny MCP server: with serverEnv set it
// serves MCP over stdio instead of running tests.
const serverEnv = "CREWBASE_MCP_TEST_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(serverEnv) != "" {
		runStdioServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func ptr[T any](v T) *T { return &v }

// newTestServer exposes echo (read-only) and wipe (destructive).
// failList makes tools/list fail (to test that a failed refresh keeps the
// previous tools).
func newTestServer(failList *atomic.Bool) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "crewbase-test", Version: "1.0.0"}, nil)
	s.AddTool(&sdk.Tool{
		Name:        "echo",
		Description: "Echo text back.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string"}, "env": map[string]any{"type": "string"},
		}, "required": []string{"text"}},
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var a struct{ Text, Env string }
		_ = json.Unmarshal(req.Params.Arguments, &a)
		out := a.Text
		if a.Env != "" {
			out += "|" + os.Getenv(a.Env)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: out}}}, nil
	})
	s.AddTool(&sdk.Tool{
		Name:        "wipe",
		Description: "Write a marker file in the working directory.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"fail": map[string]any{"type": "boolean"},
		}},
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(true)},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var a struct{ Fail bool }
		_ = json.Unmarshal(req.Params.Arguments, &a)
		if a.Fail {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "refused"}}}, nil
		}
		wd, _ := os.Getwd()
		if err := os.WriteFile(filepath.Join(wd, "wiped.txt"), []byte("x"), 0o600); err != nil {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "write: " + err.Error()}}}, nil
		}
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: "wiped " + wd},
			&sdk.ImageContent{MIMEType: "image/png", Data: []byte("png")},
		}}, nil
	})
	if failList != nil {
		s.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
			return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
				if method == "tools/list" && failList.Load() {
					return nil, errors.New("tools/list temporarily broken")
				}
				return next(ctx, method, req)
			}
		})
	}
	return s
}

func addExtra(s *sdk.Server, name string) {
	s.AddTool(&sdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: name}}}, nil
		})
}

// runStdioServer: SIGUSR1 adds tool "extra" (list_changed); SIGUSR2 breaks
// tools/list and adds "extra2". With CREWBASE_MCP_TEST_CHILD set, a
// grandchild `sleep` is spawned and its pid written to that file.
func runStdioServer() {
	var failList atomic.Bool
	s := newTestServer(&failList)
	if f := os.Getenv("CREWBASE_MCP_TEST_CHILD"); f != "" {
		cmd := exec.Command("sleep", "300")
		if err := cmd.Start(); err == nil {
			_ = os.WriteFile(f, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		}
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigs {
			switch sig {
			case syscall.SIGUSR1:
				addExtra(s, "extra")
			case syscall.SIGUSR2:
				failList.Store(true)
				addExtra(s, "extra2")
			}
		}
	}()
	if err := s.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "test server:", err)
	}
}
