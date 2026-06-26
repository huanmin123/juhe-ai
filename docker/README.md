# Docker 部署

这是轻量 Docker 入口。默认单容器运行 Web、管理 API、`/v1` 网关、background worker 和 DB service，不需要额外 Nginx、Redis、PostgreSQL 或独立 worker 容器。高性能模式使用 `compose.performance.yml` 部署 PostgreSQL、PgBouncer、Redis cache、Redis state 和应用服务，并默认复用 Redis state 承接 Redis Streams 队列。

## 文件说明

- `compose.yml`：单容器 Compose 配置，包含端口、环境变量、数据卷和镜像构建参数。
- `compose.performance.yml`：高性能模式 Compose 配置，包含 PostgreSQL、PgBouncer、Redis cache、Redis state、Redis Streams 队列配置和应用服务。
- `Dockerfile`：运行镜像构建文件，只组装已构建好的 `backend/dist`、`frontend/dist` 和后端生产依赖。
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
pnpm --filter juhe-ai-backend postgres:init-schema-only
pnpm --filter juhe-ai-backend postgres:init-schema
```

如果命令在 Docker 宿主机执行，需要把 `JUHE_AI_POSTGRES_URL`、`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL` 和显式配置时的 `JUHE_AI_REDIS_QUEUE_URL` 临时改为宿主机发布端口；详细命令见 `docs/deploy/高性能模式部署指南.md`。应用容器内使用 `pgbouncer:5432`、`redis-cache:6379`、`redis-state:6379`，宿主机验证默认使用 PgBouncer `6432`、redis-cache `6379`、redis-state `6380`，不要把 redis-state 的容器内 `6379` 误当宿主机端口。

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

Compose 默认创建两个 Docker volume：

```text
juhe-ai-data -> /app/backend/data
juhe-ai-logs -> /app/backend/logs
```

业务库、数据集目录库、使用记录目录库、统计库、usage shard、自动生成的密钥和日志都会保留在 volume 中。生产环境不要执行 `docker compose down -v`。

## 验证

```bash
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 juhe-ai
```

期望前两个接口返回 `200`，前端路径返回页面，并且日志里能看到主服务、DB service 和 background worker 启动记录。

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
