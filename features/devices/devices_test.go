package devices_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

const tok = "desktop-token-for-device-tests-xxxxxxxx"

func start(t *testing.T) (string, string) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	dir := t.TempDir()
	if _, err := h.Load(gateway.Plugin, kernel.LoadOptions{Config: map[string]any{"addr": "127.0.0.1:0", "token": tok}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Load(devices.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir}}); err != nil {
		t.Fatal(err)
	}
	// a mobile-allowed and a desktop-only route to check tiers
	g := kernel.Use[*gateway.Gateway](h.Root(), gateway.ServiceKey)
	ctx := h.Root()
	g.Handle(ctx, gateway.Route{Method: "GET", Path: "/api/view", Devices: []string{"mobile"}, Handle: func(_ context.Context, r *gateway.Request) (any, error) {
		return map[string]string{"dev": r.Device, "id": r.DeviceID}, nil
	}})
	g.Handle(ctx, gateway.Route{Method: "POST", Path: "/api/rules", Handle: func(_ context.Context, r *gateway.Request) (any, error) { return nil, nil }})
	return "http://" + addr, dir
}

func call(t *testing.T, base, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func pair(t *testing.T, base, tier string) (string, map[string]any) {
	t.Helper()
	code, p := call(t, base, tok, "POST", "/api/devices/pairing", map[string]any{"tier": tier})
	if code != 200 {
		t.Fatal(code, p)
	}
	c := p["code"].(string)
	code, r := call(t, base, "", "POST", "/api/pair", map[string]any{"code": strings.ToLower(strings.ReplaceAll(c, "-", " ")), "name": "  我的\niPhone  ", "platform": "iOS"})
	if code != 200 {
		t.Fatal(code, r)
	}
	return r["token"].(string), r["device"].(map[string]any)
}

func TestPairUseRevoke(t *testing.T) {
	base, dir := start(t)
	devTok, dev := pair(t, base, "")
	if dev["tier"] != "mobile" || dev["name"] != "我的 iPhone" || !strings.HasPrefix(devTok, "cbd_") {
		t.Fatalf("paired %v %s", dev, devTok)
	}
	// the token works for mobile routes, not for desktop-only ones
	if code, out := call(t, base, devTok, "GET", "/api/view", nil); code != 200 || out["dev"] != "mobile" || out["id"] != dev["id"] {
		t.Fatal(code, out)
	}
	if code, _ := call(t, base, devTok, "POST", "/api/rules", nil); code != 403 {
		t.Fatalf("mobile reached a desktop route: %d", code)
	}
	if code, _ := call(t, base, devTok, "GET", "/api/devices", nil); code != 403 {
		t.Fatalf("mobile listed devices: %d", code)
	}
	if code, out := call(t, base, devTok, "GET", "/api/whoami", nil); code != 200 || out["tier"] != "mobile" {
		t.Fatal(code, out)
	}
	// only the hash is stored
	b, _ := os.ReadFile(filepath.Join(dir, "devices.json"))
	if strings.Contains(string(b), devTok) || !strings.Contains(string(b), `"hash"`) {
		t.Fatalf("devices.json: %s", b)
	}
	if info, _ := os.Stat(filepath.Join(dir, "devices.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("devices.json mode %v", info.Mode())
	}
	// list hides hashes
	_, list := call(t, base, tok, "GET", "/api/devices", nil)
	if s, _ := json.Marshal(list); strings.Contains(string(s), "hash") {
		t.Fatalf("list leaks hash: %s", s)
	}

	// an open event stream is closed when the device is revoked
	req, _ := http.NewRequest("GET", base+"/api/events?token="+devTok, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // hello
	closed := make(chan bool, 1)
	go func() {
		for sc.Scan() {
		}
		closed <- true
	}()
	if code, _ := call(t, base, tok, "DELETE", "/api/devices/"+dev["id"].(string), nil); code != 200 {
		t.Fatal(code)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream of a revoked device stayed open")
	}
	if code, _ := call(t, base, devTok, "GET", "/api/view", nil); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
}

func TestCodesAreSingleUseAndRateLimited(t *testing.T) {
	base, _ := start(t)
	_, p := call(t, base, tok, "POST", "/api/devices/pairing", map[string]any{"tier": "desktop"})
	c := p["code"].(string)
	if code, _ := call(t, base, "", "POST", "/api/pair", map[string]any{"code": c, "name": "MacBook"}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := call(t, base, "", "POST", "/api/pair", map[string]any{"code": c, "name": "again"}); code != 401 {
		t.Fatalf("code reused: %d", code)
	}
	if code, _ := call(t, base, tok, "POST", "/api/devices/pairing", map[string]any{"tier": "root"}); code != 400 {
		t.Fatalf("unknown tier: %d", code)
	}
	if code, _ := call(t, base, "", "POST", "/api/devices/pairing", nil); code != 401 {
		t.Fatalf("pairing without the desktop token: %d", code)
	}
	limited := false
	for i := 0; i < 25; i++ {
		code, _ := call(t, base, "", "POST", "/api/pair", map[string]any{"code": "AAAA-AAAA"})
		if code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("redeem is not rate-limited")
	}
}

func TestExpiryAndDesktopTier(t *testing.T) {
	s, err := devices.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.StartPairing(devices.TierDesktop)
	if len(s.Pending()) != 1 {
		t.Fatal("pending")
	}
	got, err := s.Redeem(p.Code, "Mac mini", "", "relay")
	if err != nil || got.Device.Tier != "desktop" || got.Device.Via != "relay" {
		t.Fatal(got, err)
	}
	if _, ok := s.Verify(got.Token); !ok {
		t.Fatal("verify")
	}
	if _, ok := s.Verify(got.Token + "x"); ok {
		t.Fatal("wrong token accepted")
	}
	if _, ok := s.Verify("not-a-device-token"); ok {
		t.Fatal("foreign token accepted")
	}
	// reload from disk
	dir := t.TempDir()
	s1, _ := devices.New(dir)
	p, _ = s1.StartPairing("")
	r, _ := s1.Redeem(p.Code, "Pixel", "", "")
	s2, _ := devices.New(dir)
	if v, ok := s2.Verify(r.Token); !ok || v.Name != "Pixel" {
		t.Fatal("not persisted")
	}
}
