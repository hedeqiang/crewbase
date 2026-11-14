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

## 迭代 2 — 核心插件 core（第 1–2 周）
- [x] `core/session`：仅追加 JSONL 会话日志（版本号 v1）、投影（deriveMessages）、崩溃补记「中断」
- [x] `core/llm`：消息 / 流式块词汇表、适配器约定、错误分类（限流 / 额度耗尽 / 故障）
- [x] `core/tools`：注册表 + 执行流水线（tool/call 先记录 → pre-execute → 守卫 → approval → execute → post-execute → 定稿 → tools/result → tool/result）
- [x] `core/scope`：覆盖（shadowing）与限制（restriction，取交集）
- [x] `core/prompt`：提示词片段 + 工具 schema 组装
- [x] `core/agent`：Agent 注册表、组装窗口、`agent.Inject()`、FIFO 队列（暂停 / 恢复 / 插队 / 取消）（A-1 部分）
- [x] `core/agentloop`：默认 Agent 循环（turn / step 事件流）

## 迭代 3 — 模型（M-1 ~ M-4, M-6）（第 2–5 周）
- [x] `llm-openai`（官方 openai-go v3；预设 openai / deepseek / moonshot / qwen / xai / openrouter / siliconflow / groq / mistral / zhipu；xAI 无官方 Go SDK，按其文档走 OpenAI SDK）（M-2）
- [x] `llm-anthropic`（官方 anthropic-sdk-go v2；adaptive thinking + effort，默认 claude-opus-5-5，Models API）（M-2）
- [x] `llm-gemini`（官方 google.golang.org/genai）（M-2）
- [x] `llm-local`：Ollama 原生 + LM Studio / vLLM 自动发现，上下线自动注册（M-3）
- [x] `llm-mock`（测试与离线演示）→ `providers/llm/mock`
- [x] `llm-acp`：ACP 桥接 Claude Code / Codex / Gemini CLI / Cursor / OpenCode / Grok，额度耗尽识别、进程组、空闲退出（M-1；真实 CLI 交互待人工验证）
- [x] `llm-magpie`：接入 magpie 网关（读 settings.json 端口、/api/hello 识别、/v1/models 目录、key `magpie-crewbase`、随 magpie 启停自动注册；本机真实 magpie 只读验证 32 个模型）
- [x] `router-default`：每 Agent 模型顺序、`router/quota-exhausted`、`model/switched` 事件、无损切换（M-4）
- [x] `compaction-summary`：上下文占用追踪、80% 自动压缩、立即压缩、新对话（C-1）

## 迭代 4 — 执行与安全（S-1 ~ S-5）（第 3–5 周）
- [x] `shell` / `fs` 能力定义 + 本地提供方；`bash`、`read/write/edit_file`、`list_dir`、`delete_file`（先读后改、进程组、输出上限、环境白名单）
- [x] `sandbox` 能力：none / docker / seatbelt；`enforcement: full|partial`；`SANDBOX_UNAVAILABLE` 关闭式失败（seatbelt 真机验证）
- [x] 运行模式预设 A / B / C（C 限时 24h，到期记 mode/changed）+ 守卫（只读、路径越界）（S-1）
- [x] 规则引擎：动作类别 Allow / Ask / Block 默认表、Agent 只能收紧、记住的选择（S-2）
- [x] `approval` 能力：闭合结果集、无应答方 = unavailable、never 策略、审计写入失败即失败（S-3）
- [x] `credentials`：引用 + macOS Keychain / libsecret / 内存（测试）、post-execute 脱敏（S-4）
- [x] `browser` 能力：chromedp + 本机 Chrome、每 Agent 独立 Profile（登录态持久）、空闲回收、进程树清理；接管期间工具全部拒绝、页面内容不入日志；逐帧截图接管（S-5）

