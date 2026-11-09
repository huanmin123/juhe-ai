# 集群模式部署（多节点 + 外部 PG/Redis + 镜像仓库发布）

> 定位（2026-11-07）：这是多机形态的**打底模板**，代码与单机完全兼容；当前生产仍是
> `docker/single-server/` 单机 Docker，不要求迁移。本目录与单机目录**互不引用、互不修改**：
> 同一套 `gateway`/`jobs`/`maintenance` 二进制，两种部署形态各自独立演进。

## 1. 与单机模式的边界

| 维度 | 单机 `docker/single-server/` | 集群 `docker/cluster/` |
| --- | --- | --- |
| PostgreSQL / Redis | compose 内本地容器 | 外部云 RDS / 云 Redis（或独立高可用实例） |
| gateway 副本 | 1 个 | 每节点 1 个，多副本无状态 |
| jobs | 1 个 | 每节点 1 个，PG leader lease 全局单活 |
| 前端 | 打进 gateway 镜像，`/__aisys__/` 由 gateway 内嵌 dist 承接 | 独立 `frontend` 服务（nginx + dist 挂载），Caddy 分流 |
| 入口 | Caddy 与 gateway 同 compose，固定容器 IP | 独立入口机 Caddy，环境变量上游池 + 主动健康检查 |
| 镜像 | 本机构建（`deploy.sh`），`juhe-ai-single-server:<tag>-<role>` | 仓库推送 digest 引用，`<registry>/juhe-ai:<tag>-<role>` |
| 发布 | `deploy.sh`（停写窗口 + 单机重建） | `build-images.sh` + 逐节点 `rolling-release.sh` |
| 共享目录 | `./data/app/{data,logs}` 本机 | 默认也是本机 `./data/app`；跨节点共享按需配置，不强制 NFS/NAS |
| 容器名 | `juhe-ai-go-gateway` / `juhe-ai-go-jobs` / `juhe-ai-caddy` | `juhe-ai-gateway` / `juhe-ai-jobs` / `juhe-ai-go-frontend` / `juhe-ai-edge` |

两侧不共享 compose、Caddyfile、脚本或 `.env`；单机部署行为不受本目录任何文件影响。

### 1.1 共享目录决策表（默认全部本地）

应用不依赖 NFS/NAS：`JUHE_AI_SHARED_ROOT` 不配置时，compose 把节点本地
`./data/app/{data,logs}` 挂进容器；只有下表列出的跨节点能力才需要把它配成
共享挂载。**不需要共享的目录一律保持本地，避免把系统级存储问题带进启动链。**

| 目录/能力 | 默认位置 | 何时需要跨节点共享 | 原因 |
| --- | --- | --- | --- |
| `data/usage-record-spool` | 已退役（2026-11-09 T188） | 不需要 | gateway 用量记录直投 PG（共享 `usagewriter`），不再有 gateway→jobs 文件交接与 jobs drain |
| `logs/` | 节点本地 | 不需要 | 2026-11-09 起运行日志只写节点本地 JSONL，F1 索引/管理页/grep 已下架；集中检索由 ELK/Loki 等外部抓取承担 |
| `data/audit-blob` + 热搜索目录 | 节点本地 | 审计开启且 gateway 多节点 | 详情读用本节点 `blobDir`、retention 全局单活只删本节点、热搜索是本地 NDJSON；不共享会出现跨节点读不到 blob 与非清理节点 blob 永久泄漏 |
| `data/playground-artifacts` | 已退役（2026-11-09 起存业务库 `playground_artifacts`） | 不需要 | 产物改 PG bytea，跨节点可读 |
| `data/openai-compatible-files` | 已退役（2026-11-09 files/vector stores 本地运行时下架） | 不需要 | `/v1/files` 等非协议路径回落 404 |
| `data/account-health-input.key` | 节点本地 | 永远不需要 | jobs 本机密钥，启动时生成/读取；目录不可写会让 jobs 启动失败（本地写权限要求，与共享设备无关） |
| 备份、Docker json 日志 | 宿主机 | 系统层自行决定 | 不属于应用数据面 |

## 2. 目录内容

