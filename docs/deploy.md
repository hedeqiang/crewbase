# 部署指南（server Profile）

本文说明如何把 Crewbase 以无头服务器形态（`crewd --profile server`）部署到 Linux 主机、Docker、阿里云 ECS 和 AWS。对应设计见 [04-architecture.md §7](04-architecture.md)、[06-security-model.md §8–§10](06-security-model.md)。

## 0. 安全前提：零入站端口

- **不开放任何入站端口。** `crewd` 的 API 网关只监听 `127.0.0.1:7788`。所有部署文件都不映射端口、不修改防火墙、不要求安全组放行。
- **绝不把 7788 暴露到公网。** 令牌（`token` 文件）等同于桌面端的完全控制权：能改规则、装插件、以主机模式运行命令。
- 远程访问只走主动出站或私有通道：
  - **SSH 隧道**：`ssh -N -L 7788:127.0.0.1:7788 user@host`，然后打开 `http://127.0.0.1:7788`；
  - **Tailscale / WireGuard**：只对自己的 tailnet 可见（`tailscale serve --bg 7788`，或 compose 的 `tailscale` sidecar）；
  - **AWS SSM 端口转发**：不需要 SSH，也不需要入站规则；
  - 出站 WebSocket Relay（Phase 3 规划中，可用后替代以上方式）。
- 安全组 / 防火墙建议：入站全部拒绝；确需 SSH 时只放行自己的 IP，优先用云厂商的会话管理（阿里云 Workbench、AWS SSM）。
- 部署后运行 `crew doctor`：网关地址不是回环地址时会给出警告。

## 1. 检查清单：`crew doctor`

```bash
crew doctor                      # 默认检查 server Profile
crew doctor --profile desktop
crew doctor --offline            # 不访问运行中的 crewd
crew doctor --health             # 只检查 /api/health（容器健康检查使用）
```

检查项：

| 项 | 说明 |
|---|---|
| config | Profile + 补丁能否解析 |
| data dir | 数据目录存在、可写、权限为 0700；`token` 必须是 0600 |
| sandbox | Docker 守护进程可达（Linux）/ sandbox-exec 可用（macOS）；`sandbox-none` 会提示命令无隔离 |
| browser | 找到 Chrome / Chromium 可执行文件，或配置了 attach |
| gateway | 监听地址是否只在回环地址上，非回环给出警告 |
| daemon / models | crewd 是否在运行；每个模型提供方是否可达、是否列出模型；没有任何可用模型时失败 |

有失败项时退出码非零，可用于 CI 或部署脚本。

## 2. Linux + systemd：`deploy/install-server.sh`

```bash
git clone https://github.com/hedeqiang/crewbase.git && cd crewbase
sudo ./deploy/install-server.sh                  # 用本机 Go 1.27 从源码构建
sudo ./deploy/install-server.sh --bin-dir ./bin  # 或使用预编译的 crewd / crew
sudo ./deploy/install-server.sh --with-docker    # 允许使用 Docker 沙箱
```

脚本是幂等的：重复执行会升级二进制、重写 unit，但保留数据、令牌、补丁和密钥文件。

| 位置 | 内容 |
|---|---|
| `/usr/local/bin/crewd`、`crew` | 守护进程与命令行 |
| `/var/lib/crewbase`（0700，属主 `crewbase`） | 数据目录：`token`、`sessions/`、`audit/`、`workspaces/` |
| `/etc/crewbase/crewbase.patch.yml`（root:crewbase 0640） | 本机补丁（按 id 合并到 server Profile） |
| `/etc/crewbase/env`（root:crewbase 0640） | 模型 API Key 等环境变量，补丁里用 `env:NAME` 引用 |
| `/etc/systemd/system/crewbase.service` | `crewd --profile server --addr 127.0.0.1:7788` |

systemd unit 的加固：专用无登录用户、`NoNewPrivileges`、`ProtectSystem=strict`（只有数据目录可写）、`ProtectHome`、`PrivateTmp`、`PrivateDevices`、清空能力集、`UMask=0077`。没有设置 `RestrictNamespaces`，因为 Chromium 自身的沙箱需要用户命名空间。

**Docker 沙箱**：Linux 上 server Profile 默认使用 `sandbox-docker`。`crewbase` 用户需要加入 `docker` 组才能启动沙箱容器（`--with-docker`）。注意：docker 组成员等同于主机 root，这是用沙箱隔离 Agent 命令的代价；不加入时，沙箱模式的命令会被拒绝（失败即关闭），不会无隔离运行。

**配对 / 获取令牌**：

```bash
sudo cat /var/lib/crewbase/token                     # 令牌，按密码对待
ssh -N -L 7788:127.0.0.1:7788 you@server             # 在你的电脑上
# 打开 http://127.0.0.1:7788，粘贴令牌
sudo -u crewbase CREWBASE_HOME=/var/lib/crewbase crew doctor
```

## 3. Docker Compose（加固版）：`deploy/docker-compose.yml`

