# 路线图

「插件宿主」排在第一阶段最前面：后续每项功能都以插件形式交付，因此宿主的约定必须先稳定。

## Phase 0 — 技术验证（第 0 周，约 5 个工作日）

| 验证项 | 通过标准 |
|---|---|
| 桌面壳（MyGo，已选定） | macOS、Windows、Linux 上托盘常驻、自动更新、加载本地 Web UI 均可用。**进展**：`apps/desktop` 已用 MyGo v0.9 实现（`make desktop`），macOS arm64 上已实测窗口、菜单栏图标、关闭窗口后继续运行、单实例、退出时清理子进程树、附着已运行的 `crewd`；Windows / Linux 可交叉编译，待真机验证；自动更新待生成签名密钥后开启（[desktop.md](desktop.md)） |
| 插件宿主原型 | 实现 Context、`Inject` 与 PENDING、五种分发模式、Effect 撤销；用 `greet-tool` + `tool-logger` 两个插件跑通 Harness 教程第 7 章的等价示例 |
| 进程外插件桥接 | 一个 TypeScript 插件通过 stdio JSON-RPC 注册工具并监听 `tools/result`；进程被杀后其注册全部撤销 |
| ACP 桥接 | 通过 ACP 驱动 Claude Code 与 Codex 各完成一个轮次，并检测到额度耗尽信号 |

## Phase 1 — 单机核心闭环（第 1–6 周）

| 周 | 交付 |
|---|---|
| 1–2 | 插件宿主 v0（含 Profile、补丁、`--dump-config`、`crew plugins`）；会话日志与投影；Agent 注册表与默认 Agent 循环 |
| 2–3 | `llm` 能力：Anthropic、OpenAI-compatible、Gemini、DeepSeek、Ollama 适配器；`llm-acp` 桥接 Claude Code / Codex |
| 3–4 | 工具注册表与执行流水线；`shell`、`fs`、`sandbox`（Docker、Seatbelt）能力；规则引擎 v0；`approval` 能力与桌面应答方 |
| 4–5 | `router`：每个 Agent 的模型顺序与无损切换；`credentials`（Keychain / 凭据管理器 / libsecret）；`compaction` 80% 阈值 |
| 5–6 | 单 Agent 持久化工作区与 FIFO 队列；浏览器能力与接管；桌面端对话界面、Agent 设置、模型与用量 |

**出口标准**：一个 Agent 能在容器沙箱中完成编码任务，Claude Code 额度耗尽时自动切到 Codex 并继续，`git push` 触发审批并在批准后从断点继续；会话可从日志完整重建。

## Phase 2 — 多 Agent 协同、安全与自动化（第 7–12 周）

| 主题 | 交付 |
|---|---|
| 协同 | `channel` 能力：共享频道、单一负责人、交接、停止 / 恢复 / 改派 / 归档；`subagent`（local / fork / acp）；Agent 作用域的工具覆盖与限制 |
| 安全 | 三档运行模式（权限预设 + 网络旋钮）；规则引擎完整类别；审批中心；`partial` 执行上报 |
| 自动化 | `schedule` / `webhook`；Routines 与晨报；Workflows（触发器 → 串并行步骤 → 审批卡点） |
| 生态 | MCP 管理；技能按需加载；进程外插件 SDK（Go / TS / Python）与权限清单；插件与技能市场页面 |
| 界面 | 四栏窗口完整版、右侧面板、自动化页、插槽机制（对话卡片、右侧面板标签、设置卡片） |

**出口标准**：三个 Agent 在一个频道中完成「调研 → 实现 → 审查」并两次交接；第三方 TypeScript 插件可以从市场安装、按清单授权并热重载。

## Phase 3 — 远程主机、IM 网关与团队版（第 13–18 周）

| 主题 | 交付 |
|---|---|
| 部署 | `server` Profile；阿里云 ECS、AWS EC2 / ECS、通用 VPS 一键部署；零入站端口配对 |
| 多端 | 远程方法白名单网关；移动端伴侣（通知、审批、对话）；设备分级权限 |
| IM | Slack、Telegram、Discord、飞书网关（`notify` 提供方 + `approval` 应答方） |
| 团队 | 多人连接同一主机；成员与角色；审计日志（哈希链导出）；团队任务板（任务 DAG） |
| 目标 | 持久目标续跑（轮次上限、阻塞原因码、恢复后需重新授权） |

## 待决问题

| # | 问题 | 建议 | 负责人 |
|---|---|---|---|
| 1 | 交接是否需要接收方确认？ | 默认自动接收；频道可设置「交接需人确认」 | 产品 |
| 2 | 额度恢复后是否切回首选模型？ | 只在新任务开始时切回 | 产品 |
| 3 | 移动端的审批深度 | 代码推送类在手机上只提供「拒绝」与「稍后在桌面处理」 | 产品 / 设计 |
| 4 | 产品命名 | 暂定 Crewbase，需与 OpenBot、Cindy 等品牌区分并做商标检索 | 产品 |
| 5 | 远程浏览器接管的帧流方案 | Phase 2 末做带宽与延迟测试，再决定实时流或逐帧截图 | 工程 |
| 6 | 桌面壳选型 | 已采用 MyGo（`apps/desktop`）；Windows / Linux 真机验证未通过时再评估 Wails | 工程 |
| 7 | 是否兼容 Harness 插件 | 不做二进制兼容；提供 `subagent-acp` 互通，以及概念对照文档帮助迁移 | 工程 |
| 8 | OpenBot 许可证现状 | 发布对比材料前对照其仓库再次核实 | 产品 |
| 9 | 进程外插件的 Waterfall 超时 | 默认 2 秒；需在 Phase 2 用真实插件测量 | 工程 |
| 10 | 默认遥测 | 默认关闭；如开启，在首次启动时说明内容并可随时关闭 | 产品 / 法务 |
