package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	mygoproxy "github.com/egoist/mygo/netproxy"

	"github.com/hedeqiang/crewbase/internal/netproxy"
)

// App updates (and MyGo's fetch and websocket plugins) go through the proxy
// set in Settings › 网络代理, like everything else Crewbase does online.
// MyGo makes those requests on one shared transport, whose Proxy is
// replaced here:
//
//   - crewd in this process: its setting (internal/netproxy), local
//     addresses direct
//   - attached to a crewd elsewhere: that crewd's setting, asked over its
//     API; its password stays in its vault, so a proxy with a login is
//     left to the system's settings there
//   - no proxy set: MyGo's own choice, the system's proxy settings or the
//     environment's, as before
func init() {
	mygoproxy.Transport().Proxy = appProxy
}

// proxyDaemon is the crewd the shell talks to, once started.
var proxyDaemon atomic.Pointer[daemon]

func appProxy(req *http.Request) (*url.URL, error) {
	if _, on := netproxy.Current(); on {
		return netproxy.Configured(req)
	}
	if d := proxyDaemon.Load(); d != nil && d.host == nil {
		if match := attached.get(d); match != nil {
			return match(req.URL)
		}
	}
	return mygoproxy.Proxy(req)
}

// attached is the proxy setting of a crewd in another process, asked again
// after a while.
var attached attachedProxy

type attachedProxy struct {
	mu     sync.Mutex
	until  time.Time
	match  func(*url.URL) (*url.URL, error)
	warned bool
}

const attachedTTL = 10 * time.Second

func (a *attachedProxy) get(d *daemon) func(*url.URL) (*url.URL, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().Before(a.until) {
		return a.match
	}
	a.until = time.Now().Add(attachedTTL)
	st, err := fetchProxySetting(d)
	switch {
	case err != nil || !st.Enabled:
		a.match = nil
	case st.HasPassword:
		if !a.warned {
			slog.Warn("desktop: the proxy has a login that stays in crewd's vault; updates use the system's proxy settings")
			a.warned = true
		}
		a.match = nil
	default:
		a.match = netproxy.Matcher(netproxy.Proxy{Type: st.Type, Host: st.Host, Port: st.Port, Username: st.Username, Bypass: st.Bypass})
	}
	return a.match
}

type proxySetting struct {
	Enabled     bool     `json:"enabled"`
	Type        string   `json:"type"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	HasPassword bool     `json:"hasPassword"`
	Bypass      []string `json:"bypass"`
}

// fetchProxySetting asks crewd (on loopback, never through a proxy).
func fetchProxySetting(d *daemon) (proxySetting, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+d.addr+"/api/settings/proxy", nil)
	req.Header.Set("Authorization", "Bearer "+d.token)
	cl := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := cl.Do(req)
	if err != nil {
		return proxySetting{}, err
	}
	defer resp.Body.Close()
	var v struct {
		Settings proxySetting `json:"settings"`
	}
	if resp.StatusCode != http.StatusOK {
		return proxySetting{}, fmt.Errorf("crewd answered %s", resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v.Settings, err
}
