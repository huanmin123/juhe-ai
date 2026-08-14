# Docker 部署指南

> 本文只说明 Docker 单容器部署。还没确定入口场景时，先看 [服务器部署方案](scenarios/服务器部署方案.md) 或 [家庭宽带反向代理方案](scenarios/家庭宽带反向代理方案.md)。发布包部署见 `部署指南.md`。Linux、Windows、macOS 的 Docker Desktop / Docker Engine 差异见对应平台目录；公网 HTTPS 入口见 [Caddy 自动 HTTPS 部署指南](https/Caddy自动HTTPS部署指南.md)；容器常驻默认使用 Compose restart policy，外部探针只告警；上游网络代理见 [sing-box 网络代理部署指南](proxy/sing-box网络代理部署指南.md)。

## 1. 适用方式

Docker 镜像使用当前 `backend/dist`、`frontend/dist` 和后端生产依赖。Compose 拓扑是一个 Node 应用容器、`go-jobs`（F1/F2）与 `go-gateway`（F3/F4）两个 Go 容器。Node 应用容器承载：

- 管理后台：`/__aisys__/`
- 系统 API：`/__aisys__/api`
- OpenAI 兼容网关：`/v1`
- background worker 和本地 DB service 子进程

F1/F2/F3/F4 分别是各自事实的 Go owner：jobs 负责 F1 运行日志索引/保留与 F2 表监控采样、快照/保留，gateway 负责 F3 原始审计、payload/blob、hot-search 与 F4 操作日志写入/读取/保留。它们直接异步执行，不使用 Node 队列；Node 只保留 F3 capture、F4 operation log 的一次性 loopback HMAC 输入和对应只读查询。

默认 standalone 模式不需要 Nginx、Redis、PostgreSQL 或额外 Node worker 容器。如果需要 PostgreSQL + Redis 高性能模式，使用 `docker/compose.performance.yml`，并先阅读 [高性能模式部署指南](高性能模式部署指南.md)。2026-08-10 已在开发 Linux 服务器完成两种 Compose runtime 闭环；这不等于 macOS 或生产环境已部署。

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

> 下面命令是待执行操作，不是已验证的 Docker 运行记录。只应在干净、固定 release commit 构建出的发布候选上运行；当前有未提交改动的工作区不能作为生产候选。

在项目根目录执行：

```powershell
Set-Location docker
Copy-Item .env.example .env
# 编辑 .env：必须填写独立的 JUHE_AI_AUDIT_LOG_INPUT_SECRET。
docker compose config --quiet
docker compose up -d --build --wait
```

Linux / macOS：

```bash
cd docker
cp .env.example .env
# 编辑 .env：必须填写独立的 JUHE_AI_AUDIT_LOG_INPUT_SECRET。
docker compose config --quiet
docker compose up -d --build --wait
```

启动后访问：

```text
http://localhost:3000/__aisys__/
```

高性能模式启动：

```bash
cd docker
cp .env.performance.example .env.performance
# 填写 JUHE_AI_SECRET、PostgreSQL / Redis 密码、对应 URL 和独立 JUHE_AI_AUDIT_LOG_INPUT_SECRET
docker compose --env-file .env.performance -f compose.performance.yml config --quiet
docker compose --env-file .env.performance -f compose.performance.yml up -d --build --wait
```

高性能模式未迁移的 PostgreSQL adapter 入口会 fail-fast，不回退写 SQLite；中间件容器、schema 初始化和已迁移链路按 [高性能模式部署指南](高性能模式部署指南.md) 验证。

## 4. 配置

首次启动必须复制配置文件。F3 input secret 没有默认值；不复制或不填写该值时 Compose 会在配置校验阶段失败：

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
- `JUHE_AI_AUDIT_LOG_INPUT_SECRET` 与 `JUHE_AI_OPERATION_LOG_INPUT_SECRET` 必须填写为两把独立、稳定的高熵值；不能复用或回退 `JUHE_AI_SECRET`，production 至少 32 位。它们分别提供给 Node/F3、Node/F4 loopback HMAC 输入端点。
- 如果现有部署使用容器自动生成的密钥，创建 `business-*` 业务备份时必须把容器内当前有效 `JUHE_AI_SECRET` 单独写入受限权限的恢复 env / secret 文件。不得为了保留这个密钥备份整个数据卷；缺少有效密钥时，业务库中的上游凭据无法恢复。
- 迁移已有业务库时必须填写原来的 `JUHE_AI_SECRET`。
- 直接 HTTP 访问时 `JUHE_AI_COOKIE_SECURE=false`；HTTPS 反向代理后建议改为 `true`。
- `JUHE_AI_PUBLIC_ORIGIN` 是 Docker entrypoint 的便捷变量，会在 `JUHE_AI_ALLOWED_ORIGINS` 留空时转换成后端 CORS 白名单；直接运行 `backend/dist/server.js`、PM2、systemd 或托管平台注入环境变量时，必须配置 `JUHE_AI_ALLOWED_ORIGINS`。
- Go jobs 与 gateway 都必须显式配置稳定的 owner ID；F3/F4 均需独立 input secret 与 loopback input URL，F3 另需 blob/hot-search 目录。F3/F4 listener 都仅监听 loopback，所以 go-gateway 必须与 Node 共享 network namespace。
- Docker 构建阶段默认使用官方 Go proxy；受限网络可覆盖 `JUHE_AI_GO_PROXY=https://goproxy.cn,direct`，不影响运行时数据库或 Redis URL。
- 公网 IP 或域名访问时建议填写 `JUHE_AI_PUBLIC_ORIGIN`，例如 `http://你的服务器IP:3000` 或 `https://ai.example.com`。
- HTTPS 反向代理后，后台真实客户端 IP 依赖 `JUHE_AI_TRUST_PROXY=true` 和前置代理传递 `X-Forwarded-For`；完整说明见 [Caddy 自动 HTTPS 部署指南](https/Caddy自动HTTPS部署指南.md)。
- 需要免费证书自动续期时，优先使用宿主机 Caddy；完整示例见 [HTTPS 部署示例](https/HTTPS部署示例.md)。

