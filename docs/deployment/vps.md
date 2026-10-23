# AIStudio2API 独立 VPS 部署与维护

验证日期：2026-10-22（北京时间）。本方案复用 ais130 的部署方式，以固定 Git 提交构建 Docker 镜像，配置与账户数据独立持久化，使用 Caddy 提供 HTTPS。

## 本次实例

| 项目 | 配置 |
| --- | --- |
| 服务器 | `154.201.79.92` |
| 主域名 | `pro92.gemini30.pro` |
| 备用访问 | `http://154.201.79.92:2048` |
| 操作系统 | Debian 13，amd64 |
| 资源 | 8 核，系统实际内存约 15.62 GiB，磁盘约 68.73 GiB |
| 基准版本 | dev，`4ffa93115098e35e97bdc3a5eb5696a85362176c` |
| 应用镜像 | `aistudio2api:4ffa93115098` |
| Docker / Compose / Caddy | `29.9.2` / `5.6.1` / `2.11.7` |
| 应用目录 | `/opt/aistudio2api` |
| 数据目录 | `/opt/aistudio2api/data` → 容器 `/data` |
| SSH 指纹 | `SHA256:Z5LdtHiwS+EzFaoETE4ng+5nFgsI2OToRbAR/WuqrPg` |

该 SSH 指纹来自首次连接，此后连接固定校验。安装与运维记录保存在 `runtime/vps-deploy/`，不包含密码、API Key、Cookie 或账户凭证。

首次部署验收已通过：IP 与域名首页/健康接口 200，HTTPS 严格证书校验通过，HTTP 308 跳转至本站，管理登录 200，无模型 Key 返回 401。容器环境与保存的默认参数逐项一致，时区为洛杉矶，Camoufox 版本与动态库检查通过，Docker/Caddy 开机启动已启用，容器重启次数为 0、无 OOM 标志。首次部署时未导入账户，生成服务 `STOPPED`，正确模型 Key 返回 `503 service_stopped`；未执行模型生成。完整回执：`runtime/vps-deploy/deployment-154.201.79.92-latest.json`。

## 固定架构

```text
HTTPS 域名 :443 → 宿主机 Caddy → 127.0.0.1:2048 → Docker AIStudio2API
HTTP 域名  :80  → Caddy 自动跳转 HTTPS
IP:2048          → Docker AIStudio2API（保留用户要求的直接访问）

/opt/aistudio2api/
├── .env                       # 应用参数；root:root，0600
├── compose.yaml
├── deployment-manifest.json   # 提交、镜像、源码 SHA-256；不含密钥
├── deployment-state.json      # 安装阶段与结果
├── deployment-build.log      # root 可读的安装/构建日志
├── deploy-job.py              # 独立安装任务
├── source/<完整提交 SHA>/
├── backups/                   # 受保护的运维备份
└── data/                      # root:root，0700
    ├── auth/                  # 账户配置、会话、浏览器指纹
    ├── runtime/               # Camoufox 等缓存
    └── ...                    # 应用配置、管理数据库等持久化文件
```

容器设置：`restart: unless-stopped`、`init: true`、`shm_size: 1gb`；Docker 日志每份 10 MB，最多 3 份。Docker 与 Caddy 由 systemd 开机启动。应用只随明确的部署操作更新，不在运行时自动拉取分支。

应用默认参数：

```text
ADMIN_AUTH_ENABLED=true
ADMIN_USERNAME=admin
AISTUDIO_AUTH_STATES=/data/auth
AUTO_START=true
LISTEN_ADDR=0.0.0.0:2048
WEB_PORT=2048
PORT=2048
TZ=America/Los_Angeles
```

`ADMIN_PASSWORD`、`PROXY_API_KEY`、通用 `PASSWORD` 分别读取此 Windows 用户已经保存的 DPAPI 加密配置，不写入本文或仓库。`PASSWORD` 不是 SSH 密码，也不替代模型或管理 Key。管理 Key 由每台应用独立管理，不能复制其他实例的管理 Key。

环境文件采用 Compose `env_file.format: raw`，避免 `$`、引号等字符被插值。要求 Compose ≥2.30。`PORT=${WEB_PORT}` 在上传前解析为实际的 `2048`。

## 新服务器部署

适用 Debian 12/13、amd64/arm64、root SSH、systemd、Python 3。建议与本次相同的 16 GB 内存；工具要求系统实际内存至少 8 GiB、磁盘可用至少 12 GiB。浏览器并发应根据实际内存和账户负载另行调整。

