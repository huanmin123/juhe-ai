# AI 部署执行清单

> 面向自动化执行者和维护者。本文只给出可复现的部署顺序、可观察成功条件与停止条件；不保存服务器地址、账号、密码、密钥或生产连接串。

## 1. 先确认范围

当前发布拓扑是 Node 主服务加两个常驻 Go 项目：`juhe-ai-jobs` 承载 F1/F2，`juhe-ai-gateway` 承载 F3/F4；`juhe-ai-maintenance` 只执行一次性维护命令。F1/F2/F3/F4 分别保留独立 Store、schema 和 owner lease。Node 仍负责网关、账户、主 API、usage、公开接口日志、model-check、stats 和 ops；操作日志只保留直接签名 RPC producer/read adapter。

已完成的运行证据包括隔离开发 Linux 的固定 release 直接启动、Docker standalone 与 Docker performance 闭环，以及 2026-08-11 目标 Mac 上隔离 temporary release 的 F1/F2/F3 验证。F4 已完成开发 SQLite/PostgreSQL、Node RPC 和启动路径验证，但尚未执行生产 candidate/cutover。所有这些证据都不等于生产数据、生产 Docker 常驻、`current` 切换、Nginx/Caddy/Edge 或真实流量已经验证。

部署前先选择一种方式：

- 发布包：阅读 [构建指南](构建指南.md) 和 release 内的 [快速部署说明](../../deploy/README.md)。
- Docker standalone 或 performance：阅读 [Docker 部署指南](Docker部署指南.md) 和 `docker/README.md`。
- macOS：先阅读 [生产发布快速流程](生产发布快速流程.md) 和 [macOS 部署指南](macos/macOS部署指南.md)；生产 Mac 还必须按受限的现场 runbook 执行，不能把本开发文档当成切流授权。

同一数据目录、数据库或 Redis namespace 不能同时由发布包与 Docker 两套部署使用。

## 2. 通用前置条件

1. 只使用固定 commit 的干净 release。核对 `RELEASE_SOURCE_COMMIT`、压缩包 SHA-256 和部署目标的 `GOOS/GOARCH`。
2. 确认发布包存在且可执行 `backend-go/juhe-ai-jobs`、`backend-go/juhe-ai-gateway` 与 `backend-go/juhe-ai-maintenance`。目标主机还必须提供系统 `rg`，或显式配置 `JUHE_AI_RG_PATH`。
3. 不把真实 `backend/.env`、Docker `.env`、数据目录、日志、payload/blob、数据库 dump 或 Redis 数据提交进 Git、打进 release 或复制到公开文档。
4. SQLite 模式下，业务、dataset、usage catalog、stats、F1、F2、F3、F4 的八个 SQLite 文件必须物理隔离；usage shard 根目录也不得与它们重叠。路径、hard link 或 symlink 不能用来绕过该约束。
5. F3/F4 不能只依赖 `JUHE_AI_SECRET`。必须配置稳定的 `JUHE_AI_AUDIT_LOG_INSTANCE_ID` / `JUHE_AI_OPERATION_LOG_INSTANCE_ID` 和各自独立的 input secret；production 中每个 secret 至少 32 位。Node 与对应 owner 还必须使用同一个 loopback input URL/listen address 端口。
6. `NODE_ENV=production` 的 performance 模式必须为每个 Node 进程配置稳定且唯一的 `JUHE_AI_INSTANCE_ID`；缺失时 Node 拒绝启动。不得把临时 PID 当作生产 owner。
7. 构建入口必须与目标平台一致：Windows 只在原生 PowerShell 执行 Windows 打包；生产 Mac 只在目标 Mac 或受控原生 macOS 构建。不得从 Git Bash、MSYS、Cygwin 或 w64devkit 启动发布构建。核对 `RELEASE_SOURCE_COMMIT`、`frontend/dist/build-info.json` 的 `buildId` 与冻结 commit 一致；字符串搜索只用于故障诊断，不作为压缩 bundle 的发布契约。

任一项无法证明时停止部署，保留原始错误；不得用另一种存储、旧 Node writer、队列或临时 fallback 继续运行。

## 3. 发布包流程

