# Crewbase 开发 TODO（总调度维护）

> 只记还没做完的事和接下来的方向。做完的内容见 `CHANGELOG.md`（按版本）和 `git log`（按提交）。
> 状态：`[ ]` 未开始 · `[~]` 进行中 · `[-]` 推迟 / 需外部条件
> 规则：提交前 `go vet ./... && go test ./...`、`apps/web` 的 `tsc` 与文案检查通过；发版是单独的 `release: vX.Y.Z` 提交，CI 通过后再打 tag。

## 当前状态（2026-11-22，v0.9.10）

模型只来自 API 提供方和 magpie 网关（0.5.12 起移除了订阅 CLI 复用）。已有：Agent / 频道 / 团队、工具按需加载与上下文色块图、电脑操作（macOS）、桌面客户端原地更新（固定签名证书）、对话按「轮」显示、新对话为不带提示词和工具的普通 chat、团队文档库（全文搜索、版本、附到对话）、对话里让助理设置的例行巡检（需确认）、Open Agent Docs（一键启动并自动更新，每个助理一个身份，评论 @ 唤醒）、插件与技能的更新提醒、地址即位置的路由（返回 / 前进 / 刷新 / 深链接）、Mermaid 图、插话 / 排队、每轮改动与撤销、页面答复、回应 / 回复 / 未读 / 置顶 / 消息搜索、聊天框 / 命令（技能、MCP 提示词模板、插件工具、命令）。

## 待办

### 对话与界面
- [-] 在其他设备（手机 / 中继）上看预览：待定方案——中继为每个预览分配独立源（通配域名 + 证书），或只给截图；不能把 Agent 的页面放在应用同源下（会泄露令牌）
- [-] Docker 沙箱里跑起来的开发服务器的预览（端口映射；Windows 默认是 Docker 沙箱时才需要）

### 模型
- [ ] 经 magpie 的 GPT-6.1 Sol（high）不返回推理摘要，界面看不到它的思考过程
- [ ] magpie 的 openai/* 路线是否透传 OpenAI 原生 tool_search（默认关，可用 toolSearch 强制）
- [ ] magpie 的 codex/* 走 /responses 的流式事件形状，未连真实接口验证

### 电脑操作
- [ ] 真机验收：备忘录新建、Finder 操作（X 发帖已验证）
- [ ] 电脑操作：三击、中键、移动鼠标、按住修饰键点击 / 拖动 / 滚动已实现，待在装了新版本、开了辅助功能权限的电脑上实测；按下 / 抬起鼠标、按住按键仍不支持
- [-] 以后：Windows（UI Automation）、虚拟显示器 / 虚拟机、操作录制与重放

### 安全与沙箱
- [ ] ProtectedRoots 余下的缝：配置路径本身是符号链接时只保护链接目标；crewd 崩溃可能留下空占位目录（无害）；大小写不敏感的 Linux 文件系统没有专门测试
- [ ] 插件沙箱：Windows（Docker 后端）实机验证长驻插件进程；子进程权限还不强制

### IM 与连接器
- [ ] IM：真实机器人账号联调；飞书 / Lark 显示名；附件

### 发版与平台

## 方向

- **接下来**：速度与稳定；IM 附件与显示名
- **不做 / 搁置**：订阅 CLI 复用（0.5.12 移除，复用 CLI 登录有封号风险，订阅额度交给 magpie）；ACP 服务端（编辑器接入）；云端 Agent + 本机执行；跨实例 A2A
- **待外部条件**：真实 IM 账号；Windows / Linux 实机

## 约定

- 目录按「能力 / 提供方」两层：`providers/llm/{anthropic,openai,gemini,local,magpie,mock}`、`providers/sandbox/{none,docker,seatbelt,bubblewrap}`、`providers/shell/local`、`providers/fs/local`
- 包名即末级目录；与官方 SDK 包名冲突时 SDK 用 `sdk` 别名导入；内核插件名保持 `@crewbase/llm-anthropic` 形式
- 测试不碰 `~/.crewbase`、`~/.claude`、`~/.codex`，用 `/tmp` 下的数据目录；不提交二进制和截图
