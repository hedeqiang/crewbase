package agentdocs

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// Another program (Nacos uses 8848 too) listening on every address takes
// the docs port while the docs server is not up yet; the docs server then
// binds 127.0.0.1 on the same port. The start check must not stay on a
// kept-alive connection to the other program.
func TestWaitUpIsNotFooledByAnotherServerOnThePort(t *testing.T) {
	other, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := other.Addr().(*net.TCPAddr).Port
	go http.Serve(other, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	t.Cleanup(func() { other.Close() })

	// the docs server comes up a moment later, on 127.0.0.1 only
	go func() {
		time.Sleep(400 * time.Millisecond)
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Errorf("bind next to the other server: %v", err)
			return
		}
		t.Cleanup(func() { l.Close() })
		http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"signup":false,"oauth":[]}`))
		}))
	}()
	if err := waitUp(context.Background(), localURL(port), 5*time.Second); err != nil {
		t.Fatalf("waitUp: %v", err)
	}
}

// A port another program listens on (any address) is not offered.
func TestFreePortSkipsAPortTakenOnEveryAddress(t *testing.T) {
	other, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	taken := other.Addr().(*net.TCPAddr).Port
	got, err := freePort(taken)
	if err != nil {
		t.Fatal(err)
	}
	if got == taken {
		t.Fatalf("freePort gave %d, which another program listens on", taken)
	}
}

// A managed server whose port another program also listens on moves to a
// free one, and the connection (so every agent's MCP server) follows.
func TestMovePortOffASharedPort(t *testing.T) {
	other, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	taken := other.Addr().(*net.TCPAddr).Port
	m := &Managed{Version: "v0.3.1", Port: taken}
	s := &Service{dir: t.TempDir()}
	s.st.Managed = m
	s.st.Settings.URL = localURL(taken)
	s.movePort(m)
	if m.Port == taken || !portFree(m.Port) {
		t.Fatalf("port: %d (was %d)", m.Port, taken)
	}
	if s.st.Settings.URL != localURL(m.Port) {
		t.Fatalf("url: %s", s.st.Settings.URL)
	}
	// a port nobody else uses stays
	free := m.Port
	s.movePort(m)
	if m.Port != free {
		t.Fatalf("moved off a free port: %d → %d", free, m.Port)
	}
}
