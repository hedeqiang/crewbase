# Crewbase 开发 TODO（总调度维护）

> 依据 `docs/` 全部文档梳理。需求编号沿用 PRD（M / A / R / S / C / D）。
> 状态：`[ ]` 未开始 · `[~]` 进行中 · `[x]` 完成（有测试） · `[-]` 推迟 / 需外部条件
> 规则：每个迭代结束时 `make check`（go vet + go test + web build）通过后再提交。

## 迭代 0 — 仓库骨架
- [x] Go module `github.com/hedeqiang/crewbase`、目录结构（docs/04 §4）
- [x] LICENSE（Apache-2.0）、Makefile（build / test / check）、.gitignore
- [x] git 初始提交

## 迭代 1 — 插件宿主 kernel（Phase 0 / Phase 1 第 1–2 周）
- [x] Context、服务注册表（同键单实现，第二个注册报错）
- [x] Inject 依赖解析与 PENDING；依赖消失自动卸载、恢复后自动重载
- [x] 五种分发：Emit / Waterfall（next 与短路、Prepend）/ Parallel / Serial / Bail
- [x] Effect：逆序撤销、`ctx.Go` 取消并等待
- [x] 作用域：全局 + Agent 作用域；作用域分发（Agent 事件只发给全局与本 Agent 的监听器）
- [x] 隔离组（isolate）
- [x] Profile / Bundle / Patch 加载（按 id 替换、插入、禁用），`--dump-config`
- [x] 示例插件 `greet-tool` + `tool-logger` 跑通（Phase 0 出口）

## 迭代 2 — 核心插件 core（第 1–2 周）  ← 当前
- [x] `core/session`：仅追加 JSONL 会话日志（版本号 v1）、投影（deriveMessages）、崩溃补记「中断」
- [x] `core/llm`：消息 / 流式块词汇表、适配器约定、错误分类（限流 / 额度耗尽 / 故障）
- [x] `core/tools`：注册表 + 执行流水线（tool/call 先记录 → pre-execute → 守卫 → approval → execute → post-execute → 定稿 → tools/result → tool/result）
- [x] `core/scope`：覆盖（shadowing）与限制（restriction，取交集）
- [ ] `core/prompt`：提示词片段 + 工具 schema 组装
- [ ] `core/agent`：Agent 注册表、组装窗口、`agent.Inject()`、FIFO 队列（暂停 / 恢复 / 插队 / 取消）（A-1 部分）
- [ ] `core/agentloop`：默认 Agent 循环（turn / step 事件流）

## 迭代 3 — 模型（M-1 ~ M-4, M-6）（第 2–5 周）
- [ ] `llm-openai`（OpenAI-compatible：OpenAI / DeepSeek / Moonshot / Qwen / xAI）（M-2）
- [ ] `llm-anthropic`（M-2）
- [ ] `llm-gemini`（M-2）
- [ ] `llm-ollama` + 本地服务自动发现（Ollama / LM Studio / vLLM）（M-3）
- [ ] `llm-mock`（测试与离线演示）
- [ ] `llm-acp`：ACP 子进程桥接 Claude Code / Codex / Gemini CLI，额度耗尽信号识别（M-1）
- [ ] `router-default`：每 Agent 模型顺序、`router/quota-exhausted`、`model/switched` 事件、无损切换（M-4）
- [ ] `compaction-summary`：上下文占用追踪、80% 自动压缩（C-1）

## 迭代 4 — 执行与安全（S-1 ~ S-5）（第 3–5 周）
- [ ] `shell` / `fs` 能力定义 + 本地提供方；`tool-bash`、`tool-fs`（先读后改）
- [ ] `sandbox` 能力：`sandbox-none`、`sandbox-docker`、`sandbox-seatbelt`；`enforcement: full|partial`；`SANDBOX_UNAVAILABLE` 关闭式失败
- [ ] 运行模式预设 A / B / C（C 限时 24h）（S-1）
- [ ] 规则引擎：动作类别 Allow / Ask / Block 默认表、Agent 只能收紧、记住的选择（S-2）
- [x] `approval` 能力：闭合结果集、无应答方 = unavailable、never 策略、审计写入失败即失败（S-3）
- [ ] `credentials`：引用 + macOS Keychain / libsecret / 内存（测试）、post-execute 脱敏（S-4）
- [ ] `browser` 能力：CDP 本地池、每 Agent 独立 Profile、接管暂停（S-5）

