package openai

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// A model the account or plan cannot use is its own kind, whatever status
// the gateway chose; quota, auth and plain unknown models keep theirs.
func TestClassifyModelUnavailableOnAccount(t *testing.T) {
	codex := `jsonrpc error -32603: Internal error ({"message":"The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account.","code":"model_not_found"})`
	cases := []struct {
		status int
		msg    string
		code   string
		want   llm.ErrorKind
	}{
		{400, codex, "", llm.ErrModelUnavailable},
		{404, codex, "model_not_found", llm.ErrModelUnavailable},
		{500, codex, "", llm.ErrModelUnavailable},
		{0, codex, "model_not_found", llm.ErrModelUnavailable},
		{403, "The model gpt-6.1-sol is not available on your current plan.", "", llm.ErrModelUnavailable},
		{404, `magpie knows no model "nonexistent-probe-model"`, "not_found_error", llm.ErrModelNotFound},
		{404, "The model `gpt-9` does not exist or you do not have access to it.", "model_not_found", llm.ErrModelNotFound},
		{429, "You exceeded your current quota, please check your plan and billing details.", "insufficient_quota", llm.ErrQuotaExhausted},
		{401, "Incorrect API key provided", "invalid_api_key", llm.ErrAuth},
		{400, "Function tools with reasoning_effort are not supported for gpt-6.1-sol in /v1/chat/completions.", "", llm.ErrBadRequest},
	}
	for _, c := range cases {
		if got := classify("magpie", c.status, nil, c.msg, c.code, "").Kind; got != c.want {
			t.Errorf("%d %q: got %s want %s", c.status, c.msg, got, c.want)
		}
	}
}
