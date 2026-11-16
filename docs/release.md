# 发版

`.github/workflows/release.yml` 在推送 `v*` 标签时构建并发布：

| 产物 | 内容 |
|---|---|
| 桌面客户端 | macOS 通用版 `.dmg`（Apple 芯片与 Intel 同一个包）、Windows 安装程序（x64 / arm64）、Linux `.deb` / `.rpm` / `.tar.gz` + `install.sh`；每个平台带签名的自动更新清单 `update-<platform>.json` 与增量包 |
| 命令行 | `crewd` / `crew` / `crew-relay`，macOS / Linux / Windows × amd64 / arm64，名为 `crewbase-cli-<版本>-<系统>-<架构>`，附校验和（不可与桌面更新包 `crewbase-<版本>-<系统>-<架构>.tar.gz` 同名） |
| 服务端镜像 | `ghcr.io/hedeqiang/crewbase:<version>`（distroless）与 `:<version>-chromium` |

`.github/workflows/ci.yml` 在每次推送 main 与 PR 时跑 `go vet`、带 race 的全量测试（Linux + macOS）、Windows 交叉编译与网页构建。

## 一次性设置

更新签名密钥已在维护者的 Mac 上用 `go tool mygo keygen` 生成，公钥写在 `apps/desktop/mygo.json` 的 `updates.publicKey`。私钥在：

```
~/Library/Application Support/mygo/update-keys/mygo-update.key
```

把它加到仓库的 Actions Secrets（只需一次）：

```sh
gh secret set MYGO_UPDATER_PRIVATE_KEY < ~/Library/Application\ Support/mygo/update-keys/mygo-update.key
```

macOS 签名用一张固定的自签名代码签名证书（`MACOS_SIGNING_P12`，base64 的 .p12；`MACOS_SIGNING_P12_PASSWORD`）。
它让每个版本在 macOS 眼里是同一个应用，辅助功能、屏幕录制等授权在更新后保留（ad-hoc 签名把身份绑在每次构建的哈希上）。
证书只在 CI 的临时钥匙串里使用；没有这两个 Secret 时退回 ad-hoc。换证书等同于换应用，用户需要重新授权一次。

两个 Secret 已于 2026-11-17 设置（证书有效期 20 年，生成后本地私钥已删除，只存在于仓库 Secrets）。

**请另存一份到密码管理器**：已安装的客户端只接受这把密钥签名的更新，丢了就无法再给它们推送更新。

## 发一个版本

1. 改 `apps/desktop/mygo.json` 的 `version`（例如 `0.2.0`）
2. 在 `CHANGELOG.md` 加 `## 0.2.0` 一节——这就是更新窗口里显示的说明
3. 提交并推送标签：

```sh
git tag v0.2.0 && git push origin v0.2.0
```

标签与 `mygo.json` 版本、CHANGELOG 小节、密钥三者任一不对，流水线在第一步就停下；发布前还会核对每个更新清单里的归档与 Release 中的文件一致，不一致则不发布。各平台并行构建、上传到草稿 Release，全部成功后自动发布；已安装的客户端随即检查到更新（启动 10 秒后、之后每天一次，或菜单「检查更新…」）。带 `-` 的标签（`v0.2.0-beta.1`）发布为预发布版，普通用户不会收到。

## 没有 Apple 开发者证书时

macOS 应用使用**临时签名（ad hoc）**：能运行、能自动更新（更新包的完整性由上面的 Ed25519 密钥保证，不依赖 Apple 签名），但**浏览器下载**的 `.dmg` 带隔离标记，Gatekeeper 会拦截首次打开。macOS 15 起不能再右键「打开」绕过，只能走系统设置。

**推荐的安装方式（不经过 Gatekeeper 拦截）**：用终端安装脚本。curl 下载的文件没有隔离标记，脚本会先校验更新签名再装到 `/Applications`（不可写时装到 `~/Applications`）：

```sh
curl -fsSL https://github.com/hedeqiang/crewbase/releases/latest/download/install.sh | sh
```

**用 `.dmg` 安装时**，首次打开被拦截后任选其一：

- 系统设置 › 隐私与安全性 → 底部「仍要打开」→ 输入密码（macOS 15 及以后只有这条路）；macOS 14 及以前也可以在「应用程序」里右键 Crewbase → 打开
- 终端：`xattr -dr com.apple.quarantine /Applications/Crewbase.app`

之后的自动更新由应用自己下载，没有隔离标记，不会再被拦。

Windows 安装程序未签名，SmartScreen 会提示「更多信息 → 仍要运行」。Linux 不受影响。

## 以后有了证书

- **Apple**：按 MyGo 文档（`docs/github-actions.md` 的 Signing and notarizing macOS apps）加 `MACOS_CERTIFICATE` 等 5 个 Secrets，在 `mygo.json` 写 `macos.signingIdentity` 与 `macos.notarize`，并在 macOS 构建前加导入证书那一步。
- **Windows**：加 `WINDOWS_CERTIFICATE` / `WINDOWS_CERTIFICATE_PASSWORD`，安装 `osslsigncode`，`mygo.json` 写 `windows.certificate`。

更新签名密钥与平台证书互不相关，换证书不影响已安装客户端的自动更新。

## 本机打包

```sh
make desktop                                   # 当前平台，临时签名，无更新清单
MYGO_UPDATER_PRIVATE_KEY="$(cat ~/Library/Application\ Support/mygo/update-keys/mygo-update.key)" make desktop   # 带更新清单
```
