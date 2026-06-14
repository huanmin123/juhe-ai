# 后台 Worker 多角色拆分设计

> 面向后端实现、部署和 AI 维护者。
> 本文是 `PLAN-0045` 的开发设计入口，用于约束后台 worker 多角色拆分，避免实现时跑偏。计划进度见 [PLAN-0045 后台 Worker 轻量拆分与任务租约](../../plans/计划-0045-后台Worker轻量拆分与任务租约.md)，现有后台任务使用规则见 [后台任务使用说明](后台任务使用说明.md)。

## 1. 设计目标

- 先上线当前慢 SQL / 索引修复，降低生产 worker 被单个统计窗口任务拖住的风险。
- 后续多 worker 必须基于 job / 队列盘点和热点隔离推进，不按 CPU 核数复制同构 worker。
- worker 数量不设固定上限，由热点隔离、队列积压、事件循环延迟、统计滞后和 SQLite 锁等待实测决定。
- 热点功能必须完全隔离：使用记录写入、日志 / 审计写入、系统采样、重型统计窗口、外部探测和维护清理不能共用一个会被大任务长期占满的事件循环。
- 保持轻量部署边界：当前不引入 Redis、Kafka、BullMQ、Kubernetes job 或新的外部调度服务。

## 2. 非目标

- 不把多个同构 `worker.ts` 按核数直接启动。
- 不让多个 worker 同时执行同一个全局窗口删除 / 重建任务。
- 不把前端、API route 或网关请求路径变成统计汇总或任务调度路径。
- 不为了多 worker 保留旧 schema、旧字段或旧部署兼容分支。
- 不在第一步直接实现分布式多服务器调度；本期只覆盖本机多进程。

## 3. 核心原则

### 3.1 任务先归类，再定 worker

开发第一步不是拆进程，而是把当前所有 `scheduler.schedule(...)`、worker IPC 队列、内部 flush timer 和维护入口纳入统一 job registry。每个任务必须有明确元数据：

| 字段 | 说明 |
| --- | --- |
| `jobName` | 稳定任务名或队列名 |
| `kind` | `sample`、`ingest`、`stats`、`snapshot`、`probe`、`maintenance`、`log` |
| `lifecycle` | `persistent`、`temporary`、`hybrid`，说明任务由常驻 worker、临时 worker，还是常驻协调加临时执行 |
| `defaultRole` | 默认 worker 角色 |
| `hotspot` | 是否热点功能 |
| `singleOwner` | 是否必须单 owner |
| `shardable` | 是否允许分片并行 |
| `leaseRequired` | 多进程下是否需要租约 |
| `blocksUserVisibleFreshness` | 卡住后是否影响页面 / 统计新鲜度 |
| `writes` | 主要写入库或表族 |
| `notes` | 特殊边界，例如 append-only、外部请求、窗口重建 |

registry 必须有回归脚本保护：新增 `scheduler.schedule` 或 worker IPC 消息类型时，如果没有登记到 registry，测试失败。

### 3.2 热点必须隔离

以下热点不能和重任务共享同一个 worker：

| 热点 | 必须隔离原因 | 初始角色 |
| --- | --- | --- |
| 使用记录写入 | 网关请求事实源，积压会拖慢统计和排障 | `usage-ingest-worker` 或 `ingest-worker` |
| 审计 / 操作 / 运行日志写入 | 排障事实源，不能被统计窗口拖住 | `log-worker` |
| 系统指标采样 | 判断系统是否卡住的基础观测，不能被被观测对象拖住 | `metrics-worker` |
| 重型窗口刷新 | 可能长时间占用 SQLite 和事件循环，必须限制影响范围 | `snapshot-worker` |
| 外部探测 | 上游网络不可控，不能阻塞统计写入和采样 | `probe-worker` |
| 保留期和记录清理 | 低频批处理，必须独立限流 | `maintenance-worker` |

同类热点可以继续横向拆分。比如写入压力高时，可以从 `ingest-worker` 继续拆出 `usage-ingest-worker`、`audit-log-worker`、`runtime-log-worker`，不受固定数量限制。

