# Crewbase 开发 TODO（总调度维护）

> 依据 `docs/` 全部文档梳理。需求编号沿用 PRD（M / A / R / S / C / D）。
> 状态：`[ ]` 未开始 · `[~]` 进行中 · `[x]` 完成（有测试） · `[-]` 推迟 / 需外部条件
> 规则：每个迭代结束时 `make check`（go vet + go test + web build）通过后再提交。

## 迭代 0 — 仓库骨架
- [x] Go module `github.com/hedeqiang/crewbase`、目录结构（docs/04 §4）
- [x] LICENSE（MIT；2026-11-15 由 Apache-2.0 改为 MIT）、Makefile（build / test / check）、.gitignore
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
- [x] 设置 › 通知：事件 × 渠道勾选表（列 = 已连接渠道：桌面 + 已启用的 IM 网关 / 进程插件渠道，每列可「发送测试」）、每天定时免打扰（可跨午夜）+ 临时免打扰；只存用户改过的格子，覆盖 IM 配置里的 notifyKinds 默认值；审批等紧急通知始终送达；免打扰期间的通知仍进收件箱；持久化在 settings.json

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
- [x] 插槽机制：conversation.node（事件类型或 tool:<工具名>）/ sidebar-right.tab / settings.card / palette.command / composer.action；声明式、随插件上下文卸载自动消失、同键拒绝重复注册；进程外插件经 ui/panel、ui/run 回调（需声明服务 ui）；titlebar.action / settings.section 待需要时再开放
- [x] 桌面客户端（MyGo v0.9.0，纯 Go 无 CGO）：内嵌 / 连接已运行的 crewd、隐藏标题栏 + 红绿灯留位、托盘（需你 N、开机启动、关闭窗口后继续运行、退出）、Dock 角标、原生通知、单实例、深链（#/session、#/settings）、`make desktop` 产出 .app + .dmg；真实启动 / 关闭 / 再次启动 / 退出无残留已验证（D-1）
- [x] 发版流水线：CI（vet、race 全量测试 Linux + macOS、Windows 交叉编译、网页构建）；推送 v* 标签 → 桌面三平台安装包 + 签名自动更新、crewd / crew / crew-relay 六平台压缩包、ghcr 镜像，全部成功后自动发布（docs/release.md）
- [x] 桌面自动更新：本机生成 Ed25519 更新密钥（公钥在 mygo.json），更新窗口与「检查更新…」菜单；本机构建验证产出签名清单
- [ ] 桌面（暂缓，暂无 Apple Developer ID）：Developer ID 签名与公证（暂用临时签名，首次打开需右键打开）、Windows / Linux 实机运行；私钥需加入仓库 Secrets（`gh secret set MYGO_UPDATER_PRIVATE_KEY`）

