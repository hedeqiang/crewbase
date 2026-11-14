package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestDoctorGateway(t *testing.T) {
	for addr, warn := range map[string]int{"127.0.0.1:7788": 0, "localhost:1": 0, "[::1]:7788": 0, "0.0.0.0:7788": 1, ":7788": 1, "10.0.0.5:7788": 1} {
		rep := &doctorReport{}
		doctorGateway(rep, []kernel.Entry{{ID: "gateway", Config: map[string]any{"addr": addr}}})
		if rep.warns != warn || rep.fails != 0 {
			t.Errorf("%s: warns=%d fails=%d", addr, rep.warns, rep.fails)
		}
	}
}

func TestDoctorDataDir(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	rep := &doctorReport{}
	doctorDataDir(rep, dir)
	if rep.warns != 0 || rep.fails != 0 {
		t.Fatalf("clean dir: %+v", rep)
	}
	os.Chmod(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "token"), []byte("x"), 0o644)
	rep = &doctorReport{}
	doctorDataDir(rep, dir)
	if rep.warns != 1 || rep.fails != 1 {
		t.Fatalf("loose perms: %+v", rep)
	}
}

func TestDoctorDaemonModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			w.Write([]byte(`{"ok":true}`))
		case "/api/plugins":
			w.Write([]byte(`{"plugins":[{"id":"x","state":"FAILED","error":"boom"}]}`))
		case "/api/models":
			w.Write([]byte(`{"providers":[{"id":"deepseek","kind":"api","models":[{"id":"deepseek-chat"}]},{"id":"qwen","error":"401"}]}`))
		}
	}))
	defer srv.Close()
	old := addr
	addr = srv.URL
	defer func() { addr = old }()
	rep := &doctorReport{}
	doctorDaemon(rep)
	if rep.fails != 0 || rep.warns != 2 {
		t.Fatalf("%+v", rep)
	}
	if err := doctor([]string{"--health"}); err != nil {
		t.Fatal(err)
	}
}
