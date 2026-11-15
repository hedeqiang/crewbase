package chrome

// Frames: snapshots and actions inside iframes, including cross-site
// iframes that Chrome runs in their own process (out-of-process iframes,
// OOPIF), which are separate DevTools targets the page session cannot reach.
//
// Each browser instance keeps one extra DevTools connection (frameConn) in
// flat session mode: it attaches to the agent's tab and calls
// Target.setAutoAttach{autoAttach, waitForDebuggerOnStart, flatten} on it and,
// recursively, on every iframe target it is given. A new iframe target
// starts paused; it is resumed only after its own auto-attach (for nested
// iframes) and, when the agent's host filter is on, request interception
// are set up, so not one request of a cross-site iframe escapes the filter.
//
// Scripts for frames run in an isolated world ("crewbase") of the frame, so
// the page cannot see or tamper with the element refs. A frame handle is
// its frame id; the owning session is the iframe target's session for an
// OOPIF, and the session of its parent frame for an in-process iframe.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp/remote"
)

// worldName is the isolated world the frame scripts run in.
const worldName = "crewbase"

type frameConn struct {
	conn  *remote.Conn
	page  target.SessionID // session on the agent's tab
	pageT target.ID
	allow func() func(string) bool // the agent's current host filter

	mu       sync.Mutex
	next     int64
	wait     map[int64]chan *cdproto.Message
	oopif    map[string]target.SessionID // frame id (= target id) → session
	owner    map[string]target.SessionID // in-process frame id → session that owns it
	children map[target.SessionID]bool   // iframe sessions
	filtered map[target.SessionID]bool   // sessions with interception on
	closed   bool
	done     chan struct{}
}

// browserWS returns the browser-level DevTools websocket URL for a ws:// or
// http(s):// address.
func browserWS(ctx context.Context, addr string) (string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", err
	}
	if u.Scheme == "ws" || u.Scheme == "wss" {
		return addr, nil
	}
	u.Path = "/json/version"
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.WS == "" {
		return "", fmt.Errorf("no webSocketDebuggerUrl at %s", u)
	}
	return v.WS, nil
}

// openFrames connects to the browser and attaches to the tab tid.
func openFrames(ctx context.Context, wsURL string, tid target.ID, allow func() func(string) bool) (*frameConn, error) {
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	f := &frameConn{conn: conn, pageT: tid, allow: allow, wait: map[int64]chan *cdproto.Message{},
		oopif: map[string]target.SessionID{}, owner: map[string]target.SessionID{},
		children: map[target.SessionID]bool{}, filtered: map[target.SessionID]bool{}, done: make(chan struct{})}
	go f.read()
	var at struct {
		SessionID target.SessionID `json:"sessionId"`
	}
	if err := f.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": tid, "flatten": true}, &at); err != nil {
		f.close()
		return nil, err
	}
	f.page = at.SessionID
	if err := f.autoAttach(ctx, f.page); err != nil {
		f.close()
		return nil, err
	}
	return f, nil
}

func (f *frameConn) autoAttach(ctx context.Context, s target.SessionID) error {
	return f.call(ctx, s, "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}, nil)
}

func (f *frameConn) close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.mu.Unlock()
	_ = f.conn.Close()
	<-f.done
}