### 3.3 单 owner 与可分片边界

默认单 owner：

- 先删除再重建同一窗口表的任务。
- 数据保留清理、审计热保留清理、已删除资源关联清理。
- 系统设置同步、授权到期扫描、API Key 可用时段同步。
- 外部探测任务中的同一个账号 / 代理目标。

可分片候选：

- `usage-stats-aggregation`：按 usage shard 或日期 shard。
- `client-ip-stats-aggregation`：按 usage shard 或 IP bucket。
- append-only 写入队列：按队列类型、目标表族或 shard 拆 worker。

可分片不代表直接并发。进入分片前必须先有任务租约、重复执行保护、锁等待观测和回归测试。

### 3.4 持久 Worker 与临时 Worker

worker 拆分必须同时看“角色”和“生命周期”。角色回答任务属于哪个隔离域，生命周期回答进程是否需要常驻。

| 生命周期 | 适用任务 | 运行规则 |
| --- | --- | --- |
| `persistent` | 系统采样、高频 append-only 写入、日志索引、增量统计、外部复测等需要持续消费队列或固定周期运行的任务 | 由 supervisor 常驻守护，崩溃后重启，不能承载一次性大清理 |
| `temporary` | 表管理手动清理、非业务数据硬清理、历史重建、一次性修复、批量回收等有明确参数和结束条件的任务 | 按任务启动，拿到租约或 runId 后执行，完成 / 失败 / 超时后退出 |
| `hybrid` | 每天保留期清理、已删除资源清理重试等既需要固定唤醒，又可能遇到历史欠账的任务 | 常驻 worker 只负责扫描、投递、记录状态；重型执行可交给临时 worker |

表监控中的 `non_business_data_cleanup` 属于临时维护任务：管理接口只负责投递任务，临时 worker 按 `cutoffAt`、`batchSize`、`maxBatches` 分批执行，跑完本轮就退出；如果仍有 `hasMore`，由任务状态或下一次投递继续推进，不能让它长期占用常驻维护 worker。

临时 worker 的硬要求：

- 必须有稳定 `jobName`、`runId`、参数快照、提交时间、开始时间、结束时间和最终状态。
- 必须有最大批次数、最大运行时间或可取消边界。
- 必须可重复执行；失败重试不能重复扣减统计或越过安全游标。
- 不能承载系统采样、高频写入、日志索引或任何需要常驻队列消费的热点任务。
- 默认单 owner；需要并发时先进入任务租约和分片设计。

## 4. Worker 角色

| 角色 | 生命周期 | 职责 | 不允许承载 |
| --- | --- | --- | --- |
| `metrics-worker` | `persistent` | `system-metrics-sample`、进程事件循环采样协调、系统指标原始样本和小时桶写入 | 用量聚合、窗口刷新、清理、外部探测 |
| `worker` | `persistent` | 控制 / fallback 角色，保留运行态快照、事件循环采样响应和后续轻量控制入口 | 统计、写入、探测、维护、系统采样 |
| `ingest-worker` | `persistent` | 使用记录、审计、操作日志、公开接口日志、运行日志索引和运行日志文件导入等高频 append-only 写入；后续可拆更细 | 重型窗口刷新、保留期清理 |
| `log-worker` | `persistent` | 审计、操作日志、运行日志索引、运行日志文件导入 | 用量统计窗口、外部探测 |
| `stats-worker` | `persistent` | 用量增量聚合、IP 聚合、分组账号缓存、额度快照 | TopN / 范围窗口重建、长清理、外部探测 |
| `snapshot-worker` | `persistent` | TopN、概览、usage scope 范围窗口、授权范围窗口、系统趋势窗口 | 高频写入、系统采样 |
| `probe-worker` | `persistent` | 代理检测、账号级 / Key 级冷却复测、手动账号测试、OAuth token 保活 | 使用记录 flush、统计窗口 |
| `maintenance-worker` | `persistent` / `hybrid` | 轻量到期扫描、清理重试协调、授权到期扫描、表空间监控、数据维护队列协调 | 高频写入、系统采样、长时间硬清理 |
| `temporary-maintenance-worker` | `temporary` | 表管理非业务数据硬清理、手动使用记录清理、一次性历史重建、批量修复和其他有明确结束条件的维护任务 | 常驻调度、热点队列、系统采样 |

