# Docker 部署指南

> 本文只说明 Docker 单容器部署。还没确定入口场景时，先看 [服务器部署方案](scenarios/服务器部署方案.md) 或 [家庭宽带反向代理方案](scenarios/家庭宽带反向代理方案.md)。发布包部署见 `部署指南.md`。Linux、Windows、macOS 的 Docker Desktop / Docker Engine 差异见对应平台目录；公网 HTTPS 入口见 [Caddy 自动 HTTPS 部署指南](https/Caddy自动HTTPS部署指南.md)；容器常驻默认使用 Compose restart policy，外部探针只告警；上游网络代理见 [sing-box 网络代理部署指南](proxy/sing-box网络代理部署指南.md)。

## 1. 适用方式

Docker 镜像使用当前 `backend/dist`、`frontend/dist` 和后端生产依赖。容器启动后由同一个 Node 主进程承载：

- 管理后台：`/__aisys__/`
- 系统 API：`/__aisys__/api`
- OpenAI 兼容网关：`/v1`
- background worker 和本地 DB service 子进程

默认 standalone 模式不需要 Nginx、Redis、PostgreSQL 或额外 worker 容器。如果需要 PostgreSQL + Redis 高性能模式，使用 `docker/compose.performance.yml`，并先阅读 [高性能模式部署指南](高性能模式部署指南.md)。

当前两份 Compose 都只覆盖 capability v1 / legacy 进程拓扑，不是 capability v2 runbook。active deployment contract 为 capability v2 时，启动 preflight 必须拒绝直接使用本文的单主进程拓扑；只有 Docker 专用 runbook 已明确 gateway、每 host replay / quarantine、control / due / activation、Asynq、stats、coordinator 的副本、持久卷、epoch / lease 域、readyz 和启停顺序，并通过 sealed-producer drain 演练后，manifest 才可允许 v2。不能把“容器 health=200”当成这些角色已 ready。

Docker 容器访问宿主机 sing-box 代理时，Host 不是统一写法：

- Windows / macOS Docker Desktop：通常使用 `host.docker.internal:7890`。
- Linux Docker Engine：需要 Compose `extra_hosts: ["host.docker.internal:host-gateway"]`，或使用宿主机内网 / bridge 可达 IP。
- 如果 sing-box 监听 `0.0.0.0`，必须用防火墙限制来源，禁止公网访问代理端口。

Docker 公网 HTTPS 建议让 Caddy 跑在宿主机，反向代理到映射出的 `127.0.0.1:3000`。这样证书数据、`80/443` 监听和 juhe-ai 数据卷互不耦合。

Docker 端口绑定要按入口方式区分：

| 入口方式 | `JUHE_AI_PUBLIC_BIND` | `JUHE_AI_PUBLIC_ORIGIN` | `JUHE_AI_COOKIE_SECURE` | `JUHE_AI_TRUST_PROXY` |
| --- | --- | --- | --- | --- |
| 本机临时验证 | `127.0.0.1` | `http://localhost:3000` | `false` | `false` |
| 局域网 HTTP 临时验证 | `0.0.0.0` | `http://服务器IP:3000` | `false` | `false` |
| 宿主机 Caddy HTTPS | `127.0.0.1` | `https://ai.example.com` | `true` | `true` |
| 云负载均衡 / 内网可信入口 | 内网可达地址 | `https://ai.example.com` | `true` | `true` 或实际代理跳数 |

`JUHE_AI_TRUST_PROXY=true` 只在后端端口被 Caddy、负载均衡或可信内网入口保护时开启。容器端口直接暴露公网时必须保持 `false`，否则客户端可伪造 `X-Forwarded-For`。

## 2. 构建产物

首次部署或代码更新后，先在构建机器生成前后端产物：

```powershell
pnpm install
pnpm build
```

Linux / macOS：

```bash
pnpm install
pnpm build
```

Docker 镜像不会在服务器镜像构建阶段重新跑前端构建，避免低配服务器因为 Vite / 类型检查占用内存导致 SSH 和 Docker 响应变慢。

## 3. 启动

在项目根目录执行：

```powershell
Set-Location docker
docker compose up -d --build
```

Linux / macOS：

```bash
cd docker
docker compose up -d --build
```

启动后访问：

```text
http://localhost:3000/__aisys__/
```

高性能模式启动：

```bash
cd docker
cp .env.performance.example .env.performance
# 填写 JUHE_AI_SECRET、PostgreSQL / Redis 密码，以及对应 URL
docker compose --env-file .env.performance -f compose.performance.yml up -d --build
```

高性能模式未迁移的 PostgreSQL adapter 入口会 fail-fast，不回退写 SQLite；中间件容器、schema 初始化和已迁移链路按 [高性能模式部署指南](高性能模式部署指南.md) 验证。

## 4. 配置

不复制配置文件也能启动。需要改端口、公网访问地址，或沿用已按当前 schema 准备好的数据目录 / `.env` 时，再复制：

```powershell
Copy-Item .env.example .env
```

Linux / macOS：

```bash
cp .env.example .env
```

公网 HTTPS 常用配置：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=https://ai.example.com
JUHE_AI_SECRET=
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

