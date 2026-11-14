package slack_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/im/slack"
)

func TestBlocksAndInbound(t *testing.T) {
	card := im.ApprovalCard(approval.Request{Agent: "kai", Category: "exec", Summary: "run `make`"}, "Kai", "tok")
	b, _ := json.Marshal(slack.Blocks(card))
	s := string(b)
	for _, want := range []string{`"type":"header"`, "Kai 等你批准", `"type":"actions"`, `"style":"primary"`, `"style":"danger"`, "ap:tok:allow", im.LabelLater} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	push := im.ApprovalCard(approval.Request{Agent: "kai", Category: "push"}, "Kai", "tok")
	if b, _ := json.Marshal(slack.Blocks(push)); strings.Contains(string(b), "ap:tok:allow") {
		t.Fatal("push card must not have approve")
	}
	done := card
	done.Buttons, done.Footer = nil, "✅ 已批准"
	if b, _ := json.Marshal(slack.Blocks(done)); strings.Contains(string(b), `"actions"`) || !strings.Contains(string(b), "已批准") {
		t.Fatal(string(b))
	}

	in, ok := slack.Inbound(&slackevents.AppMentionEvent{User: "U7", Text: "<@UBOT> @kai hi", TimeStamp: "1.1", Channel: "C1"}, "UBOT")
	if !ok || in.Text != "@kai hi" || !in.Direct || in.MessageID != "1.1" {
		t.Fatalf("%+v", in)
	}
	if _, ok := slack.Inbound(&slackevents.MessageEvent{User: "U7", Text: "<@UBOT> hi", Channel: "C1", ChannelType: "channel"}, "UBOT"); ok {
		t.Fatal("channel message with bot mention is handled as app_mention")
	}
	if _, ok := slack.Inbound(&slackevents.MessageEvent{User: "UBOT", Text: "echo", ChannelType: "im"}, "UBOT"); ok {
		t.Fatal("own messages are dropped")
	}
	in, ok = slack.Inbound(&slackevents.MessageEvent{User: "U7", Text: "more", Channel: "D1", ChannelType: "im", TimeStamp: "2.2", ThreadTimeStamp: "1.1"}, "UBOT")
	if !ok || !in.Direct || in.Thread != "1.1" {
		t.Fatalf("%+v", in)
	}

	var cb goslack.InteractionCallback
	json.Unmarshal([]byte(`{"type":"block_actions","user":{"id":"U7","name":"ada"},"channel":{"id":"C1"},
		"container":{"message_ts":"9.9","channel_id":"C1"},"actions":[{"block_id":"b","action_id":"crewbase_0","value":"ap:t:allow"},{"block_id":"b","action_id":"other","value":"x"}]}`), &cb)
	acts := slack.Actions(cb)
	if len(acts) != 1 || acts[0].Value != "ap:t:allow" || acts[0].User.ID != "U7" || acts[0].Ref.ID != "9.9" {
		t.Fatalf("%+v", acts)
	}
}

// fakeSlack serves the Web API and a Socket Mode websocket.
type fakeSlack struct {
	srv   *httptest.Server
	mu    sync.Mutex
	posts []map[string]string
	edits []map[string]string
	ephem []map[string]string
	conn  *websocket.Conn
	ready chan struct{}
	acks  chan string
	seq   int
}

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{ready: make(chan struct{}, 4), acks: make(chan string, 16)}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v map[string]any) {
		v["ok"] = true
		json.NewEncoder(w).Encode(v)
	}
	form := func(r *http.Request) map[string]string {
		r.ParseForm()
		m := map[string]string{}
		for k, v := range r.PostForm {
			m[k] = v[0]
		}
		return m
	}
	mux.HandleFunc("/api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r, "xoxb-test") {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		reply(w, map[string]any{"user_id": "UBOT"})
	})
	mux.HandleFunc("/api/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r, "xapp-test") {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		reply(w, map[string]any{"url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws"})
	})
	mux.HandleFunc("/api/users.info", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"user": map[string]any{"id": "U7", "name": "ada", "profile": map[string]any{"display_name": "Ada"}}})
	})
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		m := form(r)
		f.mu.Lock()
		f.seq++
		ts := fmt.Sprintf("100.%04d", f.seq)
		f.posts = append(f.posts, m)
		f.mu.Unlock()
		reply(w, map[string]any{"channel": m["channel"], "ts": ts})
	})
	mux.HandleFunc("/api/chat.update", func(w http.ResponseWriter, r *http.Request) {
		m := form(r)
		f.mu.Lock()
		f.edits = append(f.edits, m)
		f.mu.Unlock()
		reply(w, map[string]any{"channel": m["channel"], "ts": m["ts"], "text": ""})
	})
	mux.HandleFunc("/api/chat.postEphemeral", func(w http.ResponseWriter, r *http.Request) {
		m := form(r)
		f.mu.Lock()
		f.ephem = append(f.ephem, m)
		f.mu.Unlock()
		reply(w, map[string]any{"message_ts": "1"})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c.WriteJSON(map[string]any{"type": "hello", "num_connections": 1})
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		f.ready <- struct{}{}
		for {
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			if id, _ := m["envelope_id"].(string); id != "" {
				f.acks <- id
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) send(t *testing.T, env map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.conn.WriteJSON(env); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeSlack) Posts() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.posts...)
}

func (f *fakeSlack) Edits() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.edits...)
}

