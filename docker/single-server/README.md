# 单机 Docker 部署（国内服务器，go-only）

国内单台服务器（Debian 13）上以 Docker Compose 运行全套 juhe-ai：PostgreSQL + Redis + gateway/jobs/maintenance（本地交叉编译的二进制）+ Caddy 入口。这是替代 K3s 混合形态的 go-only 目标部署形态；服务器地址、资产、密码与运行事实记录在 `.local/project-resources/prod/`（私有，不入库——BUG-0225）。

## 拓扑

```
:80 / :443 Caddy ──> gateway:3000（管理 SPA /__aisys__/ + OpenAI 兼容 /v1）
gateway ─┬─ postgres:5432（单库 juhe_ai，8 schema，见下）
         └─ redis:6379（DB0=cache，DB1=state，DB2=J3b circuit，namespace=prod）
jobs ─────┘（与 gateway 共用同一 PG/Redis/namespace）
maintenance：compose --profile tool 一次性容器（幂等 CLI）
```

- Redis `queue` 是 Node 时代残留概念，Go 代码不读 `JUHE_AI_REDIS_QUEUE_URL`，无需第三个实例。
- 8 个 PG schema：6 个由 `maintenance --ensure-schema` 建（business/usage/stats/chat/dataset/codex_context）；`juhe_jobs`、`juhe_j3b` 需手工 `CREATE SCHEMA AUTHORIZATION juhe_ai` 后由 maintenance `--apply-j3a-proxy-latency-postgres` / `--apply-j3b-model-check-postgres` 建表；J2 account_balance 四表按代码内 `balancePostgresSchema` 手工执行（完整序列见 `.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md`）。
- gateway 启动硬性要求 J3b 运行态索引 ready：新库必须先跑 `docker compose run --rm gateway -init-account-circuit-runtime-index`。
- 管理前端由 gateway 从镜像内 `/app/frontend/dist` 提供（必须显式 `JUHE_AI_FRONTEND_DIST_PATH`，默认空不挂 SPA）；根路径 `/` 由 Caddy 301 到 `/__aisys__/`。
- PG/Redis 不对宿主机发布端口。入口为 `https://aijh.huanmin.top`（Caddy ACME 自动续期，80 常驻 308 升级 HTTPS，443/udp HTTP/3）。
- 同机共存：聚合AI公益站（juhe-pw 栈，`gyai.huanmin.top`）以 external 方式加入本栈网络并复用本栈 PG/Redis/Caddy；`Caddyfile` 为两栈共享文件（含公益站反代块），划分与修改纪律见 `.local/project-resources/prod/assets/` 服务器资产台账「同机共存」节。

## 目录

```
docker/single-server/
├── compose.yml          # Compose 拓扑（本目录即 Compose 项目目录；gateway/jobs 必须 working_dir: /app/backend，见下）
├── Caddyfile            # 共享入口配置：aijh.huanmin.top（本项目）+ gyai.huanmin.top（同机公益站 juhe-pw 栈）；ACME 自动 HTTPS，:80 常驻 308
├── Dockerfile.runtime   # alpine + 预编译二进制 + 前端 dist（未设 WORKDIR，cwd 锚定靠 compose）
├── .env                 # 密钥、连接串与部署变量（gitignore；服务器同路径放置，见下".env 契约"）
└── build/               # 本机构建产物（gitignore）：bin/ + frontend-dist/
```

## 运行时数据目录契约（working_dir 必须锚定 /app/backend）

gateway/jobs 的全部"路径类"env 未配置时按 datadir 约定派生为 `<DATA_DIR>/<固定名>`，而 `JUHE_AI_DATA_DIR` 缺省是 `./data`——**相对进程 cwd**（gateway/jobs 的 `internal/datadir` 包注释即此约定）；文件日志等同族运行时路径同样相对 cwd。`Dockerfile.runtime` 只创建 `/app/backend/{data,logs}` 目录但**未设 `WORKDIR`**（Alpine 默认 cwd 为 `/`），因此：

