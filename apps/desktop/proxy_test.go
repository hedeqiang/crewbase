package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mygoproxy "github.com/egoist/mygo/netproxy"

	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/internal/netproxy/proxytest"
)

// The client MyGo's updater downloads with (httpGet builds it on
// mygoproxy.Transport) goes through the proxy set in Crewbase.
func TestUpdatesGoThroughTheProxy(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil); proxyDaemon.Store(nil); attached = attachedProxy{} })
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "update") }))
	defer release.Close()
	_, port, _ := net.SplitHostPort(release.Listener.Addr().String())
	target := "github.example.test:" + port
	get := func() (string, error) {
		resp, err := (&http.Client{Transport: mygoproxy.Transport(), Timeout: 5 * time.Second}).Get("http://" + target + "/update-darwin-arm64.json")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}

	// crewd in this process, a proxy with a login
	p := proxytest.SOCKS5(t, "bob", "s3cret")
	p.Route(target, release.Listener.Addr().String())
	netproxy.Set(&netproxy.Proxy{Type: netproxy.SOCKS5, Host: p.Host, Port: p.Port, Username: "bob", Password: "s3cret"})
	if body, err := get(); err != nil || body != "update" || p.Hits.Load() != 1 {
		t.Fatalf("in-process: %q %v, hits %d", body, err, p.Hits.Load())
	}
	// loopback stays direct
	if u, _ := appProxy(httptest.NewRequest("GET", release.URL, nil)); u != nil {
		t.Fatalf("loopback through %v", u)
	}

	// attached to a crewd elsewhere: its setting, asked over its API
	netproxy.Set(nil)
	hp := proxytest.HTTP(t, "", "")
	hp.Route(target, release.Listener.Addr().String())
	setting := proxySetting{Enabled: true, Type: "http", Host: hp.Host, Port: hp.Port, Bypass: netproxy.DefaultBypass}
	crewd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/settings/proxy" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"settings": setting})
	}))
	defer crewd.Close()
	proxyDaemon.Store(&daemon{addr: crewd.Listener.Addr().String(), token: "tok"})
	if body, err := get(); err != nil || body != "update" || hp.Hits.Load() != 1 {
		t.Fatalf("attached: %q %v, hits %d", body, err, hp.Hits.Load())
	}

	// its proxy needs a login kept in its vault: left to the system's settings
	setting.HasPassword = true
	attached = attachedProxy{}
	if u, _ := appProxy(httptest.NewRequest("GET", "http://"+target+"/", nil)); u != nil && u.Host == hp.Addr {
		t.Fatal("used a proxy whose password is not known here")
	}
	// switched off there: not through it either
	setting.HasPassword, setting.Enabled = false, false
	attached = attachedProxy{}
	if u, _ := appProxy(httptest.NewRequest("GET", "http://"+target+"/", nil)); u != nil && u.Host == hp.Addr {
		t.Fatal("used a proxy that is off")
	}
}