## 迭代 5 — 守护进程与 Agent 持久化（第 5–6 周）
- [x] `apps/crewd`：加载 Profile、HTTP + WebSocket API（远程方法白名单：鉴权 → 校验 → 激活）
- [x] 工作区 `workspaces/<agent-id>/`、INSTRUCTIONS.md、MEMORY.md + SQLite FTS（A-1）
- [x] 每 Agent 工具 / 提示词 / 模型（A-2）：Agent 作用域覆盖与限制、INSTRUCTIONS.md、模型顺序
- [x] `apps/cli`：`crew plugins [--state pending]`、`crew --dump-config`、`crew agent`、`crew chat`
- [x] 进程组管理：shell / MCP / ACP / 插件 / 浏览器均按进程组清理（真实验证无残留）
- [x] Phase 1 出口 e2e（mock 模型版）：mock 额度耗尽 → 切换 → `git push` 审批 → 批准后继续 → 日志重建

## 通知
- [x] `notify` 能力 + 桌面渠道；alerts：审批（紧急）、失败、交接；模型切换默认不通知
- [ ] 免打扰时段、事件 × 渠道勾选表持久化（设置 › 通知）

## 迭代 6 — 桌面 Web UI（Phase 1 界面）
- [x] `apps/web`：React + TS + Vite；视觉 token（docs/03 §2，深浅色）；crewd 内嵌静态资源
- [x] 三列窗口：会话列表 / 对话区 / 右侧面板；标题栏「需要你」胶囊；⌘K；窄屏浮层
- [x] 输入框：主机、运行权限（宿主机红色确认条）、模型选择器；状态行（目录、额度、上下文占用浮层）
- [x] 对话节点：消息、步骤折叠、模型切换说明、审批卡（以后都允许 / 二次确认）、压缩与模式变更行
- [x] 浏览器接管卡（逐帧画面 + 点击 / 键入 / 滚动 / 交还控制权；真实 GitHub 登录页验证）
- [x] 新建 Agent 表单与模板（18 色 × 7 形状头像）
- [x] 后端：`GET /api/usage`（tokens、峰值日、53 周热力图数据、来源占比、Agent 排行、自动切换次数 / 需人工介入次数、订阅复用节省估算；仅本地）
- [x] 设置：通用、模型与订阅（只读）、路由与切换、审批（已记住 / 历史）、快捷键、个人资料与用量
- [x] UI 所需后端接口（features/console）：会话摘要、上下文构成、立即压缩、新对话、工作区文件读写（防越界含符号链接）、已记住选择与撤销、审批历史、模型重置、任务分流规则、默认值设置、主机列表；Agent 等审批时 waiting ✓
- [x] UI 接入上述接口 + 频道（@提及、交接线、改派、归档、交接需确认）/ 文件面板 / 上下文构成 / 联网搜索开关 / 来源引用 / 例行巡检 + 晨报 / 工作流表单编辑器 / 审批（已记住、历史）/ 插件市场与授权 / MCP / 用量与热力图 / 路由与切换 / 新 Agent 默认值（原：Agent 等审批时状态 waiting、会话摘要（updated/last/unread）、上下文构成、立即压缩、新对话、文件树与读写 INSTRUCTIONS/MEMORY、已记住选择列表与撤销、审批历史、模型重置/启用开关/多窗口额度、设置（默认模式与模型顺序）、主机列表、频道 UI）
- [ ] 插槽机制（conversation.node / sidebar-right.tab / settings.card / composer.action …）
- [x] 桌面客户端（MyGo v0.9.0，纯 Go 无 CGO）：内嵌 / 连接已运行的 crewd、隐藏标题栏 + 红绿灯留位、托盘（需你 N、开机启动、关闭窗口后继续运行、退出）、Dock 角标、原生通知、单实例、深链（#/session、#/settings）、`make desktop` 产出 .app + .dmg；真实启动 / 关闭 / 再次启动 / 退出无残留已验证（D-1）
- [ ] 桌面：自动更新（需生成更新签名密钥）、Developer ID 签名与公证、Windows / Linux 实机运行

