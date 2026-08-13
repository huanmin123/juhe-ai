# 后台 Worker 多角色拆分设计

> **Go sidecar 现行边界（2026-08-12）。** 本文的 `standalone` / `performance` 拓扑只描述当前 Node 实现，不能推导 Go 按旧 job / queue 拆分。F1、F2、F3 均被唯一 `juhe-ai-go-sidecar` 接管，功能独立而进程统一；任何 Node runtime-log importer、F2 scheduler 或 F3 writer/queue 记载均为历史，不得重新接入 Node worker。

> 面向后端实现、部署和 AI 维护者。
> 本文的三角色拓扑现在只描述 `standalone`。`performance` 使用 `usage-worker`、`log-worker`、`stats-worker`、`ops-worker`，默认副本数为 `2/2/1/1`，并由 3 个独立 gateway 事件循环承接 AI 流量；权威设计见 [高性能模式同机多进程拓扑设计](../../functions/高性能模式同机多进程拓扑设计.md)。原三角色收敛历史见 [PLAN-20260623T122020000Z](../../plans/计划-20260623T122020000Z-后台Worker三角色收敛.md)。

## 背景

旧方案把后台任务拆成默认 worker、监控 worker、写入 worker、统计 worker、快照 worker、探测 worker 和维护 worker 等多个常驻角色。这个方案隔离充分，但对当前项目规模偏重：系统人数预期在 1000 人以内，绝大多数后台任务是 I/O 或低频扫描，多个轻角色常驻会增加 Node 进程、SQLite 连接、定时器、运行态展示和部署排障成本。

新的判断：

- 需要高实时性的事实写入和计费相关记录必须优先，不能被统计窗口或外部网络 I/O 拖住。
- Node 统计、窗口和账号质量属于重任务，必须独立隔离。
- 探测、OAuth 保活、时间计划同步、授权到期扫描和删除清理协调是轻运维任务，可以合并到一个 worker，并在内部使用受控异步并发。
- 不能合并成一个 worker，因为 Node 统计窗口仍可能明显占用事件循环和 stats SQLite 写锁；F2 表存储监控不属于 Node worker。

## 设计目标

- `standalone` 常驻后台 worker 固定为三类：`ingest-worker`、`stats-worker`、`ops-worker`。
- `performance` 把 ingest 拆为可扩容的 `usage-worker` 与 `log-worker`，Stats/Ops 各保持一个主副本。
- 保持轻量部署：外部进程管理器只守护 `server`，由 server supervisor 拉起 DB service 和三类 worker。
- 热写入优先：使用记录、审计、日志和 record maintenance 不被重统计或外部探测拖住。
- 重统计隔离：所有 Node 统计聚合、窗口刷新和系统指标集中在 `stats-worker`，便于定位慢任务。
- F1/F2/F3 已接管：表存储监控、运行日志索引和原始审计分别由唯一 Go sidecar 内的组件直接执行；Node 仅保留对应输入或只读查询。F4 操作日志已完成切换前实现，待独立发布切流后成为同一 sidecar 内的运行 owner。
- 轻运维合并：账号测试、复测、OAuth、代理检测、可用时段同步、授权到期和过期删除协调统一在 `ops-worker`。
- 运行态和前端展示只暴露当前角色，避免旧角色造成误判。
- `standalone` 不引入外部队列或多实例假设；`performance` 使用 Redis Stream consumer group 在同机多进程间分工。

## 非目标

- `standalone` 不按 CPU 核数复制同构 worker；`performance` 只按显式配置复制 Usage/Log/Gateway。
- 不保留旧常驻角色兼容分支。
- 不让多个 worker 并发写同一个 SQLite 文件。
- 不在请求链路补实时统计以替代 worker 预聚合。
- 不把 `temporary-maintenance-worker` 当作常驻拓扑的一部分。

## 角色边界

| 角色 | 生命周期 | 核心职责 | 扩容触发 |
| --- | --- | --- | --- |
| `ingest-worker` | persistent | 使用记录、公开接口日志、record maintenance、dataset / usage shard 清理 | 运行日志索引、原始审计和操作日志持久化由 Go sidecar 负责；server 到 ingest IPC 长期积压、usage 落库滞后影响计费或统计安全游标 |
| `usage-worker` | performance persistent | 使用记录消费、record maintenance、Usage spool 重放；副本 0 负责单例维护调度 | Redis usage lag、spool backlog 或落库延迟持续超标 |
| `log-worker` | performance persistent | 公开接口日志消费；运行日志文件索引与保留、原始审计和操作日志持久化由 Go sidecar 负责 | 各日志 Stream lag 持续超标 |
| `stats-worker` | persistent | 系统指标采样、事件循环 / 内存采样、用量聚合、IP 聚合、分组账号统计、额度窗口、TopN、概览、范围窗口、授权窗口、账号质量和统计保留期清理 | 统计滞后长期超过业务可接受范围、重窗口刷新阻塞系统采样或账号质量；F2 表存储监控不属于 Node worker |
| `ops-worker` | persistent | 手动账号测试、账号健康检测、账号级 / Key 级冷却复测、OAuth token 保活、代理延迟刷新、可用时段同步、授权到期扫描、过期删除账号清理协调 | 外部 I/O 队列长期积压、账号恢复明显滞后、运维任务影响 OAuth 保活 |
| `temporary-maintenance-worker` | temporary | 历史按需任务入口，运行后退出 | 不作为常驻扩容对象 |

