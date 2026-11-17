package mcp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/hedeqiang/crewbase/features/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolFilterAllows(t *testing.T) {
	var none *mcp.ToolFilter
	if !none.Allows("x", "s__x") {
		t.Fatal("nil filter must allow")
	}
	f := &mcp.ToolFilter{Include: []string{"notion-*", "s__get"}, Exclude: []string{"notion-delete*"}}
	for name, want := range map[string]bool{
		"notion-search": true, "notion-delete-page": false, "get": true, "other": false,
	} {
		if got := f.Allows(name, "s__"+name); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
	ex := &mcp.ToolFilter{Exclude: []string{"wipe"}}
	if ex.Allows("wipe", "s__wipe") || !ex.Allows("echo", "s__echo") {
		t.Fatal("exclude only")
	}
}

func TestToolFilterValidation(t *testing.T) {
	s := stdioServer("v", nil)
	s.Tools = &mcp.ToolFilter{Exclude: []string{"[bad"}}
	if _, err := s.Normalize(); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatal(err)
	}
	s.Tools = &mcp.ToolFilter{Include: []string{" ", ""}, Exclude: []string{}}
	n, err := s.Normalize()
	if err != nil || n.Tools != nil {
		t.Fatalf("empty filter should normalize away: %+v %v", n.Tools, err)
	}
}

// The filter applies at registration and to refreshed lists
// (list_changed); changing it re-registers without reconnecting.
func TestToolFilterRegisterAndListChanged(t *testing.T) {
	e := newEnv(t, nil)
	srv := stdioServer("f", nil)
	srv.Tools = &mcp.ToolFilter{Exclude: []string{"wipe", "extra*"}}
	if _, err := e.svc.Add(srv); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, "f", mcp.StateReady)
	if v := e.visible(""); !slices.Contains(v, "f__echo") || slices.Contains(v, "f__wipe") || st.Tools != 1 {
		t.Fatalf("%v %+v", v, st)
	}
	if x := e.call("", "f__wipe", `{}`); !x.Result.IsError || !strings.Contains(x.Result.Content, "unknown tool") {
		t.Fatalf("excluded tool ran: %+v", x.Result)
	}
	tl, _ := e.tools.Get("", "f__echo")
	if tl.Source != "f" {
		t.Fatalf("source %q", tl.Source)
	}
	infos, _ := e.svc.Tools("f")
	if len(infos) != 2 {
		t.Fatalf("the checklist lists every tool: %+v", infos)
	}
	for _, i := range infos {
		if i.Enabled != (i.Original == "echo") {
			t.Fatalf("%+v", i)
		}
	}
	// a new tool matching an exclude stays out on list_changed
	syscall.Kill(st.PID, syscall.SIGUSR1)
	waitFor(t, "refreshed list", func() bool { i, _ := e.svc.Tools("f"); return len(i) == 3 })
	if slices.Contains(e.visible(""), "f__extra") {
		t.Fatal("excluded tool registered on list_changed")
	}
	// switching tools on: same process, new registrations
	if _, err := e.svc.Update("f", func(s *mcp.Server) { s.Tools = &mcp.ToolFilter{Exclude: []string{"wipe"}} }); err != nil {
		t.Fatal(err)
	}
	if v := e.visible(""); !slices.Contains(v, "f__extra") || slices.Contains(v, "f__wipe") {
		t.Fatal(v)
	}
	if st2, _ := e.svc.Status("f"); st2.PID != st.PID || st2.Tools != 2 {
		t.Fatalf("filter change reconnected or miscounted: %+v (was pid %d)", st2, st.PID)
	}
	// include-only
	if _, err := e.svc.Update("f", func(s *mcp.Server) { s.Tools = &mcp.ToolFilter{Include: []string{"wi*"}} }); err != nil {
		t.Fatal(err)
	}
	if v := e.visible(""); len(v) != 1 || v[0] != "f__wipe" {
		t.Fatal(v)
	}
	// persisted
	e2 := newEnv(t, map[string]any{"dir": e.dir})
	e2.waitState(t, "f", mcp.StateReady)
	if v := e2.visible(""); len(v) != 1 || v[0] != "f__wipe" {
		t.Fatal("filter not persisted:", v)
	}
	// connectors are listed for the agent checklist
	if !e2.tools.HasConnector("f") {
		t.Fatalf("%+v", e2.tools.Connectors())
	}
}

// Tools of a team-shared server are tagged with their source and still
// limited to the team.
func TestTeamScopedSourceAndLargeSchema(t *testing.T) {
	srv := newTestServer(nil)
	props := map[string]any{}
	for i := 0; i < 200; i++ {
		props[strings.Repeat("p", 10)+string(rune('a'+i%26))+strings.Repeat("x", i%7)] = map[string]any{"type": "string", "description": strings.Repeat("long description ", 3)}
	}
	srv.AddTool(&sdk.Tool{Name: "huge", InputSchema: map[string]any{"type": "object", "properties": props}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) { return &sdk.CallToolResult{}, nil })
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	e := newEnv(t, nil)
	e.tools.SetTeamOf(func(a string) string {
		if a == "red1" {
			return "red"
		}
		return "blue"
	})
	if _, err := e.svc.Add(mcp.Server{Name: "big", Transport: mcp.TransportHTTP, URL: ts.URL, Enabled: true, Scope: "team:red"}); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, "big", mcp.StateReady)
	if v := e.visible("red1"); !slices.Contains(v, "big__huge") {
		t.Fatal(v)
	}
	if v := e.visible("blue1"); len(v) != 0 {
		t.Fatal("team scope leaked:", v)
	}
	infos, _ := e.svc.Tools("big")
	var huge mcp.ToolInfo
	for _, i := range infos {
		if i.Original == "huge" {
			huge = i
		} else if i.Large {
			t.Fatalf("small schema flagged: %+v", i)
		}
	}
	if !huge.Large || huge.SchemaSize <= mcp.LargeSchema {
		t.Fatalf("%+v", huge)
	}
}
