# Go 迁移指标与观测规划

## 1. 文档目标

本文用于固定 Node 转 Go 后的系统指标统计和观测口径，避免 Go 后端迁移完成后仍沿用 Node 事件循环、DB service 和 IPC 时代的指标字段。

本文只规划 Go 目标口径和迁移门禁，不代表当前 Node 运行态已经改变。迁移期间当前 Node 系统监控页面、`system_metrics_*`、`process_event_loop_*` 和相关回归仍按现有实现维护；当 W6 / W7 / W10 对应模块由 Go 接管时，必须按本文删除或替换 Node 专属指标。

字段级执行清单见 [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)。本文回答目标观测分层和指标口径，字段清单回答哪些 Node 字段必须删除、哪些 Go 字段接管、前端和测试如何验收。

## 2. 核心结论

- Go 目标不再采集或展示 `eventLoopLagMs`。事件循环延迟是 Node 运行时指标，不能在 Go 中用 scheduler latency、goroutine 等待数或 GC pause 伪装。
- Go 目标不再保留 `db-service` 进程角色。DB service 是 Node + SQLite 单写者治理的过渡产物，W8 删除后不能继续出现在系统指标接口、前端表格或告警中。
- Go 目标保留 `server`、`ingest-worker`、`stats-worker`、`ops-worker` 四类运行角色；如果后续轻量部署由 server 看护 worker，也不能改变指标角色、队列 owner 和告警口径。
- 内部系统监控页面读取 PostgreSQL 预聚合窗口表；外部监控采集读取 Prometheus `/__aisys__/metrics`；pprof 只用于受控排障。三者用途不同，不能把 Prometheus 高基数 label 或 pprof 原始 profile 写入业务统计表。
- 业务统计继续以 `usage_records` 和 worker 预聚合表为事实源。Go runtime 指标只说明运行健康，不能参与用量、额度、账务或账号质量计算。
- 指标命名必须使用低基数标签。禁止把系统账户 ID、API Key ID、AI 账户 ID、分组 ID、trace ID、明文 IP、用户名、上游 token、请求 prompt、模型原始长串放入 Prometheus label。

## 3. 现有 Node 指标基线

当前 Node 系统监控主要包含：

| 当前口径 | 当前用途 | Go 目标处理 |
| --- | --- | --- |
| `system_metrics_samples` / `system_metrics_hourly` / `system_metrics_trend_windows` | CPU、内存、进程 RSS / Heap、事件循环、网络、数据库体积、统计滞后趋势 | 保留系统级趋势概念，但字段改为 Go 运行和 PG/Redis/Asynq 目标口径；不再包含 Node event loop 字段 |
| `process_event_loop_samples` / `process_event_loop_hourly` / `process_event_loop_trend_windows` | 按 `server`、`ingest-worker`、`stats-worker`、`ops-worker`、`db-service` 展示事件循环延迟和进程内存 | Go 接管时删除或替换为 `go_runtime_metrics_*`；不得继续返回 `eventLoopLagMs` |
| `backgroundJobs` / 本地队列快照 | 展示 Node worker 定时任务、IPC 队列、retry queue、本地 dropped 指标 | Go W7 后改为 Asynq queue、task type、worker role、任务租约和游标 lag |
| DB service snapshot | 判断 Node DB service 是否 ready、PID 和内部状态 | Go W8 后删除；PG 连接池、查询延迟和错误率替代 DB service 状态 |
| SQLite / usage shard / stats DB 大小 | 判断 Node 多 SQLite 文件和 shard 体积 | Go W8 后改为 PostgreSQL database / table / index / partition size、Redis queue backlog 和保留期 |

Go 迁移期间，Node 和 Go 对照只能比较用户可见 SLI，例如请求成功率、接口 P95 / P99、统计新鲜度、worker lag、CPU、RSS、错误率和网关 SSE 完成率；不能要求 Go 存在与 Node event loop 一一对应的指标。

## 4. Go 目标观测分层

