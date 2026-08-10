# AI 部署执行清单

> 面向自动化执行者和维护者。本文只给出可复现的部署顺序、可观察成功条件与停止条件；不保存服务器地址、账号、密码、密钥或生产连接串。

## 1. 先确认范围

当前发布拓扑是 Node 主服务加三个 Go sidecar：F1 `juhe-ai-runtime-log-indexer`、F2 `juhe-ai-table-monitor`、F3 `juhe-ai-audit-log-writer`。Node 仍负责网关、账户、主 API、usage、操作日志、公开接口日志、model-check、stats 和 ops。

已完成的运行证据只覆盖隔离开发 Linux：固定 release 的直接启动、Docker standalone 与 Docker performance 均已验证 Node、F1、F2、F3 的闭环，测试资源已经清理。该证据不等于 macOS launchd、生产数据、生产 Docker 常驻、切流或回滚已经验证。

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

任一项无法证明时停止部署，保留原始错误；不得用另一种存储、旧 Node writer、队列或临时 fallback 继续运行。

## 3. 发布包流程

1. 按 [构建指南](构建指南.md) 从固定 commit 打包，解压到新的 release 目录，不覆盖当前运行目录。
2. 复制 `backend/.env.example` 为 `backend/.env`，填写实际 Origin、稳定 `JUHE_AI_SECRET`（若复用现有业务库必须沿用旧值）、F2 stable instance ID，以及完整 F3 配置。`start.sh` / `start.ps1` 只会生成 `JUHE_AI_SECRET` 和 F1 instance ID；不会生成 F2/F3 instance ID 或 F3 input secret。
3. SQLite 默认 F3 配置应使用独立的 `JUHE_AI_AUDIT_LOG_DATABASE_PATH`、`JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY` 和 `JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY`。performance PostgreSQL 模式还必须提供 `JUHE_AI_POSTGRES_URL` 和 F3 blob 目录。
4. 仅通过 release 根目录的 `start.sh` 或 `start.ps1` 启动。它会在 Node DB-ready health 成功后启动 F1、F2、F3，并维持 PID、日志与退出联动；不要绕过它单独常驻 Node、worker 或 sidecar。
5. 验证 Node `/__aisys__/health` 和 `/__aisys__/api/health` 均为 `200`，F3 `GET /__aiinternal__/health` 为 `204`，并检查 `backend/logs/` 中三个 sidecar 的日志。随后通过 Node 只读接口确认 F1 runtime logs 和 F2 snapshots 新鲜，再通过一次真实审计输入的管理端详情读回确认 Node -> F3 -> Node。
6. 升级前保留当前 release 和业务恢复点；生产不得使用会删除数据的清理操作。

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

macOS 公共文档已描述目标 release 和 launchd 编排，但尚无 Mac F1/F2/F3 完整现场验收。生产 Mac 必须先在隔离 temporary release 上验证三项二进制、launchd 生命周期、Node DB-ready 后启动、F3 `204`、F1/F2 数据新鲜度、F3 读回和回滚，才可以讨论切流。

不得把开发 Linux 或 Docker 的通过结果写成 macOS 或生产通过，也不得因一次 Node health `200` 宣称部署完成。

## 6. 验收记录格式

每次候选部署在受限运维记录中至少保存以下非敏感事实：

- release commit、archive SHA-256、目标 OS/arch、部署方式和开始/结束时间；
- 配置文件路径与变量名是否齐全，不记录变量值或密钥；
- Node 两个 health、F3 `204`、F1/F2 新鲜度、F3 读回的命令和结果；
- 四个进程或容器的 PID/名称、日志路径、退出联动结果；
- 使用的临时数据库/Redis namespace、清理或保留结论；
- 失败时的原始错误、未执行的步骤和回滚状态。

不要将请求正文、审计 payload、连接串、Cookie、OAuth token、API Key 或 input secret 写入验收记录。
