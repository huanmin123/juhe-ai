# juhe-ai 内网生产演练部署（单机 Docker 变体）

> 本目录是内网单机**生产演练**形态：按生产安全语义运行（`NODE_ENV=production`、验证码开启、首登强制改密门禁开启），但**不是生产实例**，不参与生产发布；生产部署以 [`../single-server/README.md`](../single-server/README.md) 为准。
>
> 唯一生产语义例外是内网 HTTP：显式 `JUHE_AI_COOKIE_SECURE=false`；配置 HTTPS 后必须改为 `true`。

## 形态

在内网生产演练服务器（实例地址见 `.local/project-resources/dev/`）上以 Docker Compose 运行：

- `gateway`：管理面 `/__aisys__/`、OpenAI 兼容 `/v1`；只发布 `127.0.0.1:13000`。
- `jobs`：后台任务与探针/统计/retention。
- `maintenance`：一次性 CLI（`profiles: ["tool"]`）。
- **不启动** PostgreSQL、Redis、Caddy：复用服务器的 `pg16` 容器与共享 Redis，入口由宿主 Nginx 8193 承担。
- 容器加入 external 网络 `infra_default`（访问 `pg16`）与 `monitoring_default`（为后续 Prometheus 抓取预留；当前未配置抓取）。

## 前置

- 服务器已存在 `pg16` 容器与 `infra_default` 网络；`monitoring` 栈与 `monitoring_default` 网络存在。
- 已按 `.local/project-resources/dev/runbooks/初始化与验证.md` 创建 `juhe_ai_dev` 角色/库，并扩容 `pg16 max_connections=200`。
- 本机构建机 Go 1.26.x（`backend-go/go.work`）、Node.js 22+、pnpm。
- 共享 Redis 的 namespace 为 `dev`；禁止 `FLUSHDB`/`FLUSHALL`。
- 服务器 `.env` 必须使用生产演练模板：`NODE_ENV=production`、验证码开启、强制改密门禁开启；不得配置 dev 自动登录。

## 配置

复制 [`.env.example`](.env.example) 为服务器 `/opt/juhe-ai-dev/.env`（600），至少确认：

| 变量 | 说明 |
| --- | --- |
| `NODE_ENV` | 固定 `production`；启用强 secret、CORS 白名单、拒绝 dev 自动登录等生产门禁 |
| `JUHE_AI_SECRET` | ≥32 位稳定随机串；有加密数据后不可更换 |
| `JUHE_AI_POSTGRES_URL` | 主库连接串；`RUNTIME_LOG` 与 `ACCOUNT_HEALTH_INPUT` 指向同一库 |
| `JUHE_AI_REDIS_URL` | 单实例连接串；密码含 `@` 时 percent-encode |
| `JUHE_AI_REDIS_NAMESPACE` | 独立 namespace（当前 `dev`），不得复用生产 namespace |
| `JUHE_AI_ALLOWED_ORIGINS` | 如 `http://<演练服务器IP>:8193,http://127.0.0.1:8193`；production 下必填且不得为 `*` |
| `JUHE_AI_COOKIE_SECURE` | 内网 HTTP 为 `false`（唯一例外）；上 HTTPS 后必须改 `true` |
| `JUHE_AI_TRUST_PROXY` | 经 Nginx 时为 `true` |
| `JUHE_AI_AUTH_CAPTCHA_DISABLED` | 固定 `false`（验证码开启） |
| `JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED` | 固定 `false`（强制改密门禁开启）；production 下 `true` 会启动失败 |
| `JUHE_AI_DEV_AUTO_LOGIN_USERNAME` | 不得配置；production 下设置会启动失败 |

- 服务器 `.env` 是运行事实，本地权威副本在 `.local/project-resources/dev/env/server.env`；不要把服务器 `.env` 复制回仓库。
- 生产专属变量（`POSTGRES_PASSWORD`、`REDIS_PASSWORD`、Caddy 域名等）不属于本形态。

## 构建与上传

