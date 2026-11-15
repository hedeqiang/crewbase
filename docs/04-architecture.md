# 技术架构

本文描述系统分层、进程模型、存储与部署。插件内核的细节见 [插件系统](05-plugin-system.md)，安全机制见 [安全模型](06-security-model.md)。

## 1. 分层

```text
┌──────────────────────────────────────────────────────────────────────┐
│ 客户端       桌面端（Web UI + 原生壳） · 移动伴侣 · IM 网关 · CLI · SDK   │
├──────────────────────────────────────────────────────────────────────┤
│ 远程网关     远程方法白名单 · 鉴权 → 校验 → 激活 · 会话事件流订阅          │
├──────────────────────────────────────────────────────────────────────┤
│ 产品插件     频道 · 交接 · 路由 · 规则引擎 · 审批 · Routines · Workflows   │
│              目标续跑 · 记忆 · 压缩 · 技能 · MCP · 浏览器 · 通知 · 团队     │
├──────────────────────────────────────────────────────────────────────┤
│ 核心插件     会话日志 · 提示词组装 · 工具注册表与执行流水线                 │
│              Agent 注册表 · Agent 循环 · LLM 适配器约定 · 作用域             │
├──────────────────────────────────────────────────────────────────────┤
│ 能力提供方   llm-* · shell-* · fs-* · sandbox-* · credentials-* ·          │
│              browser-* · persistence-* · notify-*                         │
├──────────────────────────────────────────────────────────────────────┤
│ 插件宿主     服务注册表 · 类型化事件总线 · 副作用跟踪 · 依赖解析 ·          │
│ (kernel)     Profile / 补丁加载 · 进程外与 WASM 桥接                        │
└──────────────────────────────────────────────────────────────────────┘
```

只有最底层的**插件宿主**是不可替换的，代码量目标 < 5,000 行。其上每一层都由插件组成，可以通过配置替换。

## 2. 进程模型

| 进程 | 职责 | 生命周期 |
|---|---|---|
| `crewd`（守护进程） | 插件宿主 + 所有内置插件；会话、Agent、调度、远程网关 | 桌面版：运行在 Crewbase.app 进程内，随菜单栏图标常驻；服务器上由 systemd 管理 |
| 桌面壳（Crewbase.app，`apps/desktop`） | MyGo 原生窗口、菜单栏 / 托盘、Dock 角标、原生通知；窗口加载 `crewd` 网关提供的 Web UI | 启动时若本机已有 `crewd`（127.0.0.1:7788 且接受 `${CREWBASE_HOME}/token`）则**附着**，否则在**同一进程内**启动宿主（`launch.Start`，profile `desktop`）。关闭窗口只隐藏窗口，Agent 继续工作；只有「退出」才停止，进程内模式下退出时 `Host.Close()` 清理整棵子进程树，附着模式下退出不影响外部 `crewd` |
| CLI / ACP 子进程 | Claude Code、Codex、Gemini CLI 等，由 `llm-acp` 和 `subagent-acp` 管理 | 按需启动，空闲超时退出 |
| MCP 服务器 | 外部工具连接 | 由 MCP 管理插件按需启动，在沙箱中运行 |
| 进程外插件 | 第三方插件 | 随插件启用 / 停用 |
| 浏览器池 | 每个 Agent 独立的 Chromium Profile，通过 CDP 控制 | 按需启动，空闲回收 |
| 沙箱容器 | 执行 Agent 的命令 | 每个 Agent 或每个任务一个，复用或用完即弃 |

**单实例**：桌面壳持有单实例锁，再次启动只会唤起已有窗口。更新（[交互设计 §5.1](03-ux-design.md)）只重启桌面壳与进程内的宿主：运行中的 Agent 从检查点恢复，待批准的请求保留在会话日志中。详见 [desktop.md](desktop.md)。

**进程组管理**：`crewd` 为每类子进程建立进程组（Windows 上为 Job Object），退出时清理整棵子进程树，不留孤儿进程。

## 3. 技术选型

