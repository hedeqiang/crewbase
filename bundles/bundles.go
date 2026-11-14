// Package bundles declares the builtin bundles and profiles, and imports
// every builtin plugin so the kernel catalog is populated. Config strings may
// use ${root} (the data directory, default ~/.crewbase).
package bundles

import (
	"github.com/hedeqiang/crewbase/kernel"

	_ "github.com/hedeqiang/crewbase/capabilities/approval"
	_ "github.com/hedeqiang/crewbase/core/agent"
	_ "github.com/hedeqiang/crewbase/core/agentloop"
	_ "github.com/hedeqiang/crewbase/core/llm"
	_ "github.com/hedeqiang/crewbase/core/prompt"
	_ "github.com/hedeqiang/crewbase/core/session"
	_ "github.com/hedeqiang/crewbase/core/tools"
	_ "github.com/hedeqiang/crewbase/features/api"
	_ "github.com/hedeqiang/crewbase/features/gateway"
	_ "github.com/hedeqiang/crewbase/features/router"
	_ "github.com/hedeqiang/crewbase/providers/llmmock"
)

// Base is shared by every profile: model adapters, tools, persistence,
// approval, agents and the default loop.
var Base = kernel.Bundle{Name: "base", Entries: []kernel.Entry{
	{ID: "session", Name: "@crewbase/session", Config: map[string]any{"dir": "${root}/sessions"}},
	{ID: "prompt", Name: "@crewbase/prompt"},
	{ID: "tools", Name: "@crewbase/tools"},
	{ID: "approval", Name: "@crewbase/approval"},
	{ID: "llm", Name: "@crewbase/llm"},
	{ID: "llm-mock", Name: "@crewbase/llm-mock", Config: map[string]any{"ids": []string{"mock"}}},
	{ID: "router", Name: "@crewbase/router-default"},
	{ID: "agents", Name: "@crewbase/agents", Config: map[string]any{"root": "${root}", "defaultModels": []string{"mock/default"}}},
	{ID: "agent-loop", Name: "@crewbase/agent-loop"},
}}

// Desktop adds the local gateway, API and desktop approval cards.
var Desktop = kernel.Bundle{Name: "desktop", Entries: []kernel.Entry{
	{ID: "approval-desktop", Name: "@crewbase/approval-desktop"},
	{ID: "gateway", Name: "@crewbase/gateway", Config: map[string]any{"addr": "127.0.0.1:7788", "tokenFile": "${root}/token"}},
	{ID: "api", Name: "@crewbase/api"},
}}

// Server is headless: same API bound to localhost (remote access goes
// through outbound relays, never inbound ports).
var Server = kernel.Bundle{Name: "server", Entries: []kernel.Entry{
	{ID: "approval-desktop", Name: "@crewbase/approval-desktop"},
	{ID: "gateway", Name: "@crewbase/gateway", Config: map[string]any{"addr": "127.0.0.1:7788", "tokenFile": "${root}/token"}},
	{ID: "api", Name: "@crewbase/api"},
}}

// extra bundles are appended to profiles by later packages (AddToProfiles).
var extraBase []kernel.Entry

// AddToBase lets feature packages append entries to the base bundle before
// Init runs (called from init functions).
func AddToBase(e ...kernel.Entry) { extraBase = append(extraBase, e...) }

// Init registers bundles and profiles. Call once at startup.
func Init() {
	b := Base
	b.Entries = append(append([]kernel.Entry(nil), Base.Entries...), extraBase...)
	kernel.RegisterBundle(b)
	kernel.RegisterBundle(Desktop)
	kernel.RegisterBundle(Server)
	kernel.RegisterProfile(kernel.Profile{Name: "desktop", Bundles: []string{"base", "desktop"}})
	kernel.RegisterProfile(kernel.Profile{Name: "server", Bundles: []string{"base", "server"}})
	kernel.RegisterProfile(kernel.Profile{Name: "sdk", Bundles: []string{"base"}})
}
