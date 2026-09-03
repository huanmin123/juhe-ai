# Go 系统指标字段迁移清单

> **现行口径（2026-08-12）。** 本文的 W6-W10 条目是旧 PG/Redis-only 迁移记录，不是当前删除授权。现行 B0、L1-L4 保留 SQLite 与 PostgreSQL/Redis 两种模式：SQLite 下 Node DB service、SQLite 文件和 usage shard 仍可作为 `runtimeKind=node` 观测；只有 Go 自己的 `runtimeKind=go` DTO 不得伪装为 Node event loop 或 DB service。

## 1. 文档目标

本文是 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md) 的字段清单。W6 / W7 / W8 保留为历史索引；当前按 B0、L1-L4 为每个完整功能的 Go runtime、Store 与直接异步执行分别验收。

本文不描述当前 Node 运行态的正确性。当前 Node 后端在未接管前仍可以继续使用事件循环、DB service 和 SQLite 文件指标；Go owner 接管后，下面列为删除的字段不能再作为正式 API、前端类型、Prometheus 指标、窗口表或告警口径存在。

## 当前已落地（2026-09-02）

- Go `gometrics.Collector` 已输出低基数、跨平台 runtime Prometheus 指标，固定维度为 `service`、`role`、`runtimeKind=go`；当前挂载到 gateway 和 active jobs 的 loopback health listener。除 goroutine、thread、heap 外，还暴露 Go runtime CPU 容量累计时间、基于 `total-idle` 的 busy CPU 速率、uptime，以及 scheduler / GC 原始 histogram。CPU 速率按单核 100% 计算，多核进程可以超过 100%。RSS/FD 不由该 collector 采集，避免 Windows/Linux 分支造成不同语义；如需主机容量观测，由 host metrics owner 统一提供。
- Collector 在未启用持久化 sampler 的 gateway 等场景中，会在每次 Prometheus scrape 时刷新 scalar gauges；该行为不写入趋势窗口，避免首次抓取显示为未采样的零值或后续读到陈旧值。
- 启用 `JUHE_AI_GO_RUNTIME_METRICS_STORE` 后，jobs 以可配置周期记录 Go runtime scalar snapshot，并由独立 Store 幂等写入 `go_runtime_metrics_samples`、`go_runtime_metrics_hourly`、`go_runtime_metrics_trend_windows`；聚合维度仅为 `service + role + runtime_kind=go + 时间窗口`，默认保留 30 天。scalar 采样包含 goroutine runnable/waiting、GOMAXPROCS、Go runtime CPU、uptime；历史 schema 中的 RSS/FD nullable 列仅为兼容旧样本，新的跨平台 collector 不写入。
- 持久化表位于 PostgreSQL `juhe_stats` schema（SQLite 模式使用独立文件），由 maintenance 显式 `--apply-go-runtime-metrics` 创建；jobs 启动只读校验，不自动 DDL。
- jobs loopback 管理端点 `GET /__aisys__/api/stats/go-runtime-trend` 只读小时预聚合，并显式返回 `runtimeKind=go`；不查询或写入 Node `system_metrics_*` / `process_event_loop_*`。
- scheduler latency、GC pause 继续以 Prometheus histogram 暴露，不能从单次 scrape 直接解释为 P95/P99；Go SQL 趋势只做均值/最大值，绝不把每个采样的 P95/P99 再平均。页面只有在外部 Prometheus/实时适配器提供合法分位数时才显示健康视图。
- 当前 Go 趋势页已接入现有系统指标页：前端通过 Node 同源管理员代理 `/__aisys__/api/stats/system-metrics/go-runtime-trend` 读取独立 Go 预聚合，并与 Node 原有图表并列展示；Go 卡片分为并发、内存和 runtime 摘要（Go CPU/uptime），不映射 `eventLoopLagMs` 或 Node RSS。PG/Redis、HTTP RED、业务任务状态仍未接入，缺失字段不以 0 或 Node 字段占位。

## 2. 迁移原则

- `runtimeKind` 必须显式表达运行时，Go 接管后返回 `go` 或等价版本字段；不能让前端通过字段是否存在猜测运行时。
- Node 事件循环指标只属于 Node 过渡事实。Go 不能把 scheduler latency、goroutine 堆积、GC pause 或 mutex wait 命名为 `eventLoopLagMs`。
- Go runtime 不保留 `db-service` 角色。PostgreSQL/Redis profile 由 pool、query latency、transaction、lock / timeout 和 worker lag 表达；SQLite profile 的 Node DB service 仍以 `runtimeKind=node` 表达，不能从 Node 观测中删除。
- 内部系统监控 API 读取 PostgreSQL 预聚合窗口；Prometheus `/__aisys__/metrics` 用于外部采集；pprof 只用于受控排障。三者不能互相替代。
- 不可观测必须返回 `sampleAvailable=false` 和 `null`，不能用 `0`、空数组或默认时间伪装正常。
- 指标 label 必须低基数。系统账户 ID、API Key ID、AI 账户 ID、分组 ID、trace ID、明文 IP、SQL 文本、Redis key、模型 prompt、token 和错误消息原文不能进入 Prometheus label。
- 系统指标统计迁移必须有独立实施记录和删除证据，不能只在 Go API 中临时拼出旧 Node 响应结构；W6 / W7 之前只允许保留 Node 过渡事实，Go owner 接管时必须同步改后端 DTO、前端类型、窗口表查询和页面 smoke。

