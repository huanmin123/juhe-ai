# PLAN-0045 后台 Worker 轻量拆分与任务租约

## 基本信息

- 编号：PLAN-0045
- 状态：进行中
- 创建时间：2026-06-13
- 更新时间：2026-06-14
- 需求来源：用户对话 / 生产排障
- 执行者：AI / 维护者待定
- 关联模块：后端 / 后台任务 / 统计 / 系统监控 / SQLite / 部署 / 文档 / 验证

## 需求目标

2026-06-13 生产 Mac 上出现 background worker 长时间接近单核满载，统计窗口刷新卡住后，AI 账户日用量和系统性能 / 网络吞吐趋势出现滞后与采样断档。用户进一步提出是否可以根据 CPU 核数创建多个 worker，并把任务分配到不同 worker。

本计划目标是给出最终可落地但不过度的后台 worker 演进方案：

- 当前慢 SQL / 缺失索引修复先上线，用来解决眼前 worker 卡顿和统计滞后。
- 多 worker 作为后续确定演进继续推进，但按轻量分阶段落地，不把性能 bug 直接升级成重型调度系统。
- 先把当前所有定时 job、后台异步队列和维护任务盘点出来，按资源占用、写库目标、是否可并发和业务优先级归类。
- 再由任务分类决定需要拆多少 worker、每个 worker 跑哪些 job，而不是先拍 worker 数；worker 数量不硬限制，按热点隔离、队列积压、事件循环延迟和 SQLite 锁竞争实测调整。
- worker 拆分同时按角色和生命周期推进：持续采样、写入、日志、统计、探测走持久 worker；表管理清理、非业务数据硬清理、历史重建和一次性修复走临时 worker。
- 热点功能必须完全隔离，尤其是使用记录写入、日志 / 审计写入、系统采样、重型统计窗口和外部探测，不能因为某个大任务卡住而拖死整个后台系统。
- 优先拆出轻量监控采样进程，避免重统计任务影响系统指标采样；任务租约和少量分片统计 worker 按盘点结果逐步打开。
- 保持本地 SQLite 和轻量部署边界，不引入 Redis、Kafka、BullMQ、Kubernetes job 或重型分布式调度。

## 范围边界

### 本次包含

- [x] 记录为什么不能按 CPU 核数直接复制同构 worker。
- [x] 盘点当前后台定时 job、后台异步队列和维护入口。
- [x] 定义推荐落地顺序：止血优化、任务盘点、监控采样隔离、任务租约、可选分片 worker。
- [x] 定义哪些任务适合独立 worker，哪些任务必须单 owner。
- [x] 定义持久 worker 与临时 worker 的边界，避免表管理清理等一次性任务长期占用常驻 worker。
- [x] 定义进入下一阶段的触发条件，避免为了架构升级而升级。
- [x] 定义后续实现时的验证标准和回滚边界。

### 本次不包含

- 不在当前索引修复发布中同时实现多 worker：当前文档固化后续落地方案和阶段边界。
- 不按逻辑 CPU 数自动启动同构 worker：8 核不等于应该启动 8 个后台任务进程。
- 不限制最终 worker 数量：数量由隔离域和实测压力决定，但每个 worker 必须有明确职责、队列上限、租约边界和健康指标。
- 不把所有任务都做成常驻进程：有明确参数、结束条件和批量边界的维护任务优先走临时 worker。
- 不引入外部队列、分布式锁服务或新数据库。
- 不把全局窗口刷新、排行快照、清理任务并发化；这些任务默认仍然单 owner。
- 不为旧 schema、旧统计表或旧部署方式保留运行时兼容分支。

## 关联文档

| 文档 | 关系 |
| --- | --- |
| `docs/plans/计划-0005-后台任务进程隔离.md` | 已完成的单 background worker 进程隔离基础 |
| `docs/architecture/backend/后台Worker多角色拆分设计.md` | 本期多 worker 拆分的开发设计入口和实现边界 |
| `docs/architecture/backend/后台任务使用说明.md` | 当前后台任务注册、worker 进程边界和禁止事项 |
| `docs/architecture/backend/README.md` | 后端进程、SQLite 和后台任务长期边界 |
| `docs/functions/SQLite存储说明.md` | 统计结果库、系统指标采样和窗口缓存说明 |
| `docs/deploy/部署指南.md` | 后续如拆进程，需要同步生产守护方式 |
| `docs/develop/测试与验证说明.md` | 后续实现的类型检查、构建和回归验证入口 |

## 最终推荐方案

### 0. 先修慢任务，不先扩 worker

当前生产卡顿的直接证据指向 `usage_scope_range_windows` 范围窗口发布阶段的同步 SQLite 扫描。优先级最高的是修复慢 SQL、补齐索引和回归保护。

验收目标：

- `usage-scope-range-windows-refresh` 不再出现分钟级慢阶段。
- `stats_job_state` 中 `usage_stats_aggregation.lag_seconds` 在正常流量下回落到可接受范围。
- 系统指标采样不再出现超过 2 分钟的断档。

这个阶段用于止血和降低后续拆分风险；即使止血生效，多 worker 仍按本计划继续推进，但不得跳过角色隔离直接复制同构 worker。

### 1. 先做后台任务盘点和归类

多 worker 拆分必须先从任务清单开始。当前 `worker.ts` 同时启动定时任务、运行日志文件导入、后台异步队列、手动账号测试队列和退出 flush；`background-jobs.ts` 里注册了约 23 个定时 job。后续实现前必须把这些任务变成显式角色配置，避免某个 worker 误跑全部任务。

任务归类必须同时登记生命周期：

