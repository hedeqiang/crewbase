# 参考笔记：DeepSeek Harness

> 调研日期 2026-11-14，基于 `deepseek-ai/deepseek-harness` 仓库 `master` 分支的中文文档。Harness 处于开发者预览阶段，接口会有破坏性变更；引用具体名称前请对照最新文档。

## 1. 基本信息

| 项 | 内容 |
|---|---|
| 仓库 | [github.com/deepseek-ai/deepseek-harness](https://github.com/deepseek-ai/deepseek-harness) |
| 文档 | [deepseek-harness.github.io/deepseek-harness](https://deepseek-harness.github.io/deepseek-harness/) · [产品页](https://www.deepseek.com/harness/en/) |
| 许可证 | MIT |
| 语言 | TypeScript（Node.js）；提供 TypeScript 与 Python SDK |
| 命令 | `dsh`；`npx @deepseek-ai/dsh web` 启动 Web UI（默认 `127.0.0.1:3080`） |
| 底层框架 | [Cordis](https://github.com/cordiverse/cordis)，设计见论文 *A Programming Paradigm for Spatiotemporal Composability*（arXiv 2608.25512） |
| 口号 | Everything is a Plugin |
| 安全声明 | 开发者预览，未经安全审计；沙箱、审批与权限控制不保证隔离 |

## 2. 核心思想

### 2.1 Cordis 的五个概念

1. **插件是实现服务的对象**：带可选 `inject` 和 `apply(ctx)` 的函数，或 `Service` 子类。
2. **上下文是服务的容器**：服务占据稳定的 `ctx.<key>`（如 `ctx.tools`、`ctx.llm`、`ctx.sessions`），其他插件按键查找，而不导入实现。
3. **用 `inject` 声明依赖**：依赖就绪后才启动；加载顺序由依赖表达，而非手工编排。依赖缺失时插件处于 PENDING（合法状态）。
4. **类型化事件通信**：五种分发模式 `emit` / `waterfall` / `parallel` / `serial` / `bail`。`waterfall` 是环绕中间件：调用 `next()` 委托下游，不调用即短路。
5. **注册是可逆的副作用**：提示词片段、工具 schema、适配器、监听器通过 `ctx.effect()` / `ctx.on()` 安装，reload 与 teardown 时撤销。

### 2.2 没有特权内核

产品的每一部分都是插件，包括模型适配器、工具注册表、会话日志和 Agent 循环本身。扩展方式是把插件挂到其他插件旁边，不给内核打补丁。

### 2.3 Profile、组合包与补丁

- 运行中的 `dsh` 是一棵插件树，由各层按序叠加：Profile 中有序的组合包 → Profile 的 `cordis.patch.yml` → home patch → `--patch`。
- 内置 Profile：`web`、`headless`、`sdk`、`sdk-minimal`、`acp`。共享第一层 `dsh-base` 提供模型适配器、工具、持久化、沙箱与审批策略、设置、凭据、遥测。
- 补丁按条目 `id` 替换整个配置或插入新条目；`dsh --profile web --dump-config` 打印完整配置树。
- 条目没有稳定 `id` 时，每次编辑配置都会被视为先删后加。

### 2.4 能力 seam

一项可替换能力由 **Service Definition**、**Service Provider**、**Consumer** 三种角色组成。规范范例是 Shell：`dsh-shell`（定义）、`dsh-bash-local` / `dsh-bash-sandbox`（提供方）、`dsh-tool-bash`（消费方）。能力图把 `ctx.*` 服务标为 `core`、`seam`、`service`、`bundle` 四类。

「替换一个提供方就能改变整个产品」：文件系统与进程提供方共享同一个执行世界，指向远程沙箱时，Bash、PTY、LSP 一并迁移。

### 2.5 作用域

- 两层扁平结构：全局与 Agent 作用域；Agent 作用域的注册不继承给 subagent。
- shadowing：同名的作用域注册在该作用域内替换全局版本。
- restriction：`tools.restrict` 为单个作用域过滤全局工具，多个限制取交集；被过滤的工具与不存在无法区分。
- setup window：创建 Agent 时专用于注册的时机。
- lineage：父子关系以数据携带，不影响可见性。

### 2.6 会话日志与轮次

- 会话是仅追加的 `SessionEvent` 日志；`deriveMessages()` 从中投影模型历史。
- **模型可见即已记录**：每个模型请求都必须能从日志重建。
- 层级：步骤（一次模型请求 + 其工具）、轮次（排空一批输入）、Round（外层策略的一次迭代）。
- 扩展点：`agent/pre-step`、`agent/request`、`llm/stream`、三个 `tools/*` 事件为 waterfall；`agent/turn-stopping` 为 serial。

### 2.7 工具执行流水线

`tool/call`（先记录）→ `tools/pre-execute`（waterfall：钩子、权限、沙箱）→ 单调守卫（只能拒绝或弃权）→ `ctx.approval`（ask 时）→ `tools/execute`（waterfall：超时、重试、计量）→ 工具本体（`fs/write-intent` 等能力事件）→ `projectContent` → `tools/post-execute`（waterfall：接受、阻止、替换、追加上下文）→ `finalizeContent` → `tools/result`（同步通知）→ `tool/result`（会话事件）。

## 3. 子系统要点

| 子系统 | 关键设计 | 我们的落点 |
|---|---|---|
| approval | 结果闭合集合 `allowed-once` / `rejected` / `cancelled` / `unavailable`；无应答方即 `unavailable`；`never` 在分发前强制；`approval/asked` / `approval/decided` 写日志，写不进则失败；请求只带 `callId` | 审批挂起与恢复 |
| sandbox | `read-only` / `workspace-write` / `danger-full-access`；`confine()` 返回 `ConfinedArgv`（`enforcement: full / partial`、拦截特征）；策略随调用携带；不可用时 `SANDBOX_UNAVAILABLE`；只管文件效果，不管网络 | 三档运行模式；网络单独做旋钮 |
| permission-presets | 预设 = 沙箱模式 + 审批策略；`custom` 是派生值；预设层本身不执行策略 | 输入框的权限下拉 |
| agent-team | 共享任务 DAG，`ownerId`、`blockedBy`、`writeScopes`（提示不是锁）、`revision`（CAS）；任务变更不唤醒 Agent | 团队任务板（Phase 3）；单一负责人 |
| subagent | 提供方注册表（`spawn` / `fork` / `acp` / Codex / Claude Code / SDK）；统一准入、容量、取消；`maxDepth`；诊断不含凭证、≤ 4096 字节 | 子 Agent 委派与订阅复用 |
| compaction | `compaction/start` / `summary` / `end` 事件；摘要替换原区间；先剪枝工具结果；区间边界保持工具调用与结果配对 | 80% 上下文压缩 |
| mcp | 每个服务器一个连接插件，以服务器名为命名空间；刷新失败保留上一代工具列表；可先 probe 再启用 | MCP 管理 |
| skills | 多来源按 rank 决定同名胜者；目录只注入名称与描述，正文按需加载 | 技能按需加载 |
| credentials | 配置只存 `CredentialRef`；每次操作解析；`describe()` 只给元信息与掩码；明文单向写入 | 凭证保险箱 |
| schedule | `after` / `at` / `every` / `daily` / `weekly` / `cron` / `rrule`；管理与投递共用 FIFO；最小间隔 60 秒；收件箱与回执之间崩溃可能重复投递 | Routines（加幂等键） |
| goal | 单个持久目标，`active` / `paused` / `blocked` / `complete`，Goal Round 上限；激活态不参与回放 | 目标续跑 |
| browser-use | 只允许一个提供方名；浏览器归属确定的会话；登录态不从日志恢复 | 浏览器接管 |
| extensions | Agent 可定义带版本的插件包（Host / Client 两半）；未授权包需审批；`approveFutureVersions` | 插件安装授权 |
| claude-code-mods | 桥接 Claude Code 模组；模组拥有进程全部权限、不受沙箱约束 | 提醒：第三方钩子 = 完全可信代码 |
| slots | UI 通过 `ctx.slots.register()` 贡献；数量 `single` / `list` / `keyed` / `chain` / `hoc`；组件拿不到 `ctx` | 界面插槽 |
| api-gateway | 只有标记 `@Remote` 的方法可远程调用；先 `remoteAccess.decide()` 鉴权，再校验参数，最后解析与激活 | 远程网关、移动端、IM |

## 4. 我们不照搬的部分

| 方面 | Harness | Crewbase | 原因 |
|---|---|---|---|
| 语言 | TypeScript，进程内动态加载插件 | Go 内核 + 进程外 / WASM 第三方插件 | 单文件分发、常驻低内存；Go 不适合进程内热加载（见 [ADR-0002](../decisions/0002-plugin-runtime-tiers.md)） |
| 产品视角 | 单人、以会话为中心 | 多 Agent、以频道和负责人为中心 | 我们的核心场景是团队协作 |
| 默认权限 | 预设含 `danger-full-access` + `never` | C 模式下审批策略也不允许 `never`；外部支付不可设为 Allow | 面向非开发者用户，默认值更严格 |
| 订阅复用 | 子 Agent 提供方可桥接 Claude Code / Codex | 订阅复用是一等公民：路由、额度展示、无损切换 | 核心卖点之一 |
| 遥测 | 有用户报告桌面版默认开启遥测（第三方来源，未核实） | 默认关闭 | 隐私承诺 |

## 5. 许可证说明

Harness 采用 MIT 许可证。借鉴其设计思想不产生义务；如复制其代码或文档，需要保留原版权声明与许可文本。Crewbase 核心为 Apache-2.0，可以包含 MIT 代码。本笔记只做概念摘要，没有复制其源代码。

## 6. 推荐阅读顺序（Harness 仓库内）

1. `docs/cordis-primer.zh.md`：五个核心概念与分发模式
2. `docs/cordis-tutorial/`：七章教程，第 7 章「进入 harness」最贴近我们的 Phase 0 验证
3. `docs/architecture.zh.md`：Profile、核心包、事件域、轮次流程、「新行为的归属位置」表
4. `docs/glossary.zh.md`：作用域、目标、Ralph 循环等术语
5. `docs/tool-execution-pipeline.zh.md`
6. `docs/subsystems/approval.zh.md`、`sandbox.zh.md`、`permission-presets.zh.md`、`subagent.zh.md`、`agent-team.zh.md`
7. `SAFETY.zh.md`