## Phase 2 — 多 Agent 协同、自动化、生态（第 7–12 周）
- [x] `channel-local`：共享频道、单一负责人、交接工具与 Waterfall、停止 / 恢复 / 改派 / 归档（A-3 A-4 A-5）
- [x] `subagent`：local / fork（acp 提供方待接），最大深度、并发上限、取消传播、临时 Agent 清理、诊断 ≤4KB；`delegate` 工具（A-6）
- [x] 运行模式 A 只读调研（S-1 P1）
- [x] `schedule` / `webhook`：Cron / every / daily / weekly / at、最小间隔 60s、幂等键先落盘后投递、Webhook 独立密钥（R-1）
- [x] Routines + 晨报（R-1 R-2）：默认只读、NO_ISSUES 静默、需关注才通知；晨报只列待处理项，正常项折叠计数
- [x] Workflows：手动 / 定时 / Webhook 触发 → 串并行步骤 → 审批卡点，模板 {{input}}/{{prev}}/{{steps.x}}，从失败步重跑、取消、运行可从日志重建（R-3 R-5）
- [x] MCP 客户端与管理（C-3，P0）：官方 go-sdk v1.8；stdio（沙箱内、进程组）/ Streamable HTTP；`<server>__<tool>` 命名空间；注解→类别；list_changed 刷新失败保留旧列表；崩溃退避重连；凭证引用；Agent 作用域服务器；API 含探测
- [~] 内置 Google 连接器（features/google）：Gmail / 日历 / 云端硬盘 / 文档 / 表格 / 幻灯片，一键 Google 官方登录（PKCE + 回环回调到网关，无需本机安装）；刷新令牌只在钥匙串，invalid_grant → 需要重新登录，退出登录即撤销；23 个工具按类别审批（发送、共享、邀请 = 对外发布）；市场条目 builtin:google。用本地伪造的 Google 端到端验证（界面连接 → mock Agent 调 gmail_search → 发送走审批 → 退出撤销）。待办：所有者提供桌面客户端密钥（ID 已内置）、真实账号验证、受限 scope 的 Google 安全评估（docs/06 §7.1）
- [x] `history_search` / `history_read`、`memory_add` / `memory_search`（C-2）
- [x] 技能按需加载（C-4）：目录只进提示词，`skill_load` 取全文；工作区 > 共享目录按名称覆盖
- [x] 进程外插件桥接：stdio JSON-RPC 2.0、清单权限（交集授权）、Waterfall 2s 超时、只能拒绝不能放行、退出即撤销、崩溃退避后 FAILED、可选沙箱（S-6）
- [x] 插件 SDK：TypeScript / Python / Go（宿主注入，无需 npm install）；示例 word-count / hello-py / hello-go
- [x] 插件市场后端：索引（URL / 内置）、本地 / tarball / npm 安装、授权、启停、热重载、新版本重新授权、trustFutureVersions；CLI `crew plugin …` / `crew dev`
- [x] 市场与授权 UI
- [x] 新建文件夹：列表 ＋ › 新建文件夹，空文件夹保留（settings.json）并显示提示；重命名 / 解散对空文件夹同样有效；文件夹列表统一为 useFolders()
- [x] 角色模板导入（C-5）：新建 Agent › 导入角色模板，支持 Hermes 文件夹（SOUL.md + skills/）、.zip、角色 .md（front matter）/ .json（含 tasks / workflows → 技能）、粘贴文本；先预览再导入；只迁移指令（INSTRUCTIONS.md）与技能，记忆 / 对话 / 密钥 / 原产品设置一律不导入并逐项列出原因；路径穿越、超大文件、写入均不可执行；网关按路由设置请求体上限，超限返回 413 而不是截断
- [x] 工作流画布编辑器：从左到右的流程图（触发 → 各步 → 完成），连线上「+」插入 Agent / 并行 / 审批，拖动节点放到箭头上成为新的一步、放到另一个 Agent 上即并行（单分支自动还原为普通步骤），点节点在右侧检查器编辑（Agent、权限、提示词、往前 / 往后），与后端 steps 一一对应
- [x] 按任务分流（M-5）：routine / 摘要·日志清洗 / 编码 / 超长文档 → 配置的模型，不可用时回落到 Agent 顺序；节省见 `/api/usage`
- [x] WASM 插件档（bridge/wasm，wazero）：清单 runtime = "wasm" + hooks（stream_chunk / tool_result / rule），JSON ABI（alloc + hook_<name>）；16 MiB 内存上限、只给无文件 / 网络 / 环境的 WASI、只认 WASI 导入；每次调用 50 ms 预算，超时 / 出错按「不改」处理并换新实例（编译缓存）；rule 只能收紧；钩子作为权限经安装审批；示例 examples/plugins/wasm-redact（Go wasip1）
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
- [x] ACP：CLI 自行改文件的事件（edit / delete / move 工具调用的 locations、diff 内容与 fs/write_text_file 汇总为每轮一次 acp/files-changed，SSE files.changed 让文件面板自动刷新）；重新检测 CLI（每分钟扫描 PATH，打开订阅页时也扫描，新装的 CLI 不用重启即可用）
- [x] magpie：读取 /v1/magpie/quotas 显示额度
- [x] subagent-acp 提供方（把一个轮次交给 Claude Code / Codex，共享父 Agent 工作区，改动不丢）
- [x] Agent.StopInput(id)（只停该输入）
- [x] 临时 Agent 不落盘（Spec.Ephemeral），删除即清工作区，崩溃残留启动时清扫
- [x] 中文 FTS：trigram 索引（≥3 字），短词子串回退
- [x] 记忆索引异步写入（队列 + 溢出补扫，不阻塞会话追加）