## 3. Node 字段删除清单

Go owner 接管 `/__aisys__/api/stats/system-metrics` 时，以下字段不得继续作为长期响应字段返回。

| 当前字段 / 表 / 角色 | 当前用途 | Go 处理 | 删除证据 |
| --- | --- | --- | --- |
| `eventLoopLagMs` | Node 事件循环额外延迟 | 删除；Go 使用 `schedulerLatencyP95Ms` / `schedulerLatencyP99Ms` 等 Go runtime 字段 | `rg "eventLoopLagMs" backend-go frontend/src` 只剩历史文档或无结果 |
| `eventLoopLagMsSampleCount` / `eventLoopLagMsAvg` / `eventLoopLagMsMax` | Node 事件循环窗口聚合 | 删除；Go runtime 窗口使用 scheduler / GC / goroutine 字段 | 前端图表和类型不再引用 |
| `processEventLoopLatestStatus` | 各 Node 角色最新事件循环采样 | 替换为 `goRuntimeLatestStatus` 或 `runtimeMetrics.latestByRole` | API 契约测试断言旧字段不存在 |
| `processEventLoopPeakStatus` | 最近 24 小时 Node 事件循环峰值 | 替换为 `goRuntimePeakStatus` 或 `runtimeMetrics.peakByRole` | 前端系统指标 smoke 通过 |
| `processEventLoopTrend` | 事件循环 / 进程内存趋势窗口 | 替换为 `goRuntimeTrend` 或 `runtimeMetrics.trend` | `statsChartOptions` 不再构建事件循环图 |
| `process_event_loop_samples` | Node 进程事件循环原始采样表 | Go 长期 schema 不继续写入；可作为历史离线表或删除 | Go migration 不创建该表作为 runtime 表 |
| `process_event_loop_hourly` | Node 进程事件循环小时聚合 | 替换为 `go_runtime_metrics_hourly` 或等价表 | Go store 不查询该表 |
| `process_event_loop_trend_windows` | Node 进程趋势窗口 | 替换为 `go_runtime_metrics_trend_windows` 或等价表 | Go 系统监控查询不读取该表 |
| `processHeapUsedBytes` / `processHeapTotalBytes` | V8 heap 指标 | 删除；Go 使用 `heapAllocBytes`、`heapLiveBytes`、`heapObjects`、`memoryClassesTotalBytes` | 前端类型不再包含 V8 字段 |
| `processExternalBytes` / `processArrayBuffersBytes` | V8 external / buffer 指标 | 删除；Go 不模拟 | 前端表格列删除 |
| `db-service` process role | SQLite DB service 运行态 | Go 角色不包含它；SQLite profile 可按 `runtimeKind=node` 继续显示 | Go DTO / Go role 枚举不含该角色，Node DTO 显式标识运行时 |
| SQLite 文件体积 / usage shard 文件路径 | Node standalone 存储观测 | SQLite profile 保留；PostgreSQL/Redis profile 改用 table / index / partition 与直接异步执行状态 | profile 与 `runtimeKind` 明确，禁止互相冒充 |
| DB service snapshot / IPC pending 指标 | Node DB service 和 IPC 胶水观测 | SQLite profile 保留；Go runtime 改用对应 Store、直接异步执行和功能 heartbeat | 页面按 profile / runtimeKind 分视图，不将 Node snapshot 写进 Go DTO |

允许保留的例外：历史文档、未迁移 Node 代码、迁移记录和明确标注为 Node 过渡事实的测试基线。Go runtime、Go API DTO、Go 前端目标类型和 Go 发布文档中不得保留这些字段作为正式口径。

历史 2026-07-22 opt-in `GET /__aisys__/api/stats/system-metrics` reader 记录只能作为旧 PostgreSQL 灰度对照；现行 B0/L1-L4 也不以只读路由作为完整功能接管证据。

## 4. Go 字段目标清单

Go 系统监控接口建议保留当前路径 `GET /__aisys__/api/stats/system-metrics`，但响应模型切换为以下分组。字段名可以在实现时微调，但语义和删除边界必须保持一致。