| 层级 | 入口 | 用途 | 存储 / 保留 |
| --- | --- | --- | --- |
| Health | `/__aisys__/health`、`/__aisys__/api/health` | 给反代、watchdog 和部署 smoke 判断服务可用性 | 不入长期统计，只返回当前依赖状态和错误脱敏摘要 |
| Prometheus metrics | `/__aisys__/metrics` | 外部监控采集、告警和压测观察 | 不写业务库；由 Prometheus 或外部系统保存 |
| pprof | `/__debug/pprof/*` | CPU、heap、goroutine、block、mutex 排障 | 不自动采样入库；按报告保存到 `reports/` |
| 结构化日志 | stdout / 文件日志 | 排错、审计外的运行事件、告警上下文 | 按日志保留策略和运行日志索引处理 |
| 内部系统监控 API | `GET /__aisys__/api/stats/system-metrics` | 管理后台系统指标页面 | 读取 PostgreSQL 采样表、小时表和窗口表 |
| Worker 状态 | Asynq inspector / Go worker runtime | 队列积压、任务失败、统计滞后、游标推进 | 关键状态写入 PG 窗口 / job state，实时细节走 Prometheus |

`/__aisys__/metrics` 和 pprof 必须保持 loopback 或明确受控入口。公网部署不能通过 Caddy / Nginx 把它们无鉴权暴露出去。

## 5. Go 关键指标矩阵

### 5.1 Runtime 与内存

首批 Go runtime 指标以标准库 `runtime/metrics`、`runtime.ReadMemStats` 和 Prometheus Go collector 为基础：

| 指标组 | 最小字段 | 用途 |
| --- | --- | --- |
| 进程身份 | `role`、`pid`、`goVersion`、`uptimeSeconds`、`gomaxprocs` | 确认版本、角色和容量基线 |
| goroutine | `goroutines`、`goroutinesCreated`、`goroutinesRunnable`、`goroutinesWaiting`、`goroutinesRunning` | 发现 goroutine 泄漏、阻塞堆积和调度压力 |
| scheduler | `schedulerLatencyP95Ms`、`schedulerLatencyP99Ms`、`threadsTotal` | 替代 Node event loop 延迟的 Go 调度健康信号，但不得命名为 event loop |
| GC | `gcCyclesTotal`、`gcPauseP95Ms`、`gcPauseP99Ms`、`gcCpuFraction`、`gcHeapGoalBytes` | 发现 GC 抖动和内存压力 |
| heap / memory | `heapAllocBytes`、`heapLiveBytes`、`heapObjects`、`memoryClassesTotalBytes`、`gomemlimitBytes`、`rssBytes` | 发现内存爬升、容器内存限制和泄漏风险 |
| blocking | `mutexWaitSecondsTotal`、`blockProfileEnabled` | 发现锁竞争；详细定位用 pprof block / mutex profile |

不建议首批把 runtime histogram 原始 bucket 全量写入 PostgreSQL。内部页面只保存 P95 / P99、最大值和样本数；Prometheus 可以暴露 histogram 给外部系统做分位估算。

### 5.2 HTTP 与管理 API

| 指标组 | 最小字段 / label | 规则 |
| --- | --- | --- |
| 请求计数 | `method`、`routeGroup`、`statusClass`、`code` | `routeGroup` 使用有限分组，例如 `system_api`、`public_api`、`gateway`、`health`，不使用原始路径 |
| 请求耗时 | duration histogram，按 `routeGroup` 和 `method` | 管理 options / list 需要看 P95 / P99 |
| in-flight | 当前处理中请求数，按 `routeGroup` | 防止慢管理接口或网关请求堆积 |
| 请求体大小 | body bytes histogram，按 `routeGroup` | 只记录大小，不记录内容 |
| 限流和拒绝 | `rate_limited_total`、`rejected_total`、`reason` | `reason` 使用固定枚举，不包含 IP / token |