- [x] scope 注册表：同名注册乱序撤销会复活已撤销条目 → 改为栈
- [x] 插件：包完整性与发布者签名——市场条目可固定 sha256（integrity）并带 ed25519 签名（签名绑定包内容 + 名称 + 版本），受信发布者列表（publishers）验证通过才显示「已验证」，篡改 / 签名不符 / 发布者不受信一律拒装，requireSigned 可拒绝未签名包；`crew plugin keygen` / `crew plugin sign` 生成密钥与签名
- [x] Windows 进程树：插件、MCP、ACP、命令执行启动后立即放进 Job Object（KILL_ON_JOB_CLOSE），停止时整棵树一起结束，Crewbase 自身退出时 Windows 也会清理（internal/winjob；已在 macOS 上交叉编译 windows/amd64、arm64 通过，尚未在真实 Windows 上运行）
- [ ] 插件：默认不开沙箱时网络 / 文件限制仅声明；Windows 实机验证进程树
- [x] kernel：ApplyEntries 只管理条目加载的插件；LoadEntry；Context.Fail
- [x] 浏览器：网关原始二进制响应——接管画面改为 `?raw=1` 直接取 PNG（比 base64 JSON 小三分之一，尺寸和网址在响应头里，不缓存，经中继也能用）
- [x] 浏览器：运行模式网络白名单（browser-tools allowHosts：只读 / 沙箱模式下只能打开名单内主机，点击 / 回退 / 跳转离开名单立即清空页面并告知原因；主机模式不限）
- [x] 浏览器：白名单拦截页面全部请求（Fetch 域：文档、iframe、脚本、图片、XHR / fetch），名单外一律 BlockedByClient；data: / blob: / about: 放行；人接管时解除、Agent 下次操作恢复；主机模式不限
- [x] 浏览器：新标签页——target=_blank 链接、带 target 的表单和 window.open 都在 Agent 当前标签页打开（初始化脚本），Agent 始终看得到自己到了哪里
- [x] 浏览器：接管时「在窗口中打开」——无头浏览器在同一 Profile 上关闭后以真实窗口重开，标签页（网址、滚动位置、前台标签）和会话 Cookie 一起带过去，交还时回到无头并恢复；Agent 全程暂停，窗口打不开回落画面视图，窗口被关掉也记得标签页；只给本机桌面（手机 / 中继仍用逐帧画面）；附加模式不变
- [x] 浏览器：跨源 iframe——每个实例一条 flat 会话 CDP 连接，Target.setAutoAttach（waitForDebuggerOnStart + flatten）挂上进程外 iframe；快照在每个框架的隔离 world 里运行并拼成一个大纲（全页统一编号），点击 / 输入发到对应框架，坐标逐层换算到页面；同源 iframe 同样可用；白名单拦截也覆盖进程外 iframe 的请求
- [x] MCP：信任注解默认关闭 + 每工具类别覆盖；只读 Agent 的 MCP 沙箱随之只读
- [x] MCP：host 信任服务器在只读模式下只能用读取类工具（tools.Tool.Unconfined）；resources / prompts 以 `<server>__resources` / `<server>__prompt` 读取类工具提供（服务端声明了才注册；prompt 作为参考返回，不冒充用户消息）；HTTP OAuth（MCP 授权规范：受保护资源元数据 → 授权服务器元数据 → 动态注册或预注册客户端 → PKCE 授权码；登录链接在服务状态里，浏览器授权后回调网关；令牌与刷新所需信息存系统钥匙串，刷新后写回，移除服务或退出登录即删除）
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
- [x] 零入站配对（WebSocket Relay）、设备分级权限：features/devices（一次性配对码、令牌只存哈希、手机 / 电脑两级、改名 / 移除即断开）；features/relay + apps/relayd（crew-relay）：主机与设备都只出站，ECDH P-256 + HKDF + AES-256-GCM 端到端加密、主机签名挑战、只转发 /api/；网页端 WebCrypto 隧道（REST、SSE、上传、图片）
- [x] IM 网关：Slack（Socket Mode）/ Telegram（长轮询）/ Discord（Gateway）/ 飞书（长连接），全部只出站；通知 + 审批卡（push / payment 只给拒绝和「稍后在桌面处理」，不提供「以后都允许」）、白名单用户、@Agent 发消息、回复回传线程、免打扰、限速、令牌不落日志；默认关闭（D-4）
- [x] IM：线程映射持久化（im-<name>-threads.json，重启后旧话题里的回复仍到同一个 Agent 与对话）；长回复按段落 / 句子分段（最多 4 条，代码块跨段保持闭合，标注 1/n）；设置页 UI（设置 › 即时通讯：选平台、填令牌（进钥匙串）、频道与允许的人，保存即连接，可停用 / 测试 / 断开，断开会删除令牌）
- [x] IM：飞书 / Lark 同时支持——同一网关，Lark 走 open.larksuite.com 的令牌与长连接，会话、线程与通知列互相独立；测试验证 Lark 网关只请求自己的域名
- [x] IM：飞书 / Lark 合并为一个平台（选版本：中国大陆 飞书 open.feishu.cn / 国际版 Lark open.larksuite.com，创建应用的链接跟着版本）；以前保存的 lark 网关读取时映射为「飞书 / Lark · 国际版」，id、令牌、话题文件、通知列都不变。即时通讯独立为设置分区（设备组）：每个平台一张卡（未连接 / 已连接 N 个群 / 出错及原因），连接是三步对话框（创建机器人 → 填凭据并测试连接（只问平台机器人是谁，不发消息、不保存）→ 选群、允许的人、所属团队、旁听 Agent、回复回传、群内审批）；已连接的群逐行列出，⋯ 菜单编辑 / 发送测试 / 暂停 / 移除；群聊旁听随网关移到这里，通知页只留事件 × 渠道表、免打扰和跳转入口
- [ ] IM：真实机器人账号联调；飞书 / Lark 显示名；附件
- [x] 移动端伴侣（D-3）：PWA（manifest、主屏图标、只缓存外壳的 service worker）；扫码配对页；手机权限下隐藏管理入口（新建、运行权限、管理类设置）；后台审批系统通知，点通知打开对应对话
- [x] 审计日志哈希链（S-7）：规则判定 Ask/Deny、审批、规则变更、运行模式、插件启停、Agent 增删；跨月续链、校验 API；不进入模型上下文
- [x] kernel `kernel/plugin-changed` 生命周期事件；审计改为监听（不再轮询）
- [x] 审计：设备配对 / 改名 / 移除；网关拒绝的令牌（按来源地址每分钟限一条）
- [x] 审计：凭证引用变更（credential/ref：只记引用与 set / delete，不记值；测试用 mem: 引用不记）
- [x] 团队任务板 DAG（A-7）：每棵委派树（对话 + 子 Agent 会话）共用一块板，存在根会话日志里（可回放、跟随 fork）；每个任务唯一负责人（Agent / 成员 / 未指派）、依赖不成环、依赖未完成不能开始、受阻必须写原因；Agent 只能改自己的或认领未指派的；写入范围只是提示；task_list / task_add / task_update 工具，有未完成任务时自动进入提示词；右侧任务页的看板（进度、可以开始、依赖、改状态 / 负责人）
- [x] 成员与角色：管理员 / 成员 / 只读；按类别授权审批（付款永不远程批准）；为成员配对设备；角色实时生效、移除即断开；消息与审批署名；审计 member/*
- [x] 持久目标续跑（R-4）：轮次上限、阻塞原因码、重启 / fork 后需重新授权、goal_complete / goal_blocked 工具
- [x] OpenTelemetry 导出（默认关闭，默认不含内容）；`crew doctor` 部署自检

## DeepSeek 全家桶（2026-11-13 发布）利用
- [x] 调研：deepseek-flash（V4.1-Flash-1017）三协议；原生 `deepseek_web_search` / `deepseek_read_url`；`safety_check`；`title`；独立 Search / Fetch / OCR / ASR / TTS
- [x] 真实调用（经 magpie 原生透传与标准 chat 接口）：返回 `annotations`（url_citation）、`server_tool_calls`、`usage.server_tool_use`
- [x] core：Request.NativeTools、Agent.Spec.NativeTools（按 Agent 开启，API 可改）、citation / server_tool 片段、StopRefusal、Usage.ServerTools
- [x] 杂活默认走 DeepSeek Flash：title / summarize 路由（直连优先，其次 magpie，不可用回落 Agent 顺序）；会话主题自动生成（features/topics）
- [x] openai 适配器 deepseek 方言：thinking 开关、NativeTools → deepseek_web_search / deepseek_read_url、解析 annotations（引用）/ server_tool_calls（搜索与阅读步骤，实时流式）/ server_tool_use（按次计费）、safetyCheck 与 refusal；预设 base URL 更新；magpie 对 deepseek/* 模型自动切方言
- [x] 真实端到端：Agent 开启联网搜索 → DeepSeek Flash 搜索全网 → 带出处回答；引用不回流到模型历史
- [x] 压缩摘要优先用 summarize 路由模型（默认 DeepSeek Flash；仅本地 Agent 只走本地路由；路由失败回退 Agent 自身模型，再失败用抽取式摘要；compaction/end 记录实际摘要模型）
- [x] 与模型无关的 web / OCR 工具（features/deepseek）：有 DeepSeek Key 即给所有 Agent 加 web_search（支持 site: / lang: / after: 等语法）/ web_fetch（只读、沙箱模式按 allowHosts 限制，403/404 引导改用浏览器）/ ocr_file；Key 取自已添加的 DeepSeek 提供方（或 keychain:crewbase/deepseek、DEEPSEEK_API_KEY），加 / 删 Key 后工具自动出现 / 消失
- [x] 附件 OCR：不能看图的模型收到图片 OCR 文字；扫描版 PDF（几乎无文字层）与 .doc / .ppt 走 OCR；结果缓存在 uploads/.ocr/<sha256>.txt（同一文件只识别、计费一次），失败 10 分钟内不重试
- [x] 文件输入：图片内联（deepseek-flash 视觉，经 magpie 真实验证）；PDF / Word / PowerPoint 直连上传 Files API（purpose 在 file 之前、30 天过期、file_id 按工作区缓存）；经 magpie 无 /files 时用本地提取文字
- [x] 语音：输入框麦克风听写（浏览器 Web Speech API，中间结果实时显示、最终文本追加到草稿，按语言选中文 / 英文，权限被拒有明确提示）；回复旁「朗读」（系统语音，跳过代码块与 Markdown 记号，再点停止）
- [x] 语音云端：有 DeepSeek Key 时麦克风录 16 kHz WAV（≤ 2 分钟）→ /api/voice/transcribe → DeepSeek ASR；「朗读」→ DeepSeek TTS（stella，过滤 Markdown），失败回落系统语音；无 Key 仍用浏览器语音。音频只透传不落盘。真实 Chrome + 假麦克风 + 假 DeepSeek 服务端验证：录音转写进输入框、朗读请求与播放
- [x] DeepSeek 真实调用（`go test -tags live ./test/live -run DeepSeek`）：Search / Fetch、TTS → ASR 往返原话一致、OCR；直连 Files API 读 PDF。真实调用发现并修复：文件引用须为扁平的 {"type":"file","file_id"}（SDK 默认嵌套在 file 下，DeepSeek 返回 400）
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
- **迭代 11 · 模型与通知配置可用** ✅：「添加提供方」全流程（API Key → 钥匙串 → 补丁 → 热应用，无需重启）；设置 › 通知（事件 × 渠道勾选表、免打扰时段持久化，接入 IM 网关）；压缩摘要走 summarize 路由
- **迭代 12 · 插件与界面扩展** ✅：界面插槽机制（插件可注册对话卡片 / 右侧标签 / 设置卡片）；角色模板导入（C-5，只迁移指令与流程）；新建文件夹
- [x] 新对话（⌘N）：空白开始，不带角色、指令、记忆和插件提示词，只有基础工作规则与工作区路径；照样能用工具、遵守运行权限与审批；首条消息命名，生成主题后显示主题；不加入频道 / 工作流 / 委派；新建 Agent 改为 ⇧⌘N
- [x] 添加提供方：预设按厂商 / 聚合平台分组（Anthropic、OpenAI、Gemini、DeepSeek、xAI、Kimi、智谱、MiniMax、通义千问、火山引擎；OpenRouter、SiliconFlow、Groq 等）；自定义提供方自动识别图标（地址 → 名称 → 模型），可手选厂商图标或字母图标；不提供模型列表的接口（火山方舟等）可手填模型 ID；守护进程过旧时明确提示
- [x] 个人资料页（原「个人资料与用量」）：就地改名；活动热力图旁加汇总（活跃天数、当前 / 最长连续、最常用的星期）；统计卡统一样式；已删除成员显示占位头像与标记；去掉重复的来源汇总行
- **迭代 13 · 文件与多模态** ✅：对话附件（PDF / Office / 图片 / 文本，选取、粘贴、拖放）→ 工作区 uploads/；日志只记引用，请求前按模型能力装载（图片给视觉模型、PDF 给 Anthropic / Gemini / OpenAI 原生读取、其余提取文字 + 路径）；DeepSeek 直连走 Files API（text_recognition，file_id 按工作区缓存，失败回退文字）
- **迭代 14 · 远程与多端** ✅：零入站配对（WebSocket Relay）、设备分级、移动端伴侣（PWA：通知、审批、对话）
- **迭代 15 · 团队** ✅：成员与角色、团队任务板 DAG（A-7）
- **持续**：WASM 插件档、MCP resources / prompts、插件包签名、浏览器多标签、桌面自动更新（需密钥）

## 迭代 17 进展
- [x] 连续多条消息合并成一轮、只回复一次；运行中插话在下一步就被看到（同一会话、同一运行模式与模型的普通消息；IM / 工作流 / 例行 / 目标 / 子 Agent 输入各自成轮）
- [x] 后台命令：bash {background: true} 立即返回，job_output / job_stop / job_list，结束后结果作为消息回到对话；按 Agent 隔离，最多 5 个同时运行，最长 2 小时
- [x] 卡死检测：同一工具同参同结果连续 3 次 / 连续 6 次失败先提醒换思路，5 次 / 10 次直接停下并说明原因
- [x] IM 输出规范：回复 IM 消息的那一轮要求像同事聊天——先说结论或唯一的问题、约 100 词、不罗列过程，长内容放文件
- [x] 环境说明进提示词（macOS 用 BSD 工具、Docker 沙箱时为 Linux 容器），真实模型第一次就用对命令
- [x] 真实模型（DeepSeek Flash 经 magpie）验证：三条连发消息一轮一次回复；跟进提醒备注清楚可执行；后台构建立即返回、结束后一句话汇报。发现并修复：后台结束消息曾诱导 Agent 到处翻工作区找事做
- [x] 跟进提醒（features/followups）：followup_set {in | at, note} / followup_list / followup_cancel；到点作为新输入回到原对话；持久化、重启后仍在、Agent 删除即清除；任务面板列出并可取消；真实守护进程 + 浏览器验证

## 迭代 18 进展
- [x] 重启续跑：守护进程关闭（退出应用、崩溃）时正在跑的一轮记为 interrupted，重启后自动接着做——不重复用户消息，提示模型先核对已完成的部分、不重复有副作用的操作；人手动停止的不续跑；工作流 / 例行 / 目标的输入不续跑（它们各自记录中断，目标重启后需重新授权）；等待审批时被打断的调用在续跑时重新请求批准
- [x] 记忆进提示词有上限：MEMORY.md 只带开头（长期事实）和最近的部分，中间的用 memory_search 找，每次请求的固定开销不再随记忆无限增长
- [x] 夜间整理记忆（features/memory 的 memory-tidy，每天 03:30）：记忆超出上限且有变化的 Agent 被要求合并重复、删掉过时、长期事实放前；整理前自动备份到工作区 .memory/（保留 7 份）；只读 Agent 与新对话不参与
- [x] 审批不再卡住对话：Agent 等你批准时你直接发消息，就当作拒绝这个请求，消息在同一轮的下一步被读到（像 Claude Code 的权限提示）

## 迭代 19 进展
- [x] 后台委派：delegate {background: true} 立即返回，结果作为消息回到对话；同时到达的多个结果（后台命令、委派、跟进）合并成一轮汇报
- [x] 队友名册进提示词：每个 Agent 知道能把活交给谁（名字、id、职责）
- [x] 群聊旁听：设置 › 通知里给群选一个旁听的 Agent（可加关键词）；先程序筛（只看这个群、只看允许名单里的人、长度、关键词、每小时上限），再由 Agent 判断要不要接话，没把握回 NO_REPLY 就不发；接话后这条消息的线程才归它；旁听轮一律只读
- [x] 主理人模板（排在模板第一个）：统一接待、自己不动手、并行分派给队友、核对汇总后回复、用任务板和跟进管理进度。真实模型验证：两件事并行分派给研究员和文案，结果先后回来逐个核对，最后一次性汇总，全程 24 秒

## 文件夹与队员形象（2026-11-15）
- [x] 文件夹：⋯ 菜单里可以在这里新对话 / 新建 Agent / 新建频道、添加已有的（多选一次放进来）；空文件夹直接给出这几个入口；不再单独放 ＋（和 ⋯ 重复）
- [x] 队员形象：13 种形状 × 18 色，每个队员有一双自己的圆眼睛（瞳孔跟随鼠标、错开节奏眨眼、按下眯眼笑）；悬停扭一下、工作中呼吸、等你时晃一晃、换外观时弹一下；新建 Agent 可「随机外观」、点预览会戳一下；减少动态效果时全部静止
- [x] 界面重做：新建 Agent 改为左侧「从哪里开始」（空白 / 模板 / 导入）+ 右侧卡片表单（形象、名称与职责、说明、模型 / 运行权限 / 文件夹）；团队、成员与角色改为卡片列表，展开看人 / Agent / 设置，新建团队、添加成员改为对话框，不再常驻输入框

## 设置补全（2026-11-15）
- [x] 检查更新：设置 › 通用 › 关于显示版本；桌面应用用自带更新器（立即检查 / 安装、自动检查、自动下载安装开关）；网页与服务端由守护进程每天读一次最新版本的更新清单（不走 GitHub API，避免限流），有新版本时显示说明和链接。修复：发布的程序版本号一直是 0.1.0-dev（构建时注入版本）；按架构安装的旧 macOS 客户端找不到更新清单（发布时补上 arm64 / amd64 清单）
- [x] 中英文：英文从 498 条补齐到全部 1193 条；构建时检查（缺翻译、占位符不一致、英文里残留中文都会让构建失败）；界面语言第一次打开时跟随系统；桌面菜单栏、托盘、选择文件夹对话框随界面语言切换；插件界面（审计卡片等）、默认团队名、飞书 / 渠道说明等服务端文字也双语；日期格式、截图文件名不再写死中文
- [x] 快捷键：统一注册表（20 个可改 + 4 个打字规则），新增新建频道、查看快捷键、切换主题、显示 / 隐藏侧边栏与右侧面板、搜索、上 / 下一个对话、打开审批、聚焦输入框、停止当前 Agent、切换右侧面板标签；设置 › 快捷键点击即可录制新组合（Esc 取消、Delete 清空、系统占用的组合会被拒绝、冲突时自动互换、可单独或全部恢复默认），菜单与提示里显示的快捷键跟着变；可选「按 Enter 直接发送」。真实浏览器逐个验证

## 团队（ADR-0003）✅ 三期完成
- [x] 第一期 · 团队骨架：features/teams（teams.json，默认团队「我的团队」装下已有的一切）；建 / 改名 / 公开或私密 / 删除（只能删空团队，默认团队不能删）；Agent 与频道归属团队，频道成员必须同一团队，名册与委派限定在团队内，子 Agent 跟随父 Agent 的团队，Agent 可在团队间移动（先移出当前团队的频道）；侧边栏团队切换（多于一个团队时出现），新建的 Agent / 新对话 / 频道进当前团队；设置 › 团队页。真实浏览器验证：建私密团队、移动 Agent、切换后刷新仍记住，浅 / 深色
- [x] 第二期 · 人和权限：团队里的人与角色（负责人 / 成员 / 只读，至少一位负责人）；主人和管理员看到全部团队，其他人只看到公开团队和自己加入的团队；公开团队可直接加入，私密团队只能被拉入；只读角色不能发消息、审批、改动；新对话是个人的（只有发起人能看到，主人也看不到别人的）；在三处统一执行：网关守卫（单个 Agent / 会话 / 频道 / 审批 / 团队的所有接口）、列表接口、事件推送过滤；已有成员自动进入默认团队，新成员自动加入，移除的成员离开所有团队；成员设备可在能操作的团队里新建 Agent 与自己的对话
- [x] 第三期 · 团队共享：团队记忆 TEAM.md（团队每个 Agent 的提示词里都有，负责人在设置里编辑，Agent 用 team_memory_add 追加，不进其他团队）；MCP 服务器可共享给一个团队（team:<id>，一个连接，工具只给本团队 Agent）；团队默认运行权限（新 Agent 起始权限）；IM 网关可绑定团队（只能 @ 本团队 Agent，个人对话永远不能从 IM 访问）；团队用量（本期 tokens）。真实验证：主人在浏览器里建私密团队、设只读默认、写团队记忆、加入小王；小王的配对手机只看到自己的团队；真实模型（DeepSeek Flash）的客服用团队记忆回答并写入新约定，同团队的售后直接用上

## 体验修正
- [x] 输入框选目录：去掉手填路径的输入框；桌面客户端用系统选择框，浏览器 / 中继里改为逐级浏览本机文件夹（只列文件夹、不含隐藏文件夹，只有主人可以浏览，成员设备 403），选中即用；仍保留最近用过的文件夹和回到默认工作区
- [x] 工作记忆（迭代 18 遗留，@crewbase/memory-working）：①「已告诉过你什么」——例行、目标、跟进、后台命令、委派、审批后、工作流这些主动汇报自动记入 .memory/told.jsonl（保留 40 条），提示词带最近 8 条，避免下一次汇报重复上一次；②任务进度文件——task_progress / task_progress_read，tasks/<名称>.md，提示词只列进行中任务（名称、多久前更新、第一行），完成后归档到 tasks/done/；③参考笔记——notes/ 下的 .md / .txt 只在提示词里放目录（名称、第一行、大小），用 note_read 按需分段读。右侧面板：任务页显示进行中的任务（点开看进度），Agent 页可添加 / 查看 / 删除参考笔记，查看它最近主动汇报过的内容。真实模型（DeepSeek Flash）验证：只看目录就去读了价格表并算对折扣；多日任务自己写了进度文件并设了明天的跟进；跟进回来的汇报被记下
- [x] 审批不卡住 Agent（迭代 18 遗留）：等待批准时有别的事进来（另一个对话、IM、例行、后台结果）就先停靠这次调用，Agent 说一句在等你批准就去处理别的，审批卡片和 IM 上的请求照样有效；你批准后原对话自动接着做（同一调用一次性放行、不再重复询问，1 小时有效），拒绝则重新规划；同一对话里直接回消息仍当作拒绝；停靠记录写入 parked.json，重启后告诉 Agent 审批已丢失、需要的话重新发起。真实模型（DeepSeek Flash）验证：删除文件等批准时在另一个对话问问题，立即得到回答；批准后自动删除并汇报
- [x] 速度（实测）：界面主包 761 KB → 558 KB（gzip 232 → 178 KB）——设置页、添加提供方按需加载，英文文案单独成块只在英文界面加载；守护进程与中继给网页资源 gzip 压缩（压一次、缓存在内存）、带哈希的资源缓存一年、index.html 每次校验；慢速网络（约 1.6 Mbps、150 ms，手机经中继）首屏 3.9 s → 1.6 s，再次打开走缓存。400 轮的长对话打开约 0.4 s、上滑 60 fps、输入每键约一帧，暂不需要虚拟列表
- [x] 上手体验（全新安装、无模型实测）：欢迎页改为两步——①连接模型（加载中显示查找状态；没有模型时给出「用 API Key 连接」并说明 Claude Code / Codex / Ollama 等会自动发现；已连接时列出来源），②选一位队员；没有模型时输入框上方提示并可直接打开连接对话框；「provider not configured」类错误在对话和任务面板里显示为「还没有可用的模型」并带「连接模型」；修复：连接模型前创建的对话一直停在不存在的 Claude Code 上（仍是内置默认顺序、且一个都不可用的 Agent 会在模型连接后自动改用可用模型，启动时和运行中添加都会生效；手动选过的不动）；默认按 Enter 发送（Shift+Enter 换行，可在快捷键里改回 ⌘Enter）；手机首次打开不再被空列表挡住欢迎页
- [x] 输入框里选工作目录：工具栏新增文件夹按钮（显示项目名或「默认工作区」），菜单里可原生选择文件夹（桌面客户端）、输入路径（支持 ~）、最近用过的文件夹、回到默认工作区、复制路径；切换后 Agent 收到说明；指令和记忆始终留在 Crewbase，不写进项目；去掉输入框下方的路径行

## 下一轮迭代计划（2026-11-15 再修订：吸纳常驻 Agent / Super-agent 两篇文章，见 docs/references/always-on-agents.md）
- **迭代 17 · 常驻基础（每天都会碰到的）**：连续多条消息合并成一轮、只回复一次；运行中插话并入当前运行（下一步就看到，不必等这轮结束）；Agent 给自己设跟进（`followup` 工具：到点作为新输入回来，可取消、可在界面看到）；后台命令（bash 可后台启动，立即返回，`job_output` / `job_stop`，结束后结果作为事件回到对话，期间照常响应）；卡死检测（重复相同工具调用、长时间无进展 → 提醒并可自动停止）；按信道的输出规范（IM 简短、只在需要决策或验收时说话）
- **迭代 18 · 可靠性与记忆**：守护进程重启后续跑被打断的输入；非阻塞审批（等待批准时 Agent 可先处理其他输入，批准后从断点继续；重启后待审批仍在）；记忆结构（已告知用户的记录、任务进度文件、记忆索引按需读取）；夜间整理记忆（做梦，作为例行任务，结果可审阅）
- **迭代 19 · 主理人（双层架构）**：可选的常驻「主理人」统一接待界面与 IM 的所有消息，自己不干活，分派给 Agent / 子 Agent，汇总后统一回复；IM 话题订阅直通负责的 Agent；群聊旁听（程序规则 + 模型判断两级过滤，默认安静）；用真实模型做体验审计贯穿每一项
- **之后**：上手体验（迭代 19 原计划）、速度与稳定；飞书 / Lark 显示名与 IM 附件；插件隔离落地
- **不做 / 搁置**：ACP 服务端（编辑器接入）；云端 Agent + 本机执行 daemon（BYOM）；跨实例 A2A
- **持续**：Developer ID / Windows 签名（待证书）；真实 IM 账号与 Windows / Linux 实机（待提供）

## 迭代日志
| 日期 | 迭代 | 结果 |
|---|---|---|
| 2026-11-15 | 团队 | ADR-0003 三期：团队骨架（团队 → 频道，名册 / 委派 / 频道限定在团队内）；人和权限（团队角色、公开 / 私密、个人对话；网关守卫 + 列表过滤 + 事件推送过滤）；团队共享（团队记忆、团队共享 MCP、默认权限、IM 绑定团队、团队用量）。浏览器 + 配对手机 + 真实模型全流程验证，修复：刷新后丢失所选团队、新建 Agent 未用团队默认权限、手机端出现无法打开的「管理团队」、成员设备显示主人的名字、报错为英文 |
| 2026-11-15 | 18–19 | 可靠性与团队：重启续跑被打断的一轮；等审批时直接发消息即视为拒绝并读到新消息；记忆进提示词有上限、夜间整理记忆；后台委派、队友名册、主理人模板（真实模型验证并行分派与汇总）；群聊旁听（两级过滤、默认安静、只读） |
| 2026-11-15 | 17 | 常驻基础（吸纳 Anthropic 常驻 Agent / DeepSeek Super-agent）：连发消息合并一轮、运行中插话下一步可见；跟进提醒；后台命令；卡死检测；IM 输出规范；环境说明。真实模型端到端验证，修复后台结果消息诱导乱翻工作区、macOS 上先用 GNU 命令失败两个问题 |
| 2026-11-15 | 16 | 发版与 DeepSeek 工具：GitHub Actions CI（Linux + macOS race、Windows 交叉编译、网页）与标签发版（桌面三平台 + 签名自动更新、命令行六平台、ghcr 镜像，自动发布）；本机生成更新签名密钥、更新窗口与「检查更新…」；DeepSeek Search / Fetch / OCR 作为与模型无关的工具，附件 OCR，云端语音（ASR / TTS）。真实守护进程 + 假 DeepSeek 端到端：三个工具、图片附件走 OCR、录音转写、朗读 |
| 2026-11-15 | 15+ | 收尾：凭证引用变更入审计（只记引用）；MCP resources / prompts、OAuth 登录（真实界面：添加服务 → 等你登录 → 浏览器同意 → 已连接，令牌在钥匙串、测试后已清理）、host 信任服务器只读收紧 |
| 2026-11-15 | 15 | 迭代 15 完成：团队任务板（DAG、唯一负责人、按根会话共享、工具 + 提示词 + 看板）；成员与角色（admin / member / viewer，审批按类别授权，设备随角色、移除即断开，消息与审批署名并审计）；只读设备档位（网关只放行 GET）。真实验证：三个 Agent 用工具建出依赖链，人在看板上完成 #1 后 #2 变为可以开始；成员手机批准删除被拒（403）、可以拒绝，审计与对话里都记为「小王」 |
| 2026-11-15 | 14 | 迭代 14 完成：设备配对与分级（一次性码、哈希令牌、移除即断流）；零入站中继（crew-relay，端到端加密，主机签名认证，Go 客户端与网页 WebCrypto 隧道）；手机伴侣 PWA（扫码配对、手机权限界面、后台审批通知）；补丁按键合并配置；修复只读模式下放在 /tmp 的工作区仍可写（Seatbelt 受保护路径）；新对话（⌘N，空白但能用工具）。真实验证：本机中继 + 守护进程，模拟 iPhone 扫码配对，经中继看对话、批准删除操作，手机流量不直连守护进程 |
| 2026-11-15 | 13 | 迭代 13 完成：对话附件（features/attachments：上传到工作区、按模型能力装载、PDF / docx / pptx / xlsx / 文本提取、超长截断并提示用工具读全文）；llm.Part attachment + FileAccepter；Anthropic / Gemini / OpenAI 兼容 / Ollama / ACP（给路径）适配；DeepSeek Files API；网关 RawBody 与 Blob（只有位图内联，其他一律下载）；输入框选取 / 粘贴 / 拖放、上传中禁止发送、消息内缩略图与文件卡片、文件面板图片预览与下载。真实验证：deepseek-flash 与 Claude Haiku 看图、GPT 读 PDF、DeepSeek 读 docx、三文件同一条消息；发现 magpie 的 Chat Completions 会丢弃 PDF file 段，改为提取文字 |
| 2026-11-14 | 12 | 迭代 12 完成：新建文件夹（空文件夹保留）；角色模板导入（Hermes / md / json / 粘贴，记忆与对话不迁移，真实界面导入验证）；界面插槽（capabilities/ui：对话卡片、右侧标签、设置卡片、⌘K 命令、输入框按钮；声明式渲染，进程内 / 进程外插件与 Go / TS / Python SDK 均支持；审计日志改用插槽；deploy-status 示例插件经真实守护进程验证全部五个插槽）；网关请求体超限返回 413 |
| 2026-11-14 | 11 | 迭代 11 完成：界面添加模型提供方（钥匙串 + 热挂载，经 magpie 真实添加 → 对话 → 移除验证）；压缩摘要走 summarize 路由（失败回退）；设置 › 通知（勾选表、定时 / 临时免打扰、发送测试，浅 / 深色 / 手机宽度截图验证）；修复弹窗类名冲突 |
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
