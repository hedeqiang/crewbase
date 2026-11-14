package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// Client is a device's tunnel to a host through a relay (the Go
// counterpart of the web app's client; used by tests and tools).
type Client struct {
	c  *websocket.Conn
	ch *Channel

	mu   sync.Mutex
	next int64
	wait map[int64]chan tResp
	err  error
}

// Response is a tunnelled response.
type Response struct {
	Status int
	Header map[string]string
	Body   []byte
}

// Connect dials the relay, completes the handshake against the pinned
// host key and starts reading.
func Connect(ctx context.Context, relayURL, hostID string, hostPub []byte) (*Client, error) {
	u := "ws" + strings.TrimPrefix(strings.TrimRight(relayURL, "/"), "http") + "/v1/client?host=" + hostID
	c, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(MaxFrame + 64)
	d, hello, err := NewDial(hostPub)
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	if err := c.Write(ctx, websocket.MessageBinary, hello); err != nil {
		c.CloseNow()
		return nil, err
	}
	_, reply, err := c.Read(ctx)
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	ch, err := d.Finish(reply)
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	cl := &Client{c: c, ch: ch, wait: map[int64]chan tResp{}}
	go cl.read()
	return cl, nil
}

func (cl *Client) read() {
	for {
		_, b, err := cl.c.Read(context.Background())
		if err == nil {
			var p []byte
			if p, err = cl.ch.Open(b); err == nil {
				var r tResp
				if err = json.Unmarshal(p, &r); err == nil {
					cl.mu.Lock()
					w := cl.wait[r.ID]
					cl.mu.Unlock()
					if w != nil {
						w <- r
					}
					continue
				}
			}
		}
		cl.mu.Lock()
		cl.err = err
		for _, w := range cl.wait {
			close(w)
		}
		cl.wait = map[int64]chan tResp{}
		cl.mu.Unlock()
		return
	}
}

// Do performs one request and collects the whole response.
func (cl *Client) Do(ctx context.Context, method, path string, header map[string]string, body []byte) (Response, error) {
	cl.mu.Lock()
	if cl.err != nil {
		err := cl.err
		cl.mu.Unlock()
		return Response{}, err
	}
	cl.next++
	id := cl.next
	w := make(chan tResp, 64)
	cl.wait[id] = w
	b, _ := json.Marshal(tReq{ID: id, M: method, P: path, H: header, B: body})
	frame := cl.ch.Seal(b)
	err := cl.c.Write(ctx, websocket.MessageBinary, frame)
	cl.mu.Unlock()
	if err != nil {
		return Response{}, err
	}
	var out Response
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case r, ok := <-w:
			if !ok {
				return out, errors.New("connection closed")
			}
			if r.S != 0 {
				out.Status, out.Header = r.S, r.H
			}
			out.Body = append(out.Body, r.B...)
			if r.End {
				cl.mu.Lock()
				delete(cl.wait, id)
				cl.mu.Unlock()
				if out.Status == 0 {
					return out, fmt.Errorf("no response")
				}
				return out, nil
			}
		}
	}
}

// Close ends the tunnel.
func (cl *Client) Close() { cl.c.Close(websocket.StatusNormalClosure, "") }
