# AI 部署执行清单

> 面向自动化执行者和维护者。本文只给出可复现的部署顺序、可观察成功条件与停止条件；不保存服务器地址、账号、密码、密钥或生产连接串。

## 1. 先确认范围

当前发布拓扑是 Node 主服务加三个 Go sidecar：F1 `juhe-ai-runtime-log-indexer`、F2 `juhe-ai-table-monitor`、F3 `juhe-ai-audit-log-writer`。Node 仍负责网关、账户、主 API、usage、操作日志、公开接口日志、model-check、stats 和 ops。

已完成的运行证据包括隔离开发 Linux 的固定 release 直接启动、Docker standalone 与 Docker performance 闭环，以及 2026-08-11 目标 Mac 上隔离 temporary release 的 `start.sh` 直接启动、system `launchd --apply`、F3 重启接管、F1/F2/F3 读回、F3 HMAC 拒绝、稳定观察和完整 rollback；测试资源均已清理。这些证据不等于生产数据、生产 Docker 常驻、`current` 切换、Nginx/Caddy/Edge 或真实流量已经验证。

部署前先选择一种方式：

- 发布包：阅读 [构建指南](构建指南.md) 和 release 内的 [快速部署说明](../../deploy/README.md)。
- Docker standalone 或 performance：阅读 [Docker 部署指南](Docker部署指南.md) 和 `docker/README.md`。
- macOS：先阅读 [macOS 部署指南](macos/macOS部署指南.md)；生产 Mac 还必须按受限的现场 runbook 执行，不能把本开发文档当成切流授权。

同一数据目录、数据库或 Redis namespace 不能同时由发布包与 Docker 两套部署使用。

## 2. 通用前置条件

1. 只使用固定 commit 的干净 release。核对 `RELEASE_SOURCE_COMMIT`、压缩包 SHA-256 和部署目标的 `GOOS/GOARCH`。
2. 确认发布包同时存在且可执行 F1、F2、F3：`backend-go/juhe-ai-runtime-log-indexer`、`backend-go/juhe-ai-table-monitor`、`backend-go/juhe-ai-audit-log-writer`。目标主机还必须提供系统 `rg`，或显式配置 `JUHE_AI_RG_PATH`。
3. 不把真实 `backend/.env`、Docker `.env`、数据目录、日志、payload/blob、数据库 dump 或 Redis 数据提交进 Git、打进 release 或复制到公开文档。
4. SQLite 模式下，业务、dataset、usage catalog、stats、F1、F2、F3 的七个 SQLite 文件必须物理隔离；usage shard 根目录也不得与它们重叠。路径、hard link 或 symlink 不能用来绕过该约束。
5. F3 不能只依赖 `JUHE_AI_SECRET`。必须配置稳定的 `JUHE_AI_AUDIT_LOG_INSTANCE_ID` 和独立的 `JUHE_AI_AUDIT_LOG_INPUT_SECRET`；production 中该 secret 至少 32 位。Node 与 F3 还必须使用同一个 loopback `JUHE_AI_AUDIT_LOG_INPUT_URL` / `JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS` 端口。
6. `NODE_ENV=production` 的 performance 模式必须为每个 Node 进程配置稳定且唯一的 `JUHE_AI_INSTANCE_ID`；缺失时 Node 拒绝启动。不得把临时 PID 当作生产 owner。
7. 扫描最终 `frontend/dist/assets`：必须存在根相对 `/__aisys__/api`，不得存在 `[A-Za-z]:/.../__aisys__/api` 或其它文件系统形式的 API base。不能只检查 `index.html`、`build-info.json` 或静态资源 HTTP `200`。

任一项无法证明时停止部署，保留原始错误；不得用另一种存储、旧 Node writer、队列或临时 fallback 继续运行。

## 3. 发布包流程

1. 按 [构建指南](构建指南.md) 从固定 commit 打包，解压到新的 release 目录，不覆盖当前运行目录。
2. 复制 `backend/.env.example` 为 `backend/.env`，填写实际 Origin、稳定 `JUHE_AI_SECRET`（若复用现有业务库必须沿用旧值）、F2 stable instance ID，以及完整 F3 配置。`start.sh` / `start.ps1` 只会生成 `JUHE_AI_SECRET` 和 F1 instance ID；不会生成 F2/F3 instance ID 或 F3 input secret。
3. SQLite 默认 F3 配置应使用独立的 `JUHE_AI_AUDIT_LOG_DATABASE_PATH`、`JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY` 和 `JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY`。performance PostgreSQL 模式还必须提供 `JUHE_AI_POSTGRES_URL` 和 F3 blob 目录。
4. 新建且确认为空的 PostgreSQL 库必须在 release 根目录先执行 `pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...`，再执行 `node ./backend/dist/scripts/maintenance/init-postgres-schema.js`。它会写当前 schema 与默认种子数据；`start.sh` / `start.ps1` 不会替代该步骤。已有业务库不得直接执行完整初始化，必须按既有库备份、`schema-only` 与受控迁移流程处理。
5. 仅通过 release 根目录的 `start.sh` 或 `start.ps1` 启动。它会在 Node DB-ready health 成功后启动 F1、F2、F3，并维持 PID、日志与退出联动；不要绕过它单独常驻 Node、worker 或 sidecar。
6. macOS system `launchd` 场景中，完成目标机 `pnpm install --prod` 后再由 root 固定 release 的所有权与权限：服务用户必须可递归读取/执行 release，但不能写 release；只有 runtime、日志和 spool 目录可由服务用户写入。`--apply` 必须通过 `sudo` 运行并显式传 `--service-user`。
7. 验证 Node `/__aisys__/health` 和 `/__aisys__/api/health` 均为 `200`，F3 `GET /__aiinternal__/health` 为 `204`，并检查 `backend/logs/` 中三个 sidecar 的日志。随后通过 Node 只读接口确认 F1 runtime logs 和 F2 snapshots 新鲜，再通过一次真实审计输入的管理端详情读回确认 Node -> F3 -> Node。
8. 使用真实浏览器和现有登录态打开 candidate 的管理页，至少进入一个依赖业务 API 的账户页或统计页，确认无恢复页、Axios scheme/network 错误，且业务数据实际渲染。health、静态 HTML、HTTP `200` 均不能替代这一步。
9. 升级前保留当前 release 和业务恢复点；生产不得使用会删除数据的清理操作。

