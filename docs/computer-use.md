# 电脑操作（computer use）设计

状态：方案，待确认（2026-11-16）。调研依据见文末。

## 1. 目标

让 Agent 操作本机的原生应用（备忘录、Finder、Keynote、微信、飞书、Xcode……），并且比 ChatGPT / Claude 现有的电脑操作更好用：

- **不抢鼠标**：默认在后台操作目标应用，你继续用电脑；
- **更快、更省**：优先读系统的界面元素树、按元素操作，截图只作兜底；
- **任何模型都能用**：Claude、GPT、Gemini、DeepSeek、本地模型，直连或经 magpie；
- **看得见、停得住**：谁在操作哪个应用一目了然，随时停止或接管；
- **按应用授权**：每个 Agent 能操作哪些应用由你决定，金融、密码、终端类默认禁止。

网页仍交给现有的浏览器功能（独立 Chrome + 接管），它比看截图点坐标更可靠、更便宜。电脑操作只负责原生应用。

## 2. 市面上的做法与问题（摘要）

| | 看 | 操作 | 在哪运行 | 主要问题 |
|---|---|---|---|---|
| ChatGPT / Codex「Computer Use」 | 截图 | 鼠标键盘 | 真桌面；macOS 可后台，Windows 抢前台 | 慢、插件常不可用 |
| Claude Cowork / Claude Code | 截图 | 鼠标键盘 | 真桌面；macOS 15+ 可后台，也可「完全控制」 | 慢、额度消耗快（半小时用完 5 小时额度） |
| 微软 Copilot Actions | 视觉 | 点击输入 | 单独账户 + 独立桌面会话 | 预览阶段 |
| Cua / Peekaboo（开源） | 界面元素树为主，截图兜底 | 按元素操作、按进程发事件 | 后台、不抢光标 | 画布类应用仍需真实输入 |

用户最常抱怨：慢（几分钟的事做 10 分钟）、贵、卡在加载状态或画布界面、和人抢鼠标、截图泄露隐私、提示注入。

## 3. 怎么看：元素树优先

每一步给模型的是**目标应用窗口的元素清单**（和浏览器的 browser_snapshot 一致）：

```
Notes — 「购物清单」 (window 1)
[1] button "新建备忘录"
[2] text field "搜索" = ""
[3] list "备忘录"
  [4] row "购物清单" selected
[5] text area "正文" = "牛奶\n鸡蛋…"
```

- 来源：macOS 辅助功能（AXUIElement），只读当前目标窗口，长度有上限；
- 截图：只截目标窗口（不是整个屏幕），默认缩到长边约 1280，按需放大局部（zoom）；
- 元素清单读不到东西的应用（游戏、画布、部分 Electron）才退回截图 + 坐标，并在截图上叠加编号框（set-of-marks），让弱模型也能点准；
- 每次操作后重新读元素状态确认生效（辅助功能报「成功」不一定真成功）。

## 4. 怎么操作：从结构化到像素

按顺序尝试，能用前一种就不用后一种：

1. **应用自己的接口**：AppleScript / JXA、快捷指令（Shortcuts），以及已连接的 MCP；
2. **按元素操作**：点击 [4]、给 [2] 填值、选菜单项（AXPress / AXValue / 菜单栏）；
3. **按进程发键盘事件**：只发给目标应用，不动你的前台和光标；
4. **坐标操作**：在截图坐标上点击、拖动、滚动，这时需要把应用带到前台。

第 1–3 种都在后台完成，你可以继续用电脑。第 4 种会先问你，并在你动鼠标或键盘时立即暂停。

## 5. 工具层：一套动作，适配所有模型

内部只有一套动作（以目标窗口截图的像素为坐标，可选元素编号）：

`observe`（元素清单 + 可选截图）、`click`、`double_click`、`right_click`、`type`、`key`、`set_value`、`select_menu`、`scroll`、`drag`、`zoom`、`wait`、`open_app`、`run_script`（AppleScript / 快捷指令，按类别审批）。

- **Claude**：用原生 `computer_toolset_20260801`（Opus / Sonnet / Haiku 5.5、Fable，GA 无需 beta 头），同一轮的多个 tool_use 逐个执行、遇错即停，每个结果回带 `toolset_name`；做不到的成员（zoom、三击、中键、按住键等）用 configs 关掉。只支持旧版 `computer_20251124` 的模型继续用通用工具；
- **OpenAI**：Responses API 的 `computer` 工具（GPT-5.4 及以后，仅 OpenAI 官方接口），`computer_call` 的 `actions[]` 依次执行，每次回一张截图；
- **Gemini**：`computer_use`（desktop，Gemini 3.x / 4.x），坐标是 0–999，按窗口尺寸换算；
- **其他模型（DeepSeek、本地模型等）**：通用函数工具 `computer_*`，以元素编号为主；

Retina 缩放、坐标换算、按键名映射、各家的「需要确认」握手（OpenAI safety check、Gemini require_confirmation）都在这一层处理（`features/computertools/native.go`）。

