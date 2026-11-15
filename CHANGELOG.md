# Changelog

Each `## <version>` section becomes the release notes of that version,
shown in the desktop app's update window.

## 0.2.0

Agent 更像一位常驻的同事：不会被一条命令卡住，会自己回来跟进，重启后接着干。

- 连发几条消息只回复一次；Agent 工作时发的补充，下一步就能看到，不用等这轮结束
- 跟进提醒：Agent 可以说「20 分钟后看 CI」并准时回来；待办的提醒显示在右侧任务面板，可以取消，重启后仍在
- 后台命令：长时间的构建、测试放到后台跑，Agent 照常回应，跑完自动汇报
- 原地打转会被发现：反复做同一件事时先提醒换思路，仍不行就停下并说明原因
- 重启后接着做：退出应用或意外崩溃时正在进行的工作，重启后自动继续，不重复已完成的操作
- 等你批准时直接回消息，就当作不同意，Agent 按你的新消息继续
- 新模板「主理人」：统一接待请求，并行分派给队友，核对汇总后回复；每个 Agent 都知道能把活交给谁，委派可以在后台进行
- 群聊旁听：在 设置 › 通知 里选一个 Agent 关注群聊，没被 @ 也能在有把握时接话，默认保持安静，旁听时只读
- 在聊天软件里的回复更简短，先说结论
- 记忆不再无限占用上下文；每晚自动整理过长的记忆，整理前先备份
- Agent 知道自己运行在哪个系统上，第一次就用对命令
- 安全：外部客户端不能再创建子 Agent；让 Agent 直接在某个文件夹工作前会检查这个文件夹

## 0.1.0

First public build.

- 多 Agent 桌面工作台：频道与单一负责人、委派与工作流（含画布编辑器）、团队任务板
- 模型：Anthropic、OpenAI、Gemini、DeepSeek、xAI 等原生接入，按任务路由与故障转移；复用 Claude Code / Codex 订阅（ACP）
- 执行与安全：沙箱（Seatbelt / bubblewrap / Docker）、运行权限与审批、凭证钥匙串、审计日志
- 浏览器、MCP（含 OAuth）、插件市场（签名校验、WASM 插件）
- 对话附件与多模态、语音听写与朗读、DeepSeek 联网搜索
- 远程：设备配对、零入站中继、手机伴侣；Slack / Telegram / Discord / 飞书
- 桌面客户端（macOS / Windows / Linux），自动更新
