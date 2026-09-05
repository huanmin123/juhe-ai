# 部署 go-only 双轨开关（X03 前置）

> 状态：部署资产已支持 `JUHE_AI_DEPLOY_MODE` 三模式开关；实际生产切换、PM2/LaunchDaemon 等主机侧变更仍归 X03/G20 正式工作包，本文只描述仓库内可验证的机制。
> 适用资产：`deploy/start.sh`、`deploy/start.ps1`、`docker/compose.go-only.yml`、`Jenkinsfile`（go build/test 阶段）、`scripts/validate-release-package.mjs`（`--deploy-mode` 分支）。

## 1. 模式矩阵

`JUHE_AI_DEPLOY_MODE` 可通过进程环境或 `backend/.env` 配置（环境变量优先），缺省 `hybrid`；非法值一律启动失败。

| 模式 | Node Web/API | Go gateway | Go jobs | maintenance 预检 | 用途 |
| --- | --- | --- | --- | --- | --- |
| `hybrid`（默认） | 启动（现状路径，零行为变化） | 启动（F3/F4 owner） | 启动（F1/F2 owner） | 不执行 | G20 翻转前后通用的现行拓扑 |
| `go` | 不启动，不要求 `backend/dist` 与 pnpm | 启动并强制 `JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true`，绑定 `JUHE_AI_HOST:JUHE_AI_PORT` 成为主入口 | 启动 | 可选（见下） | G20 翻转后的目标拓扑演练与切换 |
| `node` | 启动 | 不启动 | 不启动 | 不执行 | G20 翻转后回滚兜底（Node 作为入口直启） |

限制：

- `node` 模式只负责启动 Node Web/API；F1-F4 的 owner 语义不变（hybrid 现状下同样由 Go 承载）。翻转前用 `node` 模式做紧急止血时，F1/F2/F3/F4 写入与 Go owner 停机是已知的降级状态，不是受支持的长期拓扑。
- `go` 模式下 `JUHE_AI_OWNER_LOCK_ENABLED=true` 会显式拒绝启动（owner lock 的 server 包装只覆盖 Node 进程，暂无 Go 等价物；补齐前禁止静默失去部署保护）。
- `go` 模式仍要求 `node` 可用：`scripts/start-go-project.mjs` 是 Go 二进制的 detached 启动器与 env 组装器，仍是现有 launcher 的一部分；X03 正式翻转时可评估去 Node 化。
- `go` 模式下 gateway 显式配置 `JUHE_AI_GATEWAY_SYSTEM_API_ENABLED` 为非 `true` 值会拒绝启动（go 模式要求 gateway 拥有主入口）。

## 2. 环境变量（新增部分）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `JUHE_AI_DEPLOY_MODE` | `hybrid` | `go` / `hybrid` / `node`；读环境变量后回退 `backend/.env` |
| `JUHE_AI_GO_MAINTENANCE_BOOTSTRAP` | `false` | 仅 go 模式：启动前执行 `backend-go/juhe-ai-maintenance --ensure-schema`（幂等），SQLite 按 `backend/.env` 的六个库路径组装 `--paths`，PostgreSQL 用 `--dsn "$JUHE_AI_POSTGRES_URL"` |
| `JUHE_AI_GO_MAINTENANCE_SEED` | `false` | 仅 go 模式：在 bootstrap 命令上追加 `--seed`；默认种子写入依赖该显式开关 |

`hybrid` 与 `node` 模式不读取上述两个 maintenance 开关，行为与历史版本一致。

## 3. 启动顺序与健康检查端口

### hybrid（默认，与历史一致）

1. `backend/.env` 缺失时从 example 创建，生成/复用 `JUHE_AI_SECRET` 与 `JUHE_AI_ALLOWED_ORIGINS`。
2. pnpm 依赖安装与 `backend/dist/scripts/preflight/check-node-sqlite.js` 预检。
3. 启动 Node `backend/dist/server.js`，等待 `http://JUHE_AI_HOST:JUHE_AI_PORT/__aisys__/api/health` 返回 `200`。
4. 启动 `juhe-ai-go-gateway`，等待 `http://127.0.0.1:3306/health` 返回 `200`；再确认 F3 `3303 /__aiinternal__/health` 与 F4 `3304 /__aiinternal__/v1/operation-logs/health` 返回 `204`。
5. 启动 `juhe-ai-go-jobs`，等待 `http://127.0.0.1:3305/health` 返回 `200`。
6. 三个进程任一退出即结束本次启动并清理其余进程。

### go

1. 与 hybrid 相同的 `.env` 创建与默认值生成（`JUHE_AI_SECRET` 对 Go 同样必需）。
2. 校验 `JUHE_AI_GATEWAY_SYSTEM_API_ENABLED` 并强制为 `true`；按开关执行 maintenance `--ensure-schema [--seed]`（幂等，可重复启动）。
3. 启动 `juhe-ai-go-gateway`（owner health `3306 /health` 200、F3/F4 input 204 同上），随后等待 `http://JUHE_AI_HOST:JUHE_AI_PORT/__aisys__/api/health` 返回 `200`（gateway system API 组合根提供与 Node 相同的健康契约）。
4. 启动 `juhe-ai-go-jobs`（owner health `3305 /health` 200）。
5. 监控仅覆盖两个 Go 进程；任一退出即结束并清理。