发布包具体变量、命令和 PID/log 路径以 [快速部署说明](../../deploy/README.md) 为准。

## 4. Docker 流程

1. 在构建输入目录执行 `pnpm install` 与 `pnpm build`，确保 `backend/dist`、`frontend/dist` 已生成。
2. 进入 `docker/`，先复制 `.env.example` 为 `.env`；standalone 也必须填写 `JUHE_AI_AUDIT_LOG_INPUT_SECRET`，不能直接执行 Compose 默认配置。performance 必须复制 `.env.performance.example` 并填写 PostgreSQL、三类 Redis、应用密钥和 F3 input secret。
3. 在启动前运行 `docker compose config --quiet`；performance 使用 `docker compose --env-file .env.performance -f compose.performance.yml config --quiet`。配置校验失败时不得启动。
4. 使用 `docker compose up -d --build --wait` 启动，并以 `docker compose ps` 确认 Node、F1、F2、F3 均 healthy。performance 还需确认 PostgreSQL、PgBouncer、Redis cache/state/queue 均 healthy。
5. 验证 Node 两个 health 为 `200`、F3 容器 health 为 `204`、四个服务日志无启动失败；至少执行 `docker compose exec -T audit-log-writer /usr/local/bin/juhe-ai-audit-log-healthcheck`（performance 命令前加 `--env-file .env.performance -f compose.performance.yml`）。继续执行 F1/F2 新鲜度和 F3 Node -> Go -> Node 读回。健康 `200` 不能代替 sidecar 数据验证。
6. Docker 中 F3 与 Node 必须共享 loopback network namespace；不得把 F3 改成暴露在容器网卡上，也不能把 `127.0.0.1` 替换成另一个 service 地址。
7. 生产只使用 `docker compose down` 停止。`docker compose down -v` 会删除事实库、日志和密钥，只能用于明确可销毁的验证环境。

Compose 名称、变量和 sidecar 卷以 [Docker 部署指南](Docker部署指南.md) 为准。

## 5. macOS 与生产门禁

2026-08-11 的 Mac 隔离 temporary release 已验证 `start.sh`、三项二进制、Node DB-ready 后启动、F3 `204`、F1/F2 新鲜度、F3 读回和稳定观察；随后还完成了三 sidecar system `launchd` 的 temporary apply、F3 重启接管和 rollback，临时资源均已清理且 `main` 未切换。它没有验证 `current` 切换、Nginx/Caddy/Edge、生产数据库或真实流量。生产 Mac 必须从最终冻结 release 重做同一预演并复核 release SHA-256，才可以讨论切流。

同日还在目标 Intel Mac 的隔离目录，以 commit `39b2cc68983c88959872d797312ece2f6de714cb` 使用受支持的 Node 22 与 BSD tar 完成了 `tar.gz` 构建、发布目录校验、F1/F2/F3 可执行权限校验、解包后的 `install-performance-topology.sh --dry-run`，并验证缺少 F3 二进制会被明确拒绝。该归档和全部临时目录已删除，不可作为正式候选复用。正式窗口仍须从最终冻结 commit 重新构建，记录 archive SHA-256，并完成 temporary `--apply`、Node HTTP 读回、稳定观察和回滚演练。

不得把开发 Linux 或 Docker 的通过结果写成 macOS 或生产通过，也不得因一次 Node health `200` 宣称部署完成。2026-08-12 首次 F3 正式上线发生过硬停机，且错误的 Windows 盘符 API base 使静态页面可加载但所有管理 API 失败；后续 Mac 高性能发布必须使用独立 candidate 槽和 `performance-handover-controller.sh`，完成真实浏览器登录态业务页验证后才切流。同槽原地 apply 不是零停机流程。

## 6. 验收记录格式

每次候选部署在受限运维记录中至少保存以下非敏感事实：

- release commit、archive SHA-256、目标 OS/arch、部署方式和开始/结束时间；
- 配置文件路径与变量名是否齐全，不记录变量值或密钥；
- Node 两个 health、F3 `204`、F1/F2 新鲜度、F3 读回的命令和结果；
- 前端 bundle API-base 扫描、真实浏览器登录态业务页、控制台网络错误与恢复页检查结果；
- candidate/main 的独立 label、端口、PID、route identity、handover journal、access-log 增量和稳定观察结果；
- 四个进程或容器的 PID/名称、日志路径、退出联动结果；
- 使用的临时数据库/Redis namespace、清理或保留结论；
- 失败时的原始错误、未执行的步骤和回滚状态。

不要将请求正文、审计 payload、连接串、Cookie、OAuth token、API Key 或 input secret 写入验收记录。