- **compose 必须为 gateway/jobs 保持 `working_dir: /app/backend`**（2026-09-26 起，BUG-0193）。丢了这一行，`data/`、`logs/` 会解析到容器私有可写层 `/data`、`/logs`：用量记录、文件日志、chat-assets、account-health-input 等全部写进临时层，容器重建即静默丢失。
- **gateway↔jobs 同源要求**：用量记录经 `usage-record-spool` 文件目录交接（网关写 JSON 文件，jobs 的 usage spool drain 消费后写 PG `juhe_usage.usage_records`），两侧目录必须解析到**同一个宿主机路径**（当前形态 = `/app/backend/data/usage-record-spool`，挂载 `./data/app/data`）。任何一侧不一致，记录永不到库且无报错——统计全空。
- 非 compose 部署（systemd/裸进程直跑）同理：必须让进程 cwd 锚定到含 `data/` 的工作目录，或显式配置绝对路径（`JUHE_AI_DATA_DIR`，必要时 `JUHE_AI_USAGE_SPOOL_DIRECTORY`，现行名；旧名 `JUHE_AI_USAGE_SPOOL_DIR` 兼容回落）。
- 症状速查（BUG-0193）：统计页面全空但接口 200 → 先查 `SELECT count(*) FROM juhe_usage.usage_records;` 是否 0 行，再对比 gateway/jobs 两容器内 `data/usage-record-spool` 是否为同一挂载目录（`docker inspect` 挂载 + `docker exec ls` 实际内容）。


## 构建与部署（本机 Windows → 服务器）

> **红线：严禁在服务器上构建/打包**（Go 编译、前端 pnpm build 等一切编译型打包只能在本地 Windows 机执行）。
> 4C8G 服务器跑构建会直接把机器拖死（OOM/卡死，整机不可用）。服务器上只允许做两件轻量事：
> `docker compose build`（仅把本地产物的二进制/dst 组装进 alpine 镜像，无编译）和 `docker compose up -d`。

```sh
# 1. 本地构建（Go 交叉编译 + 前端）
for p in gateway jobs maintenance; do
  (cd backend-go/projects/$p && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o ../../../docker/single-server/build/bin/juhe-ai-$p ./cmd/juhe-ai-$p)
done
(cd frontend && pnpm build && rm -rf ../docker/single-server/build/frontend-dist && cp -r dist ../docker/single-server/build/frontend-dist)

# 2. 上传（服务器目录约定 /opt/juhe-ai；<PROD_SERVER_IP> = 生产服务器公网 IP，
#    见 .local/project-resources/prod/assets/，日常发布用 deploy.sh + JUHE_AI_DEPLOY_SERVER）
ssh root@<PROD_SERVER_IP> 'mkdir -p /opt/juhe-ai'
tar czf - -C docker/single-server compose.yml Caddyfile Dockerfile.runtime .env build \
  | ssh root@<PROD_SERVER_IP> 'tar xzf - -C /opt/juhe-ai && chmod 0755 /opt/juhe-ai/build/bin/*'

# 3. 服务器上构建镜像 + 初始化 + 启动（完整序列见运维手册）
cd /opt/juhe-ai
docker compose build          # BASE_IMAGE 已指向 docker.m.daocloud.io（BuildKit 解析 daemon 级 mirror 不可靠）
docker compose up -d postgres redis
# … maintenance 初始化与预置序列（见运维手册）…
docker compose up -d
```

> **非 root 容器（2026-09-28 起，BUG-0225）**：`Dockerfile.runtime` 以固定 UID/GID 1000 的非特权用户 `app` 运行，gateway/jobs/maintenance 三进程不再以 root 跑。compose 无需 `user:` 字段（镜像 `USER app` 已生效；bind mount 属主由宿主机目录决定，加 `user:` 也解决不了挂载目录写权限）。**首次启用的一次性前置**：宿主机挂载根授权 `chown -R 1000:1000 /opt/juhe-ai/data/app`（data 与 logs 两个子目录；回滚 = `chown -R root:root`），否则容器以 app 身份无权写 data/logs。caddy/PG/Redis 容器不受影响（镜像各自管理用户，caddy 仍需绑 80/443）。**发布、更新与回滚流程与命令均不变**。

## 更新发布

**推荐用发布脚本**（`docker/single-server/deploy.sh`，防呆流程）：

```sh
bash docker/single-server/deploy.sh all          # gateway + jobs 一同发布（默认）
bash docker/single-server/deploy.sh gateway      # 只发布 gateway / jobs / maintenance 同理
```

脚本固定执行：**无条件全量重编译**（不信任 `build/bin` 既有产物，防止 shared 模块修复后旧产物上线）→ 上传 → **md5 三点闭环校验**（本地新编译 = 服务器 build/bin = 容器内运行二进制）→ `docker compose build` + `up -d` → 逐容器等待 healthy → 公网健康检查。任一环节失败立即退出并给出回滚提示。

