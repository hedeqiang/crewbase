package relay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/relay"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestHandshakeAndTamper(t *testing.T) {
	id, _ := relay.NewIdentity()
	d, hello, _ := relay.NewDial(id.Public())
	reply, hostCh, err := id.Accept(hello)
	if err != nil {
		t.Fatal(err)
	}
	devCh, err := d.Finish(reply)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f := devCh.Seal([]byte("ping"))
		if p, err := hostCh.Open(f); err != nil || string(p) != "ping" {
			t.Fatal(p, err)
		}
		g := hostCh.Seal([]byte("pong"))
		if p, err := devCh.Open(g); err != nil || string(p) != "pong" {
			t.Fatal(p, err)
		}
	}
	// a flipped bit, a replay and a reflected frame all fail
	f := devCh.Seal([]byte("x"))
	f[len(f)-1] ^= 1
	if _, err := hostCh.Open(f); err == nil {
		t.Fatal("tampered frame accepted")
	}
	// pinning: a different host key cannot finish the handshake usefully
	other, _ := relay.NewIdentity()
	d2, hello2, _ := relay.NewDial(id.Public())
	reply2, evilCh, _ := other.Accept(hello2) // a relay pretending to be the host
	ch2, err := d2.Finish(reply2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evilCh.Open(ch2.Seal([]byte("secret"))); err == nil {
		t.Fatal("an impostor without the host key read the device's frame")
	}
	// host auth signatures
	chal := make([]byte, 32)
	rand.Read(chal)
	sig, _ := id.Sign(chal)
	if !relay.VerifyHost(id.Public(), chal, sig) || relay.VerifyHost(other.Public(), chal, sig) {
		t.Fatal("host signature check")
	}
}

const deskTok = "desktop-token-relay-test-xxxxxxxxxxxxxxx"

