// Package jsonrpc is a small, generic JSON-RPC 2.0 peer over a byte stream
// (typically a subprocess's stdio). Messages are framed as newline-delimited
// JSON, the framing used by ACP and most stdio JSON-RPC tools.
//
// A Conn is symmetric: it sends requests and notifications, and it serves
// requests and notifications sent by the other side through handlers.
// Incoming requests are handled concurrently (each in its own goroutine) so a
// handler may block, e.g. waiting for a user approval, without stalling the
// reader. Incoming notifications are delivered in order on the reader
// goroutine; handlers must not block for long.
//
// Lifecycle: create with NewConn, run Serve (in a goroutine the caller owns,
// e.g. kernel Context.Go) and Close when done. Serve returns when the stream
// ends, the context is cancelled or Close is called; pending calls then fail
// with an error wrapping ErrClosed.
package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ErrClosed is returned by calls on (or pending on) a closed connection.
var ErrClosed = errors.New("jsonrpc: connection closed")

// Error is a JSON-RPC error object. Handlers may return one to control the
// code and data sent to the peer; calls return one when the peer answered
// with an error.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("jsonrpc error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// NewError builds an *Error; data may be nil.
func NewError(code int, msg string, data any) *Error {
	e := &Error{Code: code, Message: msg}
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			e.Data = b
		}
	}
	return e
}

// ErrMethodNotFound may be returned by a RequestHandler for unknown methods.
var ErrMethodNotFound = &Error{Code: CodeMethodNotFound, Message: "method not found"}

// RequestHandler serves a request from the peer. The returned value is
// marshalled as the result (nil becomes JSON null). A returned *Error is sent
// as is; any other error becomes an internal error. ctx is cancelled when the
// connection closes.
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// NotificationHandler receives notifications from the peer, in order.
type NotificationHandler func(method string, params json.RawMessage)

// Options configure a Conn.
type Options struct {
	OnRequest      RequestHandler      // nil answers every request with method-not-found
	OnNotification NotificationHandler // nil drops notifications
	// MaxMessageBytes bounds one incoming line (default 64 MiB).
	MaxMessageBytes int
	// Trace, when set, sees every raw message: dir is "<-" (in) or "->" (out).
	Trace func(dir string, msg []byte)
}

// Conn is a JSON-RPC 2.0 peer.
type Conn struct {
	r    io.Reader
	w    io.Writer
	opts Options

	wmu sync.Mutex // serialises writes

	mu      sync.Mutex
	pending map[string]chan *wireMsg
	closed  bool
	err     error // why the conn closed
	done    chan struct{}

	seq atomic.Int64

	hctx    context.Context // handler context
	hcancel context.CancelFunc
	hwg     sync.WaitGroup
}

// NewConn creates a peer reading r and writing w. If r or w implement
// io.Closer they are closed by Close.
func NewConn(r io.Reader, w io.Writer, opts Options) *Conn {
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = 64 << 20
	}
	hctx, cancel := context.WithCancel(context.Background())
	return &Conn{r: r, w: w, opts: opts, pending: map[string]chan *wireMsg{}, done: make(chan struct{}), hctx: hctx, hcancel: cancel}
}

type wireMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// outgoing response: result must be present (even null) on success.
type wireResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

type wireError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *Error          `json:"error"`
}

// Done is closed when the connection has closed.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err reports why the connection closed (nil while open).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return c.closedErr()
	default:
	}
	if c.opts.Trace != nil {
		c.opts.Trace("->", b[:len(b)-1])
	}
	_, err = c.w.Write(b)
	return err
}

func (c *Conn) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil && !errors.Is(c.err, ErrClosed) {
		return fmt.Errorf("%w: %v", ErrClosed, c.err)
	}
	return ErrClosed
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}

// Call sends a request and waits for its response, decoding the result into
// result (which may be nil to discard it). If ctx ends first Call returns
// ctx.Err() and a late response is dropped; protocol-level cancellation (e.g.
// LSP $/cancelRequest, ACP session/cancel) is up to the caller.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	p, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("jsonrpc: marshal params: %w", err)
	}
	id := strconv.FormatInt(c.seq.Add(1), 10)
	ch := make(chan *wireMsg, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.closedErr()
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.write(wireMsg{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: p}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return c.closedErr()
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("jsonrpc: decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("jsonrpc: marshal params: %w", err)
	}
	return c.write(wireMsg{JSONRPC: "2.0", Method: method, Params: p})
}

// Serve reads and dispatches messages until the stream ends, ctx is
// cancelled or Close is called. It then closes the connection and returns
// the reason (nil for a clean EOF or Close).
func (c *Conn) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { c.shutdown(ctx.Err()) })
	defer stop()
	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 0, 64<<10), c.opts.MaxMessageBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if c.opts.Trace != nil {
			c.opts.Trace("<-", line)
		}
		if line[0] == '[' { // batch
			var batch []json.RawMessage
			if err := json.Unmarshal(line, &batch); err != nil {
				_ = c.write(wireError{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeParseError, Message: "parse error"}})
				continue
			}
			for _, m := range batch {
				c.dispatch(m)
			}
			continue
		}
		c.dispatch(line)
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.shutdown(err)
	c.hwg.Wait()
	if errors.Is(err, io.EOF) || errors.Is(err, ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (c *Conn) dispatch(raw []byte) {
	var m wireMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		_ = c.write(wireError{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeParseError, Message: "parse error"}})
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "" && hasID: // request
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.hwg.Add(1)
		c.mu.Unlock()
		go func() {
			defer c.hwg.Done()
			c.handle(m)
		}()
	case m.Method != "": // notification
		if h := c.opts.OnNotification; h != nil {
			func() {
				defer func() { _ = recover() }()
				h(m.Method, m.Params)
			}()
		}
	case hasID: // response
		id := string(m.ID)
		if s, err := strconv.Unquote(id); err == nil {
			id = s // tolerate peers that echo numeric ids as strings
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	default:
		// a response with a null id reports a parse error on our side; ignore
	}
}

func (c *Conn) handle(m wireMsg) {
	var res any
	err := ErrMethodNotFound
	if h := c.opts.OnRequest; h != nil {
		var herr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					herr = fmt.Errorf("handler panic: %v", r)
				}
			}()
			res, herr = h(c.hctx, m.Method, m.Params)
		}()
		if herr == nil {
			err = nil
		} else {
			var je *Error
			if errors.As(herr, &je) {
				err = je
			} else {
				err = &Error{Code: CodeInternalError, Message: herr.Error()}
			}
		}
	}
	if err != nil {
		_ = c.write(wireError{JSONRPC: "2.0", ID: m.ID, Error: err})
		return
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		_ = c.write(wireError{JSONRPC: "2.0", ID: m.ID, Error: &Error{Code: CodeInternalError, Message: "marshal result: " + merr.Error()}})
		return
	}
	_ = c.write(wireResult{JSONRPC: "2.0", ID: m.ID, Result: b})
}

// shutdown marks the conn closed, fails pending calls, cancels handlers and
// closes the underlying streams. Idempotent; the first reason wins.
func (c *Conn) shutdown(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if reason == nil {
		reason = ErrClosed
	}
	c.err = reason
	pend := c.pending
	c.pending = map[string]chan *wireMsg{}
	close(c.done)
	c.mu.Unlock()
	for _, ch := range pend {
		close(ch)
	}
	c.hcancel()
	if cl, ok := c.w.(io.Closer); ok {
		_ = cl.Close()
	}
	if cl, ok := c.r.(io.Closer); ok {
		_ = cl.Close()
	}
}

// Close closes the connection; pending calls fail with ErrClosed.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return nil
}