角色可以先合并部署，但合并必须显式记录。例如初期可以让 `ingest-worker` 和 `log-worker` 共用一个进程；如果该进程出现队列积压，就按热点继续拆。

## 5. 当前任务初始归属

### 5.1 定时 job

| job | 初始角色 | 边界 |
| --- | --- | --- |
| `system-metrics-sample` | `metrics-worker` | 热点采样，最高隔离优先级 |
| `system-metrics-trend-windows-refresh` | `snapshot-worker` | 如果证明足够轻，后续可评估是否靠近 `metrics-worker`，但不能影响采样 |
| `usage-stats-aggregation` | `stats-worker` | 后续可按 usage shard 租约分片 |
| `client-ip-stats-aggregation` | `stats-worker` | 后续按 usage shard / IP bucket 分片 |
| `group-account-stats-refresh` | `stats-worker` | 单 owner |
| `usage-rank-snapshots-refresh` | `snapshot-worker` | 单 owner |
| `usage-overview-windows-refresh` | `snapshot-worker` | 单 owner |
| `usage-scope-range-windows-refresh` | `snapshot-worker` | 单 owner，优先索引和分段优化 |
| `authorization-usage-range-windows-refresh` | `snapshot-worker` | 单 owner |
| `usage-stats-consistency-check` | `maintenance-worker` | 低频 |
| `api-key-record-cleanup-retry` | `maintenance-worker` | 单 owner |
| `account-record-cleanup-retry` | `maintenance-worker` | 单 owner |
| `api-key-availability-schedule-status-sync` | `maintenance-worker` | 单 owner |
| `resource-authorization-expiry-sweep` | `maintenance-worker` | 单 owner |
| `table-storage-monitor` | `maintenance-worker` | 低频 |
| `proxy-latency-refresh` | `probe-worker` | 外部请求 |
| `account-quality-refresh` | `probe-worker` | 依赖使用记录聚合，同时会投递失败预检队列，不能让统计 worker 承担外部探测排队 |
| `openai-oauth-access-token-refresh` | `probe-worker` | 外部请求，单 owner |
| `cooldown-account-retest` | `probe-worker` | 外部请求，避免阻塞统计 |
| `account-api-key-cooldown-retest` | `probe-worker` | 账户内 API Key 级外部复测，避免阻塞统计 |
| `runtime-log-index-maintenance` | `log-worker` | 日志维护 |
| `audit-hot-retention-cleanup` | `maintenance-worker` | 单 owner |
| `data-retention-cleanup` | `maintenance-worker` / `temporary-maintenance-worker` | hybrid；常驻 worker 可负责唤醒，历史欠账或长清理交给临时 worker |
| `expired-deleted-account-cleanup` | `maintenance-worker` / `temporary-maintenance-worker` | hybrid；常驻小批重试，积压大时临时执行 |

### 5.2 异步队列

| 队列 / 入口 | 初始角色 | 边界 |
| --- | --- | --- |
| `background_worker_usage_records` | `usage-ingest-worker` 或当前 `ingest-worker` | 高频 append-only，优先级高于聚合读取 |
| `background_worker_audit_logs` | `log-worker` 或当前 `ingest-worker` | append-only，不能 LWW |
| `background_worker_operation_logs` | `log-worker` 或当前 `ingest-worker` | append-only |
| `background_worker_public_api_logs` | `log-worker` 或当前 `ingest-worker` | 公开接口调用明细，append-only，不能被统计窗口拖住 |
| `background_worker_runtime_log_line` | `log-worker` 或当前合并到 `ingest-worker` | 不能被统计重活拖住 |
| `startRuntimeLogFileImport()` | `log-worker` 或当前合并到 `ingest-worker` | 按 cursor / offset 追增量 |
| `background_worker_record_maintenance` | `maintenance-worker` / `temporary-maintenance-worker` | 快照类可合并；`usage_records_cleanup`、`api_key_related_cleanup`、`account_related_cleanup` 单 owner；`non_business_data_cleanup` 优先临时 worker |
| `background_worker_account_test_tasks` | `probe-worker` | 外部请求 |
| `gateway-account-side-effects` | `maintenance-worker` 或独立 `account-state-worker` | 状态写入，需继续保持合并和上限 |
| `public-api-log-queue` | 当前 `ingest-worker`；后续可拆 `log-worker` | 已走 `background_worker_public_api_logs` IPC，公开接口日志本地队列只在 ingest-worker 内落库 |
| `client-ip-policy-hit-buffer` | `ingest-worker` | 当前网关本地 flush，后续评估迁移 |