### node

`.env` 与预检同 hybrid；只启动 Node Web/API 并等待 `/__aisys__/api/health` 200，随后监控 Node 进程。

PID 与日志位置不变：`backend/runtime/juhe-ai-{gateway,jobs}.pid`（Windows 为 `juhe-ai-go-{gateway,jobs}.pid`）与 `backend/logs/juhe-ai-{gateway,jobs}.log`。

## 4. Docker

- `docker/compose.go-only.yml` 是 go-only 拓扑：`gateway`（主入口，发布 `${JUHE_AI_PUBLIC_BIND}:${JUHE_AI_PUBLIC_PORT}`，`JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true`）+ `jobs`（F1/F2，`depends_on: gateway: service_healthy`）。两者不再依赖 Node 容器或 `network_mode: service:juhe-ai`；F3/F4 input listener 仍只绑容器内 loopback。
- 卷语义差异：go-only 下 `juhe-ai-data` 对 gateway 读写（system API 是业务库唯一 writer），对 jobs 只读；hybrid `compose.yml` 保持 Node 读写、两个 Go 项目只读的现状。
- 启动：`cd docker && docker compose -f compose.go-only.yml up -d --build --wait`；`.env` 的镜像与 secret 变量与 `compose.yml` 同名（`JUHE_AI_GO_IMAGE`、`JUHE_AI_GO_*_RUNTIME_IMAGE`、F3/F4 input secret、各 owner ID 必填）。
- 回滚：`docker compose -f compose.go-only.yml down` 后按 `docker/README.md` 回到 `compose.yml`（hybrid）即可；两份 compose 的卷同名，数据卷在模式切换间复用，切换前应先完成业务备份。

## 5. CI 与发布物校验

- `Jenkinsfile` 新增「构建并推送三镜像」之前的「构建并验证 Go 模块」阶段：在受控 `GO_IMAGE` 内执行 `go build ./...`（workspace 全部模块）与 `go test -count=1 ./projects/gateway/cmd/... ./projects/jobs/cmd/... ./projects/maintenance/cmd/...`；发布三镜像阶段本身不变。
- `scripts/validate-release-package.mjs` 支持 `--deploy-mode=go|hybrid|node`（API 参数 `deployMode`）：`hybrid`（默认）要求 Node server + 前端 + 三 Go 二进制（历史行为）；`go` 不要求 `backend/dist`，但三 Go 二进制必填；`node` 不要求 Go 二进制。`package-release` 打包仍产出完整 hybrid 包，go-only 部署通过 `JUHE_AI_DEPLOY_MODE=go` 在运行时选择，无需单独包形态。

## 6. 回滚开关

| 场景 | 操作 |
| --- | --- |
| go 模式运行异常（翻转后） | `JUHE_AI_DEPLOY_MODE=node` 重启（或 docker 回 `compose.yml`）；Node 重新成为入口。注意 F1-F4 owner 若已由 Go 承载，需按各自迁移契约的回滚门执行，`node` 模式本身不回切 owner |
| go 模式误开（翻转前） | 取消 `JUHE_AI_DEPLOY_MODE` 或设回 `hybrid`，重启即恢复现状拓扑 |
| schema 预检误跑 | `--ensure-schema` 幂等；如需回退 schema 变更，按 `docs/deploy/部署指南.md` 的项目/业务备份恢复，不提供自动降级 |

## 7. 主机侧待办（不在本仓库内，X03/G20 跟进）

- Mac LaunchDaemon / 八台 Edge 相关的常驻定义仍在 `.local/project-resources/prod/` 私有资料中，仓库内无对应资产可改；翻转时需按其 runbook 将 `JUHE_AI_DEPLOY_MODE=go` 落到主机环境。
- PM2/systemd ecosystem 文件不在仓库内；如有主机侧进程管理，同样需要补 go 模式的启动参数。
- owner lock（`run-with-owner-lock.mjs --role server`）暂无 Go 等价包装；go 模式下该保护被显式禁用，X03 需补齐或将 go 模式纳入新的单实例保护机制。
- `docker/compose.performance.yml`（PostgreSQL/PgBouncer/Redis 高性能拓扑）尚未提供 go-only 变体；其 schema 初始化链路（`pnpm --filter juhe-ai-backend postgres:init-schema`）仍依赖 Node 工具链，可改用 `juhe-ai-maintenance --ensure-schema --driver postgres` 替代后再翻转。
- 发布包 `scripts/package-release.sh|.ps1` 目前固定产出 hybrid 包；若 X03 决定发布 go-only 包形态，需让两个打包脚本按 `--deploy-mode` 裁剪 `backend/dist` 并调整包内 README。
