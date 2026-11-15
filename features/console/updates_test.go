package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.3.0", "0.2.0", true}, {"0.2.0", "0.2.0", false}, {"0.2.0", "0.10.0", false},
		{"1.0.0", "0.9.9", true}, {"0.2.0", "0.2.0-dev", true}, {"0.2.0-beta.2", "0.2.0-beta.1", true},
		{"0.1.0", "0.1.0-dev", true}, {"v0.3.0", "0.2.1", true},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo/releases/latest/download/update-linux-amd64.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"version":"9.0.0","notes":"- big\n\n## 安装\n\ncurl …"}`))
	}))
	defer srv.Close()
	old := Releases
	Releases = srv.URL + "/repo/releases/latest"
	defer func() { Releases = old }()
	kernel.Version = "0.2.0"
	u := &updates{enabled: true, client: srv.Client()}
	i := u.check(context.Background())
	if !i.Available || i.Latest != "9.0.0" || i.Current != "0.2.0" || i.Notes != "- big" || i.URL != srv.URL+"/repo/releases/tag/v9.0.0" || i.Error != "" {
		t.Fatalf("%+v", i)
	}
	Releases = srv.URL + "/missing/releases/latest"
	if i := u.check(context.Background()); i.Error == "" || i.Latest != "9.0.0" {
		t.Fatalf("a failed check should keep the last result: %+v", i)
	}
}
