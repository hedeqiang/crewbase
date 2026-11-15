package google

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeGoogle is an httptest stand-in for Google's OAuth endpoints and the
// parts of the Workspace APIs the tools call.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	challenges map[string]authReq // code → the authorization request
	refresh    map[string][]string
	access     map[string]bool
	revoked    []string
	grantLess  []string // scopes the "person" unticks on the consent screen
	rejectAll  bool     // refresh answers invalid_grant
	fail429    map[string]int
	calls      []string // "METHOD path"
	bodies     map[string]string
	nextCode   int
	bigBody    bool
}

type authReq struct {
	challenge, redirect, clientID string
	scopes                        []string
}

const (
	testClientID     = "test-client.apps.googleusercontent.com"
	testClientSecret = "fake-desktop-client-secret"
)

func newFake(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{t: t, challenges: map[string]authReq{}, refresh: map[string][]string{}, access: map[string]bool{},
		fail429: map[string]int{}, bodies: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func gerr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

// consent plays the person approving on Google's page: it returns the
// redirect URL (with code and state) for authURL.
func (f *fakeGoogle) consent(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		f.t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("access_type") != "offline" {
		f.t.Fatalf("auth request: %s", authURL)
	}
	f.mu.Lock()
	f.nextCode++
	code := fmt.Sprintf("code-%d", f.nextCode)
	scopes := strings.Fields(q.Get("scope"))
	var granted []string
	for _, s := range scopes {
		drop := false
		for _, x := range f.grantLess {
			drop = drop || s == x
		}
		if !drop {
			granted = append(granted, s)
		}
	}
	f.challenges[code] = authReq{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), clientID: q.Get("client_id"), scopes: granted}
	f.mu.Unlock()
	return q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}, "scope": {strings.Join(granted, " ")}}.Encode()
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	f.bodies[key] = string(body)
	if n := f.fail429[r.URL.Path]; n > 0 {
		f.fail429[r.URL.Path] = n - 1
		f.mu.Unlock()
		gerr(w, 429, "slow down")
		return
	}
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/o/oauth2/v2/auth":
		// a browser following the sign-in link: consent at once
		http.Redirect(w, r, f.consent(r.URL.String()), http.StatusFound)
		return
	case r.URL.Path == "/token":
		form, _ := url.ParseQuery(string(body))
		f.token(w, form)
		return
	case r.URL.Path == "/revoke":
		form, _ := url.ParseQuery(string(body))
		f.mu.Lock()
		f.revoked = append(f.revoked, form.Get("token"))
		delete(f.refresh, form.Get("token"))
		f.mu.Unlock()
		w.WriteHeader(200)
		return
	}
	// everything else needs a valid access token
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	ok := f.access[tok]
	f.mu.Unlock()
	if !ok {
		gerr(w, 401, "Request had invalid authentication credentials.")
		return
	}
	f.api(w, r, body)
}

