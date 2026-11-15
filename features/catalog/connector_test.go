package catalog

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

type fakeConnector struct {
	mu        sync.Mutex
	installed map[string]string // product → scope
}

func (f *fakeConnector) State(p string) ConnectorState {
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.installed[p]
	return ConnectorState{Installed: ok, Scope: sc, Configured: true, Account: "me@example.com"}
}

func (f *fakeConnector) Connect(p, scope string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed[p] = scope // a real connector waits for the sign-in
	return "https://accounts.example.com/auth?p=" + p, nil
}

func (f *fakeConnector) Disconnect(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.installed, p)
	return nil
}

// Builtin entries go to the connector their plugin provides: listing state,
// install → sign-in URL, uninstall → disconnect; without it they say so.
func TestBuiltinEntriesUseTheirConnector(t *testing.T) {
	e := newEnv(t)
	if it := e.listing(t)["gmail"]; it.Kind != KindBuiltin || it.Builtin != "google" || it.Product != "gmail" ||
		it.Installed || it.Connector == nil || it.Connector.Configured || it.Connector.Note.En == "" {
		t.Fatalf("without a connector: %+v", it)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/gmail/install", Request{}); code != 404 {
		t.Fatal("install without a connector", code)
	}

	f := &fakeConnector{installed: map[string]string{}}
	if err := e.h.Root().Fork("").Provide(ConnectorKey("google"), Connector(f)); err != nil {
		t.Fatal(err)
	}
	code, raw := e.do(t, "POST", "/api/catalog/google-calendar/install", Request{})
	if code != 200 {
		t.Fatal(code, raw)
	}
	var r Result
	_ = json.Unmarshal([]byte(raw), &r)
	if r.Kind != KindBuiltin || r.AuthURL != "https://accounts.example.com/auth?p=calendar" {
		t.Fatalf("%+v", r)
	}
	if it := e.listing(t)["google-calendar"]; !it.Installed || it.Connector == nil || it.Connector.Account != "me@example.com" {
		t.Fatalf("%+v", it)
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/google-calendar", nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/google-calendar", nil); code != 404 {
		t.Fatal("second uninstall", code)
	}
	if e.listing(t)["google-calendar"].Installed {
		t.Fatal("still installed")
	}
}
