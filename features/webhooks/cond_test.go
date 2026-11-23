package webhooks

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func ev(t *testing.T, typ, body string, h ...string) Event {
	var b any
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatal(err)
	}
	hh := http.Header{}
	for i := 0; i+1 < len(h); i += 2 {
		hh.Set(h[i], h[i+1])
	}
	return Event{Type: typ, Header: hh, Body: b, RawBody: []byte(body)}
}

func TestConditions(t *testing.T) {
	e := ev(t, "pull_request", `{"action":"opened","number":42,"pull_request":{"draft":false,"base":{"ref":"main"},"labels":[{"name":"urgent"}],"title":"Fix P0 crash"},"tags":["a","b"]}`, "X-GitHub-Event", "pull_request")
	for src, want := range map[string]bool{
		``:                   true,
		`action == "opened"`: true,
		`action == 'closed'`: false,
		`action == "opened" && pull_request.base.ref == "main"`:    true,
		`event in ["push", "pull_request"] && !pull_request.draft`: true,
		`header.X-GitHub-Event == "pull_request"`:                  true,
		`pull_request.labels contains "urgent"`:                    true,
		`pull_request.title contains "p0"`:                         true,
		`tags[1] == "b"`:                                           true,
		`number > 40 && number <= 42`:                              true,
		`number == 42`:                                             true,
		`missing.path`:                                             false,
		`!(action == "opened" || action == "reopened")`:            false,
		`pull_request.draft == false`:                              true,
	} {
		c, err := ParseCond(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got := c.Match(e); got != want {
			t.Errorf("%s: %v, want %v", src, got, want)
		}
	}
	for _, bad := range []string{`action ==`, `(action == "x"`, `action == "x" &&`, `== "x"`} {
		if _, err := ParseCond(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestRender(t *testing.T) {
	e := ev(t, "check_run", `{"check_run":{"name":"ci","conclusion":"failure"},"repository":{"full_name":"o/r"}}`)
	out := Render("{{repository.full_name}}: {{check_run.name}} {{check_run.conclusion}} ({{event}}) {{nope}}\n{{payload}}", e, 40)
	if !strings.HasPrefix(out, "o/r: ci failure (check_run) \n{") || !strings.Contains(out, "more characters]") {
		t.Fatal(out)
	}
}
