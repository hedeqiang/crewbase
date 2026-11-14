package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

type slowPage struct {
	Page
	started chan struct{}
}

func (p slowPage) Eval(ctx context.Context, _ string, out any) error {
	close(p.started)
	<-ctx.Done() // a takeover cancels in-flight operations
	return ctx.Err()
}

func TestTakeoverDiscardsInflightAndAwaits(t *testing.T) {
	c := NewControl(nil)
	p := slowPage{started: make(chan struct{})}
	s := NewSession("otto", p, c, nil)
	errc := make(chan error, 1)
	go func() { var v any; errc <- s.Eval(context.Background(), "1", &v) }()
	<-p.started
	if !c.Busy("otto") {
		t.Fatal("op should be in flight")
	}
	if started, err := c.Begin("otto", "me"); !started || err != nil {
		t.Fatal(started, err)
	}
	if err := <-errc; !errors.Is(err, ErrTakenOver) {
		t.Fatalf("in-flight result must be discarded, got %v", err)
	}
	if _, err := c.Begin("otto", "you"); !errors.Is(err, ErrNotController) {
		t.Fatal(err)
	}
	if err := s.Navigate(context.Background(), "x"); !errors.Is(err, ErrTakenOver) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Await(context.Background(), "otto") }()
	time.Sleep(20 * time.Millisecond)
	if err := c.End("otto"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.End("otto"); !errors.Is(err, ErrNotTakenOver) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Await(ctx, "otto"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