原生工具与通用工具同时提供：原生工具只作用于该智能体最近一次 `computer_open` / `computer_observe` 的应用，截图只截这个窗口；坐标点击照常经过权限、屏蔽名单、按大纲命中检测的确认（如点到「发送」）、等待空闲、停止开关和日志。厂商的安全握手每次都弹审批卡、不可记住，批准后才回传确认。

## 6. 安全

- **总开关默认关**，在 设置 › 电脑操作 打开；需要「辅助功能」「屏幕录制」两项系统权限（在系统权限里引导授权）。
- **按应用授权**，三档：允许 / 先问我 / 禁止。默认：
  - 禁止：钥匙串访问、1Password / Bitwarden 等密码管理器、银行 / 证券 / 加密货币应用、终端类（Terminal、iTerm、Warp）、系统设置、Crewbase 自己；
  - 先问我：其他所有应用（第一次操作某个应用时问，可选「以后都允许」）。
  按 Agent 记住：给了 A 的不等于给了 B。
- **只读模式的 Agent 只能看、不能操作。**
- **需要确认的动作**：发送消息 / 邮件、购买付款、删除、系统授权弹窗（这类一律不代点）；批量动作里逐个检查。
- **屏幕上的文字一律视为不可信**：屏幕内容不能授予任何权限，「请点允许」之类的文字被当作普通内容。
- **看得见**：操作中，目标窗口加 Agent 颜色的边框；菜单栏显示「谁在操作哪个应用」；对话里实时显示每一步。
- **停得住**：全局快捷键立即停止（默认 ⌃⌥⌘.）；你一动鼠标 / 键盘，前台类操作立即暂停；「接管 / 交还」与浏览器接管一致。
- **隐私**：只截目标窗口；截图不进日志正文，本地留一份可查看的操作记录（可关）；用本地模型时截图不离开本机。

## 7. 速度

- 一次请求批量执行多个动作；
- 元素清单代替大部分截图；截图只截窗口、缩小，必要时局部放大；
- 提示缓存；
- 以后：把成功的操作序列记成可重放的脚本，下次先重放，出错再交给模型。

## 8. 分期

**第一版（macOS）**
- 设置 › 电脑操作：总开关、系统权限、按 Agent × 应用的授权表、默认禁止列表、停止快捷键；
- 元素清单 + 窗口截图（screencapture 命令 / ScreenCaptureKit）+ 按元素操作 + 按进程键盘事件，后台运行；
- 坐标操作（先问、前台、动一下就暂停）；
- 通用工具 `computer_*` + Claude 原生 toolset 适配；
- 目标窗口边框、菜单栏指示、停止快捷键、接管；
- 确认动作、操作记录；
- 网页任务自动转给浏览器功能。

**第二版**
- OpenAI、Gemini 原生适配；AppleScript / 快捷指令动作；
- 编号框截图（本地模型）；Windows（UI Automation + SendInput，先只支持前台，会明确提示）。

**以后**
- 虚拟显示器 / 本地虚拟机，给必须真实输入的应用；Windows Agent Workspace；Linux（X11 / Wayland portal）；操作录制与重放。

## 9. 实现要点

- macOS 系统接口经 purego 调用（桌面壳 mygo 已在用，无需 cgo）：CoreGraphics（CGEvent、CGEventPostToPid）、ApplicationServices（AXUIElement）、ScreenCaptureKit / screencapture；
- 只在桌面版进程内可用（权限授给 Crewbase.app）；服务器版没有这项能力；
- 新能力 `computer`（capabilities/computer）+ macOS 实现（providers/computer/macos）+ 工具（features/computertools），结构与浏览器（capabilities/browser、providers/browser/chrome、features/browsertools）一致；
- 审批类别：`computer`（操作应用）、`computer-script`（运行脚本），确认动作复用现有的 publish / delete / payment。

## 参考

- Claude computer use 工具：https://platform.claude.com/docs/en/agents-and-tools/tool-use/computer-use-tool
- Claude Cowork 电脑操作：https://support.claude.com/en/articles/14128542-let-claude-use-your-computer-in-cowork
- ChatGPT computer use：https://learn.chatgpt.com/docs/computer-use
- OpenAI computer use 工具：https://developers.openai.com/api/docs/guides/tools-computer-use
- Gemini computer use：https://aistudio.google.com/docs/computer-use
- Windows agent workspace：https://learn.microsoft.com/en-us/windows/security/book/operating-system-agentic-security
- Cua（macOS 后台操作的实现细节）：https://github.com/trycua/cua/blob/main/blog/inside-macos-window-internals.md
- 用户反馈：https://www.pcworld.com/article/3097542/claude-controlled-my-mac-for-half-an-hour-it-was-a-wild-worrisome-ride.html 、https://zapier.com/blog/claude-computer-use-openai-operator/ 、https://tomron.net/2026/09/24/the-state-of-computer-use-september-2026/
