# 桌面端（Crewbase.app）

`apps/desktop` 是 Crewbase 的桌面壳，用 [MyGo](https://github.com/egoist/mygo)（v0.9.0，纯 Go、无 CGO、系统 WebView）构建：一个窗口显示 `crewd` 网关提供的 Web UI，外加菜单栏图标、Dock 角标和原生通知。进程模型见 [技术架构 §2](04-architecture.md#2-进程模型)。

## 构建

```sh
make desktop                      # 先 make web，再 go tool mygo build apps/desktop
make desktop PLATFORM=darwin/universal,windows/amd64,linux/amd64   # 交叉构建
```

产物在 `apps/desktop/build/<os>-<arch>/`：macOS 为 `Crewbase.app` 与 `crewbase-0.1.0-arm64.dmg`（默认 ad hoc 签名；正式分发需在 `mygo.json` 的 `macos.signingIdentity` / `macos.notarize` 中配置 Developer ID 与公证），Windows 为 exe 与 NSIS 安装包，Linux 为可执行文件、tar.gz + install.sh、deb / rpm / PKGBUILD。MyGo CLI 以 Go tool 形式固定在 `go.mod`（`go tool mygo`），不需要 Bun。

- 配置：`apps/desktop/mygo.json`（名称 Crewbase、标识 `app.crewbase.desktop`、版本 0.1.0）。
- 图标：`resources/icon.png`（1024×1024）与 `assets/tray.png`（32×32 模板图）以及网页的 `icon-{180,192,512}.png` 都由 `go generate ./apps/desktop` 从 [`docs/brand/crewbase-mark.svg`](brand/crewbase-mark.svg) 渲染，SVG 是唯一真源；改了标识后重新生成即可。
- 无 CGO：`CGO_ENABLED=0 GOOS=windows go build ./apps/desktop` 与 `GOOS=linux` 都可编译。Linux 运行时需要 WebKitGTK 4.1，托盘需要 `libayatana-appindicator3`；Windows 需要 WebView2 Runtime。

开发时可直接 `go run ./apps/desktop`（应用名、版本按开发版处理，带 DevTools）。

## 运行模式

启动顺序：

1. **单实例**：已在运行时，再次启动只会唤起原窗口后退出。
2. **附着（attach）**：若 `127.0.0.1:7788` 上的 `crewd` 用 `${CREWBASE_HOME:-~/.crewbase}/token` 能通过 `GET /api/health`，桌面壳直接使用它，不再启动第二个宿主。待处理数量与通知来自 SSE 流 `/api/events`（`approvals.changed`、`agent.status`、`notify`），断线自动重连。退出 Crewbase.app 不影响这个 `crewd`。
3. **进程内（in-process）**：否则在同一进程内启动宿主（等同 `crewd --profile desktop --addr 127.0.0.1:7788`，`gateway.Static = web.Dist`）。若 7788 被其他程序占用（token 不匹配），改用 `127.0.0.1:0`，实际地址取自 `gateway/listening` 事件。退出时先隐藏窗口，再 `Host.Close()`：插件逆序卸载，codex / claude / MCP 等子进程树被清理，会话落盘，然后进程退出（最多等 15 秒）。

窗口加载 `http://<addr>/?token=<token>`。该地址加入 MyGo 的 `PageOptions.TrustedOrigins`，页面才能调用桌面接口；页面在窗口隐藏时不被节流（SSE 保持连接）。`target=_blank`、`window.open` 和跳到其他源的链接一律用系统浏览器打开。

环境变量：`CREWBASE_HOME` 选择数据目录；`CREWBASE_ADDR` 覆盖默认地址 `127.0.0.1:7788`（开发、测试或多套环境并存时使用）。日志写到 `~/Library/Logs/Crewbase/desktop.log`（其他平台为用户数据目录下的 `logs/`）。

## 窗口

- 标题栏隐藏（`TitleBarHidden`），高 44px，与 Web UI 的拖拽标题条一致；红绿灯位于 (16, 15)，在 44px 内垂直居中。页面通过 CSS 变量 `--mygo-titlebar-height`（44px）、`--mygo-titlebar-inset-left`（76px）、`--mygo-titlebar-controls-bottom`（29px）避开系统按钮，用 `--app-region: drag` 拖动窗口。
- 最小 820×560，位置与大小按 `StateKey "main"` 记忆；背景色 `light-dark(#F5F5F6, #1A1A1A)`，页面就绪后再显示，避免白屏闪烁。开机自启动时窗口保持隐藏。
- 关闭窗口（红灯、⌘W、「窗口 › 关闭窗口」）只隐藏；点 Dock 图标、菜单栏「打开 Crewbase」或再次启动应用即可恢复。真正退出只能通过「Crewbase › 退出 Crewbase」（⌘Q）或菜单栏「退出 Crewbase」。

## 菜单

- **应用菜单**：关于 Crewbase、设置…（⌘,）、服务、隐藏、退出。
- **编辑**：系统标准项（撤销、剪切、复制、粘贴…）。
- **显示**：重新载入、强制重新载入、缩放、全屏；开发版另有「切换开发者工具」。
- **窗口**：最小化、缩放、关闭窗口、前置全部窗口及窗口列表。
- MyGo 自带的菜单项与对话框文字使用简体中文（`App.SetLanguages("zh-Hans")`）。

## 菜单栏图标（托盘）

模板图标（16pt，黑色透明底，由 Relay 标识的三个圆点与底座生成，macOS 自动适配深浅色菜单栏）。菜单：

- 打开 Crewbase
- 需要你 (N)：待批准的工具调用与等待接管的浏览器，例如「Kai 等你批准：git push…」「Mia 等你接管浏览器：登录 GitHub」；点击后显示窗口并跳到对应会话（见下方「待补齐」）
- 开机时启动（`App.SetOpenAtLogin`；macOS 13+ 需要打包后的 .app）
- 关闭窗口后继续运行（默认开启，保存在用户数据目录的 `desktop.json`；关闭后，关闭窗口即退出应用）
- 退出 Crewbase

N > 0 时，macOS 菜单栏图标旁显示数字，Dock 图标显示角标。数据来源：进程内模式监听 `approval/changed` 与 `agent/status`（状态为 waiting 且 detail 以 `takeover:` 开头即浏览器接管），附着模式读 SSE；另外每 30 秒兜底刷新一次。

## 通知

- 进程内模式在 notify 服务上注册名为 `desktop-native` 的通道（可在通知矩阵中按类型开关），附着模式订阅 SSE 的 `notify` 事件；两者都显示系统通知，点击后显示窗口并打开对应会话。
- 窗口在前台可见时不弹系统通知，由 Web UI 的页内提示负责，避免同一事件出现两次；窗口隐藏或应用在后台时才弹系统通知。macOS 首次通知会请求授权。

## 页面可用的桌面接口

预加载脚本只在顶层页面、且来源为 `crewd` 地址时注入（对象被冻结）：

```ts
window.crewbaseDesktop = {
  version: string,              // 应用版本，如 "0.1.0"
  platform: "darwin" | "windows" | "linux",
  quit(): void,                 // 等同菜单「退出」
  openExternal(url: string): void, // 仅 http / https / mailto，用系统默认程序打开
  setBadge(n: number): void,    // Dock 角标；实际显示 max(n, 壳自己统计的待处理数)
}
```

实现上是 MyGo 绑定的 Go 服务 `Desktop`（`window.mygo.call("Desktop.*")`），只有受信任的来源能调用。

## 自动更新

MyGo 的更新需要 Ed25519 签名密钥，`mygo.json` 的 `updates.publicKey` 不能留空，因此目前**未开启**。开启步骤：

1. `go tool mygo keygen` 生成 `mygo-update.key`（私钥，存入密码管理器与 CI secrets，绝不入库）和 `mygo-update.pub`。
2. 在 `apps/desktop/mygo.json` 中加入：

   ```json
   "updates": { "publicKey": "<mygo-update.pub 的内容>", "github": "hedeqiang/crewbase", "tagPrefix": "desktop-v" }
   ```

   用 `desktop-v` 前缀避免与 CLI / 服务端的 release 混在一起。
3. 发布：`MYGO_UPDATER_PRIVATE_KEY="$(cat mygo-update.key)" go tool mygo build -upload -platform …`，生成签名的更新包、增量包和 `update-<platform>.json`，上传为草稿 release，发布时 `gh release edit desktop-v0.2.0 --draft=false --latest=false`。
4. 应用内：用 `mygo.Updater.Check` / `Install` 驱动 [交互设计 §5.1](03-ux-design.md) 的更新胶囊（新版本 → 下载中 → 重启更新），`App.Relaunch()` 完成重启。重启只影响桌面壳与进程内宿主：Agent 从检查点恢复，待批准请求保留在会话日志中；附着模式下外部 `crewd` 需自行升级。

## 端到端检查

带 `-tags e2e` 构建时会链接 MyGo 自动化插件（`automation_e2e.go`，正式包不含），可以通过 socket 驱动应用：

```sh
GOFLAGS=-tags=e2e go tool mygo build -debug -skip-dmg -o /tmp/cbe2e apps/desktop
open --env CREWBASE_HOME=/tmp/cbdesk --env MYGO_AUTOMATION=/tmp/cbsock/app.sock /tmp/cbe2e/darwin-arm64/Crewbase.app
go run github.com/egoist/mygo/plugins/automation/cmd/mygo-automation open --socket /tmp/cbsock/app.sock
mygo-automation screenshot /tmp/desktop.png      # 页面截图（不需要屏幕录制权限）
mygo-automation menu 窗口 关闭窗口                # 关闭 → 隐藏
mygo-automation menu Crewbase "退出 Crewbase"
```

## 待补齐（Web UI）

- **打开指定会话**：托盘「需要你」与通知点击时，壳会显示窗口并把页面 hash 设为 `#/session/<id>`；Web UI 目前不处理 hash 路由，所以只会显示窗口。需要 Web UI 监听 `hashchange`（及首次加载时的 hash）并调用 `openSession(id)`。
- **打开设置**：⌘, 会把 hash 设为 `#/settings`，同样需要 Web UI 映射到 `openSettings()`（可扩展为 `#/settings/<section>`）。