```sh
# 仓库根；构建产物落在 docker/single-server/build/
VERSION=dev-$(git rev-parse --short HEAD)
COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mkdir -p docker/single-server/build/bin

for p in gateway jobs maintenance; do
  (cd backend-go/projects/$p && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags "-s -w \
      -X github.com/huanminabc/juhe-ai/backend-go-platform/version.Version=$VERSION \
      -X github.com/huanminabc/juhe-ai/backend-go-platform/version.Commit=$COMMIT \
      -X github.com/huanminabc/juhe-ai/backend-go-platform/version.BuildTime=$BUILD_TIME" \
    -o ../../../docker/single-server/build/bin/juhe-ai-$p ./cmd/juhe-ai-$p)
done

(cd frontend && pnpm install --frozen-lockfile && pnpm build)
rm -rf docker/single-server/build/frontend-dist
cp -r frontend/dist docker/single-server/build/frontend-dist
```

上传（`.env` 从私有目录复制，权限 600）：

```sh
ssh root@<演练服务器IP> 'mkdir -p /opt/juhe-ai-dev/{build,data/app/data,data/app/logs,backup}'
scp docker/dev-server/compose.yml root@<演练服务器IP>:/opt/juhe-ai-dev/compose.yml
scp docker/dev-server/.dockerignore root@<演练服务器IP>:/opt/juhe-ai-dev/.dockerignore
scp docker/single-server/Dockerfile.runtime root@<演练服务器IP>:/opt/juhe-ai-dev/Dockerfile.runtime
scp -r docker/single-server/build/bin docker/single-server/build/frontend-dist \
  root@<演练服务器IP>:/opt/juhe-ai-dev/build/
scp .local/project-resources/dev/env/server.env root@<演练服务器IP>:/opt/juhe-ai-dev/.env
ssh root@<演练服务器IP> 'chmod 600 /opt/juhe-ai-dev/.env; chown -R 1000:1000 /opt/juhe-ai-dev/data/app'
```

## 首次启动

```sh
cd /opt/juhe-ai-dev
docker compose --profile tool build
set -a && . ./.env && set +a

docker compose --profile tool run --rm maintenance upgrade \
  --dsn "$JUHE_AI_POSTGRES_URL" --secret "$JUHE_AI_SECRET" \
  --stop-write-confirmed --backup-confirmed </dev/null

docker compose run --rm gateway -init-account-circuit-runtime-index
docker compose up -d gateway jobs
docker compose ps
```

> jobs 已配置 `depends_on: gateway: condition: service_healthy`：全新库会等 gateway 建好 `juhe_dataset.operation_logs` 后再启动。若手工拆分启动，先起 gateway 等 healthy，再起 jobs。

## 更新

```sh
# 本机重新构建并上传 build/；不要覆盖服务器 .env
cd /opt/juhe-ai-dev
set -a && . ./.env && set +a
docker compose --profile tool run --rm maintenance upgrade --dry-run --dsn "$JUHE_AI_POSTGRES_URL" </dev/null
docker compose build gateway jobs
docker compose up -d gateway jobs
```

- 涉及 schema 变更：先停写、确认当日自动备份成功（或手工执行备份）、`upgrade`，再启动新版；顺序与生产一致。
- `docker/single-server/deploy.sh` 绑定生产服务器和公网健康检查，**禁止用于本环境**。

## 回滚

- 纯二进制回退：上传旧 `build/`，`docker compose build gateway jobs && docker compose up -d gateway jobs`。
- 涉及 schema 回退：先按 §备份与恢复恢复升级前备份，再回退镜像；旧二进制被 schema revision 门禁拒绝时不得跳过。
- 回退后重新执行 §入口与验证；不得只换二进制而保留不兼容的 schema 或数据。

## 入口与验证

- Nginx 配置模板：[nginx/juhe-ai-dev.conf.example](nginx/juhe-ai-dev.conf.example)。
- 管理页：`http://<演练服务器IP>:8193/__aisys__/`。
- 健康：`curl http://127.0.0.1:13000/__aisys__/health`；验证码接口应返回 `required:true`，无验证码登录应返回 400。
- `/v1/models` 无 Key 应返回 401；seed 管理员登录名已轮换为 `sys-juhe-ai`（口令见私有凭据索引），当前 `must_change_password=0`，登录仍需验证码。
- production 下 `JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED=true` 会拒绝启动；不得通过关闭门禁或验证码绕过登录安全流程。

