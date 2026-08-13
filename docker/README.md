# Docker 部署

这是轻量 Docker 入口。默认模式的 Compose 拓扑为 Node（Web、管理 API、`/v1` 网关、background worker、DB service）、`go-jobs`（F1/F2）和 `go-gateway`（F3/F4）。高性能模式使用 `compose.performance.yml` 部署 PostgreSQL、PgBouncer、Redis cache、Redis state、独立 Redis queue、Node 应用及两个独立 Go 项目；Redis Streams 可靠队列默认写入 `redis-queue`，但 F1/F2/F3/F4 不使用 Redis 数据面。

> 旧单 sidecar 的开发验证不能外推到当前双项目拓扑。当前生产候选必须从干净、固定 commit 构建，并重新验证 Node -> F3/F4 -> Node 读回、F1/F2 新鲜度以及 gateway/jobs 独立重启恢复。

## 文件说明

- `compose.yml`：单容器 Compose 配置，包含端口、环境变量、数据卷和镜像构建参数。
- `compose.performance.yml`：高性能模式 Compose 配置，包含 PostgreSQL、PgBouncer、Redis cache、Redis state、独立 Redis queue 和应用服务。
- `Dockerfile`：运行镜像构建文件，只组装已构建好的 `backend/dist`、`frontend/dist` 和后端生产依赖。
- `Dockerfile.go-project`：通用 Go 项目镜像，通过 `GO_PROJECT=gateway|jobs` 分别构建独立模块，并内置项目 `/health` probe。
- `entrypoint.sh`：容器启动入口，设置默认环境变量、创建数据目录、生成或复用 `JUHE_AI_SECRET`，并执行 SQLite 运行时预检。
- `.env.example`：standalone 必填配置模板。首次启动必须复制为 `.env` 并填写 F3/F4 input secret；不能直接使用默认 Compose 配置。
- `.env.performance.example`：高性能模式必填配置模板。复制为 `.env.performance` 后必须填写数据库、Redis、应用密钥和 F3/F4 input secret。

## 构建产物

先在项目根目录生成前后端产物：

```bash
pnpm install
pnpm build
```

Docker 镜像使用当前 `backend/dist` 和 `frontend/dist`，不会在服务器镜像构建阶段重新跑前端构建。如果缺少必要产物，`docker compose up -d --build` 会直接提示缺失文件，并要求先执行上面的构建命令。

`go-gateway` 与 `go-jobs` 分别在镜像构建时编译，不需要预先生成 Go 二进制。受限网络可在 `.env` / `.env.performance` 设置 `JUHE_AI_GO_PROXY=https://goproxy.cn,direct`；默认仍使用官方 Go proxy。

## 启动

```bash
cd docker
cp .env.example .env
# 在 .env 填写稳定的 JUHE_AI_AUDIT_LOG_INPUT_SECRET 与 JUHE_AI_OPERATION_LOG_INPUT_SECRET；production 至少 32 位，不能使用 JUHE_AI_SECRET。
docker compose config --quiet
docker compose up -d --build --wait
```

浏览器访问：

```text
http://localhost:3000/__aisys__/
```

公网访问时，把 `localhost` 换成服务器 IP 或域名，并建议在 `.env` 中配置 `JUHE_AI_PUBLIC_ORIGIN`。

## 高性能模式启动

```bash
cd docker
cp .env.performance.example .env.performance
# 填写 JUHE_AI_SECRET、PostgreSQL / Redis 密码、对应 URL、访问 Origin 和独立 JUHE_AI_AUDIT_LOG_INPUT_SECRET / JUHE_AI_OPERATION_LOG_INPUT_SECRET。
docker compose --env-file .env.performance -f compose.performance.yml config --quiet
docker compose --env-file .env.performance -f compose.performance.yml up -d --build --wait
```

当前 PostgreSQL repository adapter 未整体完成；未迁移入口会在 performance 模式下 fail-fast，避免误回退到 SQLite。PostgreSQL、PgBouncer、Redis、schema 初始化和已迁移链路按 `docs/deploy/高性能模式部署指南.md` 验证。

中间件启动后，在项目根目录执行：

```bash
pnpm --filter juhe-ai-backend postgres:init-schema
```

空库或可重建测试库只执行 `postgres:init-schema`；`postgres:init-schema-only` 只用于当前版本 DDL 复查，不作为常规初始化或旧库补结构步骤。

如果命令在 Docker 宿主机执行，需要把 `JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL` 和 `JUHE_AI_REDIS_QUEUE_URL` 临时改为宿主机发布端口；详细命令见 `docs/deploy/高性能模式部署指南.md`。应用容器内使用 `pgbouncer:5432`、`redis-cache:6379`、`redis-state:6379`、`redis-queue:6379`，宿主机验证默认使用 PgBouncer `6432`、redis-cache `6379`、redis-state `6380`、redis-queue `6381`，不要把 Redis 容器内 `6379` 误当宿主机端口。

## 独立 Go 项目

两个 Compose 文件都会分别启动 `go-jobs` 与 `go-gateway`。F1/F2 owner ID 属于 jobs，F3/F4 owner ID 属于 gateway；两项目独立重启和健康检查。二者只在 Node `/__aisys__/api/health` 确认 DB service 就绪后启动。`JUHE_AI_RUNTIME_LOG_ONCE=false` 保持 F1 常驻扫描；F2 按 interval、lease 和 retention 采样；F3/F4 使用各自独立输入密钥和 loopback 输入端点。

