package relay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHookIntake(t *testing.T) {
	srv := NewServer(ServerConfig{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id, err := LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHost(id, ts.URL, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("ab", 24)
	type got struct {
		token string
		sig   string
		body  string
	}
	seen := make(chan got, 4)
	h.SetHooks(func() []string { return []string{token, "short"} }, func(_ context.Context, tk string, hd http.Header, b []byte) int {
		seen <- got{tk, hd.Get("X-Hub-Signature-256"), string(b)}
		if hd.Get("X-Fail") != "" {
			return 401
		}
		return 202
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	// wait until the relay knows the token
	for i := 0; ; i++ {
		srv.hooks().mu.Lock()
		ok := srv.hooks().owner[token] != nil
		short := srv.hooks().owner["short"] != nil
		srv.hooks().mu.Unlock()
		if ok {
			if short {
				t.Fatal("a short token was accepted")
			}
			break
		}
		if i > 200 {
			t.Fatal("token never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	send := func(tk string, hdr map[string]string, body string) (int, string) {
		req, _ := http.NewRequest("POST", ts.URL+"/v1/hooks/"+tk, bytes.NewReader([]byte(body)))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// the host's answer reaches the sender; headers and body arrive as sent
	if st, _ := send(token, map[string]string{"X-Hub-Signature-256": "sha256=abc", "Cookie": "x"}, `{"a":1}`); st != 202 {
		t.Fatal(st)
	}
	g := <-seen
	if g.token != token || g.sig != "sha256=abc" || g.body != `{"a":1}` {
		t.Fatalf("%+v", g)
	}
	if st, _ := send(token, map[string]string{"X-Fail": "1"}, `{}`); st != 401 {
		t.Fatal(st)
	}
	<-seen
	// an unknown token
	if st, _ := send(strings.Repeat("cd", 24), nil, `{}`); st != 404 {
		t.Fatal(st)
	}
	// the host stops serving it: the relay forgets
	h.SetHooks(func() []string { return nil }, nil)
	for i := 0; ; i++ {
		st, _ := send(token, nil, `{}`)
		if st == 404 {
			break
		}
		if i > 100 {
			t.Fatal("token still served", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// a relay with hooks off
	off := httptest.NewServer(NewServer(ServerConfig{NoHooks: true}).Handler())
	defer off.Close()
	resp, _ := http.Post(off.URL+"/v1/hooks/"+token, "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
}