## Phase 2 — 多 Agent 协同、自动化、生态（第 7–12 周）
- [x] `channel-local`：共享频道、单一负责人、交接工具与 Waterfall、停止 / 恢复 / 改派 / 归档（A-3 A-4 A-5）
- [x] `subagent`：local / fork（acp 提供方待接），最大深度、并发上限、取消传播、临时 Agent 清理、诊断 ≤4KB；`delegate` 工具（A-6）
- [x] 运行模式 A 只读调研（S-1 P1）
- [x] `schedule` / `webhook`：Cron / every / daily / weekly / at、最小间隔 60s、幂等键先落盘后投递、Webhook 独立密钥（R-1）
- [x] Routines + 晨报（R-1 R-2）：默认只读、NO_ISSUES 静默、需关注才通知；晨报只列待处理项，正常项折叠计数
- [x] Workflows：手动 / 定时 / Webhook 触发 → 串并行步骤 → 审批卡点，模板 {{input}}/{{prev}}/{{steps.x}}，从失败步重跑、取消、运行可从日志重建（R-3 R-5）
- [x] MCP 客户端与管理（C-3，P0）：官方 go-sdk v1.8；stdio（沙箱内、进程组）/ Streamable HTTP；`<server>__<tool>` 命名空间；注解→类别；list_changed 刷新失败保留旧列表；崩溃退避重连；凭证引用；Agent 作用域服务器；API 含探测
- [x] `history_search` / `history_read`、`memory_add` / `memory_search`（C-2）
- [x] 技能按需加载（C-4）：目录只进提示词，`skill_load` 取全文；工作区 > 共享目录按名称覆盖
- [x] 进程外插件桥接：stdio JSON-RPC 2.0、清单权限（交集授权）、Waterfall 2s 超时、只能拒绝不能放行、退出即撤销、崩溃退避后 FAILED、可选沙箱（S-6）
- [x] 插件 SDK：TypeScript / Python / Go（宿主注入，无需 npm install）；示例 word-count / hello-py / hello-go
- [x] 插件市场后端：索引（URL / 内置）、本地 / tarball / npm 安装、授权、启停、热重载、新版本重新授权、trustFutureVersions；CLI `crew plugin …` / `crew dev`
- [x] 市场与授权 UI
- [ ] 角色模板导入（C-5）；工作流画布编辑器；新建文件夹
- [x] 按任务分流（M-5）：routine / 摘要·日志清洗 / 编码 / 超长文档 → 配置的模型，不可用时回落到 Agent 顺序；节省见 `/api/usage`
- [ ] WASM 插件档（wazero）
- [x] UI：频道与任务卡、交接 / 改派、审批中心、自动化页、插件与技能页
- [x] Phase 2 出口 e2e：三 Agent 两次交接（mock + 真实模型）；TS 插件从市场安装 → 授权 → 使用 → 改源码 → 重载生效（自动化测试）

## 品牌与依赖（用户反馈后确立）
- [x] 产品 logo「Relay」、Agent「队员徽章」头像全部原创，不仿制他人 logo / 吉祥物（docs/brand）
- [x] 模型厂商图标用 LobeHub Icons（MIT，静态 SVG 按需引入；经 magpie 的模型按 id 识别厂商）
- [x] 前端依赖全部最新（React 19.3、Vite 8、TS 7、lucide-react 1.x）
- [x] 后端 Go 依赖统一升级到最新（`go get -u ./... && go mod tidy`，直接依赖逐一核对均为最新）

## 命名约定（用户反馈后确立）
- 目录按「能力 / 提供方」两层：`providers/llm/{anthropic,openai,gemini,local,acp,mock}`、`providers/sandbox/{none,docker,seatbelt}`、`providers/shell/local`、`providers/fs/local`
- 包名即末级目录；与官方 SDK 包名冲突时 SDK 用 `sdk` 别名导入；不再使用 `llmanthropic` 这类拼接名
- 内核插件名保持 `@crewbase/llm-anthropic` 形式（与文档一致）

## 技术债 / 待跟进
- [x] 交接是否需确认（待决问题 #1）：默认自动；频道设置「交接需人确认」走审批，无人可批即拒绝；等待时不持有服务锁
- [x] 二次确认改由审批服务强制（Decision.Confirmed），UI 回传
- [x] `agent/response` Waterfall：完整回复（含工具参数）落日志前可整体改写
- [x] redact 监听 agent/response（跨分片密钥、thinking、工具参数 JSON 内字符串）
- [x] `tool/call` 参数在脱敏前落日志 → 由 agent/response 统一脱敏覆盖
- [x] 规则插件缺失时流水线 fail-open → 改为 `requirePolicy` 关闭式失败
- [x] post-execute 出错时回落原始结果 → 改为扣留结果

