# Linux 部署指南

## 1. 推荐路径

| 场景 | 推荐方式 | 入口 |
| --- | --- | --- |
| 生产形态（国内单机 Docker Compose，唯一生产形态） | Docker Compose | [部署指南](../部署指南.md) |
| 发布包直跑（systemd/裸进程，备用形态） | 本文档 | 本文第 3 节 |
| 公网 HTTPS | Caddy 自动 HTTPS | [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) |
| 常驻恢复 | systemd；外部探针默认只告警 | 本文第 8 节 |
| 上游需要代理 | sing-box 本机代理 + 后台代理管理 | [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) |

存储形态自 2026-10-24/25 起为 PostgreSQL + Redis 唯一形态（SQLite / memory 本地模式已退役）；直跑与 Docker Compose 共用同一 env 契约与 maintenance `--dsn` 单形态命令，差异只在进程编排。

## 2. 部署前检查

```bash
uname -a
df -h .
free -h
ss -lntp | grep ':3000 ' || true
# PostgreSQL / Redis 可达性（直跑必须自备两个中间件）
pg_isready -h <PG 主机> -p 5432
redis-cli -h <Redis 主机> -p 6379 ping
```

要求：

- PostgreSQL 与 Redis 必须已就绪且可从本机访问（`JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL` 三连接串缺失时 gateway/jobs 启动 fail-fast）。PG 单库 `juhe_ai` 多 schema；`juhe_jobs` / `juhe_j3b` 需手工 `CREATE SCHEMA AUTHORIZATION` 后由 maintenance `--apply-*` 建表（序列见 `docker/single-server/README.md` 拓扑节）。
- 端口 `3000` 未被占用，或已准备修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 如果服务器无法直连 Docker Hub 或上游模型 API，先配置系统代理或 sing-box。
- 生产上只对公网开放 `80/443`；不要把 PostgreSQL、Redis、sing-box 或 juhe-ai `3000` 端口暴露到公网。

## 3. 发布包部署（PG 直跑）

首次启动前先完成 schema 初始化（幂等；`--dsn` 为 maintenance 唯一存储形态，`--driver`/`--paths` 旗标已随 SQLite 退役删除）：

```bash
tar -xzf juhe-ai-release.tar.gz
cd juhe-ai-release
# start.sh 首次运行会创建 backend/.env；先编辑再启动
nano backend/.env

# schema 初始化与默认 seed（先完成 juhe_jobs/juhe_j3b schema 预置，见 docker/single-server/README.md）
backend/bin/juhe-ai-maintenance --ensure-schema --seed --dsn "$JUHE_AI_POSTGRES_URL"
# J3b 账户电路运行态索引（新库必跑一次）
backend/bin/juhe-ai-gateway -init-account-circuit-runtime-index
```

`backend/.env` 至少包含（实例值按部署环境填写，完整契约以 `docker/single-server/README.md` ".env 契约"节为准）：

```env
JUHE_AI_POSTGRES_URL=postgres://<user>:<password>@<host>:5432/juhe_ai?sslmode=require
JUHE_AI_REDIS_CACHE_URL=redis://:<password>@<host>:6379/0
JUHE_AI_REDIS_STATE_URL=redis://:<password>@<host>:6379/1
JUHE_AI_REDIS_NAMESPACE=<部署隔离前缀，必填非空>
JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://:<password>@<host>:6379/2
JUHE_AI_RUNTIME_LOG_POSTGRES_URL=postgres://<user>:<password>@<host>:5432/juhe_ai?sslmode=require&application_name=juhe-ai-jobs-runtime-log
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_LOG_DIR=./logs
JUHE_AI_DATA_DIR=./data
```

说明：

- `JUHE_AI_POSTGRES_URL` / `JUHE_AI_REDIS_CACHE_URL` / `JUHE_AI_REDIS_STATE_URL` 三连接串必填，缺失启动即失败；`JUHE_AI_RUNTIME_LOG_POSTGRES_URL` 无主 URL 回退，必须显式。
- F1/F3/F4 owner `*_INSTANCE_ID` 缺省取主机名，单机直跑可不配置；F3 审计 blob 目录、运行日志目录等未配置时按 `JUHE_AI_DATA_DIR`（缺省 `./data`）派生。
- 已退役 env（`JUHE_AI_RUNTIME_MODE`、`JUHE_AI_DATABASE_DRIVER`、`JUHE_AI_CACHE_DRIVER`、`JUHE_AI_RUNTIME_STATE_DRIVER`、全部 `*_DATABASE_PATH` / 分片 / store 选择族）不再被读取，不要写入配置。
- gateway/jobs 的相对路径以进程工作目录为基准，且用量记录经 `usage-record-spool` 目录在 gateway↔jobs 间交接，两侧必须解析到同一路径（见第 8 节 systemd `WorkingDirectory` 与 `docker/single-server/README.md`"运行时数据目录契约"）。

