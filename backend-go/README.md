# Go 后端完整功能实现

本目录当前承载 F1“运行日志索引与保留”。它已直接接管该完整被动功能；这不表示 Node 的网关、账户管理或管理 API 已迁移。

## F1：运行日志索引与保留

`cmd/juhe-ai-runtime-log-indexer` 是 F1 的 Go 实现。它直接扫描 Node 已落盘的 JSONL 运行日志，按文件 goroutine 并发处理，并直接提交索引、cursor、facet 和保留清理；不使用 Redis Stream、Asynq、任务队列或常驻通用 worker pool。

运行前必须满足：

- 稳定且唯一的 `JUHE_AI_RUNTIME_LOG_INSTANCE_ID`；它用于 Go 多实例 fencing，不是 Node/Go 选择开关。
- `JUHE_AI_RUNTIME_LOG_STORE=sqlite` 或 `postgres`；未设置时只可从 `JUHE_AI_DATABASE_DRIVER` 取得同值。
- SQLite 提供 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH` 和 `JUHE_AI_DATABASE_PATH`；前者是 Go 唯一写入的 F1 专用文件，后者只读 `system_settings.runtimeLogIndexRetentionDays`。PostgreSQL 提供 `JUHE_AI_POSTGRES_URL`，并从 `juhe_business.system_settings` 读取同一设置。
- 两种模式均提供 `JUHE_AI_LOG_DIR`；Go 启动时初始化并验证自己的 F1 schema。

示例：

```powershell
$env:JUHE_AI_RUNTIME_LOG_INSTANCE_ID = 'runtime-log-indexer-a'
$env:JUHE_AI_RUNTIME_LOG_STORE = 'sqlite'
$env:JUHE_AI_RUNTIME_LOG_DATABASE_PATH = 'F:\temp\juhe-ai\runtime-log.sqlite'
$env:JUHE_AI_DATABASE_PATH = 'F:\temp\juhe-ai\business.sqlite'
$env:JUHE_AI_LOG_DIR = 'F:\temp\juhe-ai\logs'
$env:JUHE_AI_RUNTIME_LOG_ONCE = 'true'
go run ./cmd/juhe-ai-runtime-log-indexer
```

主程序初始化并验证 F1 schema。Node 的 importer、保留清理、writer、scheduler 与所有 F1 表的通用清理已下线；Go 是该功能唯一 writer。Node 仅继续产生 JSONL 文件并只读查询 Go 产物。

已存在的 Node F1 索引数据不能在常驻启动时自动复制。先停止 Node 与 Go indexer，设置额外的 `JUHE_AI_DATASET_DATABASE_PATH` 指向旧 dataset 文件，再执行：

```powershell
go run ./cmd/juhe-ai-runtime-log-indexer --migrate-legacy-sqlite
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

未设置 URL 时，该 smoke 会显式 `skipped`，绝不等同 PostgreSQL 验证通过。设为强制模式后，缺 URL 或 URL 无法连接都会失败。本机当前未提供 PostgreSQL，因此本轮未实际执行该真实 smoke。

完整边界、归档和未完成的 PostgreSQL smoke 见 [F1 运行日志索引与保留](../docs/migration/F1-运行日志索引与保留功能冻结.md)。
