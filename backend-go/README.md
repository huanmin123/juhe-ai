# Go 后端迁移层

## 项目拓扑

最终 Go 后端拆为三个可独立构建和部署的项目：

- `projects/gateway`：管理 API、公开 API 和 AI 上游桥接。
- `projects/jobs`：定时探活、复制、统计和周期维护。
- `projects/maintenance`：一次性 schema、迁移、回填、重建和诊断。

三个项目只共享 `shared/contracts` 和无业务编排的 `shared/platform`，互相禁止 import。项目边界、并发和迁移顺序见 [Go 三项目架构基线](../docs/migration/Go三项目架构基线.md) 与 [Go 开发手册](../docs/migration/Go开发手册.md)。

`shared/platform` 还提供无业务的 `sqlpool` 生命周期/引用计数基础设施；gateway/jobs 只保留各自的 `pgx` opener 和项目薄适配层。`shared/platform/upstreamhttp` 是跨项目的上游传输基础设施：统一直连与 HTTP(S)/SOCKS5(SOCKS5H) 代理、禁用环境代理、HTTP/2、响应头大小/超时、无重定向 client、SOCKS5 握手和有界响应体读取/排空。它不包含供应商协议、SSE、余额适配器、探测结果或重试策略。当前 `jobs` 的 J1 健康探活、J2 余额查询和 J3a 代理延迟/出口检测都使用该包；`gateway` 当前还没有生产上游 client，`maintenance` 当前没有上游请求。

当前常驻入口是 `projects/jobs/cmd/juhe-ai-jobs` 与 `projects/gateway/cmd/juhe-ai-gateway`：

- `jobs` 运行 F1 运行日志索引与保留、F2 表存储监控采样与保留。
- `gateway` 当前运行 F3 原始审计日志、F4 操作日志的本机签名输入、读取与保留；它尚未接管 Node 的对外 API 或 AI 桥接。

每个项目独立加载配置、Store、schema 和 owner lease。普通单条或单轮错误必须记录并由组件的下一轮处理；租约丢失和不可恢复基础设施错误只影响所属项目，由该项目的服务管理器重启，绝不通过进程内依赖影响另一项目。

## J3a PostgreSQL 一次性 bootstrap

`projects/maintenance` 的 J3a bootstrap 不是常驻服务。它和 jobs runtime 共享同一 J3a PostgreSQL schema 契约：6 张 jobs 表、2 个索引、必需列的名称/类型/`NOT NULL`、主键/唯一约束均须匹配。maintenance 只检查或加法预置，不会创建 schema、改变 owner、`ALTER` 既有表、访问 `juhe_business`、调用 Node、写 Goose ledger 或作为 jobs runtime 的 fallback。默认检查缺项或结构不符时返回 `3`；`--apply` 需要经过授权、使用已拥有该 schema 的专用维护/目标 jobs 角色，并在完成后再次只读检查。jobs runtime 的 `Store.CheckSchema` 也会执行该结构 preflight，无法绕过 maintenance 后以同名畸形表启动。

```powershell
$env:JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL = 'postgres://<schema-owner-role>:<secret>@<host>:5432/<database>?sslmode=require'
go run ./projects/maintenance/cmd/juhe-ai-maintenance --check-j3a-proxy-latency-postgres
go run ./projects/maintenance/cmd/juhe-ai-maintenance --apply-j3a-proxy-latency-postgres
go run ./projects/maintenance/cmd/juhe-ai-maintenance --check-j3a-proxy-latency-postgres
Remove-Item Env:JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL -ErrorAction SilentlyContinue
```

维护项目的真实 bootstrap 回归也是显式 opt-in，仅接受直连 `5432` 的一次性 `juhe_ai_sub2api_dev_j3a_` scratch，且要求管理员预先创建 `juhe_jobs` schema：

```powershell
$env:JUHE_AI_MAINTENANCE_J3A_BOOTSTRAP_SMOKE_URL = 'postgres://<schema-owner-role>:<secret>@<host>:5432/juhe_ai_sub2api_dev_j3a_<name>?sslmode=require'
go test ./projects/maintenance/internal/j3aproxylatency -run '^TestPostgresBootstrapSmoke$' -count=1
Remove-Item Env:JUHE_AI_MAINTENANCE_J3A_BOOTSTRAP_SMOKE_URL -ErrorAction SilentlyContinue
```

该测试要求 scratch 初始缺少全部 J3a 表/索引，成功后保留 schema 供管理员按生命周期销毁；不会自行创建或删除数据库/schema。

jobs runtime 的对应 opt-in 检查：

