package git

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecentActivity(t *testing.T) {
	r := setup(t)
	gitRun(t, r.repo, "remote", "add", "origin", "git@github.com:acme/api.git")
	since := time.Date(2026, 11, 26, 22, 0, 0, 0, time.UTC)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		asked = append(asked, q.URL.Path+"?"+q.URL.RawQuery)
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch q.URL.Path {
		case "/user":
			send(map[string]string{"login": "acme-bot"})
		case "/repos/acme/api/issues":
			send([]map[string]any{
				{"number": 31, "title": "Crash on empty input", "html_url": "https://github.com/acme/api/issues/31", "user": map[string]string{"login": "ann"}, "created_at": "2026-11-26T23:10:00Z"},
				{"number": 30, "title": "a PR, not an issue", "pull_request": map[string]any{}, "created_at": "2026-11-26T23:00:00Z"},
				{"number": 12, "title": "old, only updated", "created_at": "2026-11-20T10:00:00Z"},
			})
		case "/repos/acme/api/pulls/comments":
			send([]map[string]any{
				{"id": 901, "user": map[string]string{"login": "bob"}, "path": "api.go", "line": 4, "body": "rename this", "created_at": "2026-11-27T01:00:00Z", "pull_request_url": "https://api.github.com/repos/acme/api/pulls/7", "html_url": "https://github.com/acme/api/pull/7#discussion_r901"},
				{"id": 902, "user": map[string]string{"login": "acme-bot"}, "body": "ours", "created_at": "2026-11-27T01:05:00Z", "pull_request_url": "https://api.github.com/repos/acme/api/pulls/7"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	r.s.ghCallFor = func(string) ghCall { return fakeCall(srv.URL) }
	a, err := r.s.RecentActivity(context.Background(), r.repo, since)
	if err != nil {
		t.Fatal(err)
	}
	if a.Repo != "acme/api" || len(a.Issues) != 1 || a.Issues[0].Number != 31 || a.Issues[0].Author != "ann" {
		t.Fatalf("issues %+v", a)
	}
	if len(a.Comments) != 1 || a.Comments[0].PR != 7 || a.Comments[0].Author != "bob" || a.Comments[0].Line != 4 {
		t.Fatalf("comments %+v", a.Comments)
	}
	if len(asked) < 3 || asked[1] != "/repos/acme/api/issues?state=open&sort=created&direction=desc&per_page=30&since=2026-11-26T22%3A00%3A00Z" {
		t.Fatalf("asked %v", asked)
	}
}
