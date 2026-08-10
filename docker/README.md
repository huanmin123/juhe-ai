# Docker 部署

这是轻量 Docker 入口。默认模式的 Compose 拓扑为 Node（Web、管理 API、`/v1` 网关、background worker、DB service）加 F1 `runtime-log-indexer`、F2 `table-monitor`、F3 `audit-log-writer` 三个独立 Go sidecar；它们不经 Node owner switch、bridge 或队列。高性能模式使用 `compose.performance.yml` 部署 PostgreSQL、PgBouncer、Redis cache、Redis state、独立 Redis queue、Node 应用和同样三个 Go sidecar；Redis Streams 可靠队列默认写入 `redis-queue`，但 F1/F2/F3 不使用 Redis 数据面。

> 2026-08-10 已在开发 Linux 服务器用固定 release 构建完成 standalone 与 performance Compose runtime 闭环：三类 Go sidecar、Node/F3 输入与 Node 只读回读、F1/F2 快照均通过，临时容器/卷已清理。该证据不构成 macOS 或生产部署已完成的声明；生产候选仍必须从干净、固定 commit 的 release 构建。

## 文件说明

- `compose.yml`：单容器 Compose 配置，包含端口、环境变量、数据卷和镜像构建参数。
- `compose.performance.yml`：高性能模式 Compose 配置，包含 PostgreSQL、PgBouncer、Redis cache、Redis state、独立 Redis queue 和应用服务。
- `Dockerfile`：运行镜像构建文件，只组装已构建好的 `backend/dist`、`frontend/dist` 和后端生产依赖。
- `Dockerfile.runtime-log-indexer`：F1 Go 运行日志索引 sidecar 镜像，直接编译 `backend-go/cmd/juhe-ai-runtime-log-indexer`。
- `Dockerfile.table-monitor`：F2 Go 表监控 sidecar 镜像，直接编译 `backend-go/cmd/juhe-ai-table-monitor`。
- `Dockerfile.audit-log-writer`：F3 Go 原始审计 writer sidecar 镜像，直接编译 `backend-go/cmd/juhe-ai-audit-log-writer`，并内置 loopback HTTP `204` health probe。
- `entrypoint.sh`：容器启动入口，设置默认环境变量、创建数据目录、生成或复用 `JUHE_AI_SECRET`，并执行 SQLite 运行时预检。
- `.env.example`：可选配置模板。不复制也能使用默认值启动；需要改端口、公网地址、密钥或镜像名时复制为 `.env`。
- `.env.performance.example`：高性能模式配置模板，复制为 `.env.performance` 后必须修改数据库、Redis 和应用密钥。

## 构建产物

先在项目根目录生成前后端产物：

```bash
pnpm install
pnpm build
```

Docker 镜像使用当前 `backend/dist` 和 `frontend/dist`，不会在服务器镜像构建阶段重新跑前端构建。如果缺少必要产物，`docker compose up -d --build` 会直接提示缺失文件，并要求先执行上面的构建命令。

F1、F2、F3 sidecar 都在镜像构建时独立编译 Go 程序，不需要预先生成 Go 二进制。受限网络可在 `.env` / `.env.performance` 设置 `JUHE_AI_GO_PROXY=https://goproxy.cn,direct`；默认仍使用官方 Go proxy。

## 启动

```bash
cd docker
docker compose up -d --build
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
# 填写 JUHE_AI_SECRET、PostgreSQL / Redis 密码、对应 URL 和访问 Origin
docker compose --env-file .env.performance -f compose.performance.yml up -d --build
```

当前 PostgreSQL repository adapter 未整体完成；未迁移入口会在 performance 模式下 fail-fast，避免误回退到 SQLite。PostgreSQL、PgBouncer、Redis、schema 初始化和已迁移链路按 `docs/deploy/高性能模式部署指南.md` 验证。

中间件启动后，在项目根目录执行：

```bash
pnpm --filter juhe-ai-backend postgres:init-schema
```

空库或可重建测试库只执行 `postgres:init-schema`；`postgres:init-schema-only` 只用于当前版本 DDL 复查，不作为常规初始化或旧库补结构步骤。