手动流程（等价于脚本内部步骤，仅排障时用）：构建（见上节命令）→ 上传 `build/` → `docker compose build gateway jobs maintenance` → `docker compose up -d`。maintenance 幂等，发布后跑一次 `--ensure-schema` 应用加法式 schema。回滚 = 上传上一个版本的 build/ 并重新 build+up。

## .env 契约（当前实例全集见服务器 /opt/juhe-ai/.env）

### Compose 与 deploy 脚本级必含变量（BUG-0225 实例事实变量化）

- 服务器 `/opt/juhe-ai/.env`：除下述业务变量外，必须包含 `POSTGRES_PASSWORD`、`REDIS_PASSWORD`（既有必填）与 **`JUHE_AI_PROXY_IP_A` / `JUHE_AI_PROXY_IP_B`**——出海代理隧道两条域名（`data.aijh.huanmin.top` / `egress.aijh.huanmin.top`）的公网 IP，注入 gateway/jobs/maintenance 三服务的 `extra_hosts`；缺失时 `docker compose config` 阶段即报错（`:?required` 模式，与 `POSTGRES_PASSWORD` 一致）。真实取值属实例事实，只在 `.local/project-resources/prod/` 私有留档。
- 本地发布机 `docker/single-server/.env`（gitignore，不入库）：须含 `JUHE_AI_DEPLOY_SERVER=<user>@<服务器IP>`——`deploy.sh` 的 ssh 目标，环境变量同名导出优先，`.env` 次之，均缺时脚本 fail-fast 并提示取值位置。

- `NODE_ENV=production`：触发全部生产校验（SECRET 强度、CORS 白名单必填、禁 dev 自动登录）。
- `JUHE_AI_SECRET`：≥32 位强随机；账号凭据/内建 API Key 加密封套，**有加密数据后不可更换**，务必留档。
- `JUHE_AI_DATABASE_DRIVER=postgres` + `JUHE_AI_POSTGRES_URL`；audit/operation/account-health 子系统缺省跟随主库驱动与主 URL。
- `JUHE_AI_RUNTIME_LOG_POSTGRES_URL` / `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`：**必须显式**（F1/F2 无主 URL 回退）。
- `JUHE_AI_LOG_DIR`（compose 已显式配 `/app/backend/logs`，对应宿主 `data/app/logs`）：运行日志文件三方同目录契约——gateway 文件日志写侧与 grep 扫描面、jobs F1 索引器与轮转文件保留清理都按它取目录；gateway 未配置时文件日志写侧与 grep 面整体禁用（启动事件 `runtime_log_file_sink_disabled`），jobs 未配置时会派生 `<DATA_DIR>/logs` 造成与 gateway 分叉，故两侧都必须显式同值。gateway 文件日志为 BUG-0195 修复（2026-09-28）：slog 同时写 stdout 与 `juhe-ai.log`（按大小轮转 `<base>.<YYYYMMDDTHHMMSSZ>.<uuid>.log`），`slog.Default()` 一并接管——HTTP 访问日志自此为 JSON 且进文件；文件清理由 jobs 保留清理负责（只删已完整索引的 rotated 文件）。
- `JUHE_AI_LOG_MAX_FILE_MB`（gateway 写侧轮转阈值，2026-09-28 随 BUG-0195 恢复——Node 既有 env，同名同默认同范围）：默认 `100`，合法 `1..1024`，越界启动失败；另有 `JUHE_AI_LOG_MAX_FILES`（默认 `500`）/ `JUHE_AI_LOG_RETENTION_DAYS`（默认 `30`，1..30）为轮转文件保留参数，由 jobs 索引器按此清理 rotated 文件（**jobs 长期停机期间 gateway 持续写不受 500×30 约束，目录会继续增长，需关注磁盘**）。注意区分：索引**行**保留是独立参数 `JUHE_AI_RUNTIME_LOG_RETENTION_DAYS`（默认 `14`，1..90，可被 system_settings `runtimeLogIndexRetentionDays` 覆盖），索引查询读窗默认最近 3 天——500×30 只约束文件，不约束索引行。
- `JUHE_AI_CHAT_*`（AI 问答子系统，gateway/jobs 双进程，越界均启动失败）：
  - `JUHE_AI_CHAT_RETENTION_DAYS`：默认 `3`，合法 `1..365`；gateway 读取保留窗口与 jobs retention 清理按它取值，**双进程必须同值**，否则清理窗口与读取保留不一致（消息先被读不到或超期残留）。
  - `JUHE_AI_CHAT_ASSETS_ROOT`：chat 资产对象根目录，未配置时按 `JUHE_AI_DATA_DIR`（缺省 `./data`）派生 `<数据根>/chat-assets`；gateway 资产写入与 jobs 资产清理**双进程必须同值**（同卷同目录）。
  - `JUHE_AI_CHAT_DATABASE_PATH`：仅 sqlite 模式生效的 chat 库文件（默认 `<数据根>/chat.sqlite3`），生产 PG 模式（`juhe_chat` schema 跟随主库）不配置。
  - `JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION`：单会话最大轮次，默认 `50`，合法 `1..1000`。
  - `JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER`：单用户最大会话数，默认 `50`，合法 `1..1000`。
  - `JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS`：单轮上游 SSE 事件预算，默认 `65536`，合法 `2048..262144`（调高只为容纳合法长输出，不解除单事件/累计内容等 DoS 防护）。