| 生命周期 | 含义 | 例子 |
| --- | --- | --- |
| `persistent` | 常驻进程，持续消费队列或按固定周期执行 | `metrics-worker`、`ingest-worker`、`log-worker`、`stats-worker`、`probe-worker` |
| `temporary` | 按单次任务启动，完成 / 失败 / 超时后退出 | 表监控 `non_business_data_cleanup`、手动 `usage_records_cleanup`、一次性历史重建、批量修复 |
| `hybrid` | 常驻 worker 负责扫描 / 投递 / 状态记录，重型执行可交给临时 worker | `data-retention-cleanup`、已删除资源关联清理重试、历史欠账维护 |

当前识别到的定时 job：

| 分类 | job | 初步建议 |
| --- | --- | --- |
| 系统采样 | `system-metrics-sample` | 放入 `metrics-worker`，最高隔离优先级 |
| 系统趋势窗口 | `system-metrics-trend-windows-refresh` | 可放 `metrics-worker` 或 `snapshot-worker`；如果会拖慢采样，必须留在 `snapshot-worker` |
| 用量增量聚合 | `usage-stats-aggregation` | 放入 `stats-worker`；后续可按 usage shard 分片 |
| IP 统计聚合 | `client-ip-stats-aggregation` | 放入 `stats-worker`；后续按真实瓶颈决定是否分片 |
| 分组账号缓存 | `group-account-stats-refresh` | 放入 `stats-worker`，单 owner |
| 额度快照 | `usage_stats_aggregation` 内部 `refreshUsageQuotaHourlyWindowsCache` / `buildGatewayQuotaSnapshot` | 跟随 `stats-worker`，单 owner |
| 用量排行窗口 | `usage-rank-snapshots-refresh` | 放入 `snapshot-worker`，单 owner |
| 统计概览窗口 | `usage-overview-windows-refresh` | 放入 `snapshot-worker`，单 owner |
| usage scope 范围窗口 | `usage-scope-range-windows-refresh` | 放入 `snapshot-worker`，单 owner，优先做索引和分段优化 |
| 授权范围窗口 | `authorization-usage-range-windows-refresh` | 放入 `snapshot-worker`，单 owner |
| 统计一致性检查 | `usage-stats-consistency-check` | 放入 `maintenance-worker` 或 `snapshot-worker`，低频单 owner |
| API Key 清理重试 | `api-key-record-cleanup-retry` | 放入 `maintenance-worker`，单 owner |
| AI 账户清理重试 | `account-record-cleanup-retry` | 放入 `maintenance-worker`，单 owner |
| API Key 可用时段同步 | `api-key-availability-schedule-status-sync` | 放入 `maintenance-worker`，单 owner |
| 授权到期扫描 | `resource-authorization-expiry-sweep` | 放入 `maintenance-worker`，单 owner |
| 表空间监控 | `table-storage-monitor` | 放入 `maintenance-worker`，低频单 owner |
| 代理延迟检测 | `proxy-latency-refresh` | 放入 `probe-worker` 或 `maintenance-worker`，外部请求型 |
| 账号质量刷新 | `account-quality-refresh` | 放入 `stats-worker` 或 `maintenance-worker`，依赖使用记录聚合 |
| OAuth token 保活 | `openai-oauth-access-token-refresh` | 放入 `maintenance-worker`，外部请求型，单 owner |
| 冷却账号复测 | `cooldown-account-retest` | 放入 `probe-worker`，外部请求型，避免阻塞统计 |
| 运行日志索引维护 | `runtime-log-index-maintenance` | 放入 `log-worker` 或 `maintenance-worker` |
| 审计热保留清理 | `audit-hot-retention-cleanup` | 放入 `maintenance-worker`，单 owner |
| 统一保留期清理 | `data-retention-cleanup` | 放入 `maintenance-worker`，单 owner |
| 已删除账号过期清理 | `expired-deleted-account-cleanup` | 放入 `maintenance-worker`，单 owner |

当前识别到的后台异步队列：

| 队列 / 入口 | 当前入口 | 初步建议 |
| --- | --- | --- |
| 使用记录队列 | `background_worker_usage_records` / `usage/record-queue.service.ts` | 放入 `ingest-worker` 或 `stats-worker`，需要优先级高于聚合读取 |
| 原始审计队列 | `background_worker_audit_logs` / `audit-log-queue.service.ts` | 放入 `log-worker`，append-only，不能 LWW |
| 操作日志队列 | `background_worker_operation_logs` / `operation-log-queue.service.ts` | 放入 `log-worker`，append-only |
| 运行日志索引队列 | `background_worker_runtime_log_line` / `runtime-log-index-queue.service.ts` | 放入 `log-worker`，避免被统计重活拖住 |
| 运行日志文件导入 | `startRuntimeLogFileImport()` | 放入 `log-worker` |
| 数据维护队列 | `background_worker_record_maintenance` / `record-maintenance-queue.service.ts` | 放入 `maintenance-worker`；快照类可合并，清理类单 owner |
| 手动账号测试队列 | `background_worker_account_test_tasks` / `account-test-task-queue.service.ts` | 放入 `probe-worker`，外部请求型 |
| 网关账号副作用队列 | `runtime/account-side-effects.service.ts` | 当前在请求 / DB service 边界执行，后续评估是否并入 `maintenance-worker` 或单独状态写 worker |
| 公开接口日志队列 | `public-api-log-queue.service.ts` | 已通过 `background_worker_public_api_logs` 投递到 `ingest-worker`；后续高压时可拆到独立 `log-worker` |
| Client IP 策略命中 flush | `runtime/client-ip-policy-cache.service.ts` | 当前在网关侧本地 flush；后续评估是否进入 `ingest-worker` |

任务归类后再确定 worker 数。第一版建议不是最终答案，而是实现时的初始分组：

