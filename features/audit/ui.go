package audit

import (
	"context"
	"fmt"
	"strconv"

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
			Desc: "规则判定、审批、权限与插件变更写入只追加的哈希链，可导出复核。",
			Panel: func(context.Context, string) (ui.Panel, error) {
				v, err := s.Verify()
				if err != nil {
					return ui.Panel{}, err
				}
				state := "完整"
				if !v.OK {
					state = "已断裂"
				}
				fields := []ui.Field{
					{Label: "哈希链", Value: state},
					{Label: "记录", Value: strconv.FormatInt(v.Records, 10) + " 条 · " + strconv.Itoa(v.Files) + " 个文件"},
				}
				if v.Head != "" {
					fields = append(fields, ui.Field{Label: "链头", Value: short(v.Head), Mono: true})
				}
				if !v.OK {
					fields = append(fields, ui.Field{Label: "断点", Value: fmt.Sprintf("#%d（%s 第 %d 行）%s", v.BrokenSeq, v.BrokenFile, v.BrokenLine, v.Error)})
				}
				return ui.Panel{Sections: []ui.Section{{Fields: fields}}}, nil
			}}); err != nil {
			return err
		}
		return u.Register(c, ui.Contribution{Slot: ui.SlotPaletteCommand, Key: "audit-verify", Title: "校验审计链", Icon: "shield-check",
			Run: func(context.Context, string) (ui.Result, error) {
				v, err := s.Verify()
				if err != nil {
					return ui.Result{}, err
				}
				if !v.OK {
					return ui.Result{Toast: fmt.Sprintf("审计链在 #%d 处断裂：%s", v.BrokenSeq, v.Error)}, nil
				}
				return ui.Result{Toast: fmt.Sprintf("审计链完整：%d 条记录", v.Records)}, nil
			}})
	},
})