```text
docker/cluster/
├── compose.gateway.yml     # gateway 节点（每台 gateway 机器一份）
├── compose.jobs.yml        # jobs 节点（全局单活，可多机 standby）
├── compose.edge.yml        # 入口机：frontend 静态站 + Caddy
├── Caddyfile               # 入口路由（/v1、/__aisys__/api、/__aipublic__ → gateway；SPA → frontend）
├── Dockerfile.runtime      # 运行时镜像（仅二进制，不含前端）
├── frontend/nginx.conf     # 管理面 SPA 静态站
├── build-images.sh         # 构建 + 推送 gateway/jobs/maintenance 镜像（含前端产物构建）
├── rolling-release.sh      # digest 门禁 → 摘流 → 替换 → 健康门禁 → 回滚
├── verify-cluster.sh       # 按角色自检（容器/健康/共享目录/入口全链路）
├── .env.example            # 分角色配置模板
└── .gitignore              # build/、.env、releases.jsonl、frontend-dist 不入库
```

## 3. 拓扑与角色

```text
                    DNS / 云 LB（可选，多入口机时）
                              │
                 ┌────────────┴────────────┐
                 │  edge 机（可多台）        │
                 │  Caddy :443 + frontend   │
                 └────────────┬────────────┘
                              │ 上游池（主动健康检查 / least_conn）
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
  ┌─────┴─────┐         ┌─────┴─────┐         ┌─────┴─────┐
  │ gateway 1 │         │ gateway 2 │   ...   │ gateway N │   无状态，可水平扩
  └─────┬─────┘         └─────┬─────┘         └─────┬─────┘
        └─────────────────────┼─────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
   外部 PostgreSQL       外部 Redis           可选共享存储（NFS/云 NAS）
   （主备/高可用）      （单节点或 cluster）    仅 logs 集中留存时需要；默认节点本地

  jobs 节点：每台一份，PG leader lease 决定唯一 leader；其余 standby 只保留健康面。
```

角色与文件对应：

| 角色 | 执行文件 | 必需变量（最小集） |
| --- | --- | --- |
| gateway 节点 | `compose.gateway.yml` | `JUHE_AI_GATEWAY_IMAGE`、`JUHE_AI_SHARED_ROOT`、`JUHE_AI_INSTANCE_ID` + 应用侧 |
| jobs 节点 | `compose.jobs.yml` | `JUHE_AI_JOBS_IMAGE`、`JUHE_AI_SHARED_ROOT` + 应用侧 |
| 入口机 | `compose.edge.yml` | `JUHE_AI_EDGE_DOMAIN`、`JUHE_AI_GATEWAY_UPSTREAMS` |
| 构建机 | `build-images.sh` | `JUHE_AI_REGISTRY`、`JUHE_AI_IMAGE_TAG` |

## 4. 前置条件

1. **Docker Engine + Compose v2**：所有节点。
2. **外部 PostgreSQL**：目标库已创建，账号具备建 schema/表权限；schema 由 `maintenance upgrade` 初始化（见 §5.1）。
3. **外部 Redis**：单节点或 cluster；同集群所有节点使用同一 `JUHE_AI_REDIS_NAMESPACE`。
4. **共享存储**：NFS/云 NAS 导出 `data/` 与 `logs/` 两个目录，所有节点挂到同一路径
   （`JUHE_AI_SHARED_ROOT`）。目录属主必须为 **UID/GID 1000**（镜像内 `app` 用户固定 1000）；
   节点 UID 映射不一致时先统一，否则会出现日志/spool 权限失败。
5. **镜像仓库**：可 push/pull 的 registry；发布使用 digest 引用，不使用可变 `latest`。
6. **入口机**：域名 DNS 已指向该机；80/443（TCP+UDP）可达。

## 5. 首次部署

### 5.1 初始化 / 升级数据库 schema

集群没有 `--profile tool` 的 compose 服务，直接用 `maintenance` 镜像执行（镜像引用取自
`build/images.txt` 的 `-maintenance` 行）。**全新空库也走 `upgrade`**：它执行 ensure-schema、
seed 与全部迁移步骤，并把升级账本推进到当前 revision。

```sh
cd /opt/juhe-ai-cluster           # 本目录部署副本
set -a && . ./.env && set +a
docker run --rm \
  -w /app/backend \
  -e JUHE_AI_SECRET -e JUHE_AI_POSTGRES_URL -e JUHE_AI_REDIS_URL \
  -v "$JUHE_AI_SHARED_ROOT/data:/app/backend/data" \
  -v "$JUHE_AI_SHARED_ROOT/logs:/app/backend/logs" \
  "$JUHE_AI_MAINTENANCE_IMAGE" upgrade \
  --dsn "$JUHE_AI_POSTGRES_URL" \
  --secret "$JUHE_AI_SECRET" \
  --stop-write-confirmed \
  --backup-confirmed </dev/null
```