func (f *fakeGoogle) token(w http.ResponseWriter, form url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if form.Get("client_id") != testClientID || form.Get("client_secret") != testClientSecret {
		writeJSON(w, 401, map[string]string{"error": "invalid_client", "error_description": "The OAuth client was not found."})
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		req, ok := f.challenges[form.Get("code")]
		delete(f.challenges, form.Get("code"))
		h := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(h[:]) != req.challenge || form.Get("redirect_uri") != req.redirect {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "Bad Request"})
			return
		}
		rt, at := fmt.Sprintf("rt-%s", form.Get("code")), fmt.Sprintf("at-%s", form.Get("code"))
		f.refresh[rt], f.access[at] = req.scopes, true
		writeJSON(w, 200, map[string]any{"access_token": at, "expires_in": 3599, "refresh_token": rt,
			"scope": strings.Join(req.scopes, " "), "token_type": "Bearer"})
	case "refresh_token":
		sc, ok := f.refresh[form.Get("refresh_token")]
		if !ok || f.rejectAll {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			return
		}
		f.nextCode++
		at := fmt.Sprintf("at-refreshed-%d", f.nextCode)
		f.access[at] = true
		writeJSON(w, 200, map[string]any{"access_token": at, "expires_in": 3599, "scope": strings.Join(sc, " "), "token_type": "Bearer"})
	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func (f *fakeGoogle) api(w http.ResponseWriter, r *http.Request, body []byte) {
	p := r.URL.Path
	switch {
	case p == "/v1/userinfo":
		writeJSON(w, 200, map[string]any{"sub": "1", "email": "me@example.com", "email_verified": true})

	// ---------------------------------------------------------------- Gmail
	case p == "/gmail/v1/users/me/messages" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"messages": []map[string]string{{"id": "m1"}, {"id": "m2"}}, "resultSizeEstimate": 2})
	case strings.HasPrefix(p, "/gmail/v1/users/me/messages/m") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/gmail/v1/users/me/messages/")
		html := "<html><head><style>x{}</style></head><body><p>Hello &amp; welcome</p><script>evil()</script><ul><li>one</li></ul></body></html>"
		if f.bigBody {
			html = "<p>" + strings.Repeat("lorem ipsum ", 10000) + "</p>"
		}
		writeJSON(w, 200, map[string]any{
			"id": id, "threadId": "t-" + id, "labelIds": []string{"INBOX", "UNREAD"}, "snippet": "Hello &amp; welcome",
			"payload": map[string]any{
				"mimeType": "multipart/mixed",
				"headers": []map[string]string{{"name": "From", "value": "Ann <ann@example.com>"}, {"name": "Subject", "value": "=?UTF-8?B?5L2g5aW9?="},
					{"name": "Date", "value": "Mon, 16 Nov 2026 09:00:00 +0800"}, {"name": "Message-ID", "value": "<orig@example.com>"}},
				"parts": []map[string]any{
					{"mimeType": "multipart/alternative", "parts": []map[string]any{
						{"mimeType": "text/html", "body": map[string]any{"data": b64(html)}},
					}},
					{"mimeType": "application/pdf", "filename": "invoice.pdf", "body": map[string]any{"size": 52000, "attachmentId": "a1"}},
				},
			},
		})
	case p == "/gmail/v1/users/me/drafts" && r.Method == "POST":
		writeJSON(w, 200, map[string]any{"id": "d1", "message": map[string]string{"id": "m9"}})
	case p == "/gmail/v1/users/me/drafts/d1":
		writeJSON(w, 200, map[string]any{"id": "d1", "message": map[string]any{"id": "m9", "payload": map[string]any{
			"headers": []map[string]string{{"name": "To", "value": "bob@example.com"}, {"name": "Subject", "value": "Plan"}}}}})
	case p == "/gmail/v1/users/me/messages/send", p == "/gmail/v1/users/me/drafts/send":
		writeJSON(w, 200, map[string]any{"id": "m10", "threadId": "t10"})

	// ---------------------------------------------------------------- Calendar
	case p == "/calendar/v3/calendars/primary/events" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"timeZone": "Asia/Shanghai", "items": []map[string]any{
			{"id": "e1", "summary": "Standup", "start": map[string]string{"dateTime": "2026-11-16T09:00:00+08:00"}, "end": map[string]string{"dateTime": "2026-11-16T09:15:00+08:00"}},
		}})
	case p == "/calendar/v3/calendars/primary/events" && r.Method == "POST":
		var ev map[string]any
		_ = json.Unmarshal(body, &ev)
		ev["id"] = "e2"
		writeJSON(w, 200, ev)
	case p == "/calendar/v3/calendars/primary/events/e1" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"id": "e1", "summary": "Standup", "attendees": []map[string]any{{"email": "me@example.com", "self": true}, {"email": "ann@example.com"}}})
	case p == "/calendar/v3/calendars/primary/events/e3" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"id": "e3", "summary": "Focus"})
	case strings.HasPrefix(p, "/calendar/v3/calendars/primary/events/") && (r.Method == "PATCH" || r.Method == "DELETE"):
		writeJSON(w, 200, map[string]any{"id": strings.TrimPrefix(p, "/calendar/v3/calendars/primary/events/"), "summary": "x"})
	case p == "/calendar/v3/freeBusy":
		writeJSON(w, 200, map[string]any{"calendars": map[string]any{"primary": map[string]any{"busy": []map[string]string{{"start": "2026-11-16T01:00:00Z", "end": "2026-11-16T02:00:00Z"}}}}})

	// ---------------------------------------------------------------- Drive
	case p == "/drive/v3/files":
		writeJSON(w, 200, map[string]any{"files": []map[string]any{{"id": "f1", "name": "Budget", "mimeType": mimeSheet, "modifiedTime": "2026-11-01T00:00:00Z"}}})
	case p == "/drive/v3/files/f1" && r.URL.Query().Get("alt") == "":
		writeJSON(w, 200, map[string]any{"id": "f1", "name": "Budget", "mimeType": mimeSheet})
	case p == "/drive/v3/files/f1/export":
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "a,b\n1,2\n")
	case p == "/drive/v3/files/bin":
		writeJSON(w, 200, map[string]any{"id": "bin", "name": "photo.jpg", "mimeType": "image/jpeg", "size": "2048000"})
	case p == "/upload/drive/v3/files":
		writeJSON(w, 200, map[string]any{"id": "up1", "name": "notes.txt", "mimeType": "text/plain"})
	case strings.HasSuffix(p, "/permissions"):
		writeJSON(w, 200, map[string]any{"id": "perm1"})

	// ---------------------------------------------------------------- Docs / Sheets / Slides
	case p == "/v1/documents/doc1":
		writeJSON(w, 200, map[string]any{"documentId": "doc1", "title": "Spec", "body": map[string]any{"content": []map[string]any{
			{"paragraph": map[string]any{"elements": []map[string]any{{"textRun": map[string]string{"content": "Hello doc\n"}}}}},
		}}})
	case p == "/v1/documents" && r.Method == "POST":
		writeJSON(w, 200, map[string]any{"documentId": "doc2"})
	case strings.HasPrefix(p, "/v1/documents/") && strings.HasSuffix(p, ":batchUpdate"):
		writeJSON(w, 200, map[string]any{"replies": []map[string]any{{"replaceAllText": map[string]int{"occurrencesChanged": 3}}}})
	case strings.HasPrefix(p, "/v4/spreadsheets/s1/values/") && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"range": "Sheet1!A1:B2", "values": [][]any{{"Name", "Total"}, {"Ann", "42"}}})
	case strings.HasPrefix(p, "/v4/spreadsheets/s1/values/") && r.Method == "PUT":
		writeJSON(w, 200, map[string]any{"updatedRange": "Sheet1!A1:B2", "updatedCells": 4})
	case strings.HasPrefix(p, "/v4/spreadsheets/s1/values/") && r.Method == "POST":
		writeJSON(w, 200, map[string]any{"updates": map[string]any{"updatedRange": "Sheet1!A3:B3", "updatedCells": 2}})
	case p == "/v1/presentations" && r.Method == "POST":
		writeJSON(w, 200, map[string]any{"presentationId": "p1", "slides": []map[string]string{{"objectId": "blank"}}})
	case p == "/v1/presentations/p1:batchUpdate":
		writeJSON(w, 200, map[string]any{})
	case p == "/v1/presentations/p1":
		writeJSON(w, 200, map[string]any{"presentationId": "p1", "title": "Deck", "slides": []map[string]any{
			{"pageElements": []map[string]any{{"shape": map[string]any{"text": map[string]any{"textElements": []map[string]any{{"textRun": map[string]string{"content": "Intro"}}}}}}}},
		}})
	default:
		gerr(w, 404, "no fake for "+r.Method+" "+p)
	}
}
