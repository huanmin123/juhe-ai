# 部署 go-only 终态（原「双轨开关」收口）

> 状态：X01/X03 收口完成。Node backend 已物理归档到 `migration-backup/node/final-archive/`（X02，2026-09-04），Go 侧 `internal/legacybridge` 过渡反代已删除（X01）。本文由「三模式双轨开关（X03 前置）」改写为 go-only 终态说明；`hybrid` / `node` 部署模式已退役，历史值会被启动脚本 fail-closed 拒绝。
> 适用资产：`deploy/start.sh`、`deploy/start.ps1`、`docker/compose.yml`、`docker/compose.performance.yml`（遗留参考）、`Jenkinsfile`、`scripts/validate-release-package.mjs`、`scripts/package-release.sh|.ps1`。

## 1. 唯一拓扑

go-only 是唯一受支持的部署拓扑：

| 角色 | 进程 | 职责 |
| --- | --- | --- |
| 主入口 | `juhe-ai-go-gateway` | 以 `JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true` 绑定 `JUHE_AI_HOST:JUHE_AI_PORT`，提供 Web/管理 API、公开协议面、`/v1` AI 网关链与 F3/F4 owner |
| 后台 owner | `juhe-ai-go-jobs` | F1 运行日志、F2 表监控（J1 默认关闭） |

- 没有 Node Web/API 进程，没有 `JUHE_AI_DEPLOY_MODE` 分支；`deploy/start.sh|start.ps1` 只有一条 go 路径。
- `JUHE_AI_DEPLOY_MODE` 仅保留 fail-closed 校验：环境变量或 `backend/.env` 中出现 `hybrid` / `node` 等历史值时拒绝启动，并提示唯一合法值为 `go`。
- 未挂载前缀的行为是 Go kernel 的 404/405 JSON 契约（`{"message":"资源不存在"}`）；`JUHE_AI_LEGACY_BRIDGE_TARGET` 环境变量与反代路径已随 `internal/legacybridge` 删除，不存在任何 502 代理回退路径。

## 2. 启动脚本行为（deploy/start.sh、deploy/start.ps1）

1. `backend/.env` 缺失时从 example 创建；生成/复用 `JUHE_AI_SECRET` 与 `JUHE_AI_ALLOWED_ORIGINS`（Go 组合根同样必需）。
2. fail-closed 校验（全部保留）：
   - `JUHE_AI_DEPLOY_MODE` 必须为 `go`（缺省即 go）；
   - `JUHE_AI_OWNER_LOCK_ENABLED=true` 拒绝启动（owner lock 的 server 包装只覆盖过 Node 进程，无 Go 等价物；补齐前禁止静默失去部署保护）；
   - `JUHE_AI_GATEWAY_SYSTEM_API_ENABLED` 配置为非 `true` 值时拒绝启动（gateway 必须拥有主入口）。
3. 可选维护预检：`JUHE_AI_GO_MAINTENANCE_BOOTSTRAP=true` 时执行幂等的 `backend-go/juhe-ai-maintenance --ensure-schema`（SQLite 按 `backend/.env` 六库路径组装 `--paths`，PostgreSQL 用 `--dsn "$JUHE_AI_POSTGRES_URL"`）；`JUHE_AI_GO_MAINTENANCE_SEED=true` 追加 `--seed`。
4. 启动 `juhe-ai-go-gateway`（owner health `3306 /health` 200、F3 `3303 /__aiinternal__/health` 与 F4 `3304 /__aiinternal__/v1/operation-logs/health` 204、业务端口 `/__aisys__/api/health` 200），随后启动 `juhe-ai-go-jobs`（owner health `3305 /health` 200）。
5. 监控仅覆盖两个 Go 进程；任一退出即结束本次启动并清理其余进程。PID 与日志位置不变：`backend/runtime/juhe-ai-{gateway,jobs}.pid`（Windows 为 `juhe-ai-go-{gateway,jobs}.pid`）与 `backend/logs/juhe-ai-{gateway,jobs}.log`。

`node` 仍为运行时依赖：`scripts/start-go-project.mjs` 是 Go 二进制的 detached 启动器与健康探测组装器；去 Node 化（纯 Go launcher）留待后续评估。

## 3. Docker

- `docker/compose.yml` 即终态 go-only 拓扑（原 `compose.go-only.yml` 已并入本文件并删除，避免双文件漂移）：`gateway`（主入口，发布 `${JUHE_AI_PUBLIC_BIND}:${JUHE_AI_PUBLIC_PORT}`）+ `jobs`（`depends_on: gateway: service_healthy`）。
- 卷语义：`juhe-ai-data` 对 gateway 读写（system API 是业务库唯一 writer），对 jobs 只读；F3/F4 input listener 仍只绑容器内 loopback，不使用 `network_mode: service:<Node>`。
- 启动：`cd docker && docker compose config --quiet && docker compose up -d --build --wait`；`.env` 必填项与原先相同（`JUHE_AI_GO_IMAGE`、`JUHE_AI_GO_*_RUNTIME_IMAGE`、F3/F4 input secret、各 owner ID）。`.env.example` 已移除 `JUHE_AI_NODE_IMAGE`、`JUHE_AI_IMAGE` 与 `JUHE_AI_USAGE_STATS_TIMEZONE`（Go 时区来自 settings 库）。
- `docker/Dockerfile`、`docker/Dockerfile.builder`、`docker/entrypoint.sh`（Node 镜像与构建器）已删除；`docker/compose.performance.yml` 是 hybrid 遗留形态的参考，其 `juhe-ai` 服务在本仓库当前状态不可构建，go-only 高性能变体仍是 X03 平台侧待办。