1. 按 [构建指南](构建指南.md) 从固定 commit 打包，解压到新的 release 目录，不覆盖当前运行目录。
2. 复制 `backend/.env.example` 为 `backend/.env`，填写实际 Origin、稳定 `JUHE_AI_SECRET`（若复用现有业务库必须沿用旧值）、稳定的 F1/F2/F3/F4 owner ID，以及完整 F3/F4 配置。`start.sh` / `start.ps1` 只会生成 `JUHE_AI_SECRET`；不会生成任一 Go owner ID 或 F3/F4 input secret。
3. SQLite 默认 F3 配置应使用独立的 `JUHE_AI_AUDIT_LOG_DATABASE_PATH`、`JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY` 和 `JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY`；F4 使用独立的 `JUHE_AI_OPERATION_LOG_DATABASE_PATH` 与只读 `JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH`。performance PostgreSQL 模式还必须提供 `JUHE_AI_POSTGRES_URL`、F3 blob 目录和 F3/F4 loopback 配置。
4. 新建且确认为空的 PostgreSQL 库必须在 release 根目录先执行 `pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...`，再执行 `node ./backend/dist/scripts/maintenance/init-postgres-schema.js`。它会写当前 schema 与默认种子数据；`start.sh` / `start.ps1` 不会替代该步骤。已有业务库不得直接执行完整初始化，必须按既有库备份、`schema-only` 与受控迁移流程处理。
5. 仅通过 release 根目录的 `start.sh` 或 `start.ps1` 启动。它会在 Node DB-ready health 成功后分别启动 Go jobs 与 gateway，并各自维护 PID、日志与退出联动；不要绕过它单独常驻 Node、worker 或 Go owner。
6. macOS system `launchd` 场景中，完成目标机 `pnpm install --prod` 后再由 root 固定 release 的所有权与权限：服务用户必须可递归读取/执行 release，但不能写 release；只有 runtime、日志和 spool 目录可由服务用户写入。`--apply` 必须通过 `sudo` 运行并显式传 `--service-user`。
7. 常规升级只请求 Node `/__aisys__/health`、`/__aisys__/api/health`、jobs `3305/health`、gateway `3306/health`（均为 `200`）、F3 `GET /__aiinternal__/health` 与 F4 `GET /__aiinternal__/v1/operation-logs/health`（均为 `204`）以及无 Key 的 `/v1/models`（`401`），再查看 `backend/logs/juhe-ai-jobs.log` 与 `backend/logs/juhe-ai-gateway.log` 的最后 80 行，不得有 `panic` 或 `fatal`。这些是发布包、Node、Go 和网关路由的最小真实检查。
8. 浏览器登录态、F1/F2 数据新鲜度、F3/F4 写后读回、长时间观察和完整回滚演练只用于首次新拓扑、存储/owner 改动、事故调查或回切，不作为无代码路径变化的 routine release 前置。
9. 升级前保留当前 release；生产不得在 routine release 中执行数据库、Redis、日志或 payload 清理。

发布包具体变量、命令和 PID/log 路径以 [快速部署说明](../../deploy/README.md) 为准。

## 4. Docker 流程

1. 在构建输入目录执行 `pnpm install` 与 `pnpm build`，确保 `backend/dist`、`frontend/dist` 已生成。
2. 进入 `docker/`，先复制 `.env.example` 为 `.env`；standalone 也必须填写 F3/F4 input secret，不能直接执行 Compose 默认配置。performance 必须复制 `.env.performance.example` 并填写 PostgreSQL、三类 Redis、应用密钥和 F3/F4 input secret。
3. 在启动前运行 `docker compose config --quiet`；performance 使用 `docker compose --env-file .env.performance -f compose.performance.yml config --quiet`。配置校验失败时不得启动。
4. 使用 `docker compose up -d --build --wait` 启动，并以 `docker compose ps` 确认 Node、`go-jobs` 与 `go-gateway` 均 healthy。performance 还需确认 PostgreSQL、PgBouncer、Redis cache/state/queue 均 healthy。
5. 常规升级验证 Node 两个 health 与 jobs/gateway project health 为 `200`、F3/F4 input health 均为 `204`、无 Key gateway 为 `401`，并查看 Node、jobs 与 gateway 启动日志。F1/F2 新鲜度和 F3/F4 Node -> Go -> Node 读回只在首次部署、owner/存储变更或故障调查时执行。
6. Docker 中 F3/F4 与 Node 必须共享 loopback network namespace；不得把任一输入 listener 改成暴露在容器网卡上，也不能把 `127.0.0.1` 替换成另一个 service 地址。
7. 生产只使用 `docker compose down` 停止。`docker compose down -v` 会删除事实库、日志和密钥，只能用于明确可销毁的验证环境。

Compose 名称、变量和 sidecar 卷以 [Docker 部署指南](Docker部署指南.md) 为准。

## 5. macOS 与生产门禁

历史 Mac temporary release 曾验证旧三二进制拓扑；它已经归档，不能作为当前 Go 项目部署的上线证据。routine release 必须从最终冻结 release 启动独立 candidate Node 槽，并以 `--go-sidecar-mode reuse` 复用正式的 Go jobs/gateway owner。

不得把开发 Linux 或 Docker 的通过结果写成 macOS 或生产通过，也不得因一次 Node health `200` 宣称部署完成。2026-08-12 首次 F3 正式上线发生过硬停机，且错误的 Windows 盘符 API base 使静态页面可加载但所有管理 API 失败；后续 Mac 高性能发布必须按 [生产发布快速流程](生产发布快速流程.md) 使用独立 candidate 槽、`--quick` 和 `quick-performance-cutover.sh`。routine release 的放行证据是 candidate control/API/gateway、共享 Go health、启动日志以及切换后的三条公网请求；同槽原地 apply 不是零停机流程。

## 6. 验收记录格式

每次候选部署在受限运维记录中至少保存以下非敏感事实：

- release commit、archive SHA-256、目标 OS/arch、部署方式和开始/结束时间；
- 配置文件路径与变量名是否齐全，不记录变量值或密钥；
- Node 两个 health、无 Key gateway `401`、F3/F4 `204` 和启动日志最后 80 行的结果；
- 原生构建环境、固定 commit/buildId、candidate/main 的独立 label、端口和 route identity；
- `QUICK_CUTOVER_OK` 或自动恢复结果，以及旧槽保留状态；
- 首次拓扑、owner/存储变更、事故或回切时，额外记录浏览器、F1/F2/F3/F4 读回、handover journal、access-log 和稳定观察结果；
- 使用的临时数据库/Redis namespace、清理或保留结论；
- 失败时的原始错误、未执行的步骤和回滚状态。

不要将请求正文、审计 payload、连接串、Cookie、OAuth token、API Key 或 input secret 写入验收记录。