- [x] `tool/pruned` 事件（DeriveMessages 识别）
- [x] compaction 对保留区内超大工具结果产出 tool/pruned（上下文真正缩小，history_read 可取原文）
- [x] Tool.PathArgs 声明路径参数
- [x] runmode 守卫使用 PathArgs；toolsbasic / MCP 声明
- [x] 「添加提供方」流程：12 个预设 + 自定义（OpenAI/Anthropic/Gemini 协议）→ 测试并拉取模型 → 勾选 → 添加；Key 写入系统钥匙串（keychain:crewbase/provider-<id>），providers.json 只存引用；独立 kernel 上下文热挂载/卸载，无需重启；移除时同步删除钥匙串条目（features/providers，2026-11-14 真机验证：经 magpie OpenAI 兼容端点添加 → 真实对话 → 移除）
- [x] llm 词汇表：Part.Meta、ModelInfo.SizeBytes、Usage.CacheRead/Write、Quota.Windows、Request.Agent/Session/Workspace
- [x] 提供方采用：Anthropic / Gemini 签名入 Part.Meta（重启可续）、Ollama SizeBytes、缓存 token；magpie 读取 /v1/magpie/quotas 多窗口额度
- [x] ACP：CLI 权限请求接入审批；会话按 agent+session、cwd = 工作区
- [ ] ACP：CLI 自行改文件的事件；重新检测 CLI
- [x] magpie：读取 /v1/magpie/quotas 显示额度
- [x] subagent-acp 提供方（把一个轮次交给 Claude Code / Codex，共享父 Agent 工作区，改动不丢）
- [x] Agent.StopInput(id)（只停该输入）
- [x] 临时 Agent 不落盘（Spec.Ephemeral），删除即清工作区，崩溃残留启动时清扫
- [x] 中文 FTS：trigram 索引（≥3 字），短词子串回退
- [x] 记忆索引异步写入（队列 + 溢出补扫，不阻塞会话追加）

- [x] scope 注册表：同名注册乱序撤销会复活已撤销条目 → 改为栈
- [ ] 插件：tarball / npm 包签名校验；Windows Job Object 进程树；发布者身份；默认不开沙箱时网络 / 文件限制仅声明
- [x] kernel：ApplyEntries 只管理条目加载的插件；LoadEntry；Context.Fail
- [ ] 浏览器：网关支持原始二进制响应（截图目前 base64 JSON）；有头窗口接管需重启浏览器；浏览器网络不受运行模式白名单约束；跨源 iframe / 新标签页
- [x] MCP：信任注解默认关闭 + 每工具类别覆盖；只读 Agent 的 MCP 沙箱随之只读
- [ ] MCP：host 信任服务器无法只读；暴露 resources / prompts；HTTP OAuth
- 有意保留：先读后改记录仅内存（重启需重读更安全）；Agent 级规则维护在引擎内部（设计选择）
- 推迟到 Phase 3：单次提权调用、Docker 白名单出站代理

- [x] UI 反馈的接口缺口：Agent 状态带 statusSession（接管 / 审批属于哪个会话）、浏览器状态 mine、审批 preview（工作流卡点摘要单行 + 可展开内容）、新 Agent 默认模型在 CLI 未安装时回落到可用提供方的强模型

## 代码评审（high，1cff1b3~25..HEAD）发现并修复
- [x] agent/response 出错时回落原始回复（脱敏失效）→ 关闭式失败
- [x] turn 早退路径不发 turn-end → 工作流 / 子 Agent 永久等待
- [x] 取消在「已领取未开始」窗口失效 → Claim 即记当前输入，StopInput 延迟生效
- [x] 按任务分流会把本地模型 Agent 的内容送到云端，且关键词分类易误判 → 本地专用 Agent 只分流到本地模型；分类改为句首意图；会话主题同样遵守
- [x] 交接确认等待后未复查归档 / 成员资格
- [x] 新建 Agent 每次阻塞查询模型列表 → 缓存至提供方变化
- [x] Context.Fail 来自旧激活会拆掉新实例 → 忽略过期上下文
- [x] MCP 刷新存在无工具窗口 → 先注册新再撤旧
- [x] 四份 common.go 复制 → providers/llm/internal/common 一份
- [x] compaction 与 DeriveMessages 对 thinking 判定不一致