1. 确认目标 IP、SSH 端口和主机指纹。首次通过控制台或受信任的 SSH 连接核对指纹并保存到 `~/.ssh/known_hosts`。已有指纹不匹配时停止，先核查实例是否重装或 IP 是否变化。
2. 选择已经验证的完整提交 SHA。工具使用 `git archive`，不会打包工作区未提交文件、`.git`、本地账户或本地密钥。
3. 在仓库根目录执行下列命令。SSH 密码使用隐藏输入；不要把它拼到命令行。

```powershell
$python = 'C:/Users/lywx2/AppData/Local/Programs/Python/Python312/python.exe'
& $python scripts/vps_deploy.py deploy `
  --host 154.201.79.92 `
  --branch dev `
  --commit 4ffa93115098e35e97bdc3a5eb5696a85362176c `
  --domain pro92.gemini30.pro
```

本机 SSH 库使用 `runtime/vps-deploy/tools` 已安装的 Paramiko；新机器需要先安装 Paramiko 及其依赖，并配置自己的应用参数来源。DPAPI 数据只能由当前 Windows 用户解密，不可直接复制到其他设备使用。

当前用户本机默认经 `127.0.0.1:7897` 的 HTTP CONNECT 代理访问 SSH 与公网。没有该本地代理时加 `--direct`；此参数不改变服务器的上游代理设置。

工具依次执行：检查空目录/端口/资源 → Docker 官方 APT 源安装 → SHA-256 校验并解包源码 → 构建带完整提交标签的镜像 → 启动容器 → 验证健康与认证 → 可选 Caddy HTTPS → 保存脱敏回执。

既有 `/opt/aistudio2api` 会被拒绝，避免覆盖现有系统。只允许在清单提交与源码哈希完全一致时使用 `--resume` 接续本工具创建的安装。重复接续已经完成的安装只做验收，不重新创建容器。

### 断线与失败恢复

安装任务由 `aistudio2api-deploy.service` 独立运行，SSH 断开后仍继续。状态文件原子更新；整个安装上限 70 分钟。

```powershell
& $python scripts/vps_deploy.py status --host 154.201.79.92 --domain pro92.gemini30.pro

& $python scripts/vps_deploy.py deploy `
  --host 154.201.79.92 --branch dev `
  --commit 4ffa93115098e35e97bdc3a5eb5696a85362176c `
  --resume --domain pro92.gemini30.pro
```

`--resume` 是接续同一版本的新装流程，不是升级入口。运行中的任务不会被重复启动；失败任务重新接续时可复用已校验的同版本镜像。若本机保存的默认参数已变化，工具拒绝覆盖既有环境文件。

在服务器上诊断：

```bash
sudo systemctl status aistudio2api-deploy.service --no-pager
sudo cat /opt/aistudio2api/deployment-state.json
sudo tail -n 80 /opt/aistudio2api/deployment-build.log
cd /opt/aistudio2api
sudo docker compose ps
sudo docker stats --no-stream aistudio2api
sudo journalctl -k -b --no-pager
```

构建和安装日志保留在服务器 root 保护目录。应用日志可能包含账户信息，检查时只提取故障所需内容，不整份上传或公开。

## 域名和 HTTPS

DNS 设置 `A`：`pro92.gemini30.pro` → `154.201.79.92`。若未配置该服务器 IPv6，不保留指向其他地址的 AAAA 记录。外部入口需要允许 TCP 80/443；保留直接访问时另允许 TCP 2048。

应用安装完成后，也可单独执行：

```powershell
& $python scripts/vps_deploy.py https --host 154.201.79.92 --domain pro92.gemini30.pro
```

工具先核对域名解析，再通过 Caddy 官方软件源安装。新配置先格式化和校验，保存旧配置备份后应用；重载失败则恢复旧配置。已有其他站点时追加本站配置，不替换其他站点。

本站配置：

```caddyfile
pro92.gemini30.pro {
    reverse_proxy 127.0.0.1:2048
}
```

Caddy 自动申请和续期公开可信证书。证书和 ACME 状态位于 `/var/lib/caddy`；只重载 Caddy，不重启应用。更新域名后在客户端保存完整 HTTPS origin，例如 `https://pro92.gemini30.pro`。

## 验收口径