```bash
cd deploy
cp .env.example .env && chmod 600 .env    # 填写模型 Key
docker compose up -d --build
docker compose exec crewbase crew doctor
docker compose exec crewbase cat /data/token
```

加固项（对应架构文档 §7）：

| 设置 | 作用 |
|---|---|
| `security_opt: no-new-privileges:true` | 禁止通过 setuid 等提权 |
| `security_opt: seccomp=./seccomp.json` | 系统调用白名单（见下） |
| `shm_size: 1g` | Chromium 需要足够的 `/dev/shm` |
| `read_only: true` + `tmpfs: /tmp` | 根文件系统只读，只有 `/data` 卷与 `/tmp` 可写 |
| `cap_drop: [ALL]`、`user: 65532` | 无任何 Linux capability，非 root 运行 |
| `pids_limit`、`mem_limit` | 防止进程 / 内存失控 |
| **没有 `ports:`** | 零入站；网关只在容器内的 127.0.0.1 上 |
| `healthcheck: crew doctor --health` | 用容器内的 `crew` CLI 检查 API |

`deploy/seccomp.json` 由 Docker 默认 profile（moby `profiles/seccomp/default.json`）裁剪而来：默认拒绝（EPERM）；保留普通进程、网络、文件与线程所需的系统调用；去掉所有需要 `CAP_SYS_ADMIN`、`CAP_SYS_PTRACE`、`CAP_SYS_MODULE`、`CAP_BPF` 等能力的调用（mount、ptrace、bpf、kexec、模块加载、`unshare`/`setns` 等）；`clone` 只允许不带 `CLONE_NEW*` 标志，`clone3` 返回 ENOSYS 让 libc 回退到可过滤的 `clone`；禁止 `AF_VSOCK` 套接字。

**容器内的沙箱与浏览器**：容器里没有 Docker socket（挂载它等于把主机 root 交给 Agent），因此 `deploy/crewbase.patch.yml` 把沙箱换成 `sandbox-none`，沙箱模式的命令会被拒绝而不是无隔离执行；需要隔离执行命令时请用 `install-server.sh` 直接部署在主机上。默认镜像（distroless）没有浏览器；需要浏览器时：

```bash
CREWBASE_RUNTIME=chromium docker compose up -d --build
```

并按 `crewbase.patch.yml` 中的注释启用 `browser`：seccomp 禁止新建命名空间，Chromium 自带的沙箱无法启动，需要 `--no-sandbox`，由容器本身（无能力、seccomp、只读根）作为隔离边界。

**访问方式**（任选其一）：

- `docker compose exec crewbase crew ...`：在主机上直接使用 CLI；
- Tailscale sidecar：在 `.env` 填 `TS_AUTHKEY`，`docker compose --profile tailscale up -d`；sidecar 共享 crewbase 的网络命名空间，通过 `tailscale-serve.json` 把 7788 只发布到你的 tailnet；
- 主机回环 + SSH / SSM：`docker compose -f docker-compose.yml -f docker-compose.loopback.yml up -d`，端口只发布在主机的 `127.0.0.1:7788`。这个覆盖文件里的 `127.0.0.1` 绝不能删除或改成 `0.0.0.0`。此时容器内网关监听 `0.0.0.0`（容器自身命名空间），`crew doctor` 会给出非回环警告，属预期。

## 4. 镜像：`Dockerfile`

多阶段构建：Node 阶段构建 `apps/web`（pnpm）→ Go 阶段把 UI 嵌入并静态编译 `crewd` 与 `crew`（`CGO_ENABLED=0`，SQLite 为纯 Go 实现）→ 运行时镜像。

| 构建参数 | 默认 | 说明 |
|---|---|---|
| `RUNTIME` | `distroless` | `distroless`（`gcr.io/distroless/static-debian12:nonroot`，无 shell 无浏览器）或 `chromium`（Alpine + Chromium + 中日韩字体） |
| `GOPROXY` | `https://proxy.golang.org,direct` | 国内可用 `https://goproxy.cn,direct` |
| `NPM_REGISTRY` | 空 | 国内可用 `https://registry.npmmirror.com` |
| `ALPINE_MIRROR` | 空 | 国内可用 `mirrors.aliyun.com`（仅 chromium 运行时） |

镜像以 uid 65532 运行，数据在 `/data`（`CREWBASE_HOME`），没有 `EXPOSE`。

## 5. 阿里云 ECS：`deploy/aliyun/`

适用：ECS（Ubuntu 24.04）或轻量应用服务器（Ubuntu 24.04 镜像）。