| 分组 | 推荐字段 | owner | 页面用途 |
| --- | --- | --- | --- |
| `runtimeKind` | `go`、`goVersion`、`schemaVersion` | 已接管完整功能的观测 owner | 前端切换 Go 系统监控视图和类型 |
| `hostMetrics` | CPU、OS memory、RSS、network in/out、FD / Windows handle、uptime | 功能观测 owner | 主机资源趋势和容量判断 |
| `runtimeMetrics.latestByRole` | `processRole`、`sampleAvailable`、`processPid`、`sampledAt`、`goroutines`、`goroutinesRunnable`、`schedulerLatencyP95Ms`、`schedulerLatencyP99Ms`、`gcPauseP95Ms`、`gcPauseP99Ms`、`heapAllocBytes`、`heapLiveBytes`、`threadsTotal`、`cpuPercent` | 各 Go 功能 role 暴露，功能观测 owner 聚合 | Go runtime 最新状态表 |
| `runtimeMetrics.peakByRole` | 最近 24 小时 goroutine、scheduler、GC、heap、CPU 峰值 | 功能观测 owner 聚合 | 容量和泄漏排查 |
| `runtimeMetrics.trend` | 按窗口 bucket 的 goroutine、scheduler、GC、heap、CPU、sampleCount | 功能观测 owner 聚合 | Go runtime 趋势图 |
| `storageHealth.postgres` | pool acquired / idle / total / max、acquire P95、query P95 / P99、error、timeout、lock timeout、deadlock、table / index / partition size | Go store / 功能观测 owner | 识别 PG 连接池、慢查询和表增长 |
| `storageHealth.redis` | cache / state role、operation latency、pool、timeout、cache hit / miss | Redis adapter / 功能观测 owner | 识别 Redis 连接异常 |
| `asyncHealth` | feature、running / completed / failed / cancelled、restartRecovered、duration P95 | Go runtime | 直接异步功能执行与恢复状态 |
| `taskHealth` | 功能 role ready、heartbeat、shutdown drain、last error、cursor 状态 | Go 功能 runtime | 后台功能可用性 |
| `statsFreshness` | usage aggregation lag、range window lag、system metrics lag、last cursor、last successful refresh | stats-worker | 判断业务统计新鲜度 |
| `gatewaySli` | request total、success rate、P95 / P99、SSE started / completed / aborted、upstream first byte / first token、backpressure wait、side effect direct commit | 未来网关完整功能 owner | 真实网关迁移后容量和稳定性 |

首个完整功能只实现自身需要的最小字段，不能为了赶前端而返回旧 Node 字段。字段暂缺时返回 `sampleAvailable=false` 或该分组 `available=false`，并在文档和测试中标明未覆盖原因。

## 5. 采样与存储 owner

| 数据 | 写入 owner | 推荐表 / 存储 | 规则 |
| --- | --- | --- | --- |
| 主机系统采样 | 已接管完整功能的观测 owner | `system_metrics_samples` / `system_metrics_hourly` / `system_metrics_trend_windows` | 可复用系统趋势概念，但字段改为 Go / PG / Redis / 直接异步执行口径 |
| Go runtime 采样 | 各 Go 功能 role 暴露，按功能观测 owner 聚合 | `go_runtime_metrics_samples` / `go_runtime_metrics_hourly` / `go_runtime_metrics_trend_windows` | 按 `processRole` 保存；角色不包含 `db-service` |
| PostgreSQL pool / query | store adapter + stats-worker | PG 窗口表或 `system_metrics_*` 扩展字段 | `operation` 必须是低基数业务枚举，不保存 SQL 文本 |
| Redis cache / state | Redis adapter + 功能观测 owner | PG 窗口表或 Prometheus | cache / state 必须能区分；未迁移 Node queue 另按 Node 观测，不成为 Go 功能依赖 |
| 直接异步执行 | Go 功能 runtime | async runtime snapshots 或窗口表 | 记录 running、completed、failed、cancelled、restartRecovered、cursor 和低基数 feature |
| stats freshness | Node stats owner 或已接管的完整功能 owner | 功能状态 / freshness 窗口 | 页面读预聚合结果，不从 usage 明细现场计算 |
| pprof profile | 人工排障 | `reports/` 原始产物 | 不自动写入业务库或统计库 |

## 6. 前端迁移清单

前端系统监控页面迁移时必须同步处理以下文件和语义：