- `metrics-worker`：只跑系统采样和必要的进程事件循环采样协调。
- `ingest-worker`：承接使用记录、审计、操作日志、运行日志、公开接口日志等高频 append-only 写入；如果写锁压力大，可先与 `log-worker` 合并。
- `stats-worker`：跑用量增量聚合、IP 聚合、账号质量、分组账号缓存和额度快照。
- `snapshot-worker`：跑 TopN、概览、范围窗口等重型窗口刷新，默认单 owner。
- `maintenance-worker`：跑清理、到期扫描、记录维护、低频表空间监控。
- `temporary-maintenance-worker`：按任务执行表监控非业务数据硬清理、手动使用记录清理、一次性历史重建和批量修复，跑完退出。
- `probe-worker`：跑代理检测、账号复测、手动账号测试、OAuth token 保活等外部请求型任务；如果外部探测量不大，可先并入 `maintenance-worker`。

热点隔离原则：

- 高频写入隔离：使用记录、审计、操作日志、运行日志索引、公开接口日志等 append-only 写入，不能和重型窗口刷新共用同一个 worker。
- 系统采样隔离：`system-metrics-sample` 必须独立于统计聚合和窗口刷新，保证系统性能图、事件循环延迟和统计滞后可观测。
- 重任务隔离：`usage-scope-range-windows-refresh`、`authorization-usage-range-windows-refresh`、TopN、概览窗口等可以集中到 `snapshot-worker`，但不能阻塞写入、采样和外部探测。
- 外部探测隔离：账号复测、代理检测、手动账号测试、OAuth token 保活等有网络等待和上游不确定性的任务，不能阻塞统计写入和系统采样。
- 维护清理隔离：保留期清理、删除记录重试、表空间监控等低频任务应独立限流，不能和热点写入抢同一队列。
- 同类热点可以继续横向拆多个 worker；例如写入队列压力大时可以拆 `usage-ingest-worker`、`audit-log-worker`、`runtime-log-worker`，而不是强行合在一个 `ingest-worker`。

落地时不要求一次拆出全部 6 类 worker。最小落地顺序是：

1. `metrics-worker` 先独立。
2. 把日志 / 使用记录 append-only 队列从重统计 worker 中剥离，形成 `ingest-worker` 或 `log-worker`。
3. 把窗口刷新留给 `snapshot-worker`，避免拖慢增量聚合。
4. 把表管理手动清理、非业务数据硬清理和一次性修复纳入 `temporary-maintenance-worker`，避免常驻维护 worker 被临时大任务长期占住。
5. 最后再根据统计滞后决定是否给 `usage-stats-aggregation` 做租约分片。

### 2. 第二阶段先拆监控采样 worker

多 worker 的第一步只拆一个轻量 `metrics-worker`，不要复制完整 background worker。

`metrics-worker` 只负责：

- `system-metrics-sample`
- 进程事件循环采样协调
- 系统指标原始样本和小时桶写入

`metrics-worker` 不负责：

- usage 增量聚合
- usage scope 范围窗口刷新
- TopN、排行、模型分布、错误排行窗口
- 数据保留清理
- 代理检测、账号复测、OAuth token 保活等业务任务

这样做的收益是明确的：即使统计 worker 又在跑重 SQL，系统性能 / 网络吞吐趋势也不会因为同一个事件循环被占住而完全断采样。

实现边界：

- 主进程 supervisor 支持按固定角色拉起 `worker` 和 `metrics-worker`。
- `metrics-worker` 只写系统监控相关小表，事务保持短小。
- 进程事件循环状态需要显式区分 `server`、`db-service`、`worker`、`metrics-worker`；缺样本时用 `sampleAvailable=false` 表达未知。
- 如果不希望前端增加新曲线，前端可先只展示 `server` / `db-service` / `worker`，但接口必须能表达 `metrics-worker` 的运行态，避免排障时看不到采样进程本身。

### 3. 第三阶段拆 append-only 写入队列

第三阶段把高频写入从重统计 worker 中剥离，优先覆盖：

- 使用记录写入。
- 原始审计日志写入。
- 操作日志写入。
- 运行日志索引。
- 公开接口日志。

这些队列属于持久 worker，因为它们需要持续消费、限流、丢弃 / 合并策略和运行态指标。它们不能被表管理清理、窗口重建或外部探测长期占用。

### 4. 第四阶段加临时维护 Worker

第四阶段新增 `temporary-maintenance-worker`，专门处理有明确参数和结束条件的维护任务。

首批适合进入临时 worker 的任务：

- 表监控 `non_business_data_cleanup`。
- 手动 `usage_records_cleanup`。
- 一次性历史重建。
- 批量修复或上线后数据收尾。

常驻 `maintenance-worker` 只负责扫描、投递、状态记录和小批协调。临时 worker 必须记录 `runId`、任务参数、开始 / 结束时间、结束原因、处理行数 / 文件数、是否还有剩余；完成、失败或超时后退出。

### 5. 第五阶段再加数据库任务租约

只有当同一类任务需要多个进程竞争执行时，才新增 `background_job_leases`。固定角色分工不需要租约；例如 `metrics-worker` 固定只跑系统采样，普通 `worker` 固定跑统计和维护任务，就不需要抢锁。

建议租约表放在统计结果库，字段保持最小：

| 字段 | 作用 |
| --- | --- |
| `job_name` | 任务名称，例如 `usage_stats_aggregation` |
| `shard_key` | 分片键；全局任务使用空字符串 |
| `owner_id` | 当前持有者进程实例 ID |
| `lease_until` | 租约过期时间 |
| `heartbeat_at` | 最近续约时间 |
| `started_at` | 本次持有开始时间 |
| `updated_at` | 行更新时间 |

抢占规则：