W2 管理只读接口迁移后，每个 options / catalog 都必须有低基数 `routeGroup` 或 `operation`，用于压测和回归确认没有全表扫描。当前已迁移的 options / catalog 建议固定操作名为 `proxy_options_list`、`provider_options_list`、`provider_model_options_list`、`provider_models_list`、`route_strategy_options_list`、`my_route_strategy_options_list`、`group_options_list`、`my_group_options_list`、`group_account_options_list`、`my_group_account_options_list`、`account_options_list` 和 `my_account_options_list`；不要把 `systemAccountId`、供应商 code、模型名、路由策略 ID、分组 ID、账号 ID、keyword 原文或用户名称放入 Prometheus label。

### 5.3 PostgreSQL

| 指标组 | 最小字段 | 用途 |
| --- | --- | --- |
| pool | `acquired`、`idle`、`total`、`max`、`emptyAcquireCount`、`acquireDurationP95Ms` | 判断连接池是否成为瓶颈 |
| query | `operation`、`durationP95Ms`、`durationP99Ms`、`errorTotal`、`timeoutTotal` | `operation` 是业务枚举，例如 `public_settings_read`、`proxy_options_list`，不能使用原始 SQL |
| transaction | `txDurationP95Ms`、`rollbackTotal`、`commitErrorTotal` | 写接口和 worker 批量写验收 |
| lock / timeout | `statementTimeoutTotal`、`lockTimeoutTotal`、`deadlockTotal` | 防止 Go 并发把数据库压住 |
| table size | table / index / partition size 窗口 | 替代 SQLite 文件大小和 usage shard 体积 |

Go 后端必须按 server、gateway hot path、management API、ingest、stats、ops 划分连接池预算或 application_name，至少要能从 PostgreSQL 侧定位来源。

### 5.4 Redis 与 Asynq

| 指标组 | 最小字段 | 用途 |
| --- | --- | --- |
| Redis operation | `redisRole=cache|state|queue`、`operation`、duration、errorTotal | 判断 cache / state / queue 是否互相污染 |
| Redis pool | hits / misses / timeouts / stale connections | 识别连接池或网络异常 |
| limiter | allowed / denied / penalty active | W1a / W1b 限流验证 |
| Asynq queue | `queue`、pending、active、retry、dead、archived、oldestTaskAgeSeconds | 判断 worker lag 和死信积压 |
| Asynq task | `taskType`、processed、failed、retried、durationP95Ms | `taskType` 必须是固定枚举，payload ID 不能入 label |
| Worker lifecycle | role ready、shutdown drain duration、last heartbeat | 部署和 watchdog 观测 |

Redis cache、state 和 queue 仍必须使用不同 DB 或实例。指标中如果发现三者指向同一个 Redis DB，应视为配置错误，而不是运行优化问题。

### 5.5 网关与上游

真实网关迁移前只做读模型和灰度观察；W10 接管时必须补齐：

| 指标组 | 最小字段 | 用途 |
| --- | --- | --- |
| 网关请求 | request total、error total、status class、endpoint mode | 判断整体成功率和错误分布 |
| 上游耗时 | upstream connect / first byte / first token / total duration histogram | 对比 Node 与 Go 性能收益 |
| SSE | stream started / completed / aborted、flush error、client canceled、backpressure wait | 验证流式稳定性 |
| 调度 | candidate count、selected provider / protocol profile、fallback count、retry count | label 只能用 provider / profile / endpoint mode，不能用 account id |
| 副作用 | usage enqueue、audit enqueue、public log enqueue、drop / reject count | 证明副作用不阻塞已可返回响应 |
| 运行态保护 | local suppression、cooldown、client IP circuit、rate limit | 只用原因枚举，不放 IP / API Key |

## 6. 内部系统监控契约目标

当前 `GET /__aisys__/api/stats/system-metrics` 路径可以保留，但 Go owner 接管时响应需要表达新的 runtime 模型。

目标响应语义：

