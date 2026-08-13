# Go 后端 sidecar

正常常驻入口只有 `cmd/juhe-ai-go-sidecar`（发布二进制名
`juhe-ai-go-sidecar`）。它在同一进程中启动 F1 运行日志索引、F2 表存储
监控、F3 审计日志和 F4 操作日志；每项功能仍独立加载配置、打开 Store、初始化
schema 并持有自己的 owner lease。普通运行错误、owner lease 暂失、组件
panic 或异常返回只会记录该组件的原因、连续失败次数和退避时间，然后仅重启
该组件；不会取消其余组件。外部 `SIGTERM`/`SIGINT`、运行时无法恢复的进程级
故障（例如 OOM 或 runtime fatal），或启动前配置、Store、schema 预检失败，才会
停止整个 sidecar 并交由服务管理器处理。日志是诊断和组件自愈的依据，不会掩盖
进程已经无法继续执行的故障。

常驻 sidecar 不支持 `--once` 或 `JUHE_AI_RUNTIME_LOG_ONCE=true`，因为 F3/F4
必须持续提供本地输入服务。离线迁移收敛到同一二进制的互斥显式模式：
`--migrate-runtime-log-legacy-sqlite`、
`--migrate-audit-log-legacy-sqlite --source-db ... --target-db ...`、
`--migrate-operation-log-legacy-sqlite --operation-log-source-db ...` 和
`--migrate-operation-log-legacy-postgres`
（F3/F4 均必须显式传入 `--node-stopped --go-stopped --backup-confirmed`）。迁移不能与 sidecar
常驻模式并发运行。

以下 F1/F2 章节描述 sidecar 内保留的独立数据契约和环境变量，不再表示旧
独立常驻命令是发布入口。

本目录当前承载 F1“运行日志索引与保留”、F2“表存储监控采样与保留”、F3“原始审计日志”和 F4“操作日志”。四项均由同一 Go sidecar 独立持有各自的存储和 lease；这不表示 Node 的网关、账户管理或其他管理 API 已迁移。

## F4：操作日志离线历史迁移

F4 正常由同一 sidecar 的 `internal/operationlog` 负责签名输入、写入、读取和保留。历史迁移不是常驻启动的一部分，也不能在 Node 或任何 Go sidecar 仍可能写入时执行。两种模式均必须先完成可恢复备份，并显式传入 `--node-stopped --go-stopped --backup-confirmed`。

SQLite 旧 Node dataset 只读复制到已配置的 F4 专库，保留原始稳定 ID、JSON 与 target/viewer 事实，只重建 Go 的派生 search terms：

```powershell
go run ./cmd/juhe-ai-go-sidecar --migrate-operation-log-legacy-sqlite --operation-log-source-db 'F:\path\to\legacy-dataset.sqlite' --node-stopped --go-stopped --backup-confirmed
```

源和目标是同一物理文件（包括 hard link/symlink）会明确拒绝，源库不会被修改。PostgreSQL 的旧 Node 表与 Go F4 表同名并位于 `juhe_dataset`，因此只能在停机窗口内原地升级旧 viewer 三列主键、重建索引和 search terms；不复制到第二个运行中的 owner：

```powershell
go run ./cmd/juhe-ai-go-sidecar --migrate-operation-log-legacy-postgres --node-stopped --go-stopped --backup-confirmed
```

两条命令都会输出 JSON 计数和迁移结果。它们不是生产切流授权：完成后仍须按 [F4 操作日志完整迁移契约](../docs/migration/F4-操作日志完整迁移契约.md) 核验可读性、首尾样本、发布候选与回滚提交。

## F1：运行日志索引与保留

sidecar 内的 F1 直接扫描 Node 已落盘的 JSONL 运行日志，按文件 goroutine 并发处理，并直接提交索引、cursor、facet 和保留清理；不使用 Redis Stream、Asynq、任务队列或常驻通用 worker pool。