- 使用短事务尝试抢占 `lease_until < now` 或空闲行。
- 持有者按固定间隔续约，任务完成后释放或缩短租约。
- worker 崩溃后等待租约过期，由下一轮接管。
- 同一 `job_name + shard_key` 同时只能一个 owner 执行。

这个租约只服务本机多 worker，不承诺跨服务器分布式调度。

### 6. 第六阶段才考虑少量分片统计 worker

只有满足以下条件，才开始分片统计 worker：

- 慢 SQL 和索引优化已上线。
- `metrics-worker` 已隔离采样。
- 生产仍持续出现统计滞后，例如 `usage_stats_aggregation.lag_seconds` 连续 30 分钟超过 10 分钟。
- SQLite `database is locked` 没有明显恶化，且慢点确实在可分片读取 / 计算阶段，而不是最终写入阶段。

可并行任务：

- `usage_stats_aggregation` 按 usage shard 或日期 shard 分片。
- `client-ip-stats-aggregation` 如果后续证明单轮处理仍偏慢，可按 IP 桶或 usage shard 分片。

仍然单 owner 的任务：

- `usage-scope-range-windows-refresh`
- `usage-rank-snapshots-refresh`
- `system-metrics-trend-windows-refresh`
- `data-retention-cleanup`
- `audit-hot-retention-cleanup`
- `table-storage-monitor`
- 任何先删除再重建同一窗口表的任务

并发数量默认不要按核数开，也不要写死上限。建议按隔离域起步，再由实测决定扩容：

- `metrics-worker`：1 个
- `stats-worker`：1 个
- `snapshot-worker`：1 个，承接重窗口刷新，必要时再拆业务窗口和系统趋势窗口
- `ingest-worker` / `log-worker`：按热点队列拆分，不限制最终数量
- `usage-shard-worker`：按 usage shard 租约横向扩展，数量由统计滞后、队列积压、事件循环延迟和 SQLite 锁等待决定

## 为什么不按核数直接复制 worker

直接启动 8 个相同 background worker 的风险大于收益：

- 当前 `WorkerScheduler` 的 `running` 状态是进程内变量，多个进程互相看不到。
- 多个 worker 会重复执行同一全局任务，可能同时删除并重建同一统计窗口。
- SQLite 单写者模型会把并发写放大成锁竞争，容易出现更多 `database is locked`。
- 统计游标如果被多个进程同时推进，可能造成漏算、重复算或窗口新旧混杂。
- 系统指标采样这种轻任务不应该和重统计任务绑定在同一个事件循环里。

所以最终方案不是“按核数复制 worker”，而是“固定角色隔离 + 必要时对可分片任务做小规模租约并行”。

## 执行拆解