## UI 重设计（用户反馈「有点怪」后）
- [x] 去掉浏览器式标签与标题栏 logo；44px 对话头部（谁、状态、负责人）；需你胶囊仅在有待处理时出现；⌘[ ⌘] 历史
- [x] 会话列表两行、居中 760px 对话列、模型名仅在变化时显示、简洁系统行、悬浮输入框、短模型名
- [x] 左下角菜单：去掉外观；姓名（可改）、版本与运行时长、今日用量、免打扰、快捷键、帮助与反馈、设置、退出 / 断开
- [x] 首次使用欢迎页（已连接的模型来源、三个模板一键开始）；浏览器顶部品牌位；收起侧栏后不再空
- [x] Markdown 表格渲染；模型名规整（DeepSeek Flash、Claude Opus 5.5）
- [x] 管理遗漏补齐：右键 / ⋯ 菜单重命名、移到文件夹、删除 Agent（工作区进回收区）、删除频道（日志保留审计）、归档；文件夹重命名与解散（成员移到顶层，不删任何东西）
- [x] 新建 Agent：取消改为描边按钮、按钮右对齐；模板库 12 个，「换一批」（欢迎页同步）；模板头像带首字；默认模型与守护进程一致（强模型优先）
- [x] 真实镜像（140MB）在加固 compose 下 healthy；发现命名卷 0755 → 启动时收紧为 0700

## Phase 3 — 远程、IM、团队
- [x] `server` Profile、install-server.sh（systemd）、加固 docker-compose（no-new-privileges、seccomp、只读根、shm 1g、不映射端口、可选 Tailscale）、Dockerfile、阿里云 ECS / AWS EC2 + Fargate 模板、docs/deploy.md（D-2）
- [x] 部署：真实 Docker 镜像构建与加固 compose 实跑
- [-] 云主机实跑（无账号）
- [ ] 零入站配对（WebSocket Relay）、设备分级权限
- [x] IM 网关：Slack（Socket Mode）/ Telegram（长轮询）/ Discord（Gateway）/ 飞书（长连接），全部只出站；通知 + 审批卡（push / payment 只给拒绝和「稍后在桌面处理」，不提供「以后都允许」）、白名单用户、@Agent 发消息、回复回传线程、免打扰、限速、令牌不落日志；默认关闭（D-4）
- [ ] IM：真实机器人账号联调；线程映射持久化；飞书显示名；长回复分段；附件；设置页 UI
- [ ] 移动端伴侣（D-3）
- [x] 审计日志哈希链（S-7）：规则判定 Ask/Deny、审批、规则变更、运行模式、插件启停、Agent 增删；跨月续链、校验 API；不进入模型上下文
- [x] kernel `kernel/plugin-changed` 生命周期事件；审计改为监听（不再轮询）
- [ ] 审计：网关鉴权失败、凭证引用变更、设备配对
- [ ] 团队任务板 DAG（A-7）、成员与角色
- [x] 持久目标续跑（R-4）：轮次上限、阻塞原因码、重启 / fork 后需重新授权、goal_complete / goal_blocked 工具
- [x] OpenTelemetry 导出（默认关闭，默认不含内容）；`crew doctor` 部署自检