## 4. CI 与发布物

- `Jenkinsfile`：已移除「构建前端与 Node 产物」与 Node 镜像构建/推送；现有阶段为「构建并验证 Go 模块」（三模块 build + 关键 cmd 测试）与「构建并推送 Go 镜像」（jobs + gateway 双镜像 digest）。
- 发布状态 schema 保持 10 列 history 与 `nodeImageDigest` metadata 键不变，避免破坏既有平台仓库与历史回滚：go-only 候选在该位写入 `'-'` 哨兵且不回写平台 kustomization 的 `juhe-ai` 镜像块；历史 Node 时代回滚候选仍携带真实 digest，可按原样复原旧拓扑。
- `scripts/validate-release-package.mjs`：`--deploy-mode` 缺省为 `go`（API `deployMode` 同步）；`hybrid` / `node` 分支仅为校验历史发布包保留。
- `scripts/package-release.sh|.ps1`：只产出 go-only 包（`frontend/dist` + 三 Go 二进制 + 部署脚本），不携带 `backend/dist`，并以 `--deploy-mode=go` 校验。

## 5. 回滚语义

| 场景 | 操作 |
| --- | --- |
| go-only 运行异常 | 按 `docs/deploy/部署指南.md` 的项目/业务备份恢复进程与数据；启动脚本不再提供 `node` 模式止血入口 |
| 历史双镜像拓扑回滚 | `ROLLBACK_PROD` 选择 Node 时代 release state 时，Jenkins 按历史 `nodeImageDigest` 复原平台 kustomization（属于恢复旧拓扑，不是受支持的长期运行形态） |
| schema 预检误跑 | `--ensure-schema` 幂等；如需回退 schema 变更，按 `docs/deploy/部署指南.md` 的备份恢复，不提供自动降级 |

## 6. 近期新增的可选运行环境变量

以下 env 均有缺省值，常规单机部署无需设置；这里补登记近期新增项，避免只在源码可见。

| 环境变量 | 说明 | 已收录于 |
| --- | --- | --- |
| `JUHE_AI_JOBS_INTERNAL_URL` | gateway 回调 jobs internal-api 派发面的 loopback origin，默认 `http://127.0.0.1:3305`；手动账户测试派发（`/v1/account-test/dispatch`）、请求失败链与 runtime-reset/激活面的账户健康检查派发（`/v1/account-health-check/dispatch`）与账户余额健康裁决都经过它 | `deploy/README.md`（完整说明） |
| `JUHE_AI_BLUE_GREEN_OWNER_MODE` | Go 进程蓝绿 owner 模式，`active` / `standby` / `drain`（缺省 `active`，非法值启动失败）；仅 `active` 参与 owner 判定 | `deploy/README.md`（完整说明） |
| `JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL` | gateway ai-health 读面合并 jobs J1 durable outcome 的 PostgreSQL outcome 库（jobs `JUHE_AI_JOBS_OUTCOME_POSTGRES_URL` 的对端）；性能拓扑使用，留空时只读 SQLite outcome（`JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH`） | 本节（新增） |
| `JUHE_AI_CONCURRENCY_GLOBAL_MAX` | Node `concurrency.globalMax` 对应项，默认 `5000`、范围 1..50000；高并发调度策略默认队列上限（全局队列与每 API Key 队列界限）与派发候选窗口默认值来源 | 本节（新增） |
| `JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT` | `/v1` 派发候选窗口终值，缺省取 `JUHE_AI_CONCURRENCY_GLOBAL_MAX`，范围 1..50000；实际扫描窗口为 limit × 2 | 本节（新增） |
| `JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS` | `true` / `1` 时放行上游 Base URL 指向本机/内网/保留地址；仅限临时回归，常规本地联调优先 `JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST` | `docs/deploy/部署指南.md`（完整说明） |

## 7. 平台侧待办（不在本仓库内，X03/G20 跟进）

- Mac LaunchDaemon / 八台 Edge 的常驻定义仍在 `.local/project-resources/prod/` 私有资料中；主机环境已无需 `JUHE_AI_DEPLOY_MODE`。
- PM2/systemd ecosystem 文件不在仓库内；如有主机侧进程管理，需改为直接拉起两个 Go 二进制。
- owner lock（`run-with-owner-lock.mjs --role server`）无 Go 等价包装；go-only 下该保护被显式拒绝，X03 需补齐或将 go 拓扑纳入新的单实例保护机制。
- 平台仓库（k8s-juhe）的 overlay 仍声明 `juhe-ai` Node 容器与 `network_mode` 时代的共享网络语义：需要移除该镜像块并切换到 go-only 双容器拓扑后，Jenkins 写入的 `'-'` 哨兵才与运行态完全一致。
- `docker/compose.performance.yml` 的 go-only 变体（PostgreSQL/PgBouncer/Redis 高性能拓扑）未落地；schema 初始化可改用 `juhe-ai-maintenance --ensure-schema --driver postgres` 替代 Node 工具链。
