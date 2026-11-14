package crewplugin_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/hedeqiang/crewbase/bridge/jsonrpc"
	crewplugin "github.com/hedeqiang/crewbase/sdk/go"
)

func TestServe(t *testing.T) {
	p := crewplugin.New("hello-go", "0.1.0")
	p.Tool(crewplugin.Tool{Name: "go_hello", Execute: func(ctx context.Context, call crewplugin.Call, h *crewplugin.Host) (crewplugin.Result, error) {
		var a struct{ Name string }
		_ = call.Decode(&a)
		return crewplugin.Text("hello " + a.Name), nil
	}})
	p.PreExecute(func(context.Context, crewplugin.Decision) (*crewplugin.Verdict, error) { return nil, nil })
	hr, pw := io.Pipe()
	pr, hw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Serve(ctx, pr, pw) }()
	host := jsonrpc.NewConn(hr, hw, jsonrpc.Options{})
	go func() { _ = host.Serve(ctx) }()
	defer host.Close()

	var reg struct {
		ProtocolVersion int
		Tools           []struct{ Name string }
		Events          []string
	}
	if err := host.Call(ctx, "initialize", map[string]any{"protocolVersion": 1}, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.ProtocolVersion != 1 || len(reg.Tools) != 1 || reg.Tools[0].Name != "go_hello" || len(reg.Events) != 1 {
		t.Fatalf("registrations %+v", reg)
	}
	var res crewplugin.Result
	err := host.Call(ctx, "tool/execute", map[string]any{"tool": "go_hello", "call": map[string]any{"args": json.RawMessage(`{"name":"crew"}`)}}, &res)
	if err != nil || res.Content != "hello crew" {
		t.Fatalf("%v %+v", err, res)
	}
	var v json.RawMessage
	if err := host.Call(ctx, "tools/pre-execute", map[string]any{"call": map[string]any{"name": "x"}}, &v); err != nil || string(v) != "null" {
		t.Fatalf("pre-execute %v %s", err, v)
	}
}