## 6. 实施阶段

### 阶段 0：慢 SQL / 索引止血

- 上线 `usage_scope_range_windows` 发布路径索引修复。
- 验证 `usage-scope-range-windows-refresh` 不再出现分钟级慢阶段。
- 这个阶段可以先单独发布，但不能关闭多 worker 后续计划。

### 阶段 1：job registry

- 新增后台 job / 队列 registry。
- `background-jobs.ts` 的 `scheduler.schedule` 统一引用 registry 中的 `jobName`。
- worker IPC 消息类型、内部队列和文件导入入口纳入 registry。
- registry 必须登记 `lifecycle`，并区分常驻任务、临时任务和常驻协调加临时执行的 hybrid 任务。
- 增加回归测试，保证 schedule job、IPC 队列和 registry 一致。
- 不改变运行行为，仍可由当前单 worker 承载全部任务。

### 阶段 2：`metrics-worker`

- supervisor 支持按角色启动 worker。
- `metrics-worker` 只启动系统采样和必要事件循环采样协调。
- API 返回能表达 `metrics-worker` 的采样可用性和进程状态。
- 压住 `snapshot-worker` 时，系统指标采样仍能稳定写入。

当前落地方式：

- 仍由 `server` 作为唯一需要外部进程管理器守护的入口，`background-worker-supervisor` 固定拉起 `worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker` 七个 `worker.ts` 子进程。
- 子进程统一保持 `JUHE_AI_PROCESS_ROLE=worker`，并通过 `JUHE_AI_WORKER_ROLE` 区分内部职责；这样不破坏既有 `processRole === 'worker'` 的运行时边界。
- 默认 `worker` 不再承载业务后台任务，只保留控制 / fallback 角色；`metrics-worker` 只注册 `system-metrics-sample` 和进程事件循环采样协调；`ingest-worker` 承接 append-only 写入；`stats-worker` 承接增量统计；`snapshot-worker` 承接重窗口快照；`probe-worker` 承接外部探测和账号测试；`maintenance-worker` 承接维护清理协调。
- `background-ipc` 按消息类型路由：使用记录、审计、操作日志、公开接口日志和运行日志投递 `ingest-worker`；账号测试和取消消息投递 `probe-worker`；记录维护消息投递 `maintenance-worker`；metrics-worker 只接收快照和事件循环采样控制消息。
- 运行态快照和系统指标接口必须能区分 `server`、`db-service`、`worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker` 和 `temporary-maintenance-worker`，缺样本继续用 `sampleAvailable=false` 表达未知。

### 阶段 3：append-only 写入隔离

- 使用记录、审计、操作日志、运行日志索引和公开接口日志从重统计 worker 中剥离。
- 高频写入队列保留各自上限、丢弃 / 合并策略和运行态指标。
- 压住窗口刷新时，写入队列仍能按自身 worker flush。

当前落地方式：