### Go 项目的 owner 与存储隔离

jobs 与 gateway 在 Node `/__aisys__/api/health` 确认 DB service 就绪后启动，各自保留容器、PID 和日志。任一 Go project 退出或功能数据不新鲜都会使部署验证失败，不得把 Node health 成功当作 Go project 成功。

- F1：`JUHE_AI_RUNTIME_LOG_INSTANCE_ID`、`JUHE_AI_RUNTIME_LOG_STORE`、`JUHE_AI_RUNTIME_LOG_DATABASE_PATH`（SQLite）或 `JUHE_AI_RUNTIME_LOG_POSTGRES_URL`（PostgreSQL），以及 `JUHE_AI_LOG_DIR`。`JUHE_AI_RUNTIME_LOG_ONCE=false` 用于常驻运行。
- F2：`JUHE_AI_TABLE_MONITOR_INSTANCE_ID`、`JUHE_AI_TABLE_MONITOR_STORE`、`JUHE_AI_TABLE_MONITOR_DATABASE_PATH`（SQLite）或 `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`（PostgreSQL）。生产常驻还应明确 interval、lease、retention 等 F2 参数。
- F3：`JUHE_AI_AUDIT_LOG_INSTANCE_ID`、`JUHE_AI_AUDIT_LOG_STORE`、`JUHE_AI_AUDIT_LOG_DATABASE_PATH`（SQLite）或 `JUHE_AI_AUDIT_LOG_POSTGRES_URL`（PostgreSQL）、独立 blob/hot-search 目录、`JUHE_AI_AUDIT_LOG_INPUT_SECRET`。
- F4：`JUHE_AI_OPERATION_LOG_INSTANCE_ID`、`JUHE_AI_OPERATION_LOG_STORE`、`JUHE_AI_OPERATION_LOG_DATABASE_PATH`（SQLite）或 `JUHE_AI_OPERATION_LOG_POSTGRES_URL`（PostgreSQL）、`JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH`（SQLite）或业务 schema、独立 input secret。PostgreSQL 启动会校验既有 F4 schema，不兼容旧 Node 表必须离线迁移后再切换。
- SQLite：F1、F2、F3、F4、业务、dataset、usage catalog、stats 和 usage shard 路径必须物理隔离；F1-F4 输出库分别由 Go 强制 `WAL` 和 `busy_timeout=5000`，Node 只读其产物。
- PostgreSQL：F1 写其对应日志 schema，F2 写 `juhe_stats`，F3 写其审计 schema，F4 写 operation-log schema；均不以 Redis 或 Node 队列为依赖。

## 5. 持久化

Compose 默认创建五个 Docker volume：

```text
juhe-ai-data -> /app/backend/data
juhe-ai-logs -> /app/backend/logs
juhe-ai-runtime-log-data -> /app/backend/runtime-log-data
juhe-ai-table-monitor-data -> /app/backend/table-monitor-data
juhe-ai-audit-log-data -> /app/backend/audit-log-data
juhe-ai-operation-log-data -> /app/backend/operation-log-data
```

业务库、数据集目录库、使用记录目录库、统计库、usage shard、F1/F2/F3/F4 专用事实、自动生成的密钥和日志都会跟随 volume 保留。不要在生产环境执行 `docker compose down -v`，否则会删除这些数据。

## 6. 验证

```powershell
docker compose ps
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/api/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
docker compose logs --tail=100 go-jobs go-gateway
```

Linux / macOS：

```bash
docker compose ps
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
docker compose logs --tail=100 go-jobs go-gateway
```

期望：

- `/__aisys__/health` 返回 `200` 和 `status: ok`。
- `/__aisys__/api/health` 返回 `200` 和 `status: ok`。
- `/__aisys__/` 返回前端页面。
- `docker compose ps` 中 Node、`go-jobs` 与 `go-gateway` 均为 `healthy`；performance 还应包含 PostgreSQL、PgBouncer 和三类 Redis。
- routine upgrade 只检查主服务、jobs 与 gateway 启动日志、Node control/API health、Go project health、无 Key gateway `401` 和 F3/F4 input health `204`。Node → Go → Node 审计读回及 F1 `/runtime-logs`、F2 `/table-monitor` 新鲜度只在首次部署、owner/存储变更、故障或回切时执行。

## 7. 状态检测和恢复

Compose `restart: unless-stopped` 负责容器进程退出后的恢复；Docker healthcheck 负责报告 `unhealthy`，但不保证自动重启。默认不再配置会执行 `docker compose restart` 的宿主机 watchdog，避免与发布、迁移和候选配置验证冲突。

外部 health 探针可以只告警。确有无人值守自动恢复需求时，再按 [状态检测与自动恢复指南](watchdog/状态检测与自动恢复指南.md) 单独评审，并且只允许重启应用容器，不能因为公网入口或上游 API 故障重启整组中间件。

不要因为 PostgreSQL、Redis 或上游模型 API 短暂失败就重启所有容器；先按 health 分层判断，避免中间件被反复重启造成更长恢复时间。

## 8. 停止和清理

停止容器：

```bash
docker compose down
```

停止并删除本次 volume：

```bash
docker compose down -v
```

`down -v` 只适合临时验证后清理，生产环境不要执行。