- 存量库升级/回滚窗口、`upgrade --dry-run` 门禁、revision 语义与单机一致，完整口径见
  `docs/deploy/部署指南.md` §7.5 与 §9；不要把 `--ensure-schema` 当作 `upgrade` 的替代。
- `gateway`/`jobs` 启动时只读校验库 revision，落后会 fail-fast 并提示 `upgrade`。
- 集群备份由外部 PG 的快照/备份机制承担；执行破坏性迁移前先确认可恢复点。

### 5.2 gateway 节点

```sh
cp .env.example .env && vi .env    # 填 GATEWAY_IMAGE/SHARED_ROOT/INSTANCE_ID + 应用侧
docker compose -f compose.gateway.yml up -d
docker compose -f compose.gateway.yml ps
curl -fsS http://127.0.0.1:3306/health
```

- `JUHE_AI_INSTANCE_ID` 必须全集群唯一（日志文件名、`build_info` 指标、后台 owner 依赖）。
- `JUHE_AI_LOG_DIR` 默认节点本地（`./data/app/logs`）；日志文件名 `juhe-ai.<INSTANCE_ID>.log`，跨节点集中检索走 ELK/Loki 外部采集。
- 健康监听 `127.0.0.1:3306` 仅宿主回环，供 Prometheus 经 SSH 隧道抓取；公网不要暴露。
- 安全基线沿用单机：非 root（1000）、只读根文件系统、`cap_drop: ALL`、`no-new-privileges`。

### 5.3 jobs 节点

```sh
cp .env.example .env && vi .env    # 填 JOBS_IMAGE/SHARED_ROOT + 应用侧
docker compose -f compose.jobs.yml up -d
curl -fsS http://127.0.0.1:3305/health
```

- 每台都起一份；只有一台 `"leader":true`。持有者崩溃后另一台约 25s 内接管
  （租约 30s TTL / 10s 续约）。
- standby 的 `/health` 会返回 `"leader":false`，属正常；容器健康检查仍为 healthy。
- 进程必须用 **SIGTERM 优雅停止**（`docker stop` 默认即是）；`SIGKILL` 会留下 30s 租约 TTL，
  新 leader 接管变慢。

### 5.4 入口机

1. 分发前端产物到入口机暂存目录（`build-images.sh` 产出的 `build/frontend-dist`，内含
   `build-info.json`；下文以 `/opt/juhe-ai-frontend/dist` 为例）：

```sh
tar czf - -C docker/cluster/build frontend-dist \
  | ssh <edge-host> "set -e; mkdir -p /opt/juhe-ai-frontend && rm -rf /opt/juhe-ai-frontend/dist.new && tar xzf - -C /opt/juhe-ai-frontend --transform 's/^frontend-dist/dist.new/' && rm -rf /opt/juhe-ai-frontend/dist && mv /opt/juhe-ai-frontend/dist.new /opt/juhe-ai-frontend/dist"
```

2. 把 dist 发布进 `frontend_dist` 命名卷（assets 等先写、`index.html` 最后切换；
   nginx 不需重启，发布过程中的新会话不会看到半套产物）：

```sh
docker run --rm \
  -v juhe-ai-edge_frontend_dist:/target \
  -v /opt/juhe-ai-frontend/dist:/src:ro \
  nginx:alpine sh -eu -c '
    mkdir -p /target
    cd /src
    for item in *; do
      [ "$item" = "index.html" ] && continue
      cp -a "$item" /target/
    done
    cp -a index.html /target/index.html
  '
```

3. 配置并启动：

```sh
cp .env.example .env && vi .env    # EDGE_DOMAIN / GATEWAY_UPSTREAMS / FRONTEND_DIST_PATH
docker compose -f compose.edge.yml up -d
curl -fsS https://<EDGE_DOMAIN>/__aisys__/health
```

- 首次部署必须先执行第 2 步再 `up -d`（卷为空时 frontend healthcheck 不通过）；
  后续发布只重复第 1、2 步，nginx 持续读取同一个卷。
- 不要执行 `docker compose -f compose.edge.yml down -v`：`-v` 会删除 `frontend_dist` 卷，
  前端需重新发布。
- 前端回滚 = 把上一版 dist 重新发布进卷（同一命令），旧 assets 保留在卷内不影响。
- `JUHE_AI_GATEWAY_UPSTREAMS` 用空格分隔，例如 `10.0.0.11:3000 10.0.0.12:3000`；
  Caddy 每 10s 主动探测 `/__aisys__/health`，失败节点 30s 内剔除、恢复后自动接回。