如果命令在 Docker 宿主机执行，需要把 `JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL` 和 `JUHE_AI_REDIS_QUEUE_URL` 临时改为宿主机发布端口；详细命令见 `docs/deploy/高性能模式部署指南.md`。应用容器内使用 `pgbouncer:5432`、`redis-cache:6379`、`redis-state:6379`、`redis-queue:6379`，宿主机验证默认使用 PgBouncer `6432`、redis-cache `6379`、redis-state `6380`、redis-queue `6381`，不要把 Redis 容器内 `6379` 误当宿主机端口。

## F1 / F2 Go sidecar

两个 Compose 文件都会启动 `runtime-log-indexer`、`table-monitor` 与 `audit-log-writer`。F1 由 `JUHE_AI_RUNTIME_LOG_INSTANCE_ID` 标识，F2 由 `JUHE_AI_TABLE_MONITOR_INSTANCE_ID` 标识，F3 由 `JUHE_AI_AUDIT_LOG_INSTANCE_ID` 标识；多实例部署必须为各自事实库使用稳定且唯一的值。三者只有在应用 `/__aisys__/api/health` 确认 DB service 就绪后才启动。`JUHE_AI_RUNTIME_LOG_ONCE=false` 是 F1 默认常驻扫描模式，不能改为默认一次性任务；F2 按 interval、lease 和 retention 参数常驻采样；F3 使用独立输入密钥和 loopback 输入端点。

standalone 模式将 `juhe-ai-data` 与 `juhe-ai-logs` 同时挂载给 F1，并为 F1/F2 提供独立输出 volume；两者显式接收对方输出路径和所有 Node SQLite owner 路径，以便拒绝物理文件复用。各自只写自己的 volume，Node 只读其产物。F1/F2 对 SQLite 输出强制 `WAL` 与 `busy_timeout=5000`。performance 模式使用 `JUHE_AI_POSTGRES_URL`：F1 写其日志 schema，F2 写 `juhe_stats`；两者等待 PgBouncer 健康和 Node `/__aisys__/api/health` 的 DB-service 就绪，不依赖 Redis 数据面。

F1 需要 `JUHE_AI_RUNTIME_LOG_INSTANCE_ID`、store、对应 SQLite 路径或 PG URL、`JUHE_AI_LOG_DIR`；F2 需要 `JUHE_AI_TABLE_MONITOR_INSTANCE_ID`、store、对应 SQLite 输出路径或 PG URL；F3 需要稳定的 `JUHE_AI_AUDIT_LOG_INSTANCE_ID`、`JUHE_AI_AUDIT_LOG_INPUT_SECRET`、`JUHE_AI_AUDIT_LOG_INPUT_URL`、独立 SQLite/PG 输出配置、blob 目录和 hot-search 目录。Docker 中 F3 与 Node 使用 `network_mode: service:juhe-ai` 共享 loopback namespace，不能改成仅监听容器网卡。SQLite 部署人工预检还必须确认 usage shard root 不与任一 F1/F2/F3 输出或源库重叠：Go 当前未实现该 root 的启动校验，此项应作为未来 usage 迁移门禁，不能写成已完成的部署验证。

## 按需配置

复制示例配置：

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
```

业务库、数据集目录库、使用记录目录库、统计库、usage shard、F1/F2/F3 专用事实、自动生成的密钥和日志都会保留在 volume 中。生产环境不要执行 `docker compose down -v`。

## 验证

```bash
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
docker compose logs --tail=100 runtime-log-indexer
docker compose logs --tail=100 table-monitor
docker compose logs --tail=100 audit-log-writer
docker compose exec -T audit-log-writer /usr/local/bin/juhe-ai-audit-log-healthcheck
```

期望前两个接口返回 `200`，F3 health probe 返回 `204`，前端路径返回页面，并且日志里能看到主服务、DB service、background worker、F1、F2 与 F3 启动记录。还必须通过 Node 只读 `/runtime-logs` 和 `/table-monitor` 接口确认 F1/F2 数据新鲜度，并用 F3 输入 POST 与审计详情读回确认 F3；Node health 不能替代任一 sidecar 验证。

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
