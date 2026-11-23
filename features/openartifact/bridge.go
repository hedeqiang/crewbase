package openartifact

// The stdio bridge: `open-artifact harness --stdio --harness crewbase`, one
// JSON-RPC 2.0 object per line each way. It finds or starts the daemon,
// holds its token (never seen here), reconnects and attaches our sessions
// again. We call it (hello, tools.call, …); it calls us (wake.deliver,
// host.open, shell.event) with ids d1, d2, …, and sends notifications.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// rpcError is a JSON-RPC error; Fix is the daemon's suggested fix line.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Code string `json:"code"`
		Fix  string `json:"fix"`
	} `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Data != nil && e.Data.Fix != "" {
		return e.Message + " — " + e.Data.Fix
	}
	return e.Message
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handler answers a call from the bridge (result, or an error).
type handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// notifier takes a notification from the bridge.
type notifier func(method string, params json.RawMessage)

// conn is one running bridge process.
type conn struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	wmu     sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan rpcMsg
	next    int64
	done    chan struct{}
	err     error
	handle  handler
	notify  notifier
}

// startConn runs the bridge (argv) and reads it until it ends.
func startConn(ctx context.Context, argv, env []string, dir string, h handler, n notifier, stderr io.Writer) (*conn, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.Stderr = env, dir, stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &conn{cmd: cmd, stdin: stdin, pending: map[int64]chan rpcMsg{}, done: make(chan struct{}), handle: h, notify: n}
	go c.read(ctx, stdout)
	return c, nil
}

func (c *conn) read(ctx context.Context, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m rpcMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0: // the bridge calls us
			go c.answer(ctx, m)
		case m.Method != "": // a notification
			if c.notify != nil {
				c.notify(m.Method, m.Params)
			}
		case len(m.ID) > 0: // an answer to our call
			id, err := strconv.ParseInt(string(m.ID), 10, 64)
			if err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	err := sc.Err()
	if werr := c.cmd.Wait(); err == nil {
		err = werr
	}
	if err == nil {
		err = errors.New("the bridge ended")
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

func (c *conn) answer(ctx context.Context, m rpcMsg) {
	// the daemon waits 30 s for an answer
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	out := rpcMsg{JSONRPC: "2.0", ID: m.ID}
	res, err := c.handle(ctx, m.Method, m.Params)
	if err != nil {
		out.Error = &rpcError{Code: -32000, Message: err.Error()}
	} else {
		b, _ := json.Marshal(res)
		out.Result = b
	}
	_ = c.write(out)
}

func (c *conn) write(m rpcMsg) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for its answer.
func (c *conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.next++
	id := c.next
	ch := make(chan rpcMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(rpcMsg{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: b}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("open artifact: %s: the bridge ended", method)
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("open artifact: %s: the bridge ended", method)
	}
}

func (c *conn) close() {
	_ = c.stdin.Close() // the bridge ends when its stdin ends; the daemon keeps running
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	}
}