| 阶段 | 任务 | 状态 | 进入条件 | 完成标准 |
| --- | --- | --- | --- | --- |
| 0 | 修复当前慢 SQL / 缺失索引 | 待执行 / 另行随修复提交 | 当前生产已复现 worker 长时间满 CPU | 统计滞后回落，系统采样不再长时间断档 |
| 1 | 完成后台 job / 队列盘点并做角色配置设计 | 已完成 | 阶段 0 发布后进入下一步 | 所有定时 job 和异步队列都有明确 worker 归属、并发边界和单 owner 说明 |
| 2 | 新增 `metrics-worker` 固定角色 | 已完成 | 阶段 1 完成 | `system-metrics-sample` 独立运行在 metrics-worker，运行态和系统指标接口可区分 `metrics-worker` |
| 3 | 拆分 append-only 写入队列 worker | 已完成（当前合并到 ingest-worker） | 阶段 2 完成后，确认日志 / 使用记录队列不应被统计重活拖住 | 使用记录、审计、操作日志、公开接口日志和运行日志索引等队列不受窗口刷新阻塞；后续高压时再拆独立 log / usage ingest 角色 |
| 4 | 新增 `temporary-maintenance-worker` | 已完成 | 阶段 1 完成，且表管理 / 维护任务已登记生命周期 | 表监控 `non_business_data_cleanup` 等一次性清理可按任务启动、跑完退出并记录状态 |
| 5 | 新增 `background_job_leases` | 进行中 | 需要为后续分片任务或并发临时任务提供互斥基础 | 同一任务分片不会重复执行，worker 崩溃后可接管 |
| 6 | 增加少量 usage shard 聚合 worker | 待开始 | 阶段 5 已完成，并确认瓶颈在可分片聚合阶段 | 聚合滞后下降，SQLite 锁错误不增加 |
| 7 | 更新部署与运维文档 | 进行中（七 worker 口径已同步） | 任一阶段进入实现 | 发布包、启动脚本、健康检查和回滚说明同步 |

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 类型检查通过 | 已执行 | 2026-06-15 通过 |
| 命令类验证 | 后端构建 | `pnpm --filter juhe-ai-backend build` | 构建生成 server / worker 产物 | 已执行 | 2026-06-15 通过 |
| 命令类验证 | 后台 worker 性能回归 | `pnpm --filter juhe-ai-backend test:background-worker-performance` | 后台长任务让出事件循环，不影响既有性能保护 | 已执行 | 2026-06-15 通过，`durationMs=32969.4`，`maxEventLoopGapMs=557.9` |
| 回归场景 | job 归属完整性 | `pnpm --filter juhe-ai-backend test:background-job-registry` | 每个 `scheduler.schedule` job 和 worker IPC 队列都有唯一或明确多 owner 归属 | 已执行 | 2026-06-14 通过 |
| 回归场景 | worker 多角色隔离 | `pnpm --filter juhe-ai-backend test:background-metrics-worker-role` | supervisor 固定拉起七类常驻 worker；metrics-worker 只注册系统采样，stats / snapshot / probe / maintenance 各自注册所属任务 | 已执行 | 2026-06-15 通过 |
| 回归场景 | 后台 worker 启动拓扑 | `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke` | 临时后端启动后由 server 拉起七个 worker 和 DB service，并写入常驻进程事件循环样本；临时维护 worker 按任务启动，不属于常驻拓扑 | 已执行 | 2026-06-15 通过，`workerChildren=7`，常驻角色包含 `stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker` |
| 回归场景 | 系统指标进程角色最新样本 | `pnpm --filter juhe-ai-backend test:system-metrics-process-latest` | 进程最新样本和峰值状态包含十类角色：server、七类常驻 worker、temporary-maintenance-worker、db-service | 已执行 | 2026-06-15 通过 |
| 回归场景 | runtime snapshot 不可用契约 | `pnpm --filter juhe-ai-backend test:runtime-snapshot-unavailable-contract` | 所有常驻 worker snapshot 不可用、临时维护 worker 暂无运行样本时接口仍能以 `sampleAvailable=false` 和 snapshot available 字段表达未知 | 已执行 | 2026-06-15 通过 |
| 命令类验证 | 前端类型检查 | `pnpm --filter juhe-ai-frontend typecheck` | 管理页系统指标类型包含七类常驻 worker 和 `temporary-maintenance-worker`，无类型错误 | 已执行 | 2026-06-15 通过 |
| 命令类验证 | 前端构建 | `pnpm --filter juhe-ai-frontend build` | 管理页系统指标和后台任务表可打包 | 已执行 | 2026-06-15 通过；Vite 仍提示既有大 chunk 警告 |
| 启动 smoke | 发布产物多进程启动 | 临时端口启动 `backend/dist/server.js`，访问 `/__aisys__/health` 和 `/__aisys__/api/health`，检查子进程和 `process_event_loop_samples` | server 拉起 7 个 worker 子进程和 1 个 DB service 子进程，常驻样本包含 `server`、`worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`、`db-service`；临时维护 worker 只在任务运行期间出现 | 已执行 | 2026-06-15 通过，`workerChildren=7`，`dbServiceChildren=1`，常驻九类角色样本齐全 |
| 回归场景 | 后台任务不可重入 | 后续新增租约回归脚本 | 同一 `job_name + shard_key` 同时只有一个 owner | 未执行 | 阶段 5 执行 |
| 回归场景 | 系统指标不断采样 | 压住统计 worker 后观察 `system_metrics_samples` | 采样间隔不超过配置间隔的 2 倍，异常时有可观测告警 | 未执行 | 阶段 2 代码隔离已完成，生产或仿真压测观察随上线执行 |
| 回归场景 | append-only / 维护队列隔离 | `pnpm --filter juhe-ai-backend test:background-ipc-protected-queue`、`test:worker-local-queue-limit` | 使用记录、审计、操作日志、公开接口日志和运行日志索引队列进入 `ingest-worker`，维护队列进入 `maintenance-worker`，各 IPC 队列满时快速拒绝并计数 | 已执行 | 2026-06-15 通过 |
| 回归场景 | 临时维护 worker 生命周期 | `pnpm --filter juhe-ai-backend test:temporary-maintenance-worker` | 临时 worker 启动、执行、记录完成状态和自身事件循环采样后退出，常驻 worker 不被长期占用 | 已执行 | 2026-06-14 通过，`deletedRows=1`，租约完成后释放 |
| 回归场景 | SQLite 锁竞争 | 压测 usage 聚合和窗口刷新 | `database is locked` 不增加，统计滞后下降 | 未执行 | 阶段 6 执行 |
| 部署验证 | 生产进程守护 | 检查 launchd / supervisor 配置和健康接口 | server、db-service、七类常驻 worker 都能被守护和重启 | 未执行 | 七 worker 部署文档已同步，生产验证随上线执行 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-13 | 草稿 | AI | 基于生产 worker 卡顿和系统指标断档排查，创建轻量 worker 演进方案。初版写为“先修慢 SQL，若解决则不继续做多 worker”。 |
| 2026-06-13 | 待开始 | AI | 根据用户反馈调整为“索引修复先上线，多 worker 后续仍要做”。执行顺序保持轻量：先 `metrics-worker`，再任务租约，再少量分片 worker；仍不按 CPU 核数复制同构 worker。 |
| 2026-06-13 | 待开始 | AI | 根据用户进一步反馈调整为“先把所有 job 和后台异步队列拿出来归类，再决定拆多少 worker”。补充当前定时 job、异步队列盘点表和基于任务分类的 worker 分组建议。 |
| 2026-06-13 | 待开始 | AI | 新增 `docs/architecture/backend/后台Worker多角色拆分设计.md` 作为本期开发设计入口；后续 worker 拆分实现必须先按设计文档推进 job registry、热点隔离和角色配置。 |
| 2026-06-14 | 进行中 | AI | 完成阶段 1 代码级 job registry：新增 `background-job-registry.ts`，定时任务名统一从 registry 获取，新增 `test:background-job-registry` 回归保护定时任务、worker IPC、内部队列和数据维护子任务登记完整性。 |
| 2026-06-14 | 进行中 | AI | 完成阶段 2 metrics-worker 隔离：server supervisor 固定拉起默认 worker 和 metrics-worker；默认 worker 承载业务后台任务和队列，metrics-worker 只承载 `system-metrics-sample` 与事件循环采样协调；运行态、系统指标进程角色、部署文档和后台任务说明已同步。 |
| 2026-06-14 | 进行中 | AI | 复查阶段 2 时修正 `system-metrics-sample` 本地事件循环样本角色误标：采样函数改为按当前 `JUHE_AI_WORKER_ROLE` 生成本地样本，避免 metrics-worker 样本被写成默认 `worker`；已补 `test:background-metrics-worker-role` 保护。 |
| 2026-06-14 | 进行中 | AI | 补充阶段 2 启动 / 管理稳定性复查：新增 `test:background-worker-topology-smoke`，用临时端口和临时 SQLite 启动真实后端，确认 server 只作为外部守护入口，内部拉起默认 worker、metrics-worker 和 DB service，且当时四类进程事件循环样本均可写入；阶段 3 后该 smoke 已扩展为常驻五类角色。 |
| 2026-06-14 | 进行中 | AI | 补充管理可观测性复查：前端系统指标类型、事件循环趋势图、峰值卡片和后台任务表均补齐 `metrics-worker`；后台任务接口为默认 worker 和 metrics-worker 任务统一返回 `workerRole`，便于排查具体哪个 worker 卡住。 |
| 2026-06-14 | 进行中 | AI | 补做阶段 2 启动 / 管理 / 稳定性复查：metrics-worker 不再主动打开数据集目录库，减少无用 SQLite 连接；发布产物启动 smoke 已确认 server 下有默认 worker、metrics-worker 和 DB service 三个子进程，健康接口可用，当时四类进程事件循环样本均能写入。 |
| 2026-06-14 | 进行中 | AI | 扩大阶段 2 风险复查：管理页运行态告警补齐 `metricsWorkerSnapshotAvailable`，模拟监控数据补齐 `metrics-worker` 样本，接口契约 / SQLite 存储 / 核心功能文档统一为阶段 2 四进程口径，避免监控 worker 缺失时页面误判为正常；阶段 3 已继续扩展到 `ingest-worker`。 |
| 2026-06-14 | 进行中 | AI | 完成阶段 3 首批 append-only 写入隔离：supervisor 增加 `ingest-worker`；使用记录、审计、操作日志、公开接口日志和运行日志索引 IPC 改投递 ingest；默认 worker 继续承载统计、维护和探测；统计聚合读取事实前检查 ingest drain 状态，避免日用量统计读到未落地使用记录。 |
| 2026-06-14 | 进行中 | AI | 补齐阶段 3 稳定性复查：运行态、队列健康、系统指标接口和前端趋势图补 `ingestWorkerSnapshotAvailable` / `ingest-worker`；IPC 队列回归覆盖默认 worker 队列满不影响 ingest、ingest 队列满快速拒绝、审计大 payload 裁剪和 snapshot current-only。 |
| 2026-06-14 | 进行中 | AI | 完成阶段 4 临时维护 worker 落地：新增 `background_task_runs` 和 `background_job_leases`，`usage_records_cleanup` / `non_business_data_cleanup` 进入临时维护进程执行；补 `test:temporary-maintenance-worker`，回归确认临时 worker 退出前实际删除符合条件的使用记录并释放租约。 |
| 2026-06-14 | 进行中 | AI | 补齐临时维护 worker 可观测性：`temporary-maintenance-worker` 运行期间独立写入 `process_event_loop_samples`，系统指标接口、前端进程事件循环延迟列表、模拟监控数据和回归脚本统一纳入六类角色口径；短任务无有效采样时仍以缺样本表达未知。 |
| 2026-06-15 | 进行中 | AI | 完成默认 worker 剩余业务任务拆分：server supervisor 固定拉起 `worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`；默认 `worker` 只保留控制 / fallback，增量统计、重窗口快照、外部探测和维护清理分别进入独立常驻 worker。 |
| 2026-06-15 | 进行中 | AI | 补齐运行态与管理可观测性：DB service runtime snapshot、系统指标接口、前端类型、事件循环角色清单和队列健康统一覆盖七类常驻 worker、临时维护 worker 和 DB service；维护队列从默认 worker 改归 `maintenance-worker`。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-13 | 不按核数自动创建同构 worker | 当前后台任务包含大量全局窗口删除 / 重建和 SQLite 写入，直接复制会造成重复执行和锁竞争 | 多 worker 必须先有固定角色或任务租约 |
| 2026-06-13 | 第一优先级是修慢 SQL | 当前生产证据指向范围窗口发布索引缺失造成的同步 SQLite 扫描 | 索引修复先上线止血，但不关闭后续多 worker 演进 |
| 2026-06-13 | 多 worker 先做 job / 队列盘点 | worker 拆分应该由任务类型、资源占用、写库目标和并发边界决定 | 后续实现先形成角色配置和归属回归，再拆具体 worker |
| 2026-06-13 | 第一批实际拆分优先 `metrics-worker` | 监控采样轻、价值明确，且能避免重统计任务拖断系统性能图 | 不改变业务统计口径，不引入复杂分片 |
| 2026-06-13 | worker 数量不写死，热点功能必须完全隔离 | 目标是避免大任务堵死系统，而不是追求固定 worker 数 | 使用记录、日志 / 审计、系统采样、重窗口统计和外部探测必须拥有独立隔离域；横向数量由实测决定 |
| 2026-06-13 | worker 按角色和生命周期双维度拆分 | 表管理清理、非业务数据硬清理、历史重建和一次性修复不需要常驻，跑完应退出 | 设计新增 `temporary-maintenance-worker`；常驻 `maintenance-worker` 优先做扫描、投递、状态记录和小批协调 |

