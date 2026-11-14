package process_test

import (
	"context"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/ui"
	"github.com/hedeqiang/crewbase/kernel"
)

const uiManifest = `name = "deploys"
version = "0.1.0"
entry = ["python3", "main.py"]
[inject]
services = ["ui"]
`

const uiMain = `
from crewbase_plugin import Plugin
p = Plugin("deploys")
p.card("deploy/result", "部署 {{env}}：{{status}}", tone="ok", fields=[
    {"label": "版本", "value": "{{version}}", "mono": True},
    {"label": "地址", "value": "{{url}}", "href": "{{url}}"},
])
@p.panel("settings.card", "deploys-status", "部署")
def status(session):
    return {"sections": [{"title": "最近一次", "fields": [{"label": "生产", "value": "v1.4.2"}, {"label": "bad", "value": "x", "href": "javascript:alert(1)"}]}]}
@p.panel("sidebar-right.tab", "deploys-tab", "部署")
def tab(session):
    return {"sections": [{"text": "session " + session}]}
@p.command("deploys-rollback", "回滚生产", confirm="确定回滚？")
def rollback(session):
    return {"toast": "已回滚（" + session + "）"}
p.run()
`

func TestPluginUIContributions(t *testing.T) {
	needPython(t)
	h, _ := newHost(t)
	if _, err := h.Load(ui.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	svc := kernel.Use[*ui.Service](h.Root(), ui.ServiceKey)

	// without the ui service declared and approved, registration fails
	dir := writePlugin(t, sneakyManifest, uiMain)
	load(t, h, "deploys-undeclared", dir, []string{"service:tools"}, nil)
	if info, _ := h.Plugin("deploys-undeclared"); info.State != kernel.StateFailed {
		t.Fatalf("state %s", info.State)
	}
	if len(svc.List()) != 0 {
		t.Fatal("contributions leaked from a failed plugin")
	}

	dir = writePlugin(t, uiManifest, uiMain)
	load(t, h, "deploys", dir, []string{"service:ui"}, nil)
	waitFor(t, "ui contributions", 10*time.Second, func() bool { return len(svc.List()) == 4 })
	byKey := map[string]ui.Contribution{}
	for _, c := range svc.List() {
		byKey[c.Key] = c
		if c.Owner != "deploys" {
			t.Fatalf("owner %q", c.Owner)
		}
	}
	if c := byKey["deploy/result"]; c.Slot != ui.SlotConversationNode || c.Card == nil || len(c.Card.Fields) != 2 {
		t.Fatalf("%+v", c)
	}
	ctx := context.Background()
	p, err := svc.Panel(ctx, ui.SlotSettingsCard, "deploys-status", "")
	if err != nil || p.Sections[0].Fields[0].Value != "v1.4.2" || p.Sections[0].Fields[1].Href != "" {
		t.Fatalf("%v %+v", err, p) // the javascript: link is dropped
	}
	if p, err := svc.Panel(ctx, ui.SlotSidebarTab, "deploys-tab", "s-1"); err != nil || p.Sections[0].Text != "session s-1" {
		t.Fatalf("%v %+v", err, p)
	}
	if byKey["deploys-rollback"].Confirm != "确定回滚？" {
		t.Fatal("confirm lost")
	}
	if r, err := svc.Run(ctx, ui.SlotPaletteCommand, "deploys-rollback", "s-1"); err != nil || r.Toast != "已回滚（s-1）" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := svc.Run(ctx, ui.SlotPaletteCommand, "nope", ""); err == nil {
		t.Fatal("unknown command ran")
	}
	// unloading the plugin removes its interface
	h.Unload("deploys")
	waitFor(t, "ui removed", 5*time.Second, func() bool { return len(svc.List()) == 0 })
}