- `JUHE_AI_J3B_CIRCUIT_REDIS_URL`：**必须显式且不得与 `JUHE_AI_REDIS_STATE_URL` 相同键空间**（gateway 启动强校验；现用同实例 DB2）。
- `JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY`、`JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE=postgres`（+ INPUT_POSTGRES_URL）：PG 模式按运维手册显式化。
- `JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE`（默认 0.1）与 `JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS`（默认 1）：成功请求正文长期采样率与热保留窗口（失败/问题请求恒全量保留 7 天，成功正文长期保留 3 天）。当前生产显式配 `1`——审计开启即全量可见正文（BUG-0198）。
- `JUHE_AI_MAINTENANCE_J3A/J3B_POSTGRES_URL`：`--apply-*` 预置命令的 maintenance 专用 URL。
- `JUHE_AI_GO_RUNTIME_METRICS_*`（系统指标页 Go Runtime 采样，2026-09-27 起出厂默认开启）：`JUHE_AI_GO_RUNTIME_METRICS_STORE` 未配置/空时跟随 `JUHE_AI_DATABASE_DRIVER`——生产 PG 模式默认即 postgres，`JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL` 可省略（自动回退复用 `JUHE_AI_POSTGRES_URL`），通常无需显式配置；显式 `sqlite|postgres` 仍有效且优先于 driver 跟随，显式 `disabled` 关闭（读接口返回 `samplingEnabled=false`）。写入仍严格限定 `juhe_stats.go_runtime_metrics_samples` / `go_runtime_metrics_hourly` / `go_runtime_metrics_trend_windows` 三表（回退后两个 URL 都空才启动报错）。可选调参：`JUHE_AI_GO_RUNTIME_METRICS_INTERVAL`（默认 `15s`，下限 1s）、`JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS`（默认 `30`，1..3650）、`JUHE_AI_GO_RUNTIME_METRICS_SERVICE`（默认 `juhe-ai`）、`JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH`（仅 sqlite 模式生效；未配置时按 `JUHE_AI_DATA_DIR`（缺省 `./data`）派生为 `<数据根>/go-runtime-metrics.sqlite3`，生产 PG 模式用不到）。`JUHE_AI_GO_RUNTIME_METRICS_ROLE` **已删除、不得配置**（role 由进程身份固定：gateway/gateway、jobs/jobs）。顺序契约按存储分派：**postgres 模式先建表再启动**——新环境首次启动 gateway/jobs 前必须先用 maintenance `--check-go-runtime-metrics` / `--apply-go-runtime-metrics`（配 `--node-stopped --go-stopped --backup-confirmed`）建好三表（Go 启动只读校验 schema、缺表即启动失败并循环重启）；**sqlite 模式启动自举建表（幂等），无需预处理**。之后 `docker compose up -d gateway jobs`；已建表环境发布/重启无需任何额外 env。
- `JUHE_AI_ALLOWED_ORIGINS`：生产必填、逗号分隔、拒绝 `*`；当前为 `https://aijh.huanmin.top` 加 `http://<生产服务器公网 IP>`（实例值见服务器 `/opt/juhe-ai/.env` 与 `.local` 资产）。
- `JUHE_AI_COOKIE_SECURE=true`（HTTPS 已启用）；`JUHE_AI_TRUST_PROXY=true`（经 Caddy）。
- `JUHE_AI_REDIS_NAMESPACE=prod`：redis 驱动下必填非空。
- 管理后台账号沿用老生产 `system_accounts`（154 个），无默认密码残留；seed 的 `sys_admin`（admin/admin）仅在全新部署或重置 schema 后出现，且 `must_change_password=1` 首登强制改密（BUG-0224），现有实例行不被 seed 重跑改写。