## 验收标准

- [ ] 阶段 0：当前统计慢点已修复，生产不再出现 `usage-scope-range-windows-refresh` 长时间卡住。
- [x] 阶段 1：所有定时 job、后台异步队列和维护入口都有明确 worker 归属、并发边界和单 owner 说明。
- [x] 阶段 2：重统计任务运行时，系统指标采样仍稳定写入，不再出现分钟级或小时级采样断档。代码隔离和回归已完成，生产采样连续性随上线观察。
- [x] 阶段 3：append-only 写入队列不被窗口刷新或统计重活阻塞。使用记录、审计、操作日志、公开接口日志和运行日志索引已迁入 `ingest-worker`；后续高压时可继续把使用记录、审计 / 日志、公开接口日志拆到独立持久 worker。
- [x] 阶段 4：表管理手动清理、非业务数据硬清理和一次性修复可由临时 worker 执行，完成 / 失败 / 超时后退出并可追踪状态。
- [~] 阶段 5：任务租约能防止同一分片重复执行，worker 崩溃后能在租约过期后接管。
- [ ] 阶段 6：如果引入分片 worker，统计滞后下降且 SQLite 锁错误没有增加。
- [x] 热点隔离：使用记录写入、日志 / 审计写入、系统采样、重型窗口刷新、外部探测和维护清理之间互不共享会被大任务长期占满的事件循环。
- [~] 文档、部署说明、测试说明和生产回滚步骤同步完成；当前设计、开发和部署文档已同步，最终发布包 smoke 后补验证记录。