- 多入口机时每台各跑一份，DNS 轮询或前级云 LB 分流。
- Caddy 路由面：`/v1`、`/v1/*`、`/__aisys__/api`、`/__aisys__/api/*`、`/__aisys__/health`、
  `/__aipublic__/*` 走 gateway；`/__aisys__/`（SPA）与 `dist/help` 走 frontend；其余 404。
- 前端 dist 在容器内挂到 `/usr/share/nginx/html/__aisys__`（vite `base=/__aisys__/`），
  `index.html`/`brand-icon.svg`/`build-info.json` 不缓存，`assets/` 长缓存 immutable。

## 6. 环境变量契约

模板见 [`.env.example`](.env.example)。两类变量：

1. **编排侧**（本目录专属）：`JUHE_AI_GATEWAY_IMAGE` / `JUHE_AI_JOBS_IMAGE`（完整 digest 引用）、
   `JUHE_AI_SHARED_ROOT`、`JUHE_AI_INSTANCE_ID`、`JUHE_AI_JOBS_INSTANCE_ID`、
   `JUHE_AI_GATEWAY_BIND`、`JUHE_AI_TRUST_PROXY`、`JUHE_AI_GATEWAY_UPSTREAMS`、
   `JUHE_AI_EDGE_DOMAIN`、`JUHE_AI_FRONTEND_UPSTREAM`（前端 dist 通过命名卷发布，不走变量）。
2. **应用侧**：与单机同名同语义（`JUHE_AI_SECRET`、`JUHE_AI_POSTGRES_URL`、
   `JUHE_AI_REDIS_*`、`JUHE_AI_ALLOWED_ORIGINS`、`JUHE_AI_COOKIE_SECURE` 等），
   完整条目与取值约束见 `docker/single-server/README.md`「.env 契约」；差异只有连接目标
   （外部 PG/Redis）和不需要 `POSTGRES_PASSWORD` / `REDIS_PASSWORD` / `JUHE_AI_PROXY_IP_*`。

补充约定：

- `JUHE_AI_TRUST_PROXY`：compose 默认 `1`（每台前面只有一层 Caddy/云 LB）。直连客户端时不要配；
  LB 层数变化时按真实跳数调整（`true` 等价 1）。
- `JUHE_AI_GATEWAY_BIND`：绑到节点内网 IP 可避免 API 直接暴露在公网网卡；LB 必须能访问该地址。
- 共享存储路径使用绝对路径；同一集群所有节点必须同值，否则 spool/日志跨机不可见。
- 应用侧变量由 compose `env_file: .env` 注入；`environment:` 中的编排值优先于 `.env` 同名项。

## 7. 构建与发布

### 7.1 构建镜像

```sh
cd docker/cluster
./build-images.sh --registry registry.example.com/juhe-ai --tag 20261107-r1
# 只构建不推送（离线/演练）：--no-push
# 复用已校验的前端产物：--skip-frontend（buildId 必须等于当前 HEAD）
```

产物：`<registry>/juhe-ai:<tag>-gateway|jobs|maintenance`，`build/images.txt` 写入完整
`repo@sha256:<digest>` 引用；前端产物在 `build/frontend-dist`（打包进入口机，不进镜像）。

构建身份：`version`/`commit`/`buildTime` 经 `-ldflags -X` 注入；前端 `buildId` 必须等于
当前 `HEAD`，否则脚本拒绝打包。

### 7.2 逐节点滚动发布（gateway/jobs）

```sh
./rolling-release.sh \
  --image registry.example.com/juhe-ai@sha256:<digest> \
  --apply-cmd 'JUHE_AI_GATEWAY_IMAGE=%IMAGE% docker compose -f compose.gateway.yml up -d --no-deps --force-recreate gateway' \
  --health-url http://127.0.0.1:3306/health \
  --container juhe-ai-gateway
```

脚本固定动作：

1. digest 门禁：`pull` 后核对 `RepoDigests`，与 `--expected-digest`/镜像引用不一致立即退出；
2. 摘流（可选 `--drain-cmd`）并等待 `--drain-wait`；
3. 替换实例（`--apply-cmd`，`%IMAGE%` 替换为 digest 引用）；
4. 健康门禁：轮询 `--health-url` 直到响应含 `"ready":true`，超时 `--health-timeout`；
5. 接回流量（可选 `--attach-cmd`）；任一步失败自动接回并回滚旧镜像。

发布顺序建议：逐台 gateway → 再 jobs（standby 先发，leader 最后发）；入口机前端产物单独分发。
每次发布追加一行到 `releases.jsonl`（镜像/digest/previous/phase），作为回滚锚点。