standalone 模式将 `juhe-ai-data` 与 F1/F2 专用 SQLite 输出目录挂载给 jobs，将业务只读库与 F3/F4 专用目录挂载给 gateway；Node 对各 Go 产物只读。F1/F2/F3/F4 的 SQLite 路径仍必须互相物理隔离，各 owner 对自己的输出维持 `WAL` 与 `busy_timeout=5000`。performance 模式下 jobs 使用 PostgreSQL 写 F1 日志 schema 与 F2 `juhe_stats`，gateway 写 F3 审计 schema 与 F4 `juhe_dataset` 操作日志表。任何项目容器退出仅由 Compose restart policy 重启该项目。

F3/F4 listener 只允许 loopback，因此 Docker 必须以 `network_mode: service:juhe-ai` 让 `go-gateway` 与 Node 共享 network namespace；不要改成容器 service DNS 或 `0.0.0.0`。SQLite 部署人工预检还必须确认 usage shard root 不与任一 F1/F2/F3/F4 输出或源库重叠。

## 按需配置

首次启动先复制示例配置。F3/F4 input secret 没有默认值，任一未配置时 `docker compose config` 和 `up` 都会失败：

```bash
cd docker
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

- `JUHE_AI_PUBLIC_BIND` 是宿主机绑定地址；宿主机 Caddy/HTTPS 反代建议 `127.0.0.1`，直接局域网 HTTP 临时验证才用 `0.0.0.0`。
- `JUHE_AI_PUBLIC_PORT` 是宿主机端口，默认 `3000`。
- `JUHE_AI_PUBLIC_ORIGIN` 是 Docker entrypoint 的便捷变量，会在 `JUHE_AI_ALLOWED_ORIGINS` 留空时转换成后端 CORS 白名单；直接运行 Node / PM2 / systemd 时必须配置 `JUHE_AI_ALLOWED_ORIGINS`。
- `JUHE_AI_PUBLIC_ORIGIN` 填浏览器实际访问的完整 Origin，例如 `http://1.2.3.4:3000` 或 `https://ai.example.com`。
- `JUHE_AI_SECRET` 留空时，容器首次启动会在数据卷里生成并复用。迁移旧业务库时必须填写旧密钥，否则 OAuth token、上游 API Key、代理密码等敏感字段无法解密。
- `JUHE_AI_AUDIT_LOG_INPUT_SECRET` 必须显式填写为独立、稳定的高熵值；不能复用或回退 `JUHE_AI_SECRET`，production 至少 32 位。它同时提供给 Node 和 F3 loopback HMAC 输入端点。
- `JUHE_AI_OPERATION_LOG_INPUT_SECRET` 必须显式填写为独立、稳定的高熵值；不能复用、回退或与 F3 secret 共用，production 至少 32 位。它同时提供给 Node 和 F4 loopback HMAC 输入端点。
- 直接 HTTP 访问时保持 `JUHE_AI_COOKIE_SECURE=false` 和 `JUHE_AI_TRUST_PROXY=false`；HTTPS 反向代理后改为 `true`，并确保容器端口只被 Caddy 或可信入口访问。
- Docker Hub 拉取慢时，可以在 `.env` 中覆盖 `JUHE_AI_NODE_IMAGE` 为可访问的 Node 22 slim 镜像。

## 数据持久化

Compose 默认创建五个 Docker volume：

```text
juhe-ai-data -> /app/backend/data
juhe-ai-logs -> /app/backend/logs
juhe-ai-runtime-log-data -> /app/backend/runtime-log-data
juhe-ai-table-monitor-data -> /app/backend/table-monitor-data
juhe-ai-audit-log-data -> /app/backend/audit-log-data
juhe-ai-operation-log-data -> /app/backend/operation-log-data
```

业务库、数据集目录库、使用记录目录库、统计库、usage shard、F1/F2/F3/F4 专用事实、自动生成的密钥和日志都会保留在 volume 中。生产环境不要执行 `docker compose down -v`。

## 验证

```bash
docker compose ps
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
docker compose logs --tail=100 go-gateway go-jobs
docker compose exec -T go-gateway /usr/local/bin/juhe-ai-go-project-healthcheck
docker compose exec -T go-jobs /usr/local/bin/juhe-ai-go-project-healthcheck
```

期望 `docker compose ps` 中 Node、`go-gateway` 与 `go-jobs` 均为 `healthy`；前两个接口返回 `200`、F3/F4 health probe 均返回 `204`，前端路径返回页面，并且两个 Go 项目日志能看见各自 owner 初始化或恢复记录。还必须通过 Node 只读 `/runtime-logs` 和 `/table-monitor` 接口确认 F1/F2 数据新鲜度，并分别用 F3 与 F4 输入 POST 及详情读回确认两个日志域；Node health 不能替代 Go 项目验证。

## 清理

只停止容器：

```bash
docker compose down
```

停止并删除本次 Docker 数据卷：

```bash
docker compose down -v
```

删除数据卷会清空数据库、日志和自动生成的密钥，只适合临时验证后清理。