## 验证记录

- 2026-06-14 完成阶段 1 job registry 代码落地，已执行：
  - `pnpm --filter juhe-ai-backend test:background-job-registry`
  - `pnpm --filter juhe-ai-backend typecheck`
  - `pnpm --filter juhe-ai-backend test:background-worker-performance`
- 2026-06-14 完成阶段 2 metrics-worker 代码落地，已执行：
  - `pnpm --filter juhe-ai-backend test:background-metrics-worker-role`
  - `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke`
  - `pnpm --filter juhe-ai-backend test:system-metrics-process-latest`
  - `pnpm --filter juhe-ai-backend test:background-job-registry`
  - `pnpm --filter juhe-ai-backend test:runtime-snapshot-unavailable-contract`
  - `pnpm --filter juhe-ai-backend typecheck`
  - `pnpm --filter juhe-ai-backend build`
  - `pnpm --filter juhe-ai-backend test:background-worker-performance`
  - `pnpm --filter juhe-ai-frontend typecheck`
  - `pnpm --filter juhe-ai-frontend build`
  - 发布产物启动 smoke：临时端口启动 `backend/dist/server.js`，确认阶段 2 的 2 个 worker 子进程、1 个 DB service 子进程、健康接口和四类进程事件循环样本；阶段 3 已复验 3 个 worker 子进程和常驻五类进程事件循环样本。
- 2026-06-14 完成阶段 3 首批 append-only ingest-worker 代码落地，已执行：
  - `pnpm --filter juhe-ai-backend typecheck`
  - `pnpm --filter juhe-ai-backend build`
  - `pnpm --filter juhe-ai-frontend typecheck`
  - `pnpm --filter juhe-ai-frontend build`
  - `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke`
  - `pnpm --filter juhe-ai-backend test:background-metrics-worker-role`
  - `pnpm --filter juhe-ai-backend test:background-job-registry`
  - `pnpm --filter juhe-ai-backend test:system-metrics-process-latest`
  - `pnpm --filter juhe-ai-backend test:runtime-snapshot-unavailable-contract`
  - `pnpm --filter juhe-ai-backend test:background-queue-health`
  - `pnpm --filter juhe-ai-backend test:background-ipc-protected-queue`
  - `pnpm --filter juhe-ai-backend test:background-ipc-payload-boundary`
  - `pnpm --filter juhe-ai-backend test:background-ipc-snapshot-current-only`
  - `pnpm --filter juhe-ai-backend test:worker-local-queue-limit`
  - `pnpm --filter juhe-ai-backend test:operation-log-queue`
  - `pnpm --filter juhe-ai-backend test:public-api-logs`
  - `pnpm --filter juhe-ai-backend test:usage-record-byte-batch`
  - `pnpm --filter juhe-ai-backend test:usage-record-batch-lookup`
  - `pnpm --filter juhe-ai-backend test:usage-record-snapshot-sanitize-boundary`
  - `pnpm --filter juhe-ai-backend test:usage-pricing`
  - `pnpm --filter juhe-ai-backend test:audit-log-async-flush`
  - `pnpm --filter juhe-ai-backend test:runtime-log-index-large-line`
  - `pnpm --filter juhe-ai-backend test:runtime-log-keyword-only-sql`
  - `pnpm --filter juhe-ai-backend test:runtime-log-search-guard`
  - `pnpm --filter juhe-ai-backend test:runtime-log-file-import-source`
  - `pnpm --filter juhe-ai-backend test:background-worker-performance`
  - 发布产物启动 smoke：临时端口启动 `backend/dist/server.js`，确认 3 个 worker 子进程、1 个 DB service 子进程、健康接口和常驻五类进程事件循环样本。
- 2026-06-15 完成默认 worker 剩余业务任务拆分，已执行：
  - `pnpm --filter juhe-ai-backend typecheck`
  - `pnpm --filter juhe-ai-backend test:background-metrics-worker-role`
  - `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke`
  - `pnpm --filter juhe-ai-backend test:system-metrics-process-latest`
  - `pnpm --filter juhe-ai-backend test:runtime-snapshot-unavailable-contract`
  - `pnpm --filter juhe-ai-backend test:background-queue-health`
  - `pnpm --filter juhe-ai-backend test:background-ipc-protected-queue`
  - `pnpm --filter juhe-ai-backend test:worker-local-queue-limit`
  - `pnpm --filter juhe-ai-backend test:background-job-registry`
  - `pnpm --filter juhe-ai-backend test:background-worker-performance`
  - `pnpm --filter juhe-ai-frontend typecheck`
  - `pnpm --filter juhe-ai-backend build`
  - `pnpm --filter juhe-ai-frontend build`
  - 源码启动拓扑 smoke：临时端口启动 `src/server.ts`，确认 `workerChildren=7`、`dbServiceChildren=1`，常驻事件循环样本包含 `server`、七类 worker 和 `db-service`。
  - 发布产物启动 smoke：临时端口启动 `backend/dist/server.js`，确认 `workerChildren=7`、`dbServiceChildren=1`，常驻事件循环样本包含 `server`、七类 worker 和 `db-service`。