- 返回 `runtimeKind: "go"` 或等价版本字段，前端据此展示 Go runtime 面板。
- 顶层结构优先拆成 `hostMetrics`、`runtimeMetrics`、`taskHealth`、`queueHealth`、`storageHealth` 和 `statsFreshness`，避免继续围绕 Node `processEventLoop*` 字段扩展。
- 用 `goRuntimeLatestStatus` / `goRuntimePeakStatus` / `goRuntimeTrend` 或等价字段替代 `processEventLoopLatestStatus` / `processEventLoopPeakStatus` / `processEventLoopTrend`。
- 每个角色的不可观测状态继续使用 `sampleAvailable=false` 和 `null` 值，不能用 0 伪装正常。
- Node 过渡期间如需要同时展示 Node 和 Go canary，只能在测试 / 灰度页显式区分 `runtimeKind=node|go`，不能把 Go 数据塞进 Node event loop 字段。
- W8 后 `db-service` 不应再出现在角色列表；如果前端仍展示该角色，视为迁移未完成。

推荐 Go runtime 行字段：

```ts
interface GoRuntimeStatus {
  processRole: 'server' | 'ingest-worker' | 'stats-worker' | 'ops-worker'
  sampleAvailable: boolean
  processPid: number | null
  sampledAt: string | null
  goroutines: number | null
  goroutinesRunnable: number | null
  schedulerLatencyP95Ms: number | null
  schedulerLatencyP99Ms: number | null
  gcPauseP95Ms: number | null
  gcPauseP99Ms: number | null
  heapAllocBytes: number | null
  heapLiveBytes: number | null
  rssBytes: number | null
  threadsTotal: number | null
}
```

字段名以后端实现时的实际 DTO 为准，但语义必须符合本文，不得继续叫 `eventLoopLagMs`。

## 7. PostgreSQL 目标表建议

Go 系统指标迁移时建议拆分：

| 表 | 粒度 | 目标 |
| --- | --- | --- |
| `system_metrics_samples` | 原始系统采样 | 保留 CPU、OS 内存、RSS、网络吞吐、PG/Redis/Asynq 高层状态和统计滞后；移除 Node event loop 字段 |
| `system_metrics_hourly` | 小时聚合 | 系统采样平均值、最大值和样本数 |
| `system_metrics_trend_windows` | 页面窗口 | 系统趋势图直读 |
| `go_runtime_metrics_samples` | 按 role 原始 Go runtime 采样 | goroutine、scheduler、GC、heap、thread、RSS |
| `go_runtime_metrics_hourly` | 按 role 小时聚合 | runtime 趋势和峰值 |
| `go_runtime_metrics_trend_windows` | 按 role 页面窗口 | 管理页面 Go runtime 趋势直读 |
| `queue_runtime_snapshots` 或 Asynq 窗口表 | 队列快照 | pending / active / retry / dead / oldest age |

上述表属于 Go 目标 schema 规划。迁移执行时可以按当时的 PostgreSQL schema 命名微调，但必须遵守两个原则：不继续维护 `process_event_loop_*` 作为 Go 长期表，不把 Prometheus 原始高基数时序完整复制进 PostgreSQL。

## 8. 迁移波次门禁