配置完成后启动：

```bash
bash ./start.sh
```

启动后检查：gateway `3000` 业务端口、gateway `3306/health` 与 jobs `3305/health` 均应返回正常；无 Key 访问 `/v1/models` 应 `401`。检查 `backend/logs/juhe-ai-jobs.log` 与 `backend/logs/juhe-ai-gateway.log` 无启动错误。统计落库验证：发起一次真实 `/v1` 请求后 `SELECT count(*) FROM juhe_usage.usage_records;` 应 > 0（恒 0 行 = spool 交接断裂，按 `docker/single-server/README.md`"运行时数据目录契约"排查）。

生产目录建议使用固定路径和共享数据目录，避免升级时覆盖 `.env` 和数据：

```text
/opt/juhe-ai-lite/
  current -> releases/某次发布/juhe-ai-release
  releases/
  shared/backend.env
  shared/data/
  backups/
  logs/
  bin/run.sh
```

首次部署后把配置和数据移到 `shared/`，后续新 release 只切换 `current` 软链接：

```bash
sudo useradd --system --home /opt/juhe-ai-lite --shell /usr/sbin/nologin juhe || true
sudo install -d -o juhe -g juhe /opt/juhe-ai-lite/shared/data /opt/juhe-ai-lite/releases /opt/juhe-ai-lite/bin
cd /opt/juhe-ai-lite/releases/时间戳/juhe-ai-release
sudo install -d /opt/juhe-ai-lite/shared
sudo cp -n backend/.env /opt/juhe-ai-lite/shared/backend.env
sudo chown juhe:juhe /opt/juhe-ai-lite/shared/backend.env
sudo nano /opt/juhe-ai-lite/shared/backend.env
ln -sfn /opt/juhe-ai-lite/shared/backend.env backend/.env
rm -rf backend/data
ln -sfn /opt/juhe-ai-lite/shared/data backend/data
ln -sfn /opt/juhe-ai-lite/releases/时间戳/juhe-ai-release /opt/juhe-ai-lite/current
```

`systemd` 推荐只调用固定包装脚本，不直接写死某次 release 路径：

```bash
sudo tee /opt/juhe-ai-lite/bin/run.sh >/dev/null <<'EOF'
#!/usr/bin/env bash
set -e
export NODE_ENV=production
cd /opt/juhe-ai-lite/current
exec bash ./start.sh
EOF
sudo chmod +x /opt/juhe-ai-lite/bin/run.sh
```

常驻运行推荐 `systemd`。写入服务文件（`WorkingDirectory` 必须指向含 `backend/data/` 的工作目录，否则 spool 与日志相对路径分叉）：

```bash
sudo tee /etc/systemd/system/juhe-ai.service >/dev/null <<'EOF'
[Unit]
Description=Juhe AI
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/juhe-ai-lite/current
ExecStart=/usr/bin/env bash /opt/juhe-ai-lite/bin/run.sh
Restart=always
RestartSec=5
LimitNOFILE=1048576
User=juhe
Environment=NODE_ENV=production

[Install]
WantedBy=multi-user.target
EOF
```