```powershell
$env:J3A_PG_SCHEMA_CONTRACT_SMOKE_URL = 'postgres://<schema-owner-role>:<secret>@<host>:5432/juhe_ai_sub2api_dev_j3a_<name>?sslmode=require'
go test ./projects/jobs/internal/proxylatency -run '^TestPostgresSchemaContractSmoke$' -count=1
Remove-Item Env:J3A_PG_SCHEMA_CONTRACT_SMOKE_URL -ErrorAction SilentlyContinue
```

它验证完整结构能被 runtime 接受、错误列会在启动 preflight 被拒绝；同样只适用于可销毁 scratch。

生产应用连接、Secret 值、runtime owner 开关与发布授权不属于该命令；完整 handoff 条件见 [J3a 代理延迟检测完整迁移契约](../docs/migration/J3a-代理延迟检测完整迁移契约.md)。

离线迁移也按 owner 分开执行：F1 使用 `juhe-ai-jobs --migrate-runtime-log-legacy-sqlite`；F3/F4 使用 `juhe-ai-gateway --migrate-audit-log-legacy-sqlite`、`--migrate-operation-log-legacy-sqlite` 或 `--migrate-operation-log-legacy-postgres`。F3/F4 离线模式必须显式传入 `--node-stopped --go-stopped --backup-confirmed`，不得和相应常驻 owner 并发运行。

## F4：操作日志离线历史迁移

F4 正常由 `projects/gateway/internal/operationlog` 负责签名输入、写入、读取和保留。历史迁移不是常驻启动的一部分，也不能在 Node 或任何 Go owner 仍可能写入时执行。两种模式均必须先完成可恢复备份，并显式传入 `--node-stopped --go-stopped --backup-confirmed`。

SQLite 旧 Node dataset 只读复制到已配置的 F4 专库，保留原始稳定 ID、JSON 与 target/viewer 事实，只重建 Go 的派生 search terms：

```powershell
go run ./projects/gateway/cmd/juhe-ai-gateway --migrate-operation-log-legacy-sqlite --operation-log-source-db 'F:\path\to\legacy-dataset.sqlite' --node-stopped --go-stopped --backup-confirmed
```

源和目标是同一物理文件（包括 hard link/symlink）会明确拒绝，源库不会被修改。PostgreSQL 的旧 Node 表与 Go F4 表同名并位于 `juhe_dataset`，因此只能在停机窗口内原地升级旧 viewer 三列主键、重建索引和 search terms；不复制到第二个运行中的 owner：

```powershell
go run ./projects/gateway/cmd/juhe-ai-gateway --migrate-operation-log-legacy-postgres --node-stopped --go-stopped --backup-confirmed
```

两条命令都会输出 JSON 计数和迁移结果。它们不是生产切流授权：完成后仍须按 [F4 操作日志完整迁移契约](../docs/migration/F4-操作日志完整迁移契约.md) 核验可读性、首尾样本、发布候选与回滚提交。

## F1：运行日志索引与保留

`jobs` 内的 F1 直接扫描 Node 已落盘的 JSONL 运行日志，按文件 goroutine 并发处理，并直接提交索引、cursor、facet 和保留清理；不使用 Redis Stream、Asynq、任务队列或常驻通用 worker pool。

运行前必须满足：

