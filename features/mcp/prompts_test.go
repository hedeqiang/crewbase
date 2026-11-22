package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
)

// A prompt template picked with / is listed for the agent and filled in
// with the person's arguments when the turn starts (ResolveUse).
func TestPromptTemplatesForTheSlashMenu(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "docs", Version: "1"}, nil)
	srv.AddPrompt(&sdk.Prompt{Name: "review", Title: "Code review", Description: "Review a change", Arguments: []*sdk.PromptArgument{{Name: "topic", Description: "what changed", Required: true}}},
		func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.TextContent{Text: "Review the " + req.Params.Arguments["topic"] + " change carefully."}},
			}}, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(func() { ts.CloseClientConnections(); ts.Close() })
	e := newEnv(t, nil)
	if _, err := e.svc.Add(mcp.Server{Name: "docs", Transport: mcp.TransportHTTP, URL: ts.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, "docs", mcp.StateReady)

	list := e.svc.AgentPrompts(context.Background(), "kai")
	if len(list) != 1 || list[0].Server != "docs" || list[0].Name != "review" || list[0].Title != "Code review" || len(list[0].Args) != 1 || !list[0].Args[0].Required {
		t.Fatalf("list: %+v", list)
	}

	run := func(u session.Invoke) (*agentloop.UseRequest, error) {
		return agentloop.ResolveUse.Run(kernel.For(e.h.Root(), "kai"), context.Background(), &agentloop.UseRequest{Agent: "kai", Text: "please", Use: []session.Invoke{u}},
			func(_ context.Context, r *agentloop.UseRequest) (*agentloop.UseRequest, error) { return r, nil })
	}
	r, err := run(session.Invoke{Kind: session.InvokePrompt, Name: "docs/review", Args: map[string]string{"topic": "login"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Use[0].Text, "Review the login change carefully.") || r.Text != "please" {
		t.Fatalf("resolved: %+v", r)
	}
	if _, err := run(session.Invoke{Kind: session.InvokePrompt, Name: "other/review"}); err == nil || !strings.Contains(err.Error(), "other/review") {
		t.Fatalf("unknown server: %v", err)
	}
}
