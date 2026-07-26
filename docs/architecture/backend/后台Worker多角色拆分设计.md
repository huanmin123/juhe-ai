# 后台 Worker 多角色拆分设计

> 面向后端实现、部署和 AI 维护者。
> 本文的三角色拓扑只描述 `standalone`。`performance` 使用 `usage-worker`、`log-worker`、`stats-worker`、`ops-worker`，默认副本数为 `2/2/1/1`，并由 3 个独立 gateway 事件循环承接 AI 流量；权威设计见 [高性能模式同机多进程拓扑设计](../../functions/高性能模式同机多进程拓扑设计.md)。

## 背景

旧方案把后台任务拆成默认 worker、监控 worker、写入 worker、统计 worker、快照 worker、探测 worker 和维护 worker 等多个常驻角色。这个方案隔离充分，但对当前项目规模偏重：系统人数预期在 1000 人以内，绝大多数后台任务是 I/O 或低频扫描，多个轻角色常驻会增加 Node 进程、SQLite 连接、定时器、运行态展示和部署排障成本。

新的判断：

- 需要高实时性的事实写入和计费相关记录必须优先，不能被统计窗口或外部网络 I/O 拖住。
- 统计、窗口、表监控和账号质量属于重任务，必须独立隔离。
- 探测、OAuth 保活、时间计划同步、授权到期扫描和删除清理协调是轻运维任务，可以合并到一个 worker，并在内部使用受控异步并发。
- 不能合并成一个 worker，因为统计窗口和表监控仍可能明显占用事件循环和 stats SQLite 写锁。

## 设计目标

- `standalone` 常驻后台 worker 固定为三类：`ingest-worker`、`stats-worker`、`ops-worker`。
- `performance` 把 ingest 拆为可扩容的 `usage-worker` 与 `log-worker`，Stats/Ops 各保持一个主副本。
- 保持轻量部署：外部进程管理器只守护 `server`，由 server supervisor 拉起 DB service 和三类 worker。
- 热写入优先：使用记录、审计、日志和 record maintenance 不被重统计或外部探测拖住。
- 重统计隔离：所有统计聚合、窗口刷新、系统指标和表监控集中在 `stats-worker`，便于定位慢任务。
- 轻运维合并：账号测试、复测、OAuth、代理检测、可用时段同步、授权到期和过期删除协调统一在 `ops-worker`。
- 运行态和前端展示只暴露当前角色，避免旧角色造成误判。
- 不引入分布式队列、外部调度器或多实例假设。

## 非目标

- 不按 CPU 核数复制同构 worker。
- 不保留旧常驻角色兼容分支。
- 不让多个 worker 并发写同一个 SQLite 文件。
- 不在请求链路补实时统计以替代 worker 预聚合。
- 不把 `temporary-maintenance-worker` 当作常驻拓扑的一部分。

## 角色边界

| 角色 | 生命周期 | 核心职责 | 扩容触发 |
| --- | --- | --- | --- |
| `ingest-worker` | persistent | 使用记录、原始审计、操作日志、公开接口日志、运行日志索引、运行日志文件导入、record maintenance、dataset / usage shard 清理 | server 到 ingest IPC 长期积压、usage 落库滞后影响计费或统计安全游标 |
| `stats-worker` | persistent | 系统指标采样、事件循环 / 内存采样、用量聚合、IP 聚合、分组账号统计、额度窗口、TopN、概览、范围窗口、授权窗口、账号质量、表监控、统计保留期清理 | 统计滞后长期超过业务可接受范围、重窗口刷新阻塞系统采样或账号质量 |
| `ops-worker` | persistent | 手动账号测试、账号健康检测、账号级 / Key 级冷却复测、OAuth token 保活、代理延迟刷新、可用时段同步、授权到期扫描、过期删除账号清理协调 | 外部 I/O 队列长期积压、账号恢复明显滞后、运维任务影响 OAuth 保活 |
| `temporary-maintenance-worker` | temporary | 历史按需任务入口，运行后退出 | 不作为常驻扩容对象 |

默认不再使用这些常驻角色：`metrics-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`、`log-worker`、`usage-ingest-worker`。如果未来确实需要拆分，只能基于指标重新建计划，并同步更新 registry、API 契约、前端展示和验证脚本。

## 任务归属表

