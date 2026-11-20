# 链接卡片与右侧预览

两个功能：消息里的链接显示成卡片（视频可以直接播放），以及在右侧面板预览 Agent 做出来的网页、HTML 文件和 Vue / React 项目。

## 参考

- **飞书 / Lark、Slack**：服务端抓取网页的 Open Graph 信息，显示标题、简介、站点名和缩略图；YouTube、B 站等视频站点在卡片里直接播放；图片和视频直链直接显示。
- **Claude（Artifacts）**：HTML 和 React 代码在隔离的域名里渲染（iframe + sandbox），页面脚本碰不到对话页面的数据。
- **Claude Code / Codex 桌面版**：Agent 启动开发服务器后，在应用内的预览窗格打开 localhost 地址，可以刷新、在浏览器里打开。

## 现状

- 网关只认 `Authorization: Bearer` 和 `?token=`，没有 cookie；令牌保存在界面所在源（`127.0.0.1:7788`）的浏览器存储里。
- `/api/agents/{id}/raw` 只在界面源里内联图片和视频，HTML、SVG、PDF 一律下载，并带 `Content-Security-Policy: default-src 'none'; sandbox`。这一点不变。
- 沙箱模式下，Agent 可以在 `127.0.0.1` 上起服务（已实测 `python3 -m http.server`），后台命令（`bash {background: true}`）可以长时间运行。

## 一、链接卡片

### 后端：`features/linkpreview`

`GET /api/unfurl?url=<url>` 返回：

```json
{
  "url": "最终地址（跟随跳转后）",
  "kind": "page | video | image | audio",
  "title": "…", "description": "…", "siteName": "…",
  "image": "缩略图地址", "icon": "站点图标地址",
  "embed": {"provider": "youtube", "src": "https://www.youtube-nocookie.com/embed/…", "aspect": 1.7778},
  "media": "视频 / 音频 / 图片直链"
}
```

- **抓取**：5 秒超时，最多跟随 5 次跳转，只读前 512 KB，按 `Content-Type` 判断：HTML 解析 `og:*`、`twitter:*`、`<title>`、`<meta name=description>`、`<link rel=icon>`；`video/*`、`audio/*`、`image/*` 直接返回直链。
- **安全（SSRF）**：只允许 http / https；解析出的每个 IP（含跳转后）都不能是回环、私有、链路本地、组播或未指定地址（127.0.0.0/8、10/8、172.16/12、192.168/16、169.254/16、::1、fc00::/7、fe80::/10…），在拨号时检查，防 DNS 重绑定。localhost 的地址不走抓取，由「预览」处理。
- **视频站点**（不需要联网就能得出播放地址）：YouTube（含 youtu.be、shorts）→ `youtube-nocookie.com/embed/<id>`；B 站（BV / av 号）→ `player.bilibili.com/player.html?bvid=…&autoplay=0`；Vimeo → `player.vimeo.com/video/<id>`。标题、缩略图仍然从页面抓。
- **缓存**：内存缓存 1 小时（最多 500 条），失败也缓存 10 分钟，同一地址并发只抓一次。
- **开关**：设置 › 通用「显示链接预览」，默认开。关掉后前端不请求。

### 前端：`components/LinkCards.tsx`

- 用户消息和 Agent 的最终答复里，最多取前 3 个 http(s) 链接（去重，跳过 localhost 和代码块里的链接），显示在文字下面。
- **网页**：左侧站点图标 + 站点名、标题（最多 2 行）、简介（最多 2 行），右侧缩略图；整张卡片可以点开链接。
- **视频站点**：缩略图上叠播放按钮，点击后在卡片里换成 16:9 的播放器（iframe，`allow="autoplay; encrypted-media; picture-in-picture; fullscreen"`）。
- **视频 / 音频直链**：`<video controls>` / `<audio controls>`；**图片直链**：直接显示图片，点开看大图。
- 图片加 `referrerpolicy="no-referrer"`、`loading="lazy"`；加载失败就退回成只有文字的卡片；抓取失败的链接不显示卡片。
- 浅色、深色、手机宽度都要好看；流式输出时不显示，这一轮结束后才出现，避免跳动。

## 二、右侧预览

### 隔离

预览的页面是 Agent 写的，不可信。它绝不能在界面源（`127.0.0.1:7788`）里运行，否则能读到令牌、调用全部 API。