- 稳定且唯一的 `JUHE_AI_RUNTIME_LOG_INSTANCE_ID`；它用于 Go 多实例 fencing，不是 Node/Go 选择开关。
- `JUHE_AI_RUNTIME_LOG_STORE=sqlite` 或 `postgres`；必须显式设置，不再从通用数据库变量回退。
- SQLite 提供 F1 专用 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH`，以及业务、数据集目录、usage catalog、stats 和 Codex Context shard 的路径。F1 会拒绝这些任一路径、F2 专用库或任一 shard 与 F1 指向同一个物理 SQLite 文件；只读业务库的 `system_settings.runtimeLogIndexRetentionDays`。PostgreSQL 必须提供专用 `JUHE_AI_RUNTIME_LOG_POSTGRES_URL`，并从 `juhe_business.system_settings` 读取同一设置。
- 两种模式均提供 `JUHE_AI_LOG_DIR`；Go 启动时初始化并验证自己的 F1 schema。

示例：

```powershell
$env:JUHE_AI_RUNTIME_LOG_INSTANCE_ID = 'go-jobs-runtime-log-a'
$env:JUHE_AI_RUNTIME_LOG_STORE = 'sqlite'
$env:JUHE_AI_RUNTIME_LOG_DATABASE_PATH = 'F:\temp\juhe-ai\runtime-log.sqlite'
$env:JUHE_AI_TABLE_MONITOR_DATABASE_PATH = 'F:\temp\juhe-ai\table-monitor.sqlite'
$env:JUHE_AI_DATABASE_PATH = 'F:\temp\juhe-ai\business.sqlite'
$env:JUHE_AI_DATASET_DATABASE_PATH = 'F:\temp\juhe-ai\dataset.sqlite'
$env:JUHE_AI_USAGE_CATALOG_DATABASE_PATH = 'F:\temp\juhe-ai\usage-catalog.sqlite'
$env:JUHE_AI_STATS_DATABASE_PATH = 'F:\temp\juhe-ai\stats.sqlite'
$env:JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = 'F:\temp\juhe-ai\codex-context\state-shards'
$env:JUHE_AI_LOG_DIR = 'F:\temp\juhe-ai\logs'
go run ./projects/jobs/cmd/juhe-ai-jobs
```

主程序初始化并验证 F1 schema。Node 的 importer、保留清理、writer、scheduler 与所有 F1 表的通用清理已下线；Go 是该功能唯一 writer。Node 仅继续产生 JSONL 文件并只读查询 Go 产物。

已存在的 Node F1 索引数据不能在常驻启动时自动复制。先停止 Node 与 Go indexer，设置 `JUHE_AI_DATASET_DATABASE_PATH` 指向旧 dataset 文件，并提供所有 SQLite owner 路径以完成物理隔离校验，再执行：

```powershell
go run ./projects/jobs/cmd/juhe-ai-jobs --migrate-runtime-log-legacy-sqlite
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

`jobs` 内的 F2 是唯一采样、快照写入和表监控历史保留 owner。它在 SQLite 和 PostgreSQL 两种正式模式下直接异步并发采样；不使用 queue、Redis、Asynq、Node IPC、Node/Go 开关、fallback 或双 writer。

运行前必须满足：

- 稳定且唯一的 `JUHE_AI_TABLE_MONITOR_INSTANCE_ID`；它用于 owner lease fencing，不是 Node/Go 选择开关。
- `JUHE_AI_TABLE_MONITOR_STORE=sqlite` 或 `postgres`；必须显式设置，不再从通用数据库变量回退。
- SQLite 提供 `JUHE_AI_TABLE_MONITOR_DATABASE_PATH` 作为 F2 专用输出文件，`JUHE_AI_RUNTIME_LOG_DATABASE_PATH` 用于验证 F1/F2 物理隔离，以及 `JUHE_AI_DATABASE_PATH`、`JUHE_AI_DATASET_DATABASE_PATH`、`JUHE_AI_USAGE_CATALOG_DATABASE_PATH`、`JUHE_AI_STATS_DATABASE_PATH` 和 `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT` 作为只读采样源；输出文件不得与 F1、任何源库或 shard 共用。PostgreSQL 必须提供专用 `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`，快照写入 `juhe_stats`。
- `JUHE_AI_TABLE_MONITOR_OWNER_LEASE` 默认 `5m`；同一事实库同一时间只允许一个 Go owner，第二实例拒绝启动，失去 lease 后采样和保留清理拒写。
- `JUHE_AI_TABLE_MONITOR_INTERVAL` 默认 `1m`，单轮 `JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT` 默认 `45s`，`JUHE_AI_TABLE_MONITOR_RETENTION_DAYS` 默认 `30`，`JUHE_AI_TABLE_MONITOR_MAX_TABLES` 默认 `256`；`JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES` 默认 `8`（范围 `1..256`），`JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE` 默认 `1000`（范围 `1..10000`），`JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES` 默认 `1000`（范围 `1..100000`）。达到 retention 批次数上限后仍有过期数据才显式失败，不静默遗漏。

Node 只保留表监控 HTTP 读取，SQLite 读取只打开 F2 专用输出文件；Node scheduler、stats writer 和 Node retention 已退出。配置、连接、schema、owner lease 或采样失败都必须保留原始错误并显式失败，不能伪造空结果或切回旧 Node 路径。

SQLite 定向测试覆盖采样、快照写入和保留清理。2026-08-09 开发 PostgreSQL/PgBouncer 可连通性已确认；共享开发库已有 F2 快照，smoke 对该库按空库保护拒绝写入。经用户明确授权创建一次性空库后，强制 smoke 已通过真实 adapter、lease takeover、relation-size 快照、五个 schema 的 `RunOnce` 采样和 retention 清理，测试库已删除。生产发布生命周期、Docker 实启动和 listener 仍未验证，不能据此称生产运行通过。完整边界见 [F2 表存储监控采样与保留功能冻结](../docs/migration/F2-表存储监控采样与保留功能冻结.md)。