常用命令：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now juhe-ai
sudo journalctl -u juhe-ai -f
```

发布切换：

```bash
sudo ln -sfn /opt/juhe-ai-lite/releases/新版本/juhe-ai-release /opt/juhe-ai-lite/current
sudo systemctl restart juhe-ai
sudo systemctl status juhe-ai --no-pager
ss -lntp | grep ':3000 ' || true
pgrep -af 'juhe-ai' | grep '/opt/juhe-ai-lite' || true
```

当前发布包为 go-only 形态：`start.sh` 直接拉起并看护 `juhe-ai-gateway` 与 `juhe-ai-jobs` 两个 Go 进程（`juhe-ai-maintenance` 仅按需一次性执行），不存在 Node worker 或 DB service 子进程，不要把任何二进制单独注册成 systemd 服务绕过 `start.sh`。升级后必须确认 `3000` 只由当前 gateway 进程监听，进程命令行里没有旧 release 路径。只有强杀、系统崩溃或疑似进程脱离 `start.sh` 看护时，再检查是否有残留进程。

一次性维护窗口（迁移、回填、退场清理）的直跑等价操作见 [Linux 部署目录](README.md)「直跑等价操作」条目，与 Docker 窗口序列同源（maintenance 二进制 + `--dsn` 单形态）。

## 4. Docker 差异

Linux Docker Engine 不一定内置 `host.docker.internal`。如果 juhe-ai 容器需要访问宿主机上的 sing-box 代理，可以二选一：

- 在 Compose override 中给 `juhe-ai` 增加 `extra_hosts: ["host.docker.internal:host-gateway"]`，后台代理 Host 填 `host.docker.internal`。
- 让 sing-box 监听宿主机内网 IP 或 Docker bridge 可达地址，后台代理 Host 填该 IP。

不要把 sing-box 的本地代理端口无鉴权暴露到公网；如果必须监听 `0.0.0.0`，用系统防火墙限制来源只允许本机、Docker bridge 或应用服务器。

如果宿主机 Caddy 反代 Docker 容器，`docker/.env` 建议：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

如果只是局域网 HTTP 临时验证，才把 `JUHE_AI_PUBLIC_BIND=0.0.0.0`，并保持 `JUHE_AI_TRUST_PROXY=false`。

## 5. HTTPS 和反向代理

Linux 生产默认推荐 Caddy 做 HTTPS：域名解析到服务器后，Caddy 会自动申请和续期免费证书。完整步骤见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

如果已经使用 Nginx，可以继续用 Nginx + Certbot；必须关闭 `/v1/*` 流式接口 buffering，并设置足够长的读写超时。备选示例见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

发布包 `backend/.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

只有 Caddy、Nginx 或可信负载均衡能访问 `127.0.0.1:3000` 时才开启 `JUHE_AI_TRUST_PROXY=true`。如果后端端口直接对公网开放，必须保持 `false`。

高并发长连接的保守系统起步值是 `net.core.somaxconn=4096`、`net.ipv4.tcp_max_syn_backlog=4096`。Caddy 建议使用 `LimitNOFILE=1048576`，并在受控重启后从 `/proc/<PID>/limits` 验证实际值。UDP buffer、netdev backlog、conntrack、softnet/IRQ 和 BBR 只在当前内核能力与区间指标证明需要时调整，不作为无条件部署项。

## 6. 端口和防火墙

公网服务器建议只放行：

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw deny 3000/tcp
sudo ufw deny 7890/tcp
```

使用 firewalld 时：

```bash
sudo firewall-cmd --permanent --add-service=http
sudo firewall-cmd --permanent --add-service=https
sudo firewall-cmd --reload
```

PostgreSQL、Redis 和 sing-box 如果确实需要跨机器访问，只允许内网、WireGuard 私网或安全组白名单；不要用 `0.0.0.0/0` 放行。

真实客户端 IP 验证：

- 从两个不同网络访问 `https://ai.example.com/v1`。
- 在后台“使用记录”或“公开接口日志”确认 `clientIp` 不再全部是 `127.0.0.1`、Caddy 内网地址或负载均衡地址。
- 如果前面还有 CDN / 负载均衡，按 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) 的 `trusted_proxies` 配置可信来源。

## 7. 上游网络代理

juhe-ai 中转请求上游时，推荐在后台“代理管理”中新增代理，然后绑定到 AI 账户：

| 部署形态 | 代理类型 | Host | 端口 |
| --- | --- | --- | --- |
| 裸机同机 sing-box | `socks5h` | `127.0.0.1` | `7890` |
| Linux Docker + host-gateway | `socks5h` | `host.docker.internal` | `7890` |
| sing-box 独立代理机 | `socks5h` | 代理机内网 IP | `7890` |

`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理；普通上游模型请求应优先使用账号绑定代理。

## 8. 状态检测和恢复

Linux 生产默认只由 systemd 守护 `start.sh`（其内部看护 gateway 与 jobs 两个 Go 进程）；默认不部署会自动执行 `systemctl restart` 的外部 watchdog，避免与发布、候选配置验证和受控重启冲突。

CentOS 7 等旧内核无法加载 WireGuard 内核模块时，只允许从 WireGuard 官方源码固定提交构建 `wireguard-go`，记录源码提交和 SHA-256，并由 `wg-quick@<interface>.service` 托管；不得使用来源不明的预编译二进制。必须以真实握手、收发计数和 HTTP/TLS 请求验证用户态实现。

外部 health 探针可以只告警。公网失败但本机 health 正常时，先按 DNS、Caddy、WireGuard、证书和 Nginx 分层排查，不重启应用。确有无人值守自动恢复需求时，须单独评审防抖、限频和发布互斥（原《状态检测与自动恢复指南》已随旧形态文档清理删除）。