本机或临时 HTTP 验证：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=http://localhost:3000
JUHE_AI_COOKIE_SECURE=false
JUHE_AI_TRUST_PROXY=false
```

说明：

- `JUHE_AI_PUBLIC_BIND` 是宿主机绑定地址；宿主机 Caddy 反代时建议 `127.0.0.1`，需要局域网 HTTP 临时访问时才用 `0.0.0.0`。
- `JUHE_AI_SECRET` 留空时，容器首次启动会在数据卷里生成 `/app/backend/data/.juhe-ai-secret` 并复用；生产建议在宿主机 env 显式保存稳定密钥。
- 如果现有部署使用容器自动生成的密钥，创建 `business-*` 业务备份时必须把容器内当前有效 `JUHE_AI_SECRET` 单独写入受限权限的恢复 env / secret 文件。不得为了保留这个密钥备份整个数据卷；缺少有效密钥时，业务库中的上游凭据无法恢复。
- 迁移已有业务库时必须填写原来的 `JUHE_AI_SECRET`。
- 直接 HTTP 访问时 `JUHE_AI_COOKIE_SECURE=false`；HTTPS 反向代理后建议改为 `true`。
- `JUHE_AI_PUBLIC_ORIGIN` 是 Docker entrypoint 的便捷变量，会在 `JUHE_AI_ALLOWED_ORIGINS` 留空时转换成后端 CORS 白名单；直接运行 `backend/dist/server.js`、PM2、systemd 或托管平台注入环境变量时，必须配置 `JUHE_AI_ALLOWED_ORIGINS`。
- 公网 IP 或域名访问时建议填写 `JUHE_AI_PUBLIC_ORIGIN`，例如 `http://你的服务器IP:3000` 或 `https://ai.example.com`。
- HTTPS 反向代理后，后台真实客户端 IP 依赖 `JUHE_AI_TRUST_PROXY=true` 和前置代理传递 `X-Forwarded-For`；完整说明见 [Caddy 自动 HTTPS 部署指南](https/Caddy自动HTTPS部署指南.md)。
- 需要免费证书自动续期时，优先使用宿主机 Caddy；完整示例见 [HTTPS 部署示例](https/HTTPS部署示例.md)。

## 5. 持久化

Compose 默认创建两个 Docker volume：

```text
juhe-ai-data -> /app/backend/data
juhe-ai-logs -> /app/backend/logs
```

业务库、数据集目录库、使用记录目录库、统计库、usage shard、自动生成的密钥和日志都会跟随 volume 保留。不要在生产环境执行 `docker compose down -v`，否则会删除这些数据。

## 6. 验证

```powershell
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/api/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
```

Linux / macOS：

```bash
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
```

期望：

- `/__aisys__/health` 返回 `200` 和 `status: ok`。
- `/__aisys__/api/health` 返回 `200` 和 `status: ok`。
- `/__aisys__/` 返回前端页面。
- 日志中能看到主服务、DB service 和 background worker 启动记录。

## 7. 状态检测和恢复

Compose `restart: unless-stopped` 负责容器进程退出后的恢复；Docker healthcheck 负责报告 `unhealthy`，但不保证自动重启。默认不再配置会执行 `docker compose restart` 的宿主机 watchdog，避免与发布、迁移和候选配置验证冲突。

外部 health 探针可以只告警。确有无人值守自动恢复需求时，再按 [状态检测与自动恢复指南](watchdog/状态检测与自动恢复指南.md) 单独评审，并且只允许重启应用容器，不能因为公网入口或上游 API 故障重启整组中间件。

不要因为 PostgreSQL、Redis 或上游模型 API 短暂失败就重启所有容器；先按 health 分层判断，避免中间件被反复重启造成更长恢复时间。

## 8. 停止和清理

停止 capability v1 / legacy 容器：

```bash
docker compose down
```

active deployment contract 为 capability v2 时，应先由实现后的 deployment coordinator 冻结 ingress 和 current producer inventory certificate revision / digest，drain 已接受请求 / finalizer，逐个 seal certificate 对应 producer 并证明 active pointer 未变化、`controlResolved=producerAck=finalSequence`、unacked=0、无 unknown tail / lost / global barrier；replay / quarantine owner 必须在 ingress 停止后继续运行到 drain certificate 签发。Docker 专用 coordinator / runbook 尚未实现或未演练时，v2 保持禁止启用，不能手工按猜测顺序停容器。

包装入口无法拦截已经持有 Docker socket、宿主机服务控制权或 root 权限的人直接执行 `docker compose down`。v2 生产必须把这些权限只授予 deployment coordinator 专用身份，普通发布 / 运维身份不得直连 socket 或拥有等价 sudo；wrapper 只在该基础设施 ACL 内校验签名 drain certificate。直接 kill、Docker daemon restart、宿主机崩溃或绕过 coordinator 的停止都属于 unclean shutdown；下次启动必须保持 ingress 与 capability writer fail-closed，完成 deployment epoch、producer inventory certificate revision / digest / chain / active pointer、registry set、producer epoch / finalSequence、barrier、replica ACK 和 spool / replay 水位对账，取得新鲜 runtime authorization并签发 reconciliation certificate 后才允许接流，不能以容器重新 healthy 或过期 activation envelope 代替恢复证明。

停止并删除本次 volume：

```bash
docker compose down -v
```

`down -v` 只适合 capability v1 的临时验证后清理，生产环境不要执行；capability v2 manifest 必须声明无条件拒绝该命令，备份存在也不构成删除 volume 的授权。真正的删除门禁还必须由 Docker authorization / socket proxy、宿主机权限与独立存储 IAM 实现，把 volume delete 权限保留给单独审计的 break-glass 身份；仅靠 wrapper 或 manifest 不能约束原始 Docker socket 持有者。