| 层次 | 必须检查 | 通过代表什么 |
| --- | --- | --- |
| 系统 | 实际 RAM/CPU/磁盘、Docker 开机启动 | 宿主机可支撑安装 |
| 容器 | running、固定镜像提交、持久化挂载、restart policy、无 OOM | 应用进程按预期部署 |
| 公网 | `/` 200，`/health` 200 且 `status=ok` | 服务可访问 |
| HTTPS | 严格校验通过，HTTP 跳转到正确域名 | 域名和证书可用 |
| 认证 | 控制台登录成功，无模型 Key 返回 401 | 管理与模型入口受保护 |
| 模型目录 | 正确模型 Key 返回 200 或明确的服务未就绪状态 | Key 被接受；未必已可生成 |
| 账户/模型 | 导入账户后 readiness、指定账户/模型/实际渠道验证 | 另行确认实际模型调用能力 |

新装默认账户数为 0。此时 `STOPPED`、`503 service_stopped` 可以是正常的未导入状态；不能把它报告成“模型已可用”。安装验证不导入账户、不接受 Google 协议、不发起模型生成请求。

账户迁移另行授权后，必须同时保留 `account.json`、`storage-state.json`、`camoufox-fingerprint.json` 和相关业务数据库的一致性。Google 协议、地区、套餐权限与账户额度仍需分别确认。

此固定版本使用默认 Camoufox 后端，即使账户数为 0，也会在首次启动时准备浏览器，完成前健康接口可能尚未监听。本次下载匹配的 `152.0.4-beta.29`，发行包约 632 MiB，准备耗时约 81 秒，之后宿主机与容器内的健康接口均通过。新装工具为该阶段保留最多约 15 分钟的轮询窗口。初始化失败先检查 GitHub 下载、磁盘、出口网络、账户配置和内存，不把首次下载等待直接认定为服务故障。

密码和模型 Key 不通过公网 HTTP 发送：本方案经固定主机密钥校验的 SSH 将认证请求转至服务器 loopback；公网验证只访问不带凭证的首页和健康接口。

## 升级与回退

2026-10-22 用户要求：**后续应用更新默认不创建备份**。不打包 `data.tar`，不复制 `.env`、Compose 或账户文件；单独明确要求备份时才允许启用备份选项。历史备份不因这条要求被删除。

默认流程：固定提交 → 旧服务运行时构建新镜像 → 短暂停机并在内存中核对配置/账户文件哈希 → 只切换 Compose 镜像标签 → 健康、readiness、账户数、既有指纹与配置一致性验收。旧镜像保留；运行配置、凭证和指纹原位保留，不进行迁移覆盖。不把 `docker compose down -v` 或清理应用卷作为升级步骤。

失败时优先切回旧镜像。若新版改变了持久化数据格式，需要另行评估兼容性，不能自动清空或覆盖业务数据。脱敏版本记录、旧镜像标签和检查结果仍会保存，帮助后续诊断。

本机更新入口为 `runtime/vps-deploy/update_existing_vps.py`，默认关闭备份；只有新的明确备份请求才使用 `--backup`。旧的默认备份脚本已禁用，避免后续误用。普通空间检查不再要求额外预留整份数据备份空间。

本次两台已升级到 dev `4ffa93115098e35e97bdc3a5eb5696a85362176c`：pro92 保留 3 个账户，ais130 保留 24 个账户；均为 RUNNING、readiness=true，HTTPS、健康接口、管理登录及模型目录认证通过。已有指纹与配置保持一致。此次备份在用户发出“不备份”要求之前已完成，之后已按用户明确要求删除本次 pro92 和 ais130 的两个更新备份目录，共释放约 3.29 GiB；其他历史备份及在线数据保持原状。后续更新不自动创建应用数据备份。ais130 的空间不足通过清理约 2.49 GiB 过期构建缓存解决，新旧镜像与业务数据保留。

## 依据与版本边界

- [Docker Debian 官方安装说明](https://docs.docker.com/engine/install/debian/)：当前支持 Debian 12/13，使用官方 APT 仓库。
- [Caddy 官方安装说明](https://caddyserver.com/docs/install#debian-ubuntu-raspbian)：使用官方包与 systemd 服务。
- [Caddy 自动 HTTPS](https://caddyserver.com/docs/automatic-https)：DNS、80/443、证书存储与自动续期要求。

应用源码固定到已验证提交；Docker/Caddy 安装包和基础镜像使用部署时仓库提供的版本，尚未固定 APT 包版本和基础镜像 digest。本方案保证应用源码可追溯，不承诺未来重建的镜像逐字节相同；每次发布保留镜像与实际版本回执。