| 位置 | 当前问题 | Go 目标 |
| --- | --- | --- |
| `frontend/src/types/domain/usage-stats.ts` | `SystemMetricsOverview` 仍包含 Node `processEventLoop*` / `eventLoopLagMs` 字段 | 增加 Go runtime DTO；Go owner 后删除或隔离 Node DTO |
| `frontend/src/views/stats/SystemMetricsStatsView.vue` | 页面文案和计算属性仍围绕事件循环趋势 | 根据 `runtimeKind=go` 展示 Go runtime、PG、Redis、直接异步执行和 stats freshness |
| `frontend/src/views/stats/statsChartOptions.ts` | `buildProcessEventLoopOption`、`processEventLoopRoles` 固定包含 `db-service` | 替换为 Go runtime 图表，角色只包含 Go roles |
| `frontend/src/views/stats/StatsProcessEventLoopTable.vue` | 表格列展示事件循环和 V8 memory | 替换为 Go runtime 状态表 |
| `frontend/src/views/stats/statsProcessEventLoop.ts` | 角色顺序和字段是 Node 专属 | 删除或改为 `statsGoRuntime.ts` |
| `frontend/src/types/domain/base.ts` | `ProcessRole` 包含 `db-service` | Go 系统监控角色类型不得包含 `db-service`；未迁移 Node 页面例外需要显式命名 |

前端迁移验收必须运行类型检查和页面 smoke。页面可以短期支持 Node / Go 双视图用于灰度对照，但必须通过 `runtimeKind` 明确分支，不能把 Go 数据塞进 Node `processEventLoop*` 字段。

## 7. 当前 B0、L1-L4 字段门禁

| 阶段 | 必须完成 | 不允许 |
| --- | --- | --- |
| B0 | 两个 profile 的 `runtimeKind`、Store / 直接异步执行和启动错配字段可辨 | 将 SQLite Node DB service 或 event loop 字段伪装为 Go runtime |
| L1 | 完整功能的 Node / Go 运行指标边界和归档计划明确 | 用局部 Go 指标证明完整功能已接管 |
| L2/L3 | 已接管功能的 running、write latency、cancel、failure、restart recovery 和 owner 指标按 profile 验收 | 用未接管功能的 Go 指标证明 Node 路径已删除 |
| L4 | 活跃 Node 功能指标清零，归档目录不生成 runtime 指标 | 删除 SQLite 模式的 DB service、文件或 Node 观测 |

### 历史 W6-W10 字段门禁（非当前执行依据）

| 波次 | 必须完成 | 不允许 |
| --- | --- | --- |
| W6 记录与统计读 API | Go 系统监控 API 契约测试、Go runtime DTO、PG 窗口查询、前端类型分支 | 返回 `eventLoopLagMs`、`processEventLoop*` 或 `db-service` 作为 Go 正式字段 |
| W7 worker | stats-worker 采样 Go runtime、Asynq queue、worker heartbeat、stats freshness；写入窗口表 | 继续写 Node worker event loop 采样作为 Go 观测 |
| W8 storage 删除收尾 | DB service、SQLite 文件体积、usage shard 文件指标和 `process_event_loop_*` Go 运行引用删除 | 用 SQLite 文件大小或 DB service 状态判断 Go 服务健康 |
| W10 网关 | gateway SLI、SSE、upstream、side effect enqueue、goroutine / GC / FD 进入压测报告 | 只用业务成功率替代网关运行态指标 |

## 8. 验证命令建议

Go 系统指标迁移完成后，至少执行：

```powershell
Set-Location backend-go
. .\scripts\use-go-env.ps1
go test ./...
go test ./... -race
go vet ./...
golangci-lint run
govulncheck ./...
```

前端契约迁移完成后，至少执行：

```powershell
pnpm --filter juhe-ai-frontend typecheck
pnpm --filter juhe-ai-frontend build
```

删除证据建议记录：

```powershell
rg "eventLoopLagMs|processEventLoop|process_event_loop|db-service" backend-go frontend/src/views/stats frontend/src/types/domain/usage-stats.ts
rg "system_metrics|go_runtime_metrics|asyncHealth|storageHealth|statsFreshness" backend-go frontend/src docs/migration
```

第一条命令在 Go 系统监控正式接管后只能剩下明确的 Node 历史分支、迁移文档或无结果；如果 Go DTO、Go handler、Go store 或 Go 前端目标页面仍命中 Node 专属字段，W6 / W8 不得标记完成。

## 9. 维护规则

- 修改本文时，同步检查 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md)、[模块迁移顺序与减法清单](模块迁移顺序与减法清单.md) 和 [测试与验收策略](测试与验收策略.md)。
- 修改系统监控 API 契约时，同步更新 `docs/functions/接口契约与权限矩阵.md` 和前端类型。
- 修改采样表或窗口表时，同步更新 `docs/functions/统计指标与分层聚合设计.md`。
- 修改 Prometheus label、pprof、health 或 watchdog 入口时，同步更新 [开发构建部署调整](开发构建部署调整.md) 和部署文档。