- **工作区里的文件**：`features/preview` 为每个预览的根目录单独起一个只监听 `127.0.0.1` 的静态服务器（随机端口），把这个目录当作网站根（`/`）提供。这样一来：
  - 每个预览一个独立的源，和界面、和别的 Agent 的预览都互相隔离；
  - Vite / CRA 打包出来的绝对路径（`/assets/…`）能直接用；
  - 找不到的路径回退到 `index.html`（单页应用的前端路由）。
  - 只读、不列目录、不跟随跳出根目录的符号链接；空闲 30 分钟、Agent 删除或守护进程退出时关闭。
- **开发服务器**（`http://localhost:5173` 这类）：直接在 iframe 里打开，本来就是另一个源。
- iframe：`sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-modals allow-downloads"`。`allow-same-origin` 指的是预览服务器自己的源，不是界面的源。

### 打开预览的方式

1. **Agent 调用工具 `preview_open`**：`{path?: "dist/index.html" | "site/", url?: "http://localhost:5173", title?: "…"}`。工作区路径会启动（或复用）静态服务器；URL 只接受 http(s)。会话里记一条 `preview/opened` 事件（标题、地址、来源），返回给模型预览地址。
2. **文件面板**：`.html` / `.htm` 文件和包含 `index.html` 的目录旁边出现「预览」按钮。
3. **答复里的代码块**：` ```html ` 代码块右上角有「预览」，把内容写进工作区 `.crewbase/previews/<hash>.html` 后打开。
4. **答复里的 localhost 链接**：显示成一张「在右侧预览」的卡片（不走链接抓取）。

### 界面：右侧面板「预览」标签

- 打开预览时，右侧面板自动展开并切到「预览」，同一轮的步骤里显示一行「打开了预览：xx」，点它回到预览。
- **工具栏**：标题 / 地址、刷新、在浏览器中打开、宽度切换（桌面 / 平板 390 / 手机），关闭。
- **打不开时**（开发服务器没在运行、端口没人听）：说清楚原因，比如「localhost:5173 没有响应，开发服务器可能已经停止」，给一个「重试」。
- 同一个会话里可以有多个预览，标签里列最近的几个，可以切换。

### Vue / React 项目

不需要特殊支持，Agent 用现有能力就能做：

1. `npm create vite@latest`、`npm install`（沙箱可以联网，缓存在独立临时目录）；
2. `npm run dev -- --host 127.0.0.1 --port <端口>` 放到后台运行（`bash {background: true}`）；
3. 调用 `preview_open {url: "http://127.0.0.1:<端口>"}`；或者 `npm run build` 之后 `preview_open {path: "dist/"}`。

`preview_open` 的工具说明要把这个流程写清楚。

## 不做

- 不在界面源里渲染任何 Agent 写的 HTML。
- 不抓取 localhost 和内网地址的链接卡片。
- 不做在线编辑器（改代码仍然在文件面板或让 Agent 改）。

## 三、页面答复（`show_page`）

有些答复看比读更清楚：带图表的报告、仪表盘、对比表、时间线、小计算器。Agent 用 `show_page {title, html | path, force?}` 把一个页面当作答复，直接显示在对话里。

- **保存**：给 `html` 时存成工作区 `pages/<标题>.html`；给 `path` 时用工作区里已有的 `.html`（可以引用旁边的文件）。和右侧预览一样，由独立源的静态服务器提供，在沙箱 iframe 里运行。
- **先检查再展示**：显示之前，用一个用完即删的无头 Chrome（临时配置，不碰 Agent 自己的浏览器）按 1280×800 和 390×844 各渲染一次：
  - 两张截图存到 `.crewbase/pages/`，作为图片附在工具结果里（能看图的模型直接看到，不能看图的模型拿到路径说明）；
  - 找出问题：脚本异常和 `console.error`、加载失败的文件（4xx / 网络错误，浏览器自己请求的 `favicon.ico` 不算）、空白页面、在手机宽度下比屏幕宽。
  - 有问题时**不展示**，告诉 Agent 改好再调用；只有问题是有意为之时才传 `force: true`（这些提示会记在卡片上）。没有本地 Chrome 时直接展示，并说明没有检查。
- **对话里**：会话事件 `page/shown`（标题、路径、地址、是否检查过）。这一轮里显示一张页面卡片：标题，刷新、放大 / 缩小、在右侧打开、在浏览器中打开；页面在进入视野时才加载，地址失效（服务器空闲关闭、重启）时按路径重新取。同一轮里同一个页面再次展示时，新版本替换旧的。