- `ingest-worker` 已作为写入隔离常驻 worker 由 supervisor 拉起，专门承接 `background_worker_usage_records`、`background_worker_audit_logs`、`background_worker_operation_logs`、`background_worker_public_api_logs` 和 `background_worker_runtime_log_line` 五类 background IPC append-only 写入。
- `ingest-worker` 打开数据集目录库，安装使用记录、审计日志、操作日志、公开接口日志、运行日志索引五类本地队列 shutdown hook，接管 `startRuntimeLogFileImport()` 和 `runtime-log-index-maintenance`；默认 `worker` 不再启动这些 append-only 本地写队列。
- `background-ipc` 将 usage 写入放入 ingest 专用 usage 队列，将审计 / 操作 / 公开接口 / 运行日志放入 ingest regular 队列；两类队列分别有消息数和字节上限，拒绝计数进入运行态。
- `stats-worker` 承载 `usage-stats-aggregation`、`client-ip-stats-aggregation` 和 `group-account-stats-refresh`；`snapshot-worker` 承载 TopN、概览、范围窗口、授权窗口和系统趋势窗口刷新；`probe-worker` 承载账号质量刷新、账号级 / Key 级复测、代理检测、OAuth token 保活和手动账号测试；`maintenance-worker` 承载维护重试、授权到期扫描、表空间监控和保留期清理协调。
- 统计聚合和账号质量刷新在读取事实前会请求 ingest drain 状态；如果 server 到 ingest IPC 仍有使用记录积压、ingest 本地 usage 队列未清空或 flush 有失败，本轮统计 / 探测刷新跳过，避免读到未落地的事实。
- 运行态快照、队列健康和系统指标接口补充所有常驻 worker 的 snapshot available 字段，`cooldown-account-retest`、`account-api-key-cooldown-retest` 和 `account-quality-refresh` 的复测 / 预检队列必须随 `probe-worker` 快照进入后台任务表；进程事件循环样本覆盖 `server`、`worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`、`temporary-maintenance-worker`、`db-service` 十类角色；临时维护 worker 只有任务运行期间写入样本，平时显示缺样本。
- 公开接口日志当前已合并到 `ingest-worker`；若公开接口日志写入成为热点，再从 `ingest-worker` 拆出 `log-worker` 或更细的公开接口日志 worker。

### 阶段 4：临时维护 Worker

- 新增按任务启动的 `temporary-maintenance-worker` 执行模式。
- 表监控 `non_business_data_cleanup`、手动 `usage_records_cleanup` 和一次性历史维护任务优先走临时 worker。
- 常驻 `maintenance-worker` 只负责投递、状态记录和小批协调，不直接长期执行硬清理。
- 临时 worker 必须暴露任务状态、结束原因、耗时、处理行数 / 文件数和是否还有剩余。

当前落地方式：

- `record-maintenance-queue.service.ts` 由 `maintenance-worker` 消费维护队列；遇到 `usage_records_cleanup` 或 `non_business_data_cleanup` 时，只创建 `background_task_runs` 运行记录并 fork `temporary-maintenance-worker`。
- `temporary-maintenance-worker` 使用独立 Node 子进程执行单个 `runId`，通过 `background_task_runs` 保存参数快照、状态、结果、耗时、退出码和错误摘要；完成后退出，不进入 supervisor 常驻看护列表。
- `background_job_leases` 先作为临时任务单 owner 保护使用，当前 lease key 绑定单次 run，完成后释放；后续阶段 5 若要支持同一 `jobName + shardKey` 多 worker 竞争，需要继续补租约抢占、续约和过期接管回归。
- 临时 worker 会继承业务库、数据集库、统计库和 usage shard 根目录配置，保证在开发和发布产物中操作同一组数据库；源码运行时会通过 `tsx` loader 启动，发布产物优先使用 `dist/temporary-maintenance-worker.js`。

### 阶段 5：任务租约

- 新增 `background_job_leases` 或等价本机 SQLite 租约表。
- 同一 `jobName + shardKey` 只能一个 owner 执行。
- 租约支持心跳、过期接管和任务完成释放。
- 全局窗口刷新继续单 owner。

### 阶段 6：分片 worker

- 仅对 registry 标记为 `shardable` 的任务开放。
- 首批候选是 `usage-stats-aggregation` 和 `client-ip-stats-aggregation`。
- worker 数量由统计滞后、队列积压、事件循环延迟和 SQLite 锁等待决定，不写死上限。

## 7. 上线策略