运行前必须满足：

- 稳定且唯一的 `JUHE_AI_RUNTIME_LOG_INSTANCE_ID`；它用于 Go 多实例 fencing，不是 Node/Go 选择开关。
- `JUHE_AI_RUNTIME_LOG_STORE=sqlite` 或 `postgres`；未设置时只可从 `JUHE_AI_DATABASE_DRIVER` 取得同值。
- SQLite 提供 F1 专用 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH`，以及业务、数据集目录、usage catalog、stats 和 Codex Context shard 的路径。F1 会拒绝这些任一路径、F2 专用库或任一 shard 与 F1 指向同一个物理 SQLite 文件；只读业务库的 `system_settings.runtimeLogIndexRetentionDays`。PostgreSQL 提供 `JUHE_AI_POSTGRES_URL`，并从 `juhe_business.system_settings` 读取同一设置。
- 两种模式均提供 `JUHE_AI_LOG_DIR`；Go 启动时初始化并验证自己的 F1 schema。

示例：

```powershell
$env:JUHE_AI_RUNTIME_LOG_INSTANCE_ID = 'go-sidecar-runtime-log-a'
$env:JUHE_AI_RUNTIME_LOG_STORE = 'sqlite'
$env:JUHE_AI_RUNTIME_LOG_DATABASE_PATH = 'F:\temp\juhe-ai\runtime-log.sqlite'
$env:JUHE_AI_TABLE_MONITOR_DATABASE_PATH = 'F:\temp\juhe-ai\table-monitor.sqlite'
$env:JUHE_AI_DATABASE_PATH = 'F:\temp\juhe-ai\business.sqlite'
$env:JUHE_AI_DATASET_DATABASE_PATH = 'F:\temp\juhe-ai\dataset.sqlite'
$env:JUHE_AI_USAGE_CATALOG_DATABASE_PATH = 'F:\temp\juhe-ai\usage-catalog.sqlite'
$env:JUHE_AI_STATS_DATABASE_PATH = 'F:\temp\juhe-ai\stats.sqlite'
$env:JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = 'F:\temp\juhe-ai\codex-context\state-shards'
$env:JUHE_AI_LOG_DIR = 'F:\temp\juhe-ai\logs'
go run ./cmd/juhe-ai-go-sidecar
```

主程序初始化并验证 F1 schema。Node 的 importer、保留清理、writer、scheduler 与所有 F1 表的通用清理已下线；Go 是该功能唯一 writer。Node 仅继续产生 JSONL 文件并只读查询 Go 产物。

已存在的 Node F1 索引数据不能在常驻启动时自动复制。先停止 Node 与 Go indexer，设置 `JUHE_AI_DATASET_DATABASE_PATH` 指向旧 dataset 文件，并提供所有 SQLite owner 路径以完成物理隔离校验，再执行：

```powershell
go run ./cmd/juhe-ai-go-sidecar --migrate-runtime-log-legacy-sqlite
```

该一次性命令会校验源库完整性、F1 表、数量、主键和字段值；失败时保留原文件，不能回退到 Node F1 或将 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH` 改成 dataset 文件。

验证：

```powershell
rtk go test ./...
```

### PostgreSQL adapter 真实 smoke

在 `backend-go` 工作目录执行。`JUHE_AI_RUNTIME_LOG_POSTGRES_SMOKE_URL` 必须指向专用、可销毁的 PostgreSQL 数据库，并且运行前 `juhe_dataset` 的全部 F1 表必须为空；测试会初始化 schema、真实写入并通过 adapter 清理自己的数据，不会对非空数据库执行。

```powershell
$env:JUHE_AI_RUNTIME_LOG_POSTGRES_SMOKE_URL = 'postgres://<user>:<password>@<host>:5432/<database>?sslmode=disable'
$env:JUHE_AI_RUNTIME_LOG_POSTGRES_SMOKE_REQUIRED = 'true'
rtk go test -race ./internal/runtimelog -run '^TestPostgresRuntimeLogAdapterSmoke$' -count=1
```