func (f *frameConn) read() {
	defer func() {
		f.mu.Lock()
		f.closed = true
		for id, ch := range f.wait {
			close(ch)
			delete(f.wait, id)
		}
		f.mu.Unlock()
		close(f.done)
	}()
	for {
		msg := new(cdproto.Message)
		if err := f.conn.Read(context.Background(), msg); err != nil {
			return
		}
		if msg.ID != 0 {
			f.mu.Lock()
			ch := f.wait[msg.ID]
			delete(f.wait, msg.ID)
			f.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		switch msg.Method {
		case "Target.attachedToTarget":
			var ev target.EventAttachedToTarget
			// only targets auto-attached under one of our sessions (the
			// attach to the tab itself arrives on the browser session)
			if msg.SessionID != "" && jsonv2.Unmarshal(msg.Params, &ev) == nil && ev.TargetInfo != nil {
				go f.attached(ev)
			}
		case "Target.detachedFromTarget":
			var ev struct {
				SessionID target.SessionID `json:"sessionId"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				f.detached(ev.SessionID)
			}
		case "Fetch.requestPaused":
			var ev struct {
				RequestID string `json:"requestId"`
				Request   struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				go f.decide(msg.SessionID, ev.RequestID, ev.Request.URL)
			}
		}
	}
}

// attached sets up an auto-attached target and lets it run.
func (f *frameConn) attached(ev target.EventAttachedToTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := ev.SessionID
	if ev.TargetInfo.Type == "iframe" {
		f.mu.Lock()
		f.oopif[string(ev.TargetInfo.TargetID)] = s
		f.children[s] = true
		f.mu.Unlock()
		_ = f.autoAttach(ctx, s)
		if f.allow() != nil {
			f.setFetch(ctx, s, true)
		}
	}
	if ev.WaitingForDebugger {
		_ = f.call(ctx, s, "Runtime.runIfWaitingForDebugger", map[string]any{}, nil)
	}
	if ev.TargetInfo.Type != "iframe" {
		// workers and the like: only resumed, not kept
		_ = f.call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": s}, nil)
	}
}

func (f *frameConn) detached(s target.SessionID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.children, s)
	delete(f.filtered, s)
	for id, x := range f.oopif {
		if x == s {
			delete(f.oopif, id)
		}
	}
	for id, x := range f.owner {
		if x == s {
			delete(f.owner, id)
		}
	}
}

func (f *frameConn) setFetch(ctx context.Context, s target.SessionID, on bool) {
	f.mu.Lock()
	have := f.filtered[s]
	f.mu.Unlock()
	if have == on {
		return
	}
	var err error
	if on {
		err = f.call(ctx, s, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*"}}}, nil)
	} else {
		err = f.call(ctx, s, "Fetch.disable", map[string]any{}, nil)
	}
	if err == nil {
		f.mu.Lock()
		f.filtered[s] = on
		f.mu.Unlock()
	}
}

// setFilter turns interception in the iframe targets on or off.
func (f *frameConn) setFilter(on bool) {
	f.mu.Lock()
	var ss []target.SessionID
	for s := range f.children {
		ss = append(ss, s)
	}
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range ss {
		f.setFetch(ctx, s, on)
	}
}

func (f *frameConn) decide(s target.SessionID, id, raw string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	allow := f.allow()
	ok := allow == nil || localURL(raw)
	if !ok {
		if u, err := url.Parse(raw); err == nil {
			ok = allow(u.Hostname())
		}
	}
	if ok {
		_ = f.call(ctx, s, "Fetch.continueRequest", map[string]any{"requestId": id}, nil)
		return
	}
	_ = f.call(ctx, s, "Fetch.failRequest", map[string]any{"requestId": id, "errorReason": network.ErrorReasonBlockedByClient}, nil)
}

// call sends a command on session s ("" = the browser) and decodes the
// result into res (may be nil).
func (f *frameConn) call(ctx context.Context, s target.SessionID, method string, params, res any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan *cdproto.Message, 1)
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errors.New("the browser connection is closed")
	}
	f.next++
	id := f.next
	f.wait[id] = ch
	f.mu.Unlock()
	if err := f.conn.Write(ctx, &cdproto.Message{ID: id, SessionID: s, Method: cdproto.MethodType(method), Params: p}); err != nil {
		f.mu.Lock()
		delete(f.wait, id)
		f.mu.Unlock()
		return err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return errors.New("the browser connection is closed")
		}
		if msg.Error != nil {
			return msg.Error
		}
		if res != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, res)
		}
		return nil
	case <-ctx.Done():
		f.mu.Lock()
		delete(f.wait, id)
		f.mu.Unlock()
		return ctx.Err()
	}
}

// session returns the session that owns frame ("" = the tab's main frame)
// and the frame id to use on it.
func (f *frameConn) session(frame string) (target.SessionID, string, error) {
	if frame == "" {
		return f.page, string(f.pageT), nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.oopif[frame]; ok {
		return s, frame, nil
	}
	if s, ok := f.owner[frame]; ok {
		return s, frame, nil
	}
	return "", "", errStaleFrame
}

var errStaleFrame = errors.New("the frame is gone (the page changed)")

type remoteObject struct {
	Type     string          `json:"type"`
	Subtype  string          `json:"subtype"`
	Value    json.RawMessage `json:"value"`
	ObjectID string          `json:"objectId"`
}

func (f *frameConn) evaluate(ctx context.Context, frame, expr string, byValue bool) (*remoteObject, error) {
	s, fid, err := f.session(frame)
	if err != nil {
		return nil, err
	}
	var w struct {
		ID int64 `json:"executionContextId"`
	}
	if err := f.call(ctx, s, "Page.createIsolatedWorld", map[string]any{"frameId": fid, "worldName": worldName}, &w); err != nil {
		if frame != "" {
			return nil, errStaleFrame
		}
		return nil, err
	}
	var r struct {
		Result    remoteObject `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := f.call(ctx, s, "Runtime.evaluate", map[string]any{"expression": expr, "contextId": w.ID,
		"returnByValue": byValue, "awaitPromise": true, "objectGroup": worldName}, &r); err != nil {
		return nil, err
	}
	if r.Exception != nil {
		msg := r.Exception.Text
		if r.Exception.Exception != nil && r.Exception.Exception.Description != "" {
			msg = r.Exception.Exception.Description
		}
		return nil, fmt.Errorf("script error: %s", strings.SplitN(msg, "\n", 2)[0])
	}
	return &r.Result, nil
}

// eval evaluates expr in frame's isolated world and decodes the value.
func (f *frameConn) eval(ctx context.Context, frame, expr string, out any) error {
	r, err := f.evaluate(ctx, frame, expr, true)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	raw := r.Value
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return json.Unmarshal(raw, out)
}

// child returns the frame shown by window.__crewbaseFrames[index] of frame.
func (f *frameConn) child(ctx context.Context, frame string, index int) (string, error) {
	r, err := f.evaluate(ctx, frame, fmt.Sprintf("(window.__crewbaseFrames || [])[%d] || null", index), false)
	if err != nil {
		return "", err
	}
	if r.ObjectID == "" {
		return "", errStaleFrame
	}
	s, _, err := f.session(frame)
	if err != nil {
		return "", err
	}
	var d struct {
		Node struct {
			FrameID string `json:"frameId"`
		} `json:"node"`
	}
	err = f.call(ctx, s, "DOM.describeNode", map[string]any{"objectId": r.ObjectID}, &d)
	_ = f.call(ctx, s, "Runtime.releaseObject", map[string]any{"objectId": r.ObjectID}, nil)
	if err != nil {
		return "", err
	}
	id := d.Node.FrameID
	if id == "" {
		return "", errStaleFrame
	}
	// An OOPIF target is attached right after it is created; give a frame
	// that just started a moment to show up.
	for i := 0; i < 20; i++ {
		f.mu.Lock()
		_, remote := f.oopif[id]
		f.mu.Unlock()
		if remote {
			return id, nil
		}
		// in-process when the parent session knows the frame
		var tree struct {
			FrameTree json.RawMessage `json:"frameTree"`
		}
		if err := f.call(ctx, s, "Page.getFrameTree", map[string]any{}, &tree); err == nil && strings.Contains(string(tree.FrameTree), `"id":"`+id+`"`) {
			f.mu.Lock()
			f.owner[id] = s
			f.mu.Unlock()
			return id, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return "", errStaleFrame
}
