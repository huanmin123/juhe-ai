# Docker 部署指南

> 本文只说明 Docker 单容器部署。发布包部署见 `部署指南.md`。

## 1. 适用方式

Docker 镜像使用当前 `backend/dist`、`frontend/dist` 和后端生产依赖。容器启动后由同一个 Node 主进程承载：

- 管理后台：`/__aisys__/`
- 系统 API：`/__aisys__/api`
- OpenAI 兼容网关：`/v1`
- background worker 和本地 DB service 子进程

默认不需要 Nginx、Redis、PostgreSQL 或额外 worker 容器。

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

## 4. 配置

不复制配置文件也能启动。需要改端口、公网访问地址或复用旧数据时，再复制：

```powershell
Copy-Item .env.example .env
```

Linux / macOS：

```bash
cp .env.example .env
```

常用配置：

```env
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=http://你的服务器IP:3000
JUHE_AI_SECRET=
JUHE_AI_COOKIE_SECURE=false
```

说明：

- `JUHE_AI_SECRET` 留空时，容器首次启动会在数据卷里生成 `/app/backend/data/.juhe-ai-secret` 并复用。
- 迁移已有业务库时必须填写原来的 `JUHE_AI_SECRET`。
- 直接 HTTP 访问时 `JUHE_AI_COOKIE_SECURE=false`；HTTPS 反向代理后建议改为 `true`。
- 公网 IP 或域名访问时建议填写 `JUHE_AI_PUBLIC_ORIGIN`，例如 `http://你的服务器IP:3000` 或 `https://ai.example.com`。

## 5. 持久化

Compose 默认创建两个 Docker volume：

```text
juhe-ai-data -> /app/backend/data
juhe-ai-logs -> /app/backend/logs
```

业务库、数据集目录库、统计库、usage shard、自动生成的密钥和日志都会跟随 volume 保留。不要在生产环境执行 `docker compose down -v`，否则会删除这些数据。

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

## 7. 停止和清理

停止容器：

```bash
docker compose down
```

停止并删除本次 volume：

```bash
docker compose down -v
```

`down -v` 只适合临时验证后清理，生产环境不要执行。
