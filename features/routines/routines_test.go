package routines_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/routines"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestQuietAttentionAndBrief(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {notify.Plugin, nil}, {schedule.Plugin, nil},
		{routines.Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	var sent []notify.Notification
	notify.Sent.Listen(h.Root(), func(n notify.Notification) { sent = append(sent, n) })
	r := h.Root()
	agents := kernel.Use[*agent.Registry](r, agent.ServiceKey)
	svc := kernel.Use[*routines.Service](r, routines.ServiceKey)
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("mock")
	m := ad.(*mock.Adapter)

	otto, _ := agents.Create(agent.Spec{Name: "Otto"})
	rt, err := svc.Create(routines.Routine{Name: "竞品巡检", Agent: otto.ID(), Prompt: "Check competitor pricing.", Schedule: "daily 09:00"})
	if err != nil {
		t.Fatal(err)
	}
	var modes []string
	tools.OnCall.Listen(r, func(c tools.Call) { modes = append(modes, c.Mode) })

	// 1) normal → quiet, no notification
	m.Script = func(req llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "NO_ISSUES")}, nil
	}
	svc.RunNow(context.Background(), rt.ID)
	agentloop.WaitIdle(otto, 2*time.Second)
	runs, _ := svc.Runs(rt.ID)
	if len(runs) != 1 || runs[0].Status != routines.StatusQuiet || len(sent) != 0 {
		t.Fatalf("runs=%+v sent=%v", runs, sent)
	}

	// 2) something found → attention + notification; runs read-only
	m.Script = func(req llm.Request) (llm.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == llm.RoleTool {
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Competitor X cut prices by 20%.")}, nil
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c1", Name: "noop", Args: []byte("{}")}}}}}, nil
	}
	kernel.Use[*tools.Registry](r, tools.ServiceKey).Register(r, &tools.Tool{Name: "noop", Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("ok"), nil }})
	svc.RunNow(context.Background(), rt.ID)
	time.Sleep(50 * time.Millisecond)
	agentloop.WaitIdle(otto, 2*time.Second)
	runs, _ = svc.Runs(rt.ID)
	if runs[0].Status != routines.StatusAttention || !strings.Contains(runs[0].Summary, "20%") {
		t.Fatalf("%+v", runs[0])
	}
	if len(sent) != 1 || sent[0].Kind != notify.KindRoutine {
		t.Fatalf("sent %+v", sent)
	}
	if len(modes) == 0 || modes[0] != agent.ModeReadOnly {
		t.Fatalf("routine must run read-only, got %v", modes)
	}

	// 3) brief lists only what needs a person; quiet runs folded
	b := svc.ComposeBrief()
	if len(b.Attention) != 1 || b.Folded != 1 || b.Empty() {
		t.Fatalf("%+v", b)
	}
	if _, err := svc.Create(routines.Routine{Name: "x", Agent: otto.ID(), Prompt: "p", Schedule: "every 10s"}); err == nil {
		t.Fatal("below minimum interval must fail")
	}
	if _, err := svc.Create(routines.Routine{Name: "x", Agent: otto.ID(), Prompt: "p", Schedule: "daily 09:00", Mode: agent.ModeHost}); err == nil {
		t.Fatal("host mode routine must fail")
	}
}
