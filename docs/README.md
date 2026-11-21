# Crewbase 文档

Crewbase（暂定名）是一个 Local-First、模型无关、插件优先的开源多 Agent 协作工作台：接上你已有的模型（API、magpie 网关）和本地算力，组建一支可审计、可自托管、可商用的 AI 团队。

## 文档目录

| 文档 | 内容 | 主要读者 |
|---|---|---|
| [01 产品需求](01-product-requirements.md) | 背景、定位、用户、功能需求（带优先级与编号）、非功能需求、指标、协议 | 全员 |
| [02 竞品与参考分析](02-competitive-analysis.md) | 对标矩阵；为什么选 DeepSeek Harness 作为技术参考 | 产品、工程 |
| [03 交互设计](03-ux-design.md) | 设计原则、视觉风格、窗口结构、核心流程、页面规格 | 设计、前端 |
| [04 技术架构](04-architecture.md) | 分层、进程模型、选型、目录、存储、关键流程、部署 | 工程 |
| [05 插件系统](05-plugin-system.md) | **核心**：插件、上下文、事件分发、副作用、作用域、能力、会话日志、工具流水线、Profile、运行形态、界面插槽 | 工程、插件开发者 |
| [06 安全模型](06-security-model.md) | 威胁模型、三档运行模式、规则引擎、审批、凭证、插件安全、远程 | 工程、安全 |
| [07 路线图](07-roadmap.md) | Phase 0–3 交付与出口标准、待决问题 | 全员 |
| [术语表](glossary.md) | 统一用词 | 全员 |
| [参考笔记：DeepSeek Harness](references/deepseek-harness.md) | Harness 的设计要点、子系统摘要、我们不照搬的部分 | 工程 |
| [参考笔记：常驻 Agent 与 Super-agent](references/always-on-agents.md) | Anthropic 常驻 Agent 五个基础件、DeepSeek Super-agent 架构，对照 Crewbase 的差距与吸纳计划 | 产品、工程 |
| [ADR-0001 插件优先的内核](decisions/0001-plugin-first-kernel.md) | 为什么采用插件内核、为什么用 Go 移植而不是 fork | 工程 |
| [ADR-0002 插件分四档运行](decisions/0002-plugin-runtime-tiers.md) | 内置 / 进程外 / WASM / 内容型 | 工程 |
| [ADR-0003 团队](decisions/0003-teams.md) | 主机 → 团队 → 频道；团队是人 + Agent 的编组与共享资源边界（提议） | 产品、工程 |
| [预览](previews.md) | 链接卡片与右侧预览：Agent 做的网页、开发服务器 | 工程 |
| [电脑操作](computer-use.md) | macOS 上操作应用：权限、确认、动作 | 工程 |
| [Open Agent Docs](agentdocs.md) | 连接自托管文档服务器：每个 Agent 一个身份，评论 @ 它就成为它的任务 | 工程 |

## 建议阅读顺序

- **第一次了解项目**：01 → 03（配合原型）→ 07
- **准备写代码**：02 第 2 节 → 05 → 04 → 06 → 两份 ADR → 参考笔记
- **准备写插件**：05（重点第 2、5、7、9、10 节）→ 06 第 7 节

## 文档约定

- 中文为主；英文版在 Phase 2 前按「双语配对」维护（借鉴 Harness）：英文与中文同名、同结构，改一边必须同步另一边。
- 需求用编号引用（如 `M-4`、`S-3`），设计与代码评审中引用编号而不是复述。
- 重大技术决策写成 ADR，放在 `decisions/`，状态为「提议 → 接受 / 否决 / 被取代」。
- 引用外部产品的事实（价格、许可证、功能）需注明来源与核实日期；未核实的写明「待核实」。
