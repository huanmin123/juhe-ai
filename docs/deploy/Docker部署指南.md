# Docker 部署指南

> 本文只说明 Docker 单容器部署。还没确定入口场景时，先看 [服务器部署方案](scenarios/服务器部署方案.md) 或 [家庭宽带反向代理方案](scenarios/家庭宽带反向代理方案.md)。发布包部署见 `部署指南.md`。Linux、Windows、macOS 的 Docker Desktop / Docker Engine 差异见对应平台目录；公网 HTTPS 入口见 [Caddy 自动 HTTPS 部署指南](https/Caddy自动HTTPS部署指南.md)；状态检测和自动恢复见 [状态检测与自动恢复指南](watchdog/状态检测与自动恢复指南.md)；上游网络代理见 [sing-box 网络代理部署指南](proxy/sing-box网络代理部署指南.md)。

## 1. 适用方式

Docker 镜像使用当前 `backend/dist`、`frontend/dist` 和后端生产依赖。容器启动后由同一个 Node 主进程承载：

- 管理后台：`/__aisys__/`
- 系统 API：`/__aisys__/api`
- OpenAI 兼容网关：`/v1`
- background worker 和本地 DB service 子进程

默认 standalone 模式不需要 Nginx、Redis、PostgreSQL 或额外 worker 容器。如果需要 PostgreSQL + Redis 高性能模式，使用 `docker/compose.performance.yml`，并先阅读 [高性能模式部署指南](高性能模式部署指南.md)。

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
- `JUHE_AI_SECRET` 留空时，容器首次启动会在数据卷里生成 `/app/backend/data/.juhe-ai-secret` 并复用。
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

## 7. 自动恢复

Compose `restart: unless-stopped` 只能在容器进程退出时恢复；Docker healthcheck 标记 `unhealthy` 不一定会自动重启容器。长期运行建议再配置宿主机 watchdog，连续检查本机 `/__aisys__/health` 和 `/__aisys__/api/health`，达到阈值后重启容器，并设置冷却和窗口限频。完整策略见 [状态检测与自动恢复指南](watchdog/状态检测与自动恢复指南.md)。

宿主机 watchdog 重启目标通常是：

```bash
cd /opt/juhe-ai/docker
docker compose restart juhe-ai
```

高性能模式是：

```bash
cd /opt/juhe-ai/docker
docker compose --env-file .env.performance -f compose.performance.yml restart juhe-ai
```

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
