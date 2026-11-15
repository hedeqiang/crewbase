package audit

import (
	"context"
	"fmt"

	"github.com/hedeqiang/crewbase/capabilities/ui"
	"github.com/hedeqiang/crewbase/kernel"
)

// UIPlugin contributes the audit log's interface through ui slots: a
// settings card with the chain status and a ⌘K command to verify it.
var UIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/audit-ui",
	Inject:      []string{ui.ServiceKey, ServiceKey},
	Description: "Audit log card (settings) and ⌘K verify command.",
	Apply: func(c *kernel.Context) error {
		u := kernel.Use[*ui.Service](c, ui.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		short := func(h string) string {
			if len(h) > 16 {
				return h[:16] + "…"
			}
			return h
		}
		if err := u.Register(c, ui.Contribution{Slot: ui.SlotSettingsCard, Key: "audit", Title: "审计日志", Icon: "shield-check", Order: 50,
			Desc:    "规则判定、审批、权限与插件变更写入只追加的哈希链，可导出复核。",
			TitleEn: "Audit log", DescEn: "Rule decisions, approvals, permission and plugin changes go into an append-only hash chain you can export and verify.",
			Panel: func(ctx context.Context, _ string) (ui.Panel, error) {
				en := ui.Lang(ctx) == "en"
				w := func(zh, eng string) string {
					if en {
						return eng
					}
					return zh
				}
				v, err := s.Verify()
				if err != nil {
					return ui.Panel{}, err
				}
				state := w("完整", "intact")
				if !v.OK {
					state = w("已断裂", "broken")
				}
				fields := []ui.Field{
					{Label: w("哈希链", "Hash chain"), Value: state},
					{Label: w("记录", "Records"), Value: fmt.Sprintf(w("%d 条 · %d 个文件", "%d · %d files"), v.Records, v.Files)},
				}
				if v.Head != "" {
					fields = append(fields, ui.Field{Label: w("链头", "Head"), Value: short(v.Head), Mono: true})
				}
				if !v.OK {
					fields = append(fields, ui.Field{Label: w("断点", "Break"), Value: fmt.Sprintf(w("#%d（%s 第 %d 行）%s", "#%d (%s line %d) %s"), v.BrokenSeq, v.BrokenFile, v.BrokenLine, v.Error)})
				}
				return ui.Panel{Sections: []ui.Section{{Fields: fields}}}, nil
			}}); err != nil {
			return err
		}
		return u.Register(c, ui.Contribution{Slot: ui.SlotPaletteCommand, Key: "audit-verify", Title: "校验审计链", TitleEn: "Verify the audit chain", Icon: "shield-check",
			Run: func(ctx context.Context, _ string) (ui.Result, error) {
				en := ui.Lang(ctx) == "en"
				v, err := s.Verify()
				if err != nil {
					return ui.Result{}, err
				}
				if !v.OK {
					if en {
						return ui.Result{Toast: fmt.Sprintf("The audit chain breaks at #%d: %s", v.BrokenSeq, v.Error)}, nil
					}
					return ui.Result{Toast: fmt.Sprintf("审计链在 #%d 处断裂：%s", v.BrokenSeq, v.Error)}, nil
				}
				if en {
					return ui.Result{Toast: fmt.Sprintf("The audit chain is intact: %d records", v.Records)}, nil
				}
				return ui.Result{Toast: fmt.Sprintf("审计链完整：%d 条记录", v.Records)}, nil
			}})
	},
})