## 迭代 5 — 守护进程与 Agent 持久化（第 5–6 周）
- [ ] `apps/crewd`：加载 Profile、HTTP + WebSocket API（远程方法白名单：鉴权 → 校验 → 激活）
- [ ] 工作区 `workspaces/<agent-id>/`、INSTRUCTIONS.md、MEMORY.md + SQLite FTS（A-1）
- [ ] 每 Agent 工具 / 提示词 / 模型（A-2）
- [ ] `apps/cli`：`crew plugins [--state pending]`、`crew --dump-config`、`crew agent`、`crew chat`
- [ ] 进程组管理：退出清理子进程树
- [ ] Phase 1 出口 e2e：mock 额度耗尽 → 切换 → `git push` 审批 → 批准后继续 → 日志重建

## 迭代 6 — 桌面 Web UI（Phase 1 界面）
- [ ] `apps/web`：React + TS + Vite；视觉 token（docs/03 §2，深浅色）
- [ ] 三列窗口：会话列表 / 对话区 / 右侧面板；标题栏「需要你」胶囊；⌘K
- [ ] 输入框：主机、运行权限、模型选择器；状态行（目录、额度、上下文占用浮层）
- [ ] 对话节点：消息、步骤折叠、模型切换说明、审批卡、浏览器接管卡
- [ ] 新建 Agent 表单与模板
- [ ] 设置：通用、模型与订阅（提供方 / 路由与切换 / 添加提供方）、审批（记住的选择）、个人资料与用量
- [ ] 插槽机制（conversation.node / sidebar-right.tab / settings.card / composer.action …）
- [-] 原生桌面壳（托盘、自动更新）D-1 —— 需验证 Wails；先以 crewd 内嵌静态资源 + 浏览器打开交付

## Phase 2 — 多 Agent 协同、自动化、生态（第 7–12 周）
- [ ] `channel-local`：共享频道、单一负责人、交接工具与 Waterfall、停止 / 恢复 / 改派 / 归档（A-3 A-4 A-5）
- [ ] `subagent`：local / fork / acp，最大深度、诊断 ≤4KB（A-6）
- [ ] 运行模式 A 只读调研（S-1 P1）
- [ ] `schedule` / `webhook`：Cron、最小间隔 60s、幂等投递（R-1）
- [ ] Routines + 晨报（R-1 R-2）
- [ ] Workflows：触发器 → 串并行步骤 → 审批卡点 → 发布，从失败步重跑（R-3 R-5）
- [ ] MCP 客户端与管理（C-3，P0）
- [ ] `history_search` / `history_read`（C-2）
- [ ] 技能按需加载（C-4）
- [ ] 进程外插件桥接：stdio JSON-RPC 2.0、清单权限、Waterfall 2s 超时、退出即撤销（S-6）
- [ ] 插件 SDK：TypeScript / Python / Go
- [ ] 插件与技能市场页、角色模板导入（C-5）
- [ ] 按任务分流 `router-cost` + 节省 token 展示（M-5）
- [ ] WASM 插件档（wazero）
- [ ] UI：频道与任务卡、交接 / 改派、审批中心、自动化页、插件与技能页
- [ ] Phase 2 出口 e2e：三 Agent 两次交接；TS 插件安装授权热重载

## Phase 3 — 远程、IM、团队
- [ ] `server` Profile、install-server.sh、加固 docker-compose.yml、阿里云 / AWS 模板（D-2）
- [ ] 零入站配对（WebSocket Relay）、设备分级权限
- [ ] IM 网关：Slack / Telegram / Discord / 飞书（notify + approval 应答方）（D-4）
- [ ] 移动端伴侣（D-3）
- [ ] 审计日志哈希链（S-7）
- [ ] 团队任务板 DAG（A-7）、成员与角色
- [ ] 持久目标续跑（R-4）
- [ ] OpenTelemetry 导出（默认关闭）

## 迭代日志
| 日期 | 迭代 | 结果 |
|---|---|---|
| 2026-11-14 | 2a | session 日志（JSONL、残行容错、投影、fork）、tools 流水线（先记录、pre-execute、守卫、审批、post-execute 脱敏）、approval（never / 无应答方 / 审计失败均关闭式失败）、作用域覆盖与限制；greet-tool + tool-logger 通过 |
| 2026-11-14 | 0 + 1 | 骨架、kernel（服务 / PENDING / 五种分发 / Effect / 作用域分发 / 隔离组 / Profile 补丁），race 测试通过；greet-tool 示例待 core/tools 完成后补 |