- 阶段 2 上线后还需要在生产或仿真环境观察长任务压测下的系统指标采样连续性。
- 后续若实现阶段 5 / 6，需要补充租约抢占、租约过期接管、分片重复执行保护和 SQLite 锁竞争回归。
- 后续若继续扩展临时维护 worker 以外的并发任务，需要补充表监控 `non_business_data_cleanup` 以外的租约抢占、任务状态、进程退出、失败重试和超时取消回归。

## 风险与注意事项

- 多 worker 的目标是隔离和减少滞后，不是让 SQLite 并发写无限扩展。
- worker 数量不设固定上限，但每次新增 worker 都必须对应一个明确热点或隔离域，并有队列上限、租约边界、事件循环和锁等待观测。
- 临时 worker 不是常驻队列消费者；不能把系统采样、使用记录写入、日志索引或外部复测放进去。
- 全局窗口刷新任务默认保持单 owner；这些任务的优化方向是索引、分段、短事务和跳过无变化数据。
- `metrics-worker` 拆出后，需要避免把重统计任务又挂进去，否则隔离失效。
- 启动风险：外部 supervisor 仍只守护 server，server 内部 fork `worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker` 和 DB service；上线验证必须看子进程 PID、ready 状态和重启日志，不能只看 Web 端口打开。
- 重启风险：worker 崩溃后必须退避重启，不能秒级死循环拉满 CPU；如果某个角色反复退出，管理页必须能看到对应 snapshot 不可用，而不是空任务数组。
- IPC 风险：使用记录、审计、操作日志、公开接口日志和运行日志索引投递 ingest-worker；账号测试 / 取消投递 probe-worker；记录维护投递 maintenance-worker；metrics-worker 只接收状态和事件循环采样控制。任何新增消息类型都要登记 owner、队列上限、超时和丢弃策略。
- 观测风险：系统性能 / 网络吞吐趋势、进程事件循环趋势、后台任务表和模拟数据必须覆盖 `server`、`worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`、`temporary-maintenance-worker`、`db-service` 十类角色；其中 `temporary-maintenance-worker` 只在任务运行期间写样本，缺样本必须显示未知，不能用 0、空数组或默认时间伪装正常。
- 资源风险：每新增一个常驻 worker 都会增加 Node heap、SQLite 连接、文件句柄和日志输出；轻任务 worker 要避免打开无关数据库和启动无关队列。
- 数据一致性风险：统计游标、窗口发布、清理任务和历史重建在没有租约前只能单 owner；临时 worker 必须记录 runId、参数快照、状态、超时和退出结果。
- 部署风险：Mac / Linux / Windows 的子进程退出信号、路径、日志目录和打包产物入口不同，发布验证要覆盖构建产物启动 smoke；回滚方案不能依赖临时数据库 schema。
- 阶段 0 可以单独先发布止血，但不能因此关闭后续多 worker 计划；阶段 1 以后按本计划分批实施。
- 如果未来进入多服务器部署，应另开计划处理跨机器锁、进程发现、部署拓扑和故障转移；本计划只覆盖本机多进程。

## 完成总结

- 完成时间：计划整体未完成；阶段 1、阶段 2、阶段 3、阶段 4 和默认 worker 剩余业务任务拆分于 2026-06-15 完成。
- 实际完成内容：已完成 job registry 归属保护、`metrics-worker` 固定角色隔离、`ingest-worker` 写入隔离、`stats-worker` 增量统计隔离、`snapshot-worker` 重窗口快照隔离、`probe-worker` 外部探测隔离、`maintenance-worker` 维护清理隔离，以及 `temporary-maintenance-worker` 临时维护落地。默认 `worker` 只保留控制 / fallback；metrics-worker 只承载系统采样与事件循环采样协调；ingest-worker 承载使用记录、审计、操作日志、公开接口日志和运行日志索引五类 append-only 写入；临时维护 worker 承接 `usage_records_cleanup` 和 `non_business_data_cleanup` 这类一次性维护任务。
- 主要改动位置：`backend/src/modules/background/`、`backend/src/worker.ts`、`backend/src/config/runtime.ts`、`backend/src/shared/process-event-loop-monitor.ts`、`backend/src/storage/`、`backend/src/modules/db-service/`、`backend/src/modules/stats/stats.routes.ts`、`backend/src/scripts/regression/`、`backend/src/scripts/maintenance/`、`frontend/src/views/stats/`、`frontend/src/types/domain/`、`docs/plans/计划-0045-后台Worker轻量拆分与任务租约.md`、`docs/architecture/架构总览.md`、`docs/architecture/backend/后台Worker多角色拆分设计.md`、`docs/architecture/backend/后台任务使用说明.md`、`docs/develop/运行说明.md`、`docs/deploy/部署指南.md`、`docs/functions/`
- 验证结果：阶段 2 / 阶段 3 / 默认 worker 剩余拆分相关后端回归、后端类型检查、后端构建、前端类型检查、前端构建、源码七 worker 启动 smoke、发布产物七 worker 启动 smoke 和 `test:temporary-maintenance-worker` 已通过；生产采样连续性和各 worker 队列积压需要随上线观察。
- 后续建议：继续把公开接口日志、Client IP 命中 flush 等剩余 append-only / ingest 候选按热点纳入后续拆分，再推进阶段 5 任务租约；不要按 CPU 核数复制同构 worker。
