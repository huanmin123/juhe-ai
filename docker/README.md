# Docker 部署

这是轻量 Docker 入口。Compose 拓扑为 go-only 终态（X01/X03）：`gateway`（唯一 HTTP 入口，`JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true`，承载 Web/管理 API、公开协议面、`/v1` 网关链与 F3/F4 owner）与 `jobs`（F1/F2 owner）。Node backend 已物理归档（X02），Node 容器、`docker/Dockerfile`、`Dockerfile.builder` 与 `entrypoint.sh` 已删除；原 `compose.go-only.yml` 已并入 `compose.yml`。

高性能模式使用 `compose.performance.yml` 部署 PostgreSQL、PgBouncer、Redis cache、Redis state、独立 Redis queue；该文件是 hybrid 遗留形态的参考，其 `juhe-ai` Node 服务在当前仓库不可构建，go-only 高性能变体为 X03 待办。

## 文件说明

- `compose.yml`：go-only Compose 配置，包含端口、环境变量、数据卷和 Go 镜像构建参数。
- `compose.performance.yml`：高性能模式遗留参考配置（未收口，不得用于 `--build` 启动）。
- `Dockerfile.go-project`：通用 Go 项目镜像，通过 `GO_PROJECT=gateway|jobs` 分别构建独立模块，并内置项目 `/health` probe。
- `.env.example`：go-only 必填配置模板。首次启动必须复制为 `.env` 并填写 F3/F4 input secret；不能直接使用默认 Compose 配置。
- `.env.performance.example`：高性能模式必填配置模板（遗留）。

## 构建与启动

`gateway` 与 `jobs` 在镜像构建时编译，不需要预先生成 Go 二进制或前端产物。受限网络可在 `.env` 设置 `JUHE_AI_GO_PROXY=https://goproxy.cn,direct`；默认仍使用官方 Go proxy。

```bash
cd docker
cp .env.example .env
# 在 .env 填写 Harbor digest 镜像引用，以及稳定的 JUHE_AI_AUDIT_LOG_INPUT_SECRET 与 JUHE_AI_OPERATION_LOG_INPUT_SECRET；production 至少 32 位，不能使用 JUHE_AI_SECRET。
docker compose config --quiet
docker compose up -d --build --wait
```

浏览器访问：

```text
http://localhost:3000/__aisys__/
```

公网访问时，把 `localhost` 换成服务器 IP 或域名，并建议在 `.env` 中配置 `JUHE_AI_PUBLIC_ORIGIN`。

## 镜像来源

所有 Compose 基础镜像与中间件镜像必须是 Harbor 内网的 `@sha256:` 不可变引用。先在 `infra-linux` 运行 k8s 仓库的 `platform/harbor/sync-base-images.sh`，再将生成的 `harbor-base-images` 中对应 `JUHE_AI_*_IMAGE` 值写入 `.env`。缺少任一构建基础镜像时，Compose 会失败；不会回退到 Docker Hub、镜像加速器或其他外网 Registry。

## 独立 Go 项目

F1/F2 owner ID 属于 jobs，F3/F4 owner ID 属于 gateway；两项目独立重启和健康检查，`jobs` 在 `gateway` 健康（`service_healthy`）后启动。`JUHE_AI_RUNTIME_LOG_ONCE=false` 保持 F1 常驻扫描；F2 按 interval、lease 和 retention 采样；F3/F4 使用各自独立输入密钥和 loopback 输入端点。

go-only 卷语义：`juhe-ai-data` 对 gateway 读写（system API 是业务库唯一 writer），对 jobs 只读；runtime-log/table-monitor 源卷对 gateway 只读（F3/F4 SQLite 隔离预检用），对 jobs 读写。F1/F2/F3/F4 的 SQLite 路径仍必须互相物理隔离，各 owner 对自己的输出维持 `WAL` 与 `busy_timeout=5000`。任何项目容器退出仅由 Compose restart policy 重启该项目。

F3/F4 listener 只允许 loopback；gateway 自己就是主入口，因此不需要任何共享 network namespace。SQLite 部署人工预检还必须确认 usage shard root 不与任一 F1/F2/F3/F4 输出或源库重叠。

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
- `JUHE_AI_SECRET` 是业务库敏感字段（OAuth token、上游 API Key、代理密码等）的加密密钥，Docker go-only 下必须显式配置；迁移旧数据时必须填写旧密钥，否则敏感字段无法解密。
- `JUHE_AI_AUDIT_LOG_INPUT_SECRET` 必须显式填写为独立、稳定的高熵值；不能复用或回退 `JUHE_AI_SECRET`，production 至少 32 位。它同时提供给 gateway 内 F3 loopback HMAC 输入端点。
- `JUHE_AI_OPERATION_LOG_INPUT_SECRET` 必须显式填写为独立、稳定的高熵值；不能复用、回退或与 F3 secret 共用，production 至少 32 位。它同时提供给 gateway 内 F4 loopback HMAC 输入端点。
- 直接 HTTP 访问时保持 `JUHE_AI_COOKIE_SECURE=false` 和 `JUHE_AI_TRUST_PROXY=false`；HTTPS 反向代理后改为 `true`，并确保容器端口只被 Caddy 或可信入口访问。
- `JUHE_AI_GO_IMAGE` 与两个 `JUHE_AI_GO_*_RUNTIME_IMAGE` 必须是 Harbor 内网的不可变 digest 引用；不得填 Docker Hub 或镜像加速器地址。

## 数据持久化

Compose 默认创建以下 Docker volume：

```text
juhe-ai-data -> /app/backend/data
juhe-ai-logs -> /app/backend/logs
juhe-ai-runtime-log-data -> /app/backend/runtime-log-data
juhe-ai-table-monitor-data -> /app/backend/table-monitor-data
juhe-ai-audit-log-data -> /app/backend/audit-log-data
juhe-ai-operation-log-data -> /app/backend/operation-log-data
juhe-ai-account-health-data -> /app/backend/account-health-data
juhe-ai-account-health-inputs -> /app/backend/account-health-inputs
juhe-ai-go-runtime-metrics-data -> /app/backend/go-runtime-metrics-data
```

业务库、数据集目录库、使用记录目录库、统计库、usage shard、F1/F2/F3/F4 专用事实、自动生成的密钥和日志都会保留在 volume 中。生产环境不要执行 `docker compose down -v`。

## 验证

```bash
docker compose ps
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
docker compose logs --tail=100 gateway jobs
docker compose exec -T gateway /usr/local/bin/juhe-ai-go-project-healthcheck
docker compose exec -T jobs /usr/local/bin/juhe-ai-go-project-healthcheck
```

期望 `docker compose ps` 中 `gateway` 与 `jobs` 均为 `healthy`、健康接口返回 `200`、F3/F4 health probe 均返回 `204`，前端路径返回页面，并且两个 Go 项目日志能看见各自 owner 初始化或恢复记录。还必须通过 `/runtime-logs` 和 `/table-monitor` 接口确认 F1/F2 数据新鲜度，并分别用 F3 与 F4 输入 POST 及详情读回确认两个日志域。

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