1. 创建实例时挂载一块 **ESSD 数据盘**；安全组**不放行任何入站**（需要 SSH 时只放行自己的 IP，或用 Workbench 远程连接）。
2. 在「实例自定义数据」中粘贴 `deploy/aliyun/user-data.sh`，或登录后 `sudo REGISTRY_MIRROR=https://<你的ID>.mirror.aliyuncs.com bash user-data.sh`。脚本会：
   - 自动识别空白数据盘，格式化为 ext4 并按 UUID 挂载到 `/data`（已有文件系统不会被格式化）；
   - 从 `mirrors.aliyun.com` 安装 Docker CE 与 Compose 插件；
   - 写入 `/etc/docker/daemon.json` 的 `registry-mirrors`（镜像加速地址在「容器镜像服务 ACR → 镜像工具 → 镜像加速器」获取）；
   - 构建时使用 `goproxy.cn`、`npmmirror` 与阿里云 Alpine 源；
   - 用 `deploy/aliyun/crewbase.patch.yml` 预置 **DeepSeek / 通义千问（DashScope）/ Kimi（Moonshot）** 三个 OpenAI 兼容提供方，Key 通过 `env:DEEPSEEK_API_KEY`、`env:DASHSCOPE_API_KEY`、`env:MOONSHOT_API_KEY` 引用；
   - 把数据卷绑定到 ESSD 上的 `/data/crewbase`，启动加固版 compose。
3. 在 `/opt/crewbase/deploy/.env` 填写 Key 后 `docker compose up -d`；`docker compose exec crewbase crew doctor` 确认模型可达。
4. 访问：设置 `TS_AUTHKEY` 使用 Tailscale，或 SSH 隧道 + loopback 覆盖文件。

## 6. AWS：`deploy/aws/`

### EC2（Docker Compose）

`deploy/aws/user-data.sh` 支持 Amazon Linux 2023 与 Ubuntu 24.04：挂载第二块 EBS（gp3）到 `/data`、安装 Docker 与 Compose、启动加固版 compose。安全组不设入站规则，用 **SSM Session Manager** 登录；配合 `docker-compose.loopback.yml` 可以用 `AWS-StartPortForwardingSession` 把 7788 转发到本机，全程无入站端口。

### ECS / Fargate + EFS

`deploy/aws/ecs-task-definition.json` 是任务定义模板，替换所有 `<...>` 占位符：

- 镜像推到 ECR（建议 `docker buildx build --platform linux/arm64`，模板使用 ARM64）；
- **EFS 访问点**：POSIX 用户 uid/gid `65532`，根目录创建权限 `0700`；启用传输加密与 IAM 授权，任务角色需要 `elasticfilesystem:ClientMount`、`ClientWrite`；
- 补丁文件放在 EFS 上的 `/data/crewbase.patch.yml`（内容参考 `deploy/crewbase.patch.yml`）；
- 模型 Key 放在 Secrets Manager，通过 `secrets` 注入为环境变量，补丁用 `env:NAME` 引用；
- 服务使用私有子网，安全组无入站规则；`portMappings` 为空，不挂负载均衡；
- 加固：`readonlyRootFilesystem`、`user 65532`、`capabilities.drop: ALL`、`initProcessEnabled`。Fargate 不支持自定义 seccomp profile 与 `shm_size`（使用平台默认 seccomp），也不支持 tmpfs，因此 `TMPDIR` 指向 EFS 上的 `/data/tmp`；需要浏览器或完整加固时请用 EC2 + Compose；
- 访问：在任务中加入 Tailscale sidecar（userspace 模式），或使用 ECS Exec（`aws ecs execute-command ... --command "crew agents"`）。

## 7. 可选插件

server 部署建议启用审计日志；以下插件尚未进入默认组合包时，用补丁启用：

```yaml
# 审计日志：只追加、哈希链，${dir}/audit/audit-YYYYMM.jsonl
- id: audit
  name: '@crewbase/audit'
  config: { dir: /var/lib/crewbase }
# 审计导出 API（仅桌面端设备）：GET /api/audit?from=&to=&type=&limit=，GET /api/audit/verify
- id: audit-api
  name: '@crewbase/audit-api'
# 持久目标（R-4）
- id: goals
  name: '@crewbase/goals'
- id: goals-api
  name: '@crewbase/goals-api'
# OpenTelemetry 导出（默认关闭；默认不导出消息内容与工具参数 / 结果）
- id: otel
  name: '@crewbase/otel'
  config:
    endpoint: http://127.0.0.1:4318
    headers: { Authorization: 'Bearer ${env:OTEL_TOKEN}' }
    includeContent: false
```

审计记录不写入会话日志，也不会进入模型上下文。校验哈希链：`GET /api/audit/verify`。

## 8. 升级与备份

- systemd：`git pull && sudo ./deploy/install-server.sh`；Compose：`git pull && docker compose up -d --build`。
- 备份数据目录即可（`/var/lib/crewbase` 或 compose 的数据卷 / ESSD / EFS）。会话日志与审计日志都是只追加的 JSONL，可以直接增量备份；恢复后用 `/api/audit/verify` 校验审计链。
- 重启或恢复后，持久目标的自动续跑默认关闭，需要人通过 `POST /api/sessions/{id}/goal/authorize` 重新授权（[06-security-model.md §9](06-security-model.md)）。