func (f *fakeSlack) Ephemerals() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.ephem...)
}

func TestSocketModeEndToEnd(t *testing.T) {
	e := imtest.New(t)
	f := newFakeSlack(t)
	e.Creds.Set("mem:slack-bot", "xoxb-test")
	e.Creds.Set("mem:slack-app", "xapp-test")
	cfg := map[string]any{"botTokenRef": "mem:slack-bot", "appTokenRef": "mem:slack-app", "channel": "C1",
		"allowUsers": []string{"U7"}, "relayReplies": true, "apiURL": f.srv.URL + "/api/"}
	if _, err := e.Host.Load(slack.Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	g, _ := kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"slack")
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("no ws: %+v\n%s", g.Status(), e.Logs.String())
	}
	g, _ = kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"slack")
	imtest.Eventually(t, func() bool { return g.Status().Connected })

	// approval: card → press by a stranger (ignored) → press by U7
	kai := e.Agent("Kai")
	done := e.RunTool(kai, "deploy")
	imtest.Eventually(t, func() bool { return len(f.Posts()) == 1 })
	card := f.Posts()[0]
	if card["channel"] != "C1" || !strings.Contains(card["blocks"], "ap:") {
		t.Fatalf("%v", card)
	}
	var blocks []map[string]any
	json.Unmarshal([]byte(card["blocks"]), &blocks)
	var allow string
	for _, b := range blocks {
		if b["type"] == "actions" {
			allow = b["elements"].([]any)[0].(map[string]any)["value"].(string)
		}
	}
	press := func(env, user string) {
		f.send(t, map[string]any{"envelope_id": env, "type": "interactive", "payload": map[string]any{
			"type": "block_actions", "user": map[string]any{"id": user, "name": user}, "channel": map[string]any{"id": "C1"},
			"container": map[string]any{"message_ts": "100.0001", "channel_id": "C1"},
			"actions":   []any{map[string]any{"block_id": "crewbase_actions", "action_id": "crewbase_0", "value": allow}}}})
		if id := imtest.Recv(t, (<-chan string)(f.acks)); id != env {
			t.Fatal(id)
		}
	}
	press("e1", "U666")
	imtest.Eventually(t, func() bool { return len(f.Ephemerals()) == 1 })
	press("e2", "U7")
	if exe := imtest.Recv(t, done); exe.Outcome != approval.OutcomeAllowedOnce {
		t.Fatalf("%+v", exe)
	}
	imtest.Eventually(t, func() bool { return len(f.Edits()) == 1 && strings.Contains(f.Edits()[0]["blocks"], "已批准") })

	// inbound @kai → enqueued → reply relayed into the thread
	queued := make(chan agent.Input, 4)
	agent.QueueChanged.Listen(e.Host.Root(), func(id string) {
		for _, in := range kai.Queue() {
			queued <- in
		}
	})
	f.send(t, map[string]any{"envelope_id": "e3", "type": "events_api", "payload": map[string]any{
		"type": "event_callback", "event": map[string]any{"type": "app_mention", "user": "U7", "text": "<@UBOT> @kai hello", "ts": "200.1", "channel": "C1"}}})
	in := imtest.Recv(t, queued)
	if in.Text != "hello" || in.From != "Ada" {
		t.Fatalf("%+v", in)
	}
	kai.Claim()
	e.EndTurn(kai, in, "hi from kai")
	imtest.Eventually(t, func() bool {
		for _, p := range f.Posts() {
			if strings.Contains(p["text"], "hi from kai") && p["thread_ts"] == "200.1" {
				return true
			}
		}
		return false
	})
	if strings.Contains(e.Logs.String(), "xoxb-test") || strings.Contains(e.Logs.String(), "xapp-test") {
		t.Fatal("token in logs")
	}
	e.Host.Unload(slack.Plugin.Name)
}

func authed(r *http.Request, tok string) bool {
	r.ParseForm()
	return r.Header.Get("Authorization") == "Bearer "+tok || r.PostForm.Get("token") == tok
}
