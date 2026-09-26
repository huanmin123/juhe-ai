# 单机 Docker 部署（国内服务器，go-only）

国内单台服务器（103.36.63.105，Debian 13）上以 Docker Compose 运行全套 juhe-ai：PostgreSQL + Redis + gateway/jobs/maintenance（本地交叉编译的二进制）+ Caddy 入口。这是替代 K3s 混合形态的 go-only 目标部署形态；服务器资产、密码与运行事实记录在 `.local/project-resources/prod/`（私有）。

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
- 同机共存：聚合AI公益站（juhe-pw 栈，`gyai.huanmin.top`）以 external 方式加入本栈网络并复用本栈 PG/Redis/Caddy；`Caddyfile` 为两栈共享文件（含公益站反代块），划分与修改纪律见 `.local/project-resources/prod/assets/国内单机-103.36.63.105.md`「同机共存」节。

## 目录

```
docker/single-server/
├── compose.yml          # Compose 拓扑（本目录即 Compose 项目目录；gateway/jobs 必须 working_dir: /app/backend，见下）
├── Caddyfile            # 共享入口配置：aijh.huanmin.top（本项目）+ gyai.huanmin.top（同机公益站 juhe-pw 栈）；ACME 自动 HTTPS，:80 常驻 308
├── Dockerfile.runtime   # alpine + 预编译二进制 + 前端 dist（未设 WORKDIR，cwd 锚定靠 compose）
├── .env                 # 密钥与连接串（gitignore；服务器同路径放置）
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

# 2. 上传（服务器目录约定 /opt/juhe-ai）
ssh root@103.36.63.105 'mkdir -p /opt/juhe-ai'
tar czf - -C docker/single-server compose.yml Caddyfile Dockerfile.runtime .env build \
  | ssh root@103.36.63.105 'tar xzf - -C /opt/juhe-ai && chmod 0755 /opt/juhe-ai/build/bin/*'

# 3. 服务器上构建镜像 + 初始化 + 启动（完整序列见运维手册）
cd /opt/juhe-ai
docker compose build          # BASE_IMAGE 已指向 docker.m.daocloud.io（BuildKit 解析 daemon 级 mirror 不可靠）
docker compose up -d postgres redis
# … maintenance 初始化与预置序列（见运维手册）…
docker compose up -d
```

## 更新发布

**推荐用发布脚本**（`docker/single-server/deploy.sh`，防呆流程）：

```sh
bash docker/single-server/deploy.sh all          # gateway + jobs 一同发布（默认）
bash docker/single-server/deploy.sh gateway      # 只发布 gateway / jobs / maintenance 同理
```

脚本固定执行：**无条件全量重编译**（不信任 `build/bin` 既有产物，防止 shared 模块修复后旧产物上线）→ 上传 → **md5 三点闭环校验**（本地新编译 = 服务器 build/bin = 容器内运行二进制）→ `docker compose build` + `up -d` → 逐容器等待 healthy → 公网健康检查。任一环节失败立即退出并给出回滚提示。

手动流程（等价于脚本内部步骤，仅排障时用）：构建（见上节命令）→ 上传 `build/` → `docker compose build gateway jobs maintenance` → `docker compose up -d`。maintenance 幂等，发布后跑一次 `--ensure-schema` 应用加法式 schema。回滚 = 上传上一个版本的 build/ 并重新 build+up。

## .env 契约（当前实例全集见服务器 /opt/juhe-ai/.env）

- `NODE_ENV=production`：触发全部生产校验（SECRET 强度、CORS 白名单必填、禁 dev 自动登录）。
- `JUHE_AI_SECRET`：≥32 位强随机；账号凭据/内建 API Key 加密封套，**有加密数据后不可更换**，务必留档。
- `JUHE_AI_DATABASE_DRIVER=postgres` + `JUHE_AI_POSTGRES_URL`；audit/operation/account-health 子系统缺省跟随主库驱动与主 URL。
- `JUHE_AI_RUNTIME_LOG_POSTGRES_URL` / `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`：**必须显式**（F1/F2 无主 URL 回退）。
- `JUHE_AI_J3B_CIRCUIT_REDIS_URL`：**必须显式且不得与 `JUHE_AI_REDIS_STATE_URL` 相同键空间**（gateway 启动强校验；现用同实例 DB2）。
- `JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY`、`JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE=postgres`（+ INPUT_POSTGRES_URL）：PG 模式按运维手册显式化。
- `JUHE_AI_MAINTENANCE_J3A/J3B_POSTGRES_URL`：`--apply-*` 预置命令的 maintenance 专用 URL。
- `JUHE_AI_ALLOWED_ORIGINS`：生产必填、逗号分隔、拒绝 `*`；当前 `https://aijh.huanmin.top,http://103.36.63.105`。
- `JUHE_AI_COOKIE_SECURE=true`（HTTPS 已启用）；`JUHE_AI_TRUST_PROXY=true`（经 Caddy）。
- `JUHE_AI_REDIS_NAMESPACE=prod`：redis 驱动下必填非空。
- 管理后台账号沿用老生产 `system_accounts`（154 个），无默认密码残留。