`log-worker` 与 `usage-worker` 只在 `performance` 启用；`standalone` 继续由 `ingest-worker` 承担对应职责。`metrics-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker` 仍不是常驻角色。

## 任务归属表

| 任务 / 队列 | 当前角色 | 说明 |
| --- | --- | --- |
| `background_worker_usage_records` | standalone: `ingest-worker`; performance: `usage-worker` | 高频计费事实；performance 由同一 consumer group 多副本分摊 |
| Go sidecar F4 | 同一 Go sidecar 内 | 操作日志接收、SQLite/PostgreSQL 写入、查询、摘要索引和保留；Node 仅在业务成功后作一次签名 RPC 提交，不再注册 `background_worker_operation_logs`。 |
| `background_worker_public_api_logs` | standalone: `ingest-worker`; performance: `log-worker` | 公开接口调用明细 |
| Go sidecar F1 | 同一 Go sidecar 内 | 从角色 JSONL 文件按 offset / cursor 追增量，批量提交成功后推进 cursor；Node worker 不再拥有该功能 |
| `background_worker_record_maintenance` | standalone: `ingest-worker` / `stats-worker`; performance: `usage-worker#1` / `stats-worker` | usage / dataset 进 usage owner；stats-only command 进 stats writer |
| `api-key-record-cleanup-retry` / `account-record-cleanup-retry` | standalone: `ingest-worker`; performance: `usage-worker#1` | 关联明细清理，等待统计安全游标 |
| `audit-hot-retention-cleanup` / `data-retention-cleanup` dataset 部分 | standalone: `ingest-worker`; performance: `log-worker#1` / `usage-worker#1` | 小批多轮，不能压住热写入 |
| `system-metrics-sample` | `stats-worker` | 系统采样和进程事件循环 / 内存样本统一写 stats SQLite |
| `usage-stats-aggregation` | `stats-worker` | 按 usage shard 游标增量聚合 |
| `client-ip-stats-aggregation` | `stats-worker` | dirty IP 增量窗口刷新 |
| `group-account-stats-refresh` | `stats-worker` | 分组账号统计缓存 |
| `account-quality-refresh` | `stats-worker` | 真实请求质量聚合，失败预检候选交给 ops 队列 |
| `usage-rank-snapshots-refresh` | `stats-worker` | TopN 和重窗口刷新 |
| `usage-overview-windows-refresh` / `usage-scope-range-windows-refresh` / `authorization-usage-range-windows-refresh` | `stats-worker` | 概览、范围和授权窗口 |
| `system-metrics-trend-windows-refresh` | `stats-worker` | 系统趋势窗口 |
| Go sidecar F2 | 同一 Go sidecar 内 | 每分钟直接异步并发采样；SQLite 只写专用 F2 输出库，PostgreSQL 写入 `juhe_stats`，由 Go 负责快照写入、owner lease 和 retention；Node 仅保留 HTTP 查询 |
| Go sidecar F3 | 同一 Go sidecar 内 | 接收 Node loopback HMAC RPC，直接持久化审计、payload/blob、hot-search 与 retention；Node 不保留 writer、queue、transport 或 fallback |
| `manual-account-test-queue` | `ops-worker` | 手动测试队列，支持取消和等待上限 |
| `account-health-check` | `ops-worker` | 正常账户低频健康检测 |
| `cooldown-account-retest` / `account-api-key-cooldown-retest` | `ops-worker` | 外部复测 I/O，可受控并发，写记录仍投递 ingest |
| `openai-oauth-access-token-refresh` | `ops-worker` | OAuth token 保活 |
| `proxy-latency-refresh` | `ops-worker` | 代理延迟检测 |
| `api-key-availability-schedule-status-sync` / `account-availability-schedule-status-sync` | `ops-worker` | 时间计划状态同步 |
| `resource-authorization-expiry-sweep` | `ops-worker` | 授权到期扫描 |
| `expired-deleted-account-cleanup` | `ops-worker` + DB service + ingest / stats | ops 调度业务库候选和最终删除；明细清理由 ingest / stats 推进 |

## IPC 与运行态

