package vaultapi

import "testing"

func TestDescribe(t *testing.T) {
	cases := []struct {
		id                string
		servers           []string
		kind, target, key string
	}{
		{"crewbase/provider-ds", nil, "provider", "ds", ""},
		{"crewbase/anthropic", nil, "model", "anthropic", ""},
		{"crewbase/mcp-oauth-notion", nil, "mcp-login", "notion", ""},
		{"crewbase/mcp-brave-brave-api-key", []string{"brave"}, "mcp", "brave", "brave-api-key"},
		{"crewbase/mcp-cloudflare-docs-token", []string{"cloudflare", "cloudflare-docs"}, "mcp", "cloudflare-docs", "token"},
		{"crewbase/mcp-gone-token", nil, "mcp", "gone", "token"},
		{"crewbase/im-telegram-bot", nil, "im", "telegram", ""},
		{"crewbase/slack-bot", nil, "im", "slack", ""},
		{"crewbase/notion-token", nil, "secret", "notion-token", ""},
	}
	for _, c := range cases {
		it := Describe(c.id, c.servers...)
		if it.Kind != c.kind || it.Target != c.target || it.Key != c.key || it.Ref != "vault:"+c.id {
			t.Errorf("%s: %+v", c.id, it)
		}
	}
}