## DeepSeek 全家桶（2026-11-13 发布）利用
- [x] 调研：deepseek-flash（V4.1-Flash-1017）三协议；原生 `deepseek_web_search` / `deepseek_read_url`；`safety_check`；`title`；独立 Search / Fetch / OCR / ASR / TTS
- [x] 真实调用（经 magpie 原生透传与标准 chat 接口）：返回 `annotations`（url_citation）、`server_tool_calls`、`usage.server_tool_use`
- [x] core：Request.NativeTools、Agent.Spec.NativeTools（按 Agent 开启，API 可改）、citation / server_tool 片段、StopRefusal、Usage.ServerTools
- [x] 杂活默认走 DeepSeek Flash：title / summarize 路由（直连优先，其次 magpie，不可用回落 Agent 顺序）；会话主题自动生成（features/topics）
- [x] openai 适配器 deepseek 方言：thinking 开关、NativeTools → deepseek_web_search / deepseek_read_url、解析 annotations（引用）/ server_tool_calls（搜索与阅读步骤，实时流式）/ server_tool_use（按次计费）、safetyCheck 与 refusal；预设 base URL 更新；magpie 对 deepseek/* 模型自动切方言
- [x] 真实端到端：Agent 开启联网搜索 → DeepSeek Flash 搜索全网 → 带出处回答；引用不回流到模型历史
- [ ] 压缩摘要优先用 summarize 路由模型
- [ ] 与模型无关的 `web` 能力：DeepSeek Search / Fetch 独立 API 作为 web_search / web_fetch 工具提供方（待官方文档）
- [ ] 文件输入（PDF / Office / 图片）、ASR / TTS（移动端语音）
- [x] UI：Agent 设置「联网搜索 / 阅读链接」开关、回复下方来源列表与搜索步骤（当前构建真实验证：thinking → server_tool → text → citation）

## 真实环境验证（2026-11-14，本机 + OrbStack Linux）
- [x] 完整桌面 Profile（含 ACP）启动：50 个插件条目全部 ACTIVE；`crew doctor` 全绿
- [x] Claude Code（ACP）真实轮次；CLI 写文件的权限请求进入 Crewbase 审批 → 批准 → 文件落入 Agent 工作区
- [x] Codex（ACP）真实轮次；Gemini CLI 未配置 key → 自动切到 Codex（auth）并完成
- [x] 真实 MCP 服务器（@modelcontextprotocol/server-filesystem，npx，seatbelt 沙箱内）探测 → 启用 → 真实模型调用其工具读文件
- [x] 插件市场：CLI 安装 TS 插件 → 授权 → 真实模型调用 → 改源码 → reload → 新行为生效
- [x] 浏览器：Agent 用本机 Chrome 打开 example.com 并正确读出标题与链接
- [x] Linux（Ubuntu 24.04 + systemd 容器）：install-server.sh 安装、幂等重跑、仅监听 127.0.0.1、数据目录 0700 / token 0600
- [x] Linux 沙箱：真实模型 → bash 在 bubblewrap 内执行，工作区内写入成功，/etc 写入被只读文件系统拦截
- [x] 加固 docker-compose：用户 65532、只读根、cap_drop ALL、seccomp、无端口映射、healthcheck healthy；容器内连真实模型
- 真实环境才暴露并已修复：
  - 网关路径参数含 `/`（ACP 审批 id `acp/<session>/<tool>`）无法批准 → 按转义路径逐段匹配
  - MCP 文件服务器服务的目录不在工作区 → 守卫拒绝；新增 Tool.AllowedRoots / Server.roots（命令行中的目录自动授予）
  - Linux 只有 Docker 沙箱提供方，容器 / 服务器无 Docker 时无法执行命令 → 新增 `sandbox-namespace`（bubblewrap），Linux 默认，安装脚本自动安装并自检
  - systemd 的 ProtectKernelTunables / ProtectKernelLogs / ProtectHostname 组合使 bubblewrap 无法挂载 /proc → 仅在 Docker 沙箱时启用（逐项二分定位）
  - `crew doctor` 与 crewd 读取的补丁不一致 → CREWBASE_PATCH 环境变量统一（compose 与 systemd 均已切换）
- [x] 完整镜像构建（经 docker.m.daocloud.io 镜像源，`REGISTRY` / `DISTROLESS` 构建参数）
- [-] IM 网关、阿里云 / AWS 云主机：无账号，无法真实验证

## 真实联调（本机 magpie，`go test -tags live ./test/live`）
- [x] 跨厂商对话：Claude Haiku / Sonnet、DeepSeek、OpenAI GPT、Codex 订阅、Cursor Composer
- [x] 沙箱内真实工具调用（bash 写文件、读回）
- [x] git push 触发审批 → 拒绝 → 模型重新规划
- [x] 请求时故障切换（magpie 404 → 下一个模型）
- [x] memory_add 写入 MEMORY.md
- [x] 频道 @指派 + 真实模型调用 handover → 接收方完成
- [x] 工作流：草稿 → 审批卡点 → 两厂商并行评审 → 定稿；子 Agent 委派计算并清理
- 发现并修复：模型 id 含 `@`（cursor/grok-4.7@500k）被误解析为推理强度；404「模型不存在」不触发切换；频道专用工具在一对一会话里暴露导致模型误用；工作流并行步骤共享历史导致提示词串扰（workflow 会话改为隔离投影）

## 接下来的迭代计划（2026-11-14 制定）
- **迭代 11 · 模型与通知配置可用**：「添加提供方」全流程（API Key → 钥匙串 → 补丁 → 热应用，无需重启）；设置 › 通知（事件 × 渠道勾选表、免打扰时段持久化，接入 IM 网关）；压缩摘要走 summarize 路由
- **迭代 12 · 插件与界面扩展**：界面插槽机制（插件可注册对话卡片 / 右侧标签 / 设置卡片）；角色模板导入（C-5，只迁移指令与流程）；新建文件夹
- **迭代 13 · 文件与多模态**：对话附件（PDF / Office / 图片）→ 工作区 + 模型多模态输入；DeepSeek 文件输入
- **迭代 14 · 远程与多端**：零入站配对（WebSocket Relay）、设备分级、移动端伴侣（PWA：通知、审批、对话）
- **迭代 15 · 团队**：成员与角色、团队任务板 DAG（A-7）
- **持续**：WASM 插件档、MCP resources / prompts、插件包签名、浏览器多标签、桌面自动更新（需密钥）

## 迭代日志
| 日期 | 迭代 | 结果 |
|---|---|---|
| 2026-11-14 | 10c | 删除 / 重命名 / 移动 / 文件夹管理（前后端 + 真实界面操作验证）；模板换一批；表单按钮；真实镜像 compose 验证 |
| 2026-11-14 | 10b | MyGo 桌面客户端交付并实机验证；UI 重设计（子 agent）+ 首次使用欢迎页、表格、模型名、深链；Dockerfile 支持镜像源前缀 |
| 2026-11-14 | 10a | 真实环境验证一轮：ACP 订阅 CLI、MCP、插件、浏览器、Linux systemd、bubblewrap 沙箱、加固 compose；修复 5 个真实环境问题；55 包 race 通过 |
| 2026-11-14 | 9f | 高强度代码评审 10 项全部修复；54 包 race、Windows 编译、真实模型回归通过 |
| 2026-11-14 | 9e | UI 反馈的 4 处接口缺口修复；确认当前构建联网引用真实可用（UI agent 测的是旧构建） |
| 2026-11-14 | 9d | 桌面 UI 第二轮（子 agent，真实 crewd + magpie 驱动，浅/深色多宽度截图审阅）提交 |
| 2026-11-14 | 9c | Go 依赖全量升级 + tidy；kernel 插件生命周期事件替代审计轮询（修复偶发失败）；54 包 race 通过，Windows / Linux 交叉编译通过 |
| 2026-11-14 | 9b | Phase 3 平台（子 agent）：部署、审计、目标续跑、OTel、crew doctor 接入；修 IM 审批卡跨端决定竞态；全量 53 包 race 测试通过 |
| 2026-11-14 | 9a | IM 网关（子 agent）接入，默认关闭 |
| 2026-11-14 | 8g | DeepSeek 原生联网搜索 / 阅读链接 / 安全检查端到端打通（真实调用） |
| 2026-11-14 | 8f | 技术债第三批（子 agent，10 项）接入；ACP 委派共享父工作区（SharedWorkspace），subagent-acp 接入 |
| 2026-11-14 | 8e | DeepSeek：core 词汇 + 默认廉价路由 + 会话主题；适配器部分待技术债 agent 交付后做（同文件） |
| 2026-11-14 | 8d | 浏览器能力 + 接管（子 agent，真实 Chrome 157 验证）接入；DeepSeek 全家桶调研 + 经 magpie 真实调用原生联网 |
| 2026-11-14 | 8c | 进程外插件 + SDK + 市场 + 热重载（子 agent）接入 crewd，Phase 2 出口全部满足；kernel 三项改进 |
| 2026-11-14 | 8b | 技术债第二批（core 接口）：agent/response、PathArgs、llm 词汇表扩展、tool/pruned、临时 Agent、交接确认；提供方 / 功能侧采用交给子 agent |
| 2026-11-14 | 8a | MCP（子 agent）接入；技术债第一批：二次确认强制、StopInput、scope 乱序撤销复活修复；技术债分类（修 / 保留 / 推迟） |
| 2026-11-14 | 7d | 桌面 Web UI（子 agent 交付，headless Chrome 浅/深色 4 个宽度截图验证）提交；`make build` 内嵌 UI |
| 2026-11-14 | 7c | 工作流 + 子 Agent（子 agent 交付）接入；core：agent-loop drivers 竞态、PreStep 拒绝补 turn-end、workflow 会话隔离投影；真实联调新增工作流与委派 |
| 2026-11-14 | 7b | 本机 magpie 真实联调 6 个模型 + 6 个场景全部通过；修复 3 个真实环境才暴露的问题（见上） |
| 2026-11-14 | 7a | 模型层完成：官方原生 SDK（OpenAI / Anthropic / Gemini）、本地发现、ACP 订阅桥接、magpie 网关（用户新增需求）；默认 Profile：订阅 CLI 优先，mock 默认关闭；usage 新增 gateway 类别；真实环境发现 4 个订阅 CLI + magpie |
| 2026-11-14 | 6e | 技能按需加载；Windows 交叉编译检查（生产代码通过，修一处测试构建标签）；并行中：原生 SDK 适配器、ACP、UI、工作流 + 子 Agent |
| 2026-11-14 | 6d | Routines（schedule 作业承载、独立会话、只读、静默哨兵）、晨报、alerts、routines API；冒烟：Webhook 无密钥 401、带密钥触发 → 巡检运行 |
| 2026-11-14 | 6c | 执行层（子 agent）：sandbox / shell / fs 能力 + seatbelt/docker/none + 基础工具 + runmode 守卫；端到端：沙箱内 bash、越界写被拦截、git push 挂起审批；调度器（cron 解析、幂等、Webhook 公开路由自带密钥）；notify 能力；core：turn-start 事件、重进宿主机模式重置 24h |
| 2026-11-14 | 6b | 压缩（回合边界切点不拆工具对、模型摘要 + 抽取式回退、用量分段）、记忆（MEMORY.md + SQLite FTS5、history_search/read、memory_add/search）；core：Input 级运行模式（只能收紧）、Agent.SetSession、turn-end 携带最终文本 |
| 2026-11-14 | 6a | 频道（单一负责人、@指派、交接工具 + Waterfall、停止/恢复/改派/归档、API）；三 Agent 两次交接 e2e；规则引擎、凭证保险箱（Keychain/libsecret/env/mem）、脱敏（子 agent 交付）；工具结果 StopTurn；两处 fail-open 改关闭式失败；用户要求：官方提供方改用原生 SDK（适配器 agent 重做中） |
| 2026-11-14 | 5a | gateway（远程方法白名单：鉴权→校验→激活、设备分级、SSE 事件流）、core API、bundles / profiles（${root} 替换、补丁层）、crewd 与 crew CLI；本机冒烟通过。并行中：API 模型适配器、执行与沙箱、规则与凭证、ACP 桥接、压缩与记忆 |
| 2026-11-14 | 2b | prompt / agents（工作区、INSTRUCTIONS/MEMORY、作用域、FIFO 队列可从日志恢复）/ agent-loop / router-default / llm-mock；Phase 1 出口用 mock 模型跑通（切换、请求可从日志重建、push 审批断点续跑、重启恢复） |
| 2026-11-14 | 2a | session 日志（JSONL、残行容错、投影、fork）、tools 流水线（先记录、pre-execute、守卫、审批、post-execute 脱敏）、approval（never / 无应答方 / 审计失败均关闭式失败）、作用域覆盖与限制；greet-tool + tool-logger 通过 |
| 2026-11-14 | 0 + 1 | 骨架、kernel（服务 / PENDING / 五种分发 / Effect / 作用域分发 / 隔离组 / Profile 补丁），race 测试通过；greet-tool 示例待 core/tools 完成后补 |