- server 到 ingest 维护三组 pending：`usageRecords`、高优先级 regular、低优先级 `recordMaintenance`。出队时高优先级 regular 可在 usage burst 后插队，避免清理任务压住日志 / 数据集写入。
- server 到 ops 维护独立 pending 队列，只承载账号测试和取消消息。
- stats write request 只发往 `stats-worker`；dataset write request 在 standalone 发往 `ingest-worker`，在 performance 由 primary `usage-worker` 兼容该 IPC owner。
- standalone 的 process event loop 采样角色固定为 `server`、`ingest-worker`、`stats-worker`、`ops-worker`、`db-service`；performance 额外按实例记录 `gateway`、`usage-worker`、`log-worker`。
- 系统监控接口使用 `ingestWorkerSnapshotAvailable`、`statsWorkerSnapshotAvailable`、`opsWorkerSnapshotAvailable` 表达三类 worker 可观测性；不可观测时对应 runtime 返回 `null`，不能用空数组或 0 伪装正常。

以上 process event loop 和 `db-service` 口径只描述当前 Node 过渡实现。未来某个完整功能在 F3 / F4 完成接管后，必须按 [Go 迁移指标与观测规划](../../migration/Go迁移指标与观测规划.md) 单独定义 Go runtime、直接异步执行、cursor / freshness 等指标；不得把旧 Asynq / queue 设为新 Go 功能前置，也不再把 `eventLoopLagMs`、`process_event_loop_*` 或 `db-service` 冒充为 Go 长期契约。

## 并发策略

- `ops-worker` 内外部请求可以并发，但必须使用固定并发上限、超时、取消边界和批间让出。
- 账号健康检测、账号级冷却复测和 Key 级冷却复测通过 `createRetryQueue` + `p-limit` 执行门禁按 batch size 派生并发，上限 10；账号质量 full diagnostic 预检上限 3。
- `stats-worker` 内重窗口刷新可以分阶段执行，但同一个窗口任务不可重入；阶段之间必须短事务提交并让出事件循环。
- `ingest-worker` 内 append-only 队列可批量 flush，但 usage、日志和维护队列要分优先级；清理任务不能连续占用 writer。
- 所有 worker 的 SQLite 写入仍遵守单 owner。增加 worker 数量不是解决 SQLite 写锁的默认手段。

## 扩展条件

只有满足以下条件之一，才考虑在三角色之外拆新 worker：

| 触发条件 | 可能动作 | 前置要求 |
| --- | --- | --- |
| usage / 审计 / 操作 / 公开接口日志长期积压，且 ingest 事件循环或 dataset writer 成为瓶颈 | 从 `ingest-worker` 拆出专门日志或 usage ingest worker | 有队列积压、写耗时和 dropped 指标支撑；先确认 SQLite owner 和 shard 策略；运行日志 F1 不属于该拆分范围 |
| 统计窗口刷新长期压住系统采样或账号质量 | 从 `stats-worker` 拆出窗口 worker 或按 shard 租约分片 | 先补任务租约、幂等和 stats writer typed command 边界 |
| ops 外部 I/O 长期影响 OAuth 保活或账号恢复 | 从 `ops-worker` 拆出 account-test / probe worker | 有运行态队列和外部请求耗时证据；保留业务库写回走 DB service |

拆分不是兼容开关；一旦拆分，必须同步调整代码、文档、接口契约、前端展示和回归脚本。

## 上线复查

| 风险面 | 必查项 |
| --- | --- |
| 启动拓扑 | server、DB service、`ingest-worker`、`stats-worker`、`ops-worker` ready 和 PID |
| IPC 路由 | append-only 和 record maintenance 进 ingest；账号测试进 ops；stats write 进 stats |
| SQLite 单写者 | 非 owner 不直接 import repository 写目标库 |
| 统计新鲜度 | ingest 未 drain 时 stats 聚合跳过，等待下一轮 |
| 运行态 | Node 系统监控、后台任务表、队列健康和前端文案只出现仍由 Node 管理的 worker；F1/F2/F3/F4 由唯一 Go sidecar 单独观测 |
| 资源占用 | Node 子进程数、SQLite 连接、定时器和日志输出明显少于旧七角色方案 |

## 验证要求

必须覆盖：

- 后端类型检查。
- worker topology smoke：standalone 确认三类常驻 worker；performance 默认确认 Usage 2 / Log 2 / Stats 1 / Ops 1。
- runtime snapshot unavailable contract，确认接口可用性字段是 ingest / stats / ops。
- system metrics process latest：standalone 确认只有 `server`、`ingest-worker`、`stats-worker`、`ops-worker`、`db-service`；performance 确认每个 Gateway、DB service、Usage / Log / Stats / Ops 副本都有独立动态角色；退出节点的注册表 key 在 TTL 后消失，latest 超过 2 分钟必须标记缺失，24 小时峰值和趋势继续保留。
- background IPC snapshot current only，确认旧角色 snapshot 请求不再进入当前状态。
- queue health 和 local queue limit，确认 record maintenance 仍归 ingest，账号测试归 ops。
- Go sidecar，确认 F1/F2/F3/F4 在同一进程运行且每个组件仍保留独立 owner；F2 每分钟直接异步并发采样，SQLite 只写专用输出库、PostgreSQL 写 `juhe_stats`，Node 仅 HTTP 查询且 `stats-worker` 不调度、不写、不清理 F2。

禁止覆盖为空的“通过”：如果某项不能执行，必须在计划验证记录里写明原因和残余风险。