2026-08-09 已在用户授权的专用、可销毁开发 PostgreSQL 数据库中强制执行并通过该 smoke；测试结束后已终止残留会话并删除临时数据库，连接串未写入仓库或文档。未设置 URL 时，该 smoke 会显式 `skipped`，绝不等同 PostgreSQL 验证通过；设为强制模式后，缺 URL 或 URL 无法连接都会失败。

完整边界、归档和 PostgreSQL smoke 记录见 [F1 运行日志索引与保留](../docs/migration/F1-运行日志索引与保留功能冻结.md)。

## F2：表存储监控采样与保留

sidecar 内的 F2 是唯一采样、快照写入和表监控历史保留 owner。它在 SQLite 和 PostgreSQL 两种正式模式下直接异步并发采样；不使用 queue、Redis、Asynq、Node IPC、Node/Go 开关、fallback 或双 writer。

运行前必须满足：

- 稳定且唯一的 `JUHE_AI_TABLE_MONITOR_INSTANCE_ID`；它用于 owner lease fencing，不是 Node/Go 选择开关。
- `JUHE_AI_TABLE_MONITOR_STORE=sqlite` 或 `postgres`；未设置时只可从 `JUHE_AI_DATABASE_DRIVER` 取得同值。
- SQLite 提供 `JUHE_AI_TABLE_MONITOR_DATABASE_PATH` 作为 F2 专用输出文件，`JUHE_AI_RUNTIME_LOG_DATABASE_PATH` 用于验证 F1/F2 物理隔离，以及 `JUHE_AI_DATABASE_PATH`、`JUHE_AI_DATASET_DATABASE_PATH`、`JUHE_AI_USAGE_CATALOG_DATABASE_PATH`、`JUHE_AI_STATS_DATABASE_PATH` 和 `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT` 作为只读采样源；输出文件不得与 F1、任何源库或 shard 共用。PostgreSQL 提供 `JUHE_AI_POSTGRES_URL`，快照写入 `juhe_stats`。
- `JUHE_AI_TABLE_MONITOR_OWNER_LEASE` 默认 `5m`；同一事实库同一时间只允许一个 Go owner，第二实例拒绝启动，失去 lease 后采样和保留清理拒写。
- `JUHE_AI_TABLE_MONITOR_INTERVAL` 默认 `1m`，单轮 `JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT` 默认 `45s`，`JUHE_AI_TABLE_MONITOR_RETENTION_DAYS` 默认 `30`，`JUHE_AI_TABLE_MONITOR_MAX_TABLES` 默认 `256`；`JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES` 默认 `8`（范围 `1..256`），`JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE` 默认 `1000`（范围 `1..10000`），`JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES` 默认 `1000`（范围 `1..100000`）。达到 retention 批次数上限后仍有过期数据才显式失败，不静默遗漏。

Node 只保留表监控 HTTP 读取，SQLite 读取只打开 F2 专用输出文件；Node scheduler、stats writer 和 Node retention 已退出。配置、连接、schema、owner lease 或采样失败都必须保留原始错误并显式失败，不能伪造空结果或切回旧 Node 路径。

SQLite 定向测试覆盖采样、快照写入和保留清理。2026-08-09 开发 PostgreSQL/PgBouncer 可连通性已确认；共享开发库已有 F2 快照，smoke 对该库按空库保护拒绝写入。经用户明确授权创建一次性空库后，强制 smoke 已通过真实 adapter、lease takeover、relation-size 快照、五个 schema 的 `RunOnce` 采样和 retention 清理，测试库已删除。生产发布生命周期、Docker 实启动和 listener 仍未验证，不能据此称生产运行通过。完整边界见 [F2 表存储监控采样与保留功能冻结](../docs/migration/F2-表存储监控采样与保留功能冻结.md)。
