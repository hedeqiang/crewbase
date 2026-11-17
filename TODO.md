# Crewbase 开发 TODO（总调度维护）

> 只记还没做完的事和接下来的方向。做完的内容见 `CHANGELOG.md`（按版本）和 `git log`（按提交）。
> 状态：`[ ]` 未开始 · `[~]` 进行中 · `[-]` 推迟 / 需外部条件
> 规则：提交前 `go vet ./... && go test ./...`、`apps/web` 的 `tsc` 与文案检查通过；发版是单独的 `release: vX.Y.Z` 提交，CI 通过后再打 tag。

## 当前状态（2026-11-17，v0.5.13）

模型只来自 API 提供方和 magpie 网关（0.5.12 起移除了订阅 CLI 复用）。已有：Agent / 频道 / 团队、工具按需加载与上下文色块图、电脑操作（macOS）、桌面客户端原地更新（固定签名证书）、对话按「轮」显示、新对话为不带提示词和工具的普通 chat。

## 待办

### 对话与界面
- [ ] alwaysLoad / toolSearch 的界面开关（现在只能在配置里设）

### 模型
- [ ] 经 magpie 的 GPT-6.1 Sol（high）不返回推理摘要，界面看不到它的思考过程
- [ ] magpie 的 openai/* 路线是否透传 OpenAI 原生 tool_search（默认关，可用 toolSearch 强制）
- [ ] magpie 的 codex/* 走 /responses 的流式事件形状，未连真实接口验证
- [ ] 通用工具搜索路径每加载一个工具会让一次提示缓存失效

### 电脑操作
- [ ] 真机验收：备忘录新建、Finder 操作（X 发帖已验证）
- [ ] 原生工具缺的动作：缩放、三击、中键、按住修饰键点击、鼠标移动（目前返回明确错误或忽略）
- [-] 以后：Windows（UI Automation）、虚拟显示器 / 虚拟机、操作录制与重放

### 安全与沙箱
- [ ] ProtectedRoots 余下的缝：配置路径本身是符号链接时只保护链接目标；crewd 崩溃可能留下空占位目录（无害）；大小写不敏感的 Linux 文件系统没有专门测试
- [ ] 插件：默认不开沙箱时网络 / 文件限制仅是声明；Windows 实机验证进程树

### IM 与连接器
- [ ] IM：真实机器人账号联调；飞书 / Lark 显示名；附件
- [~] Google 连接器：需要所有者提供桌面客户端密钥、真实账号验证、受限 scope 的 Google 安全评估（docs/06 §7.1）

### 发版与平台
- [ ] 发版脚本：等 CI 再打 tag 的脚本最近两次没等到 CI 结果就退出（tag 是手动打的），改成按提交轮询直到出现结果

## 方向

- **接下来**：速度与稳定；插件隔离真正落地（默认沙箱）；IM 附件与显示名
- **不做 / 搁置**：订阅 CLI 复用（0.5.12 移除，复用 CLI 登录有封号风险，订阅额度交给 magpie）；ACP 服务端（编辑器接入）；云端 Agent + 本机执行；跨实例 A2A
- **待外部条件**：真实 IM 账号；Windows / Linux 实机

## 约定

- 目录按「能力 / 提供方」两层：`providers/llm/{anthropic,openai,gemini,local,magpie,mock}`、`providers/sandbox/{none,docker,seatbelt,bubblewrap}`、`providers/shell/local`、`providers/fs/local`
- 包名即末级目录；与官方 SDK 包名冲突时 SDK 用 `sdk` 别名导入；内核插件名保持 `@crewbase/llm-anthropic` 形式
- 测试不碰 `~/.crewbase`、`~/.claude`、`~/.codex`，用 `/tmp` 下的数据目录；不提交二进制和截图
