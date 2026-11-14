package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type rw struct {
	io.Reader
	io.Writer
}

// pair returns two connected peers (a <-> b), already serving.
func pair(t *testing.T, ao, bo Options) (*Conn, *Conn) {
	t.Helper()
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	a := NewConn(ar, aw, ao)
	b := NewConn(br, bw, bo)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.Serve(ctx) }()
	go func() { defer wg.Done(); _ = b.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = a.Close()
		_ = b.Close()
		wg.Wait()
	})
	return a, b
}

func TestCallNotifyAndServerRequests(t *testing.T) {
	var mu sync.Mutex
	var notes []string
	gotNote := make(chan struct{}, 4)
	_, b := pair(t, Options{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "add":
				var p [2]int
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, NewError(CodeInvalidParams, "bad", nil)
				}
				return p[0] + p[1], nil
			case "fail":
				return nil, NewError(-32000, "nope", map[string]string{"why": "x"})
			case "boom":
				return nil, errors.New("plain")
			case "null":
				return nil, nil
			}
			return nil, ErrMethodNotFound
		},
		OnNotification: func(method string, params json.RawMessage) {
			mu.Lock()
			notes = append(notes, method+":"+string(params))
			mu.Unlock()
			gotNote <- struct{}{}
		},
	}, Options{})
	ctx := context.Background()

	var sum int
	if err := b.Call(ctx, "add", []int{2, 3}, &sum); err != nil || sum != 5 {
		t.Fatalf("add: %v %d", err, sum)
	}
	err := b.Call(ctx, "fail", nil, nil)
	var je *Error
	if !errors.As(err, &je) || je.Code != -32000 || string(je.Data) != `{"why":"x"}` {
		t.Fatalf("fail: %v", err)
	}
	if err := b.Call(ctx, "boom", nil, nil); !errors.As(err, &je) || je.Code != CodeInternalError || je.Message != "plain" {
		t.Fatalf("boom: %v", err)
	}
	if err := b.Call(ctx, "missing", nil, nil); !errors.As(err, &je) || je.Code != CodeMethodNotFound {
		t.Fatalf("missing: %v", err)
	}
	var v any = "sentinel"
	if err := b.Call(ctx, "null", nil, &v); err != nil || v != nil {
		t.Fatalf("null: %v %v", err, v)
	}
	if err := b.Notify("hello", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gotNote:
	case <-time.After(2 * time.Second):
		t.Fatal("no notification")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 || notes[0] != `hello:{"n":1}` {
		t.Fatalf("notes %v", notes)
	}
}

func TestConcurrentCallsAndBlockingHandler(t *testing.T) {
	release := make(chan struct{})
	_, b := pair(t, Options{OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "block" {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return method, nil
	}}, Options{})
	ctx := context.Background()
	blocked := make(chan error, 1)
	go func() { blocked <- b.Call(ctx, "block", nil, nil) }()
	// other calls are served while one handler blocks
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var s string
			if err := b.Call(ctx, "echo", nil, &s); err != nil || s != "echo" {
				t.Errorf("echo: %v %q", err, s)
			}
		}()
	}
	wg.Wait()
	close(release)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
}

func TestCallContextCancel(t *testing.T) {
	_, b := pair(t, Options{OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.Call(ctx, "slow", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
}

func TestCloseFailsPending(t *testing.T) {
	a, b := pair(t, Options{OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}, Options{})
	errc := make(chan error, 1)
	go func() { errc <- b.Call(context.Background(), "slow", nil, nil) }()
	time.Sleep(30 * time.Millisecond)
	_ = a.Close() // peer goes away: b's reader sees EOF
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("want ErrClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending call not failed")
	}
	select {
	case <-b.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("b not done")
	}
	if err := b.Call(context.Background(), "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close: %v", err)
	}
	if err := b.Notify("x", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("notify after close: %v", err)
	}
}

func TestParseErrorAndStringIDs(t *testing.T) {
	// raw peer: write garbage and a string-id request, read the replies
	ar, pw := io.Pipe()
	pr, aw := io.Pipe()
	a := NewConn(ar, aw, Options{OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return "ok", nil
	}})
	go func() { _ = a.Serve(context.Background()) }()
	defer a.Close()
	dec := json.NewDecoder(pr)
	go func() {
		_, _ = pw.Write([]byte("not json\n"))
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":"abc","method":"m"}` + "\n"))
	}()
	var m1, m2 map[string]any
	if err := dec.Decode(&m1); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&m2); err != nil {
		t.Fatal(err)
	}
	if m1["error"].(map[string]any)["code"].(float64) != CodeParseError {
		t.Fatalf("m1 %v", m1)
	}
	if m2["id"] != "abc" || m2["result"] != "ok" {
		t.Fatalf("m2 %v", m2)
	}
	_ = pw.Close()
}