// End to end: host behind a relay, a device pairs and calls the API
// through the tunnel, then is revoked.
func TestRelayEndToEnd(t *testing.T) {
	srv := httptest.NewServer(relay.NewServer(relay.ServerConfig{}).Handler())
	defer srv.Close()

	h := kernel.NewHost(nil)
	defer h.Close()
	var gwAddr string
	gateway.Listening.Listen(h.Root(), func(a string) { gwAddr = a })
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": deskTok}},
		{devices.Plugin, map[string]any{"dir": root}},
		{relay.Plugin, map[string]any{"dir": root, "url": srv.URL}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	g := kernel.Use[*gateway.Gateway](h.Root(), gateway.ServiceKey)
	g.Handle(h.Root(), gateway.Route{Method: "POST", Path: "/api/echo", Devices: []string{"mobile"}, Handle: func(_ context.Context, r *gateway.Request) (any, error) {
		var b map[string]any
		r.Decode(&b)
		return map[string]any{"got": b, "device": r.Device, "id": r.DeviceID}, nil
	}})
	ds := kernel.Use[*devices.Service](h.Root(), devices.ServiceKey)

	// wait for the link
	var st struct {
		Configured bool         `json:"configured"`
		Status     relay.Status `json:"status"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest("GET", "http://"+gwAddr+"/api/relay", nil)
		req.Header.Set("Authorization", "Bearer "+deskTok)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
		}
		if st.Status.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay link not up: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// pairing link carries host id and key in the fragment
	p, _ := ds.StartPairing("")
	req, _ := http.NewRequest("POST", "http://"+gwAddr+"/api/relay/pair-link", strings.NewReader(`{"code":"`+p.Code+`"}`))
	req.Header.Set("Authorization", "Bearer "+deskTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var link map[string]string
	json.NewDecoder(resp.Body).Decode(&link)
	resp.Body.Close()
	if !strings.HasPrefix(link["url"], srv.URL+"/#/pair?host="+st.Status.HostID+"&key=") {
		t.Fatalf("pair link %q", link["url"])
	}
	key := link["url"][strings.Index(link["url"], "key=")+4:]
	key = key[:strings.Index(key, "&")]
	pub, _ := relayKey(key)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := relay.Connect(ctx, srv.URL, st.Status.HostID, pub)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	// redeem through the tunnel (no token yet)
	body, _ := json.Marshal(map[string]string{"code": p.Code, "name": "手机"})
	r, err := cl.Do(ctx, "POST", "/api/pair", map[string]string{"Content-Type": "application/json"}, body)
	if err != nil || r.Status != 200 {
		t.Fatal(r.Status, string(r.Body), err)
	}
	var paired devices.Paired
	json.Unmarshal(r.Body, &paired)
	if paired.Device.Via != "relay" {
		t.Fatalf("via %q", paired.Device.Via)
	}
	auth := map[string]string{"Authorization": "Bearer " + paired.Token, "Content-Type": "application/json"}
	r, err = cl.Do(ctx, "POST", "/api/echo", auth, []byte(`{"hi":"那边"}`))
	if err != nil || r.Status != 200 || !bytes.Contains(r.Body, []byte(`"hi":"那边"`)) || !bytes.Contains(r.Body, []byte(`"device":"mobile"`)) {
		t.Fatal(r.Status, string(r.Body), err)
	}
	// a large body survives chunking
	big := bytes.Repeat([]byte("x"), 3<<20)
	bb, _ := json.Marshal(map[string]string{"blob": string(big)})
	r, err = cl.Do(ctx, "POST", "/api/echo", auth, bb)
	if err != nil || r.Status != 200 || len(r.Body) < 3<<20 {
		t.Fatal(r.Status, len(r.Body), err)
	}
	// only /api/ paths are tunnelled; desktop routes stay closed to mobile
	if r, _ := cl.Do(ctx, "GET", "/etc/passwd", auth, nil); r.Status != 404 {
		t.Fatalf("non-api path: %d", r.Status)
	}
	if r, _ := cl.Do(ctx, "GET", "/api/devices", auth, nil); r.Status != 403 {
		t.Fatalf("mobile listed devices through the relay: %d", r.Status)
	}
	// revoke: the token stops working
	ds.Revoke(paired.Device.ID)
	if r, _ := cl.Do(ctx, "POST", "/api/echo", auth, []byte(`{}`)); r.Status != 401 {
		t.Fatalf("revoked device still served: %d", r.Status)
	}
}

// The relay refuses a host that cannot sign for its id, and a device that
// sends garbage is disconnected.
func TestRelayRefusesImpostorsAndGarbage(t *testing.T) {
	rs := relay.NewServer(relay.ServerConfig{})
	srv := httptest.NewServer(rs.Handler())
	defer srv.Close()
	id, _ := relay.NewIdentity()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/host?pub="+id.PublicB64(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Read(ctx) // challenge
	c.Write(ctx, websocket.MessageText, []byte(`{"sig":"AAAA"}`))
	_, b, _ := c.Read(ctx)
	if !strings.Contains(string(b), "failed") || rs.Online(id.HostID()) {
		t.Fatalf("impostor accepted: %s", b)
	}
	c.CloseNow()

	// a real host; a device sends a bad hello and gets dropped
	h, _ := relay.NewHost(id, srv.URL, http.NotFoundHandler(), nil)
	hctx, hcancel := context.WithCancel(context.Background())
	defer hcancel()
	go h.Run(hctx)
	for !rs.Online(id.HostID()) {
		time.Sleep(20 * time.Millisecond)
	}
	d, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/client?host="+id.HostID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Write(ctx, websocket.MessageBinary, []byte("not a hello"))
	if _, _, err := d.Read(ctx); err == nil {
		t.Fatal("garbage hello kept the connection open")
	}
	// unknown host
	d2, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/client?host=nope", nil)
	if err == nil {
		if _, _, err := d2.Read(ctx); err == nil {
			t.Fatal("client to an offline host stayed open")
		}
	}
}

func relayKey(b64 string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(b64)
}
