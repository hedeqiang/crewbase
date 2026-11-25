// Package proxytest runs small HTTP and SOCKS5 proxies for tests, with an
// optional login, counting what goes through them. Route sends a target
// elsewhere (a name that resolves nowhere to a local test server).
package proxytest

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// Proxy is a running test proxy.
type Proxy struct {
	Addr string
	Host string
	Port int
	// Hits counts requests that went through.
	Hits atomic.Int64

	user, pass string
	mu         sync.Mutex
	route      map[string]string
}

// Route sends connections for target (host:port) to addr.
func (p *Proxy) Route(target, addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.route == nil {
		p.route = map[string]string{}
	}
	p.route[target] = addr
}

func (p *Proxy) dial(target string) (net.Conn, error) {
	p.mu.Lock()
	if to, ok := p.route[target]; ok {
		target = to
	}
	p.mu.Unlock()
	return net.Dial("tcp", target)
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func (p *Proxy) at(ln net.Listener) *Proxy {
	p.Addr = ln.Addr().String()
	h, ps, _ := net.SplitHostPort(p.Addr)
	p.Host = h
	p.Port, _ = strconv.Atoi(ps)
	return p
}

// HTTP starts an HTTP proxy (CONNECT and absolute-form requests). With a
// user, requests without the right Proxy-Authorization get 407.
func HTTP(t *testing.T, user, pass string) *Proxy {
	ln := listen(t)
	p := (&Proxy{user: user, pass: pass}).at(ln)
	srv := &http.Server{Handler: http.HandlerFunc(p.serveHTTP)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return p
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if p.user != "" {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.user+":"+p.pass))
		if r.Header.Get("Proxy-Authorization") != want {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
	}
	if r.Method == http.MethodConnect {
		up, err := p.dial(r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, _ := w.(http.Hijacker)
		c, rw, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		p.Hits.Add(1)
		_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		if n := rw.Reader.Buffered(); n > 0 {
			b, _ := rw.Reader.Peek(n)
			_, _ = up.Write(b)
		}
		pipe(c, up)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "not a proxy request", http.StatusBadRequest)
		return
	}
	host := r.URL.Host
	if r.URL.Port() == "" {
		host = net.JoinHostPort(r.URL.Hostname(), "80")
	}
	up, err := p.dial(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer up.Close()
	p.Hits.Add(1)
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	if err := out.Write(up); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(up), out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("X-Test-Proxy", "http")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// SOCKS5 starts a SOCKS5 proxy (CONNECT only). With a user it asks for
// username/password authentication.
func SOCKS5(t *testing.T, user, pass string) *Proxy {
	ln := listen(t)
	p := (&Proxy{user: user, pass: pass}).at(ln)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serveSOCKS(c)
		}
	}()
	return p
}

func (p *Proxy) serveSOCKS(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	want := byte(0)
	if p.user != "" {
		want = 2
	}
	ok := false
	for _, m := range methods {
		ok = ok || m == want
	}
	if !ok {
		_, _ = c.Write([]byte{5, 0xff})
		return
	}
	_, _ = c.Write([]byte{5, want})
	if want == 2 {
		u, pw, err := readUserPass(br)
		if err != nil {
			return
		}
		if u != p.user || pw != p.pass {
			_, _ = c.Write([]byte{1, 1})
			return
		}
		_, _ = c.Write([]byte{1, 0})
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	up, err := p.dial(net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	p.Hits.Add(1)
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	if n := br.Buffered(); n > 0 {
		b, _ := br.Peek(n)
		_, _ = up.Write(b)
	}
	pipe(c, up)
}

func readUserPass(br *bufio.Reader) (string, string, error) {
	v, err := br.ReadByte()
	if err != nil || v != 1 {
		return "", "", errors.New("bad auth version")
	}
	read := func() (string, error) {
		n, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		b := make([]byte, n)
		_, err = io.ReadFull(br, b)
		return string(b), err
	}
	u, err := read()
	if err != nil {
		return "", "", err
	}
	pw, err := read()
	return u, pw, err
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}