- 阶段 0 可先发布；如果按现有发布方式需要重启服务，可能出现数秒连接抖动，但不需要停机维护窗口。
- 阶段 1 registry 不改变运行行为，可以随普通版本上线。
- 从阶段 2 开始会改变生产进程拓扑，必须同步部署文档、启动脚本、健康检查和回滚步骤。
- 每新增一个 worker 角色，先在默认关闭或合并承载模式验证，再切到独立进程。
- 回滚时应能把角色重新合并回当前单 worker 运行，不改变数据库当前 schema。

上线复查不能只看 CPU 是否下降，还必须覆盖：

| 风险面 | 必查项 | 处理原则 |
| --- | --- | --- |
| 启动拓扑 | server、worker、metrics-worker、ingest-worker、stats-worker、snapshot-worker、probe-worker、maintenance-worker、DB service 的 PID、ready 状态和重启日志 | 外部只守护 server，内部子进程由 server supervisor 拉起；缺任一子进程都算上线异常 |
| 重启稳定性 | 子进程异常退出后的退避重启、重复退出次数和最近错误 | 禁止无退避重启风暴；反复退出时管理页必须显示 snapshot 不可用 |
| IPC 队列 | 业务消息 owner、队列上限、超时、拒绝和丢弃计数 | append-only 写入投递 ingest-worker，账号测试投递 probe-worker，记录维护投递 maintenance-worker；metrics-worker 不能接业务消息 |
| SQLite 锁 | 写事务耗时、busy / locked 错误、统计滞后和 DB service 事件循环延迟 | 新 worker 不能靠增加并发写来压 SQLite；先短事务、索引和单 owner |
| 观测准确性 | 系统指标、事件循环趋势、后台任务表和模拟数据是否覆盖 `server`、`worker`、`metrics-worker`、`ingest-worker`、`stats-worker`、`snapshot-worker`、`probe-worker`、`maintenance-worker`、`temporary-maintenance-worker`、`db-service` 十类角色 | 临时维护 worker 平时可缺样本；所有缺样本必须显示未知，不能用 0、空数组或默认时间伪装正常 |
| 资源占用 | Node heap、RSS、SQLite 连接、文件句柄、日志输出和定时器数量 | 轻角色只打开必需资源，不能启动无关队列或无关数据库 |
| 临时 worker | runId、参数快照、状态、超时、退出码、失败重试和人工取消 | 临时任务跑完退出，不能替代常驻队列消费者 |
| 回滚路径 | 发布包入口、环境变量、数据 schema 和单 worker 合并承载能力 | 回滚不依赖临时 schema；必要时先把角色合并回默认 worker |

## 8. 验证要求

| 阶段 | 必跑验证 |
| --- | --- |
| 阶段 0 | `test:usage-rank-staged-refresh`、`test:background-worker-performance`、后端 typecheck |
| 阶段 1 | job registry 完整性回归、后端 typecheck |
| 阶段 2 | 系统指标采样不断档回归、runtime snapshot contract、后端 build |
| 阶段 3 | 使用记录 / 日志 / 审计队列隔离回归、队列上限回归 |
| 阶段 4 | 临时 worker 启动 / 退出、任务状态、失败重试、表监控清理边界回归 |
| 阶段 5 | 租约抢占、续约、过期接管、重复执行保护回归 |
| 阶段 6 | SQLite 锁等待、统计滞后、分片重复处理保护和压力验证 |

每个阶段完成后必须更新 `PLAN-0045` 的验证记录和完成总结。

## 9. 开发禁止事项

- 禁止新增未登记 registry 的 `scheduler.schedule`。
- 禁止新增未登记 registry 的 worker IPC 队列类型。
- 禁止把热点写入、系统采样或外部探测挂到 `snapshot-worker`。
- 禁止让 `metrics-worker` 承担统计窗口、清理或外部探测。
- 禁止让临时 worker 承担常驻队列、系统采样或高频写入。
- 禁止把表管理硬清理、历史重建或一次性修复长期挂在常驻维护 worker 里执行。
- 禁止多个 worker 同时无租约执行同一全局 job。
- 禁止为了拆 worker 在请求路径做统计汇总或同步写大表。