### 7.3 回滚

- gateway/jobs：用上一版 digest 重跑 `rolling-release.sh`（`--image old@sha256:...`）。
- 数据库：按 `docs/deploy/部署指南.md` §9 的窗口纪律恢复备份 + 回退旧镜像；schema 加法式
  变更通常允许旧二进制直接启动，破坏性迁移必须恢复备份。

### 7.4 滚动发布语义与限制

- **摘流**：Caddy 主动健康检查每 10s 探测；节点停止后最多约 10-20s 被剔除。没有独立 LB 时
  `--drain-cmd` 可省略，但停止瞬间正在分配的请求会失败（Caddy 不做非幂等自动重试，
  与设计 §8.1 契约一致）；有严格零错误要求时，应在入口机前使用支持摘流 API 的云 LB，
  先摘除节点再发布。
- **长连接**：gateway 收到 SIGTERM 后主 HTTP 服务优雅排空预算 10s，超过预算的 SSE/WS 长连接会被
  中断。对长流有严格零中断要求时，需要额外的连接级排空手段（独立 LB 摘流 + 等待在途流结束）。
- **实例 ID**：滚动替换时同一节点可复用 `JUHE_AI_INSTANCE_ID`；不要两台并用同一 ID。
- **jobs 单活**：先发 standby 不影响 leader；最后发 leader 时任务在接管窗口内暂停，接管约 25s。

## 8. 自检

```sh
./verify-cluster.sh --role node    # gateway 节点
./verify-cluster.sh --role jobs    # jobs 节点（leader/standby 状态说明）
./verify-cluster.sh --role edge    # 入口机（Caddy 上游池 + 全链路）
./verify-cluster.sh                # 检查本机存在的角色
```

检查项：容器健康、`/health` ready、`build_info` 的 `instance_id`、共享 `data/`/`logs/` 可写、
spool 目录、本实例日志文件、Caddy 上游池 healthy、HTTPS 健康、SPA `no-store`、`/v1/models` 与
`/__aipublic__` 返回 401（证明路由到 gateway 而非前端 404）。退出码 0/1/2。

## 9. 运行时一致性契约（跨节点必须成立）

1. **共享根可选**：默认所有 gateway/jobs 的 `data/`、`logs/` 保持节点本地；
   只有需要集中留存日志时才把 `JUHE_AI_SHARED_ROOT` 配成同一 NFS/云 NAS。
   用量记录直投 PG、审计关闭时无跨节点文件面，不依赖共享存储。
2. **UID/GID 一致**：镜像内 `app=1000`；如启用共享目录，属主 1000 且节点间
   UID 映射一致。
3. **实例 ID 唯一**：gateway 的 `JUHE_AI_INSTANCE_ID` 与 jobs 的 `JUHE_AI_INSTANCE_ID`
   在集群内唯一；日志文件名按实例区分，避免互相覆盖。
4. **Redis namespace 同值**：所有节点同一 `JUHE_AI_REDIS_NAMESPACE`；并发计数键
   `juhe-ai:{<ns>:concurrency}:gateway-account-concurrency:*` 与 jobs 投影读面同源。
5. **数据库 revision 一致**：所有节点同一镜像代；升级先 `maintenance upgrade`，再滚动实例。
6. **入口上游池准确**：`JUHE_AI_GATEWAY_UPSTREAMS` 必须覆盖全部在线 gateway 节点。

## 10. 代码兼容性

- 同一套 `gateway`/`jobs` 二进制同时支持单机与集群：集群专属变量均为**可选**，未设置时行为与
  单机一致（单机 compose 不设置 `JUHE_AI_INSTANCE_ID` 等集群专属项，实例标识由 hostname /
  `JUHE_AI_LOG_INSTANCE_ID` 决定）。
- 前端拆分只影响部署编排：gateway 在 `JUHE_AI_FRONTEND_DIST_PATH` 为空时不挂 SPA
  （集群模式即如此），`/__aisys__/api`、`/__aisys__/health`、`/v1` 路由不变。
- 两种形态的镜像由各自 Dockerfile 构建：单机镜像烤入前端 dist，集群镜像不含前端
  （前端产物发布到入口机命名卷）；二进制、配置语义与业务行为相同。

## 11. 已知限制与待办

已登记在 `docs/待办与阻塞事项.md`（T158），当前包括：PG/Redis 主备切换演练、真实 Realtime
上游握手、生产规模表锁/WAL 观测、首个集群生产发布窗口验证。本目录是打底模板，
**未在现生产启用**；生产仍为单机形态。