| 任务 / 队列 | 当前角色 | 说明 |
| --- | --- | --- |
| `background_worker_usage_records` | `ingest-worker` | 高频计费事实，独立 usage 队列，优先级高 |
| `background_worker_audit_logs` | `ingest-worker` | 原始审计 append-only，失败样本优先保留 |
| `background_worker_operation_logs` | `ingest-worker` | 操作日志批量写入和摘要索引 |
| `background_worker_public_api_logs` | `ingest-worker` | 公开接口调用明细 |
| `runtime-log-file-import` | `ingest-worker` | 从角色 JSONL 文件按 offset / cursor 追增量，批量提交成功后推进 cursor |
| `background_worker_record_maintenance` | `ingest-worker` / `stats-worker` | usage / dataset 进 ingest；stats-only command 进 stats writer |
| `api-key-record-cleanup-retry` / `account-record-cleanup-retry` | `ingest-worker` | 关联明细清理，等待统计安全游标 |
| `audit-hot-retention-cleanup` / `data-retention-cleanup` dataset 部分 | `ingest-worker` | 小批多轮，不能压住热写入；PostgreSQL performance 下 `data-retention-cleanup` 只投递 record-maintenance 维护任务 |
| `system-metrics-sample` | `stats-worker` | 系统采样和进程事件循环 / 内存样本统一写 stats SQLite |
| `usage-stats-aggregation` | `stats-worker` | 按 usage shard 游标增量聚合 |
| `client-ip-stats-aggregation` | `stats-worker` | dirty IP 增量窗口刷新 |
| `group-account-stats-refresh` | `stats-worker` | 分组账号统计缓存 |
| `account-quality-refresh` | `stats-worker` | 真实请求质量聚合，失败预检候选交给 ops 队列 |
| `usage-rank-snapshots-refresh` | `stats-worker` | TopN 和重窗口刷新 |
| `usage-overview-windows-refresh` / `usage-scope-range-windows-refresh` / `authorization-usage-range-windows-refresh` | `stats-worker` | 概览、范围和授权窗口 |
| `system-metrics-trend-windows-refresh` / `table-storage-monitor` | `stats-worker` | 系统趋势和表空间监控 |
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
- stats write request 只发往 `stats-worker`；dataset write request 只发往 `ingest-worker`。
- process event loop 采样角色固定为 `server`、`ingest-worker`、`stats-worker`、`ops-worker`、`db-service`。
- 系统监控接口使用 `ingestWorkerSnapshotAvailable`、`statsWorkerSnapshotAvailable`、`opsWorkerSnapshotAvailable` 表达三类 worker 可观测性；不可观测时对应 runtime 返回 `null`，不能用空数组或 0 伪装正常。

以上 process event loop 和 `db-service` 口径只描述当前 Node 过渡实现。Go W6 / W7 接管系统指标和 worker 后，必须按 [Go 迁移指标与观测规划](../../migration/Go迁移指标与观测规划.md) 替换为 Go runtime、Asynq queue、worker heartbeat、worker lag 和 stats freshness 指标，不再暴露 `eventLoopLagMs`、`process_event_loop_*` 或 `db-service` 作为长期契约。

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
| usage / 审计 / 运行日志长期积压，且 ingest 事件循环或 dataset writer 成为瓶颈 | 从 `ingest-worker` 拆出专门日志或 usage ingest worker | 有队列积压、写耗时和 dropped 指标支撑；先确认 SQLite owner 和 shard 策略 |
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
| 运行态 | 系统监控、后台任务表、队列健康和前端文案只出现三类 worker |
| 资源占用 | Node 子进程数、SQLite 连接、定时器和日志输出明显少于旧七角色方案 |

## 验证要求

必须覆盖：

- 后端类型检查。
- worker topology smoke，确认 supervisor 只拉起三类常驻 worker。
- runtime snapshot unavailable contract，确认接口可用性字段是 ingest / stats / ops。
- system metrics process latest，确认事件循环角色只有 `server`、`ingest-worker`、`stats-worker`、`ops-worker`、`db-service`。
- background IPC snapshot current only，确认旧角色 snapshot 请求不再进入当前状态。
- queue health 和 local queue limit，确认 record maintenance 仍归 ingest，账号测试归 ops。

禁止覆盖为空的“通过”：如果某项不能执行，必须在计划验证记录里写明原因和残余风险。