| 领域 | 选型 | 理由 |
|---|---|---|
| 守护进程与插件宿主 | Go | 单一可执行文件、低内存、并发模型适合管理大量子进程与流 |
| 桌面壳 | [MyGo](https://github.com/egoist/mygo) v0.9（纯 Go、无 CGO，系统 WebView）；备选 Wails | 与守护进程同语言、同一进程内运行宿主；`go tool mygo build` 产出 .app + .dmg / Windows 安装包 / Linux 包，`GOOS=windows/linux` 可交叉编译。macOS 上窗口、托盘、通知、单实例、开机启动已实测；自动更新需签名密钥（见 [desktop.md](desktop.md)） |
| 前端 | TypeScript + React，组件来自插槽注册 | 插件可携带前端模块；与 Web 版、远程访问共用 |
| 本地存储 | 会话日志：JSONL（带版本号、可选 zstd 压缩）；索引与队列：SQLite（WAL） | 日志是唯一真源，便于迁移与审计；SQLite 负责查询与全文检索 |
| 进程外插件协议 | stdio 上的 JSON-RPC 2.0，描述符由代码生成 | 与 MCP / LSP / ACP 一致，任何语言都能写 |
| WASM 运行时 | wazero | 纯 Go、无 CGO |
| 浏览器 | Chromium + CDP（Playwright 兼容） | 支持持久 Profile、远程帧流与接管 |
| 沙箱 | Docker Rootless、bubblewrap / Landlock、Seatbelt | 见 [安全模型 · 沙箱后端](06-security-model.md#沙箱后端) |
| 远程连接 | WebSocket Relay / Tailscale / WireGuard | 只出站，零入站端口 |
| 可观测性 | OpenTelemetry（默认关闭） | 会话日志可导出为 OTLP |

> 为什么不直接使用 DeepSeek Harness（TypeScript）？见 [ADR-0001](decisions/0001-plugin-first-kernel.md)。简要地说：我们需要单文件分发、常驻低内存、大量子进程管理，以及与 PRD 一致的 Go 技术栈；Harness 的设计思想可以移植，其代码不必照搬。同时，我们保留与 Harness 互通的可能：`subagent-acp` 可以把一个轮次委派给 Harness 的 ACP profile。

## 4. 目录结构（规划）

```text
crewbase/
├── kernel/                 插件宿主：Context、事件、Effect、依赖解析、Profile 加载
├── core/
│   ├── session/            仅追加的会话日志、投影
│   ├── prompt/             提示词片段与工具 schema 组装
│   ├── tools/              工具注册表与执行流水线
│   ├── agent/              Agent 接口、注册表、agent/* 事件
│   ├── agentloop/          默认 Agent 循环
│   ├── scope/              作用域注册原语
│   └── llm/                消息与流式词汇表、适配器约定
├── capabilities/           每项能力的定义（shell、fs、sandbox、approval、credentials …）
├── providers/              能力提供方（llm-anthropic、sandbox-docker、credentials-keychain …）
├── features/               产品插件（channel、router、rules、routines、workflows、memory …）
├── bridge/                 进程外插件与 WASM 桥接、插件 SDK（Go / TS / Python）
├── bundles/                组合包：base、desktop、server、acp、team
├── apps/
│   ├── crewd/              守护进程入口
│   ├── desktop/            桌面壳 Crewbase.app（MyGo）：mygo.json、resources/icon.png、托盘图标由 docs/brand 生成
│   ├── web/                前端
│   └── cli/                crew 命令行
└── docs/
```

## 5. 数据与存储

```text
~/.crewbase/
├── profiles/<name>/            Profile：组合包列表、补丁、已安装的第三方插件
├── workspaces/<agent-id>/      Agent 工作目录（产物文件）
│   ├── INSTRUCTIONS.md         角色指令
│   ├── MEMORY.md               长期记忆（人类可读、可编辑）
│   ├── tasks/<name>.md         进行中任务的进度文件（提示词只带列表；done/ 为已完成）
│   ├── notes/*.md|txt          参考笔记（提示词只带目录，note_read 按需读）
│   ├── .memory/told.jsonl      主动告诉过用户的内容（例行、跟进、后台结果等，避免重复）
│   └── browser/                独立浏览器 Profile
├── sessions/<session-id>/
│   └── session.v1.jsonl.zst    会话日志（唯一真源）
├── index.sqlite                会话索引、全文检索、队列、定时任务、审批状态
├── parked.json                 停靠中的审批调用（重启后告诉 Agent 重新发起）
└── audit/                      审计日志（Phase 3，哈希链）
```

- **会话日志**：仅追加；每次模型请求可从日志重建；格式升级按相邻版本迁移，已写入的文件不改写、不重命名。
- **SQLite**：只存可以从日志重建的派生数据和调度状态；损坏时可以重建索引。
- **凭证**不落盘到上述任何位置，只存引用。

## 6. 关键流程的实现

### 6.1 无损模型切换

1. `llm` 适配器返回限额 / 额度耗尽错误，触发 `agent/request-error`。
2. `router` 插件监听该事件，发出 `router/quota-exhausted`（Bail），默认提供方按 Agent 的模型顺序返回下一个可用模型。
3. 写入会话事件 `model/switched`（原因、前后模型），对话界面据此渲染说明行。
4. Agent 循环用新适配器从同一份日志重建请求并重试；如新模型上下文窗口更小，先调用 `compaction`。
5. 订阅 CLI（ACP）切换到 API 模型时，工作区文件不变，因为文件在 `fs` / `sandbox` 能力中，与模型无关。

### 6.2 交接

1. 负责人调用 `handover` 工具（或人在界面上改派）。
2. `channel/handover` Waterfall：检查产物、接收方状态，可改写交接说明。
3. 写入会话事件 `channel/handover`，频道负责人变更；向接收方 `agent.Inject()` 交接说明与文件引用。
4. 发出 `channel/owner-changed`，界面与通知更新。

### 6.3 审批挂起

见 [插件系统 · 工具执行流水线](05-plugin-system.md#5-工具执行流水线) 与 [安全模型 · 审批](06-security-model.md#4-审批)。

### 6.4 子 Agent 与订阅复用

- `subagent` 能力的提供方：`subagent-local`（新建本地 Agent）、`subagent-fork`（从当前会话 fork）、`subagent-acp`（把一个轮次交给 Claude Code / Codex / Gemini CLI）。
- 所有提供方共用准入、容量、取消和释放机制；结果统一为 `output`、`structured`、`diagnostic`、`stopReason`。
- 有最大委派深度；子 Agent 的父子关系以数据记录，不影响可见性。

### 6.5 附件与多模态

- 上传：`POST /api/agents/{id}/uploads?name=` 收原始字节，存进 Agent 工作区 `uploads/<日期>/`（同名不同内容自动编号，相同内容复用），单个文件 30 MB 以内。
- 记录：`message/user` 只写附件引用（路径、名称、类型、大小、sha256），不写文件内容，所以日志里不会出现文件正文。
- 装载：每次请求前，`attachments` 服务（agentloop.Hydrator）读回文件，再问适配器 `llm.FileAccepter.AcceptsFile(model, mime)` 能否原生接收：
  - 能：附带原始字节。例如图片给视觉模型；PDF 给 Anthropic、Gemini 和 OpenAI；DeepSeek 直连时，PDF / Word / PowerPoint 先上传到它的 Files API，再用 file_id 引用。file_id 缓存在 `uploads/.remote/`，每个文件只解析、计费一次。
  - 不能：用本地提取的文字（PDF、docx、pptx、xlsx、文本文件），附上工作区路径。超过 12 万字时截断，并提示模型用工具读取全文。看不了图片的模型只拿到一条说明。
  - 原生通道失败（例如上传出错）时，退回同样的文字。
- ACP（CLI 订阅）：只给工作区路径，由 CLI 自己的读文件工具读取。
- 下载：`GET /api/agents/{id}/raw?path=` 提供工作区文件。只有 png / jpeg / gif / webp 内联显示，其他类型一律作为附件下载（nosniff，CSP sandbox），所以上传的 HTML / SVG 不会在应用的源里执行。

### 6.6 DeepSeek 工具 API（与模型无关）

`features/deepseek`（`@crewbase/deepseek-tools`、`@crewbase/voice-api`）调用 DeepSeek 的独立工具接口，不论 Agent 用哪个模型：

- **工具**：`web_search`（/v1/tools/search）、`web_fetch`（/v1/tools/fetch，读页面为 Markdown）、`ocr_file`（/v1/tools/ocr）。三者都是 read 类别；`web_fetch` 在只读 / 沙箱模式下按 `allowHosts` 限制。只在配置了 Key 时注册，Key 增删后自动出现 / 消失。
- **Key**：已添加的 DeepSeek 提供方的 Key，其次 `keychain:crewbase/deepseek`、`env:DEEPSEEK_API_KEY`；`config.keyRef` 可覆盖。
- **附件 OCR**：提供 `ocr` 服务；附件装载时，不能看图的模型收到图片的识别文字，扫描版 PDF 与旧版 .doc / .ppt 也走 OCR，结果缓存在 `uploads/.ocr/<sha256>.txt`。
- **语音**：`/api/voice/transcribe`（16-bit WAV / Ogg Opus ≤ 8 MiB → ASR）与 `/api/voice/speak`（TTS → WAV），网页在有 Key 时优先使用，否则用浏览器语音。音频只透传，不保存。

## 7. 部署形态

| 形态 | Profile | 说明 |
|---|---|---|
| 桌面版 | `desktop` | 单机，托盘常驻 |
| 无头服务器 | `server` | `install-server.sh`（systemd）或 `docker-compose.yml` |
| 阿里云 | `server` + `bundle-aliyun` | ECS（Ubuntu 24.04 + Docker Compose）与轻量应用服务器一键脚本；镜像加速；DeepSeek / Qwen / Kimi 直连预设；数据卷挂载 ESSD |
| AWS | `server` + `bundle-aws` | EC2（Docker Compose）一键启动；ECS / Fargate + EFS 任务定义模板 |
| ACP 服务器 | `acp` | 供编辑器或其他 Agent 产品调用 |
| SDK | `sdk` | 嵌入到用户自己的程序 |

加固版 `docker-compose.yml` 默认开启 `--security-opt no-new-privileges:true`、`--security-opt seccomp=seccomp.json`、`--shm-size 1g`，且不映射任何入站端口。
