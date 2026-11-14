// Package imtest provides a test host and a fake transport for IM gateway
// tests (features/im and providers/im/*).
package imtest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/kernel"
)

// SyncBuffer is a goroutine-safe log sink.
type SyncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *SyncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Env is a host with sessions, agents, tools (every call asks for
// approval), approval + desktop inbox, notify and credentials.
type Env struct {
	T      *testing.T
	Host   *kernel.Host
	Tools  *tools.Registry
	Inbox  *approval.Inbox
	Creds  *credentials.Service
	Agents *agent.Registry
	Notify *notify.Service
	Logs   *SyncBuffer
}

// New builds the environment; it is closed on test cleanup.
func New(t *testing.T) *Env {
	t.Helper()
	logs := &SyncBuffer{}
	h := kernel.NewHost(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		t.Helper()
		if _, err := h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
		if p, _ := h.Plugin(s.Name); p.State != kernel.StateActive {
			t.Fatalf("%s: %s %s %v", s.Name, p.State, p.Error, p.Missing)
		}
	}
	load(session.Plugin, nil)
	load(prompt.Plugin, nil)
	load(agent.Plugin, map[string]any{"root": t.TempDir()})
	load(tools.Plugin, nil)
	load(approval.Plugin, nil)
	load(approval.DesktopPlugin, nil)
	load(notify.Plugin, nil)
	load(credentials.Plugin, nil)
	load(kernel.Spec{Name: "imtest-tools", Inject: []string{tools.ServiceKey}, Apply: func(c *kernel.Context) error {
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		for name, cat := range map[string]string{"deploy": tools.CategoryExec, "git_push": tools.CategoryPush} {
			reg.Register(c, &tools.Tool{Name: name, Category: cat, Description: name,
				Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text(name + " done"), nil }})
		}
		tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			d.RequireApproval("test: ask everything")
			return next(ctx, d)
		})
		return nil
	}}, nil)
	r := h.Root()
	return &Env{
		T: t, Host: h, Logs: logs,
		Tools:  kernel.Use[*tools.Registry](r, tools.ServiceKey),
		Inbox:  kernel.Use[*approval.Inbox](r, approval.InboxKey),
		Creds:  kernel.Use[*credentials.Service](r, credentials.ServiceKey),
		Agents: kernel.Use[*agent.Registry](r, agent.ServiceKey),
		Notify: kernel.Use[*notify.Service](r, notify.ServiceKey),
	}
}

// Agent creates an agent.
func (e *Env) Agent(name string) *agent.Agent {
	e.T.Helper()
	a, err := e.Agents.Create(agent.Spec{Name: name})
	if err != nil {
		e.T.Fatal(err)
	}
	return a
}

// RunTool runs tool for a in the background ("deploy" = exec category,
// "git_push" = push category); every call asks for approval.
func (e *Env) RunTool(a *agent.Agent, tool string) <-chan tools.Execution {
	e.T.Helper()
	ss, err := a.Session()
	if err != nil {
		e.T.Fatal(err)
	}
	out := make(chan tools.Execution, 1)
	call := tools.Call{ID: session.NewID("call"), Name: tool, Args: json.RawMessage(`{}`), Agent: a.ID(), Session: ss}
	go func() { out <- e.Tools.Run(context.Background(), e.Host.Root(), call) }()
	return out
}

// WaitPending waits until the desktop inbox has n pending requests.
func (e *Env) WaitPending(n int) []approval.Request {
	e.T.Helper()
	var p []approval.Request
	Eventually(e.T, func() bool { p = e.Inbox.Pending(); return len(p) == n })
	return p
}

// EndTurn emits a TurnEnded for in (what the agent loop does after a turn).
func (e *Env) EndTurn(a *agent.Agent, in agent.Input, text string) {
	agentloop.TurnEnded.Emit(kernel.For(e.Host, a.ID()), agentloop.TurnEndEvent{Agent: a.ID(), Session: in.Session, Input: in, Text: text,
		TurnEnd: agentloop.TurnEnd{Status: "completed"}})
}

// Eventually polls cond for up to 5s.
func Eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Recv receives from ch within 5s.
func Recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting")
	}
	var zero T
	return zero
}

// ---------------------------------------------------------------- fake

// Posted is a message recorded by Fake.
type Posted struct {
	Ref im.Ref
	Msg im.Message
}

// Fake is an in-memory transport.
type Fake struct {
	mu        sync.Mutex
	ev        im.Events
	posts     []Posted
	edits     []Posted
	seq       int
	ready     chan struct{}
	readyOnce sync.Once
}

func NewFake() *Fake { return &Fake{ready: make(chan struct{})} }

func (f *Fake) Run(ctx context.Context, ev im.Events) error {
	f.mu.Lock()
	f.ev = ev
	f.mu.Unlock()
	ev.Connected(true, nil)
	f.readyOnce.Do(func() { close(f.ready) })
	<-ctx.Done()
	return nil
}

// Events waits for Run and returns the gateway's event sink.
func (f *Fake) Events(t *testing.T) im.Events {
	Recv(t, (<-chan struct{})(f.ready))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ev
}

func (f *Fake) Post(_ context.Context, m im.Message) (im.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	ref := im.Ref{Chat: m.Chat, ID: strconv.Itoa(f.seq), Thread: m.Thread}
	f.posts = append(f.posts, Posted{ref, m})
	return ref, nil
}

func (f *Fake) Edit(_ context.Context, ref im.Ref, m im.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, Posted{ref, m})
	return nil
}

func (f *Fake) Posts() []Posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Posted(nil), f.posts...)
}

func (f *Fake) Edits() []Posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Posted(nil), f.edits...)
}

// Plugin returns a spec that starts a gateway on fake.
func Plugin(name string, fake *Fake) kernel.Spec {
	return kernel.Spec{Name: "imtest-" + name, Inject: []string{notify.ServiceKey, approval.ServiceKey, credentials.ServiceKey},
		Apply: func(c *kernel.Context) error {
			cfg, err := kernel.Decode[im.Config](c)
			if err != nil {
				return err
			}
			_, err = im.Start(c, name, cfg, func(*im.Gateway) (im.Transport, error) { return fake, nil })
			return err
		}}
}