| 波次 | 指标要求 | 验收证据 |
| --- | --- | --- |
| W0 | `/__aisys__/metrics` 和 pprof 受 loopback 保护；health 依赖错误脱敏；Prometheus Go collector 可用 | health / metrics / pprof 路由测试和 smoke |
| W1a / W1b | 公开接口限流、HTTP 状态、日志入队、Asynq 任务基础指标可观察 | 单元测试、maintenance smoke 输出、Redis / Asynq integration |
| W2 | options / catalog 管理读接口要有低基数操作名和耗时观察计划 | 文档和压测门禁，不能只看功能测试 |
| W3-W5 | 登录、CRUD、权限、写事务和操作日志要覆盖 PG pool / query / tx 指标 | CRUD 压测和连接池观察 |
| W6 | 系统指标读 API 和前端系统监控契约必须切到 Go runtime 字段；禁止继续模拟 event loop | API 契约测试、前端类型更新、旧字段 `rg` 删除证据 |
| W7 | stats-worker 采样 Go runtime、Asynq queue、统计滞后和游标 lag；Node worker IPC 指标删除 | worker integration、queue lag、dead / retry、shutdown drain 验证 |
| W8 | 删除 DB service、SQLite 文件体积、usage shard 文件指标；改为 PG/Redis/Asynq 指标 | `rg` 删除证据、部署 smoke、表监控更新 |
| W9 | 网关准备层读模型压测必须记录 PG/Redis pool、cache hit、候选过滤耗时 | 压测报告 |
| W10 | 网关真实转发必须记录 HTTP、SSE、upstream、goroutine、GC、FD、PG/Redis/Asynq 和副作用入队 | `docs/reports/` 独立性能报告 |
| W11 | 部署文档、watchdog、Docker 和服务化脚本以 Go health / metrics / pprof / worker lag 为基线 | 发布 smoke 和文档删除检查 |

## 9. 安全与标签规范

Prometheus label 只允许使用低基数、安全枚举：

- 允许：`service`、`role`、`routeGroup`、`method`、`statusClass`、`code`、`operation`、`dependency`、`redisRole`、`queue`、`taskType`、`providerCode`、`protocolProfile`、`endpointMode`、`result`、`reason`。
- 禁止：系统账户 ID、系统账户名、用户名、API Key ID、API Key 明文、AI 账户 ID、分组 ID、路由策略 ID、trace ID、request ID、明文 IP、User-Agent 原文、模型 prompt、完整 URL、Redis key、SQL 文本、错误消息原文、token、secret、cookie。
- 需要排查单个资源时，使用结构化日志、审计、使用记录和管理页面筛选；不要把资源 ID 放入指标 label。

内部系统监控 API 也只能返回安全摘要。管理员可见不等于可以返回密钥、token、完整请求体或高基数内部 key。

## 10. 压测与报告要求

涉及系统指标、worker 或网关的 Go 迁移报告必须记录：

- Go 版本、GOMAXPROCS、GOMEMLIMIT、OS、CPU、内存、PostgreSQL、Redis、Asynq 配置。
- 请求量、并发、成功率、P50 / P95 / P99、错误分布。
- goroutine 数量、scheduler latency、GC pause、heap、RSS、FD 或 Windows handle。
- PG pool、query latency、statement timeout、lock timeout。
- Redis latency、pool、timeouts、cache hit / miss、state limiter 拒绝数。
- Asynq pending / active / retry / dead、oldest task age、worker shutdown drain。
- 网关场景还要记录 SSE 完成率、客户端取消、上游首字 / 首 token、backpressure、usage / audit / log 副作用入队。

原始产物写入仓库根目录 `reports/`，人工整理报告写入 `docs/reports/`。报告不能只给“通过”结论，必须说明未覆盖项和环境限制。

## 11. 维护规则

- 新增 Go 指标前先确认本文和 [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)；如果指标进入用户或管理员可见页面，还要更新 [统计指标与分层聚合设计](../functions/统计指标与分层聚合设计.md) 和 [接口契约与权限矩阵](../functions/接口契约与权限矩阵.md)。
- 修改 `/__aisys__/metrics`、pprof、health 或 watchdog 入口时，同步更新 [开发构建部署调整](开发构建部署调整.md) 和部署文档。
- 修改 worker / queue 指标时，同步更新 [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md) 的 W7 门禁。
- Go 接管系统指标页面前，必须先给前端类型和页面文案做契约更新；不要在后端返回旧字段让前端“暂时能显示”。
- 如果后续引入 OpenTelemetry collector，必须先更新 [Go 技术选型与依赖基线](Go技术选型与依赖基线.md)，说明采样率、部署拓扑、敏感字段处理和退出条件。