## 备份与恢复

- 每日 **03:40** 由 `/etc/cron.d/juhe-ai-dev-backup` 执行 `/opt/juhe-ai-dev/backup/backup-juhe-ai-dev.sh`。
- 备份对象：`juhe_ai_dev`（部署实例库）；格式 `pg_dump -Fc`；每次先写临时文件，`pg_restore -l` 校验通过后才原子改名。
- 产物：`/opt/juhe-ai-dev/backup/juhe_ai_dev-YYYYmmdd-HHMMSS.dump`，权限 600；保留最近 7 份；日志 `backup.log`（600）。
- 手工执行与校验：

```sh
ssh root@<演练服务器IP> '/opt/juhe-ai-dev/backup/backup-juhe-ai-dev.sh'
ssh root@<演练服务器IP> 'tail -5 /opt/juhe-ai-dev/backup/backup.log; ls -lh /opt/juhe-ai-dev/backup/juhe_ai_dev-*.dump'
```

- 2026-11-05 已做一次真实恢复演练：把最新备份 `pg_restore` 到临时库，退出 0，关键业务表非空，随后删除临时库。
- 恢复流程（停写、重建库、`pg_restore`、启动、账本核对）见 `.local/project-resources/dev/runbooks/数据库生命周期.md`；恢复前必须确认目标库和备份文件，禁止对生产实例套用。
- **限制**：当前备份与数据库在同一台服务器，不能抵御宿主磁盘/整机丢失；异机或对象存储灾备尚未接入，见 `docs/待办与阻塞事项.md`。Redis 是运行态，不承担恢复点职责；`.env` 的权威副本在 `.local/project-resources/dev/env/server.env`。

## 监控

核心应用指标已接入现有 Prometheus/Grafana（2026-11-05）：

- Prometheus 配置 `/opt/infra/monitoring/prometheus/prometheus.yml` 增加 `juhe-ai-gateway` / `juhe-ai-jobs` 两个 job，60s 抓取 `/__aisys__/metrics`。
- 本演练 compose 不把 health/metrics 端口发布到宿主；Prometheus 与 gateway/jobs 同在 `monitoring_default` 网络，直接按容器名 `juhe-ai-go-gateway:3306` / `juhe-ai-go-jobs:3305` 抓取，不需要 SSH 隧道。
- Grafana 面板已复制为 `/opt/infra/monitoring/grafana/dashboards/juhe-ai-app-overview.json`，标题「juhe-ai 应用总览（单机部署）」，UID `juhe-ai-app-overview`，provisioning 自动加载。
- 验证：Prometheus Targets 两个 job 均 `up=1`；Grafana 面板能查到 `juhe_ai_http_requests_total` / `juhe_ai_jobs_task_runs_total`；真实 `/v1` 请求后计数增长。
- 可选 `postgres_exporter`、`node_exporter`、`cadvisor`、`redis_exporter`、Loki/Alloy 尚未接入；应用总览“存储”行、主机/PG/Redis/日志面板为空，见 `docs/待办与阻塞事项.md` T156 和 `.local/project-resources/dev/monitoring/README.md`。接入时不能直接照搬 collector 默认的 `juhe-ai-postgres` / `juhe-ai-redis` 容器名（本实例为 `pg16` 与共享 Redis）。

## 边界

- 只允许 `<内网段>` 访问；不配置公网域名/HTTPS，不加入生产反代。HTTP 与 `COOKIE_SECURE=false` 是唯一的生产语义例外，若未来暴露到不可信网络必须先上 HTTPS。
- 本环境是生产演练，不等同生产实例；不得用演练结果替代生产发布验证，也不得对外宣称生产已验证。
- 不修改 aiagent 的 PG/Redis 业务库；juhe-ai 只用 `juhe_ai_dev` / `juhe_ai_dev_local`。
- 不把服务器 `.env`、`.local` 内容复制回仓库或公开文档。
