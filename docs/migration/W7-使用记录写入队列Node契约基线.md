# W7 使用记录写入队列 Node 契约基线

> 状态：Node 仍是唯一 writer；本文冻结迁移输入和已知缺陷，不表示 Go writer 已实现、已切流或可以删除 Node。

## 1. 范围与证据

- 生产 owner：`backend/src/modules/gateway/usage/record-queue.service.ts`、`backend/src/storage/usage-record-writer-pool.ts`、`backend/src/storage/usage-record-writer-worker.ts`。
- 现有行为回归：`pnpm --filter juhe-ai-backend test:usage-record-writer-pool`、`pnpm --filter juhe-ai-backend test:usage-record-byte-batch`。
- 本文对应的漂移门禁：`pnpm --filter juhe-ai-backend test:usage-record-writer-queue-contract`。
- 读取侧仍是 Go W6 的 `juhe_usage.usage_records` reader；Node 保留 schema、分区、writer、queue、首屏缓存和增量发布，详见 [W6 记录与统计读接口迁移记录](W6-记录与统计读接口迁移记录.md)。

新增漂移门禁只核对 Node 源码和稳定标识，不能证明 ACK / 持久化的动态控制流顺序，也不能替代切流验收。writer-pool 与 byte-batch 回归提供 SQLite 行为证据；Redis / PostgreSQL 仍须在 W7 实现阶段补可注入行为测试和真实 crash/retry smoke。

## 2. 当前 owner 与投递路径

| 模式 / 角色 | 当前 Node 行为 | Go W7 的目标处理 |
| --- | --- | --- |
| `queueDriver=redis_stream` | 任意生产写入先冻结请求时定价，再写 `juhe-ai:queue:usage-records`；consumer group 固定 `juhe-ai:usage-record-writers`。写库成功后才 ACK，失败保留 pending 并重投。enqueue 失败向调用方抛出，禁止回退 IPC / 本地队列。 | 保持单一 durable queue、显式 ACK 和请求时事实冻结；可采用 Go 的 Redis consumer / outbox 设计，但不得复制 Node 本地队列回退分支。 |
| `server` 或非 ingest worker | 经 background IPC 投递 ingest worker。IPC 不可用时只计数 / 日志并丢弃该 usage。 | 这是确认的 Node 可靠性缺陷。Go 必须提供 durable enqueue 或明确返回可观测失败；不能继续 silent drop。 |
| `db-service` | 默认同样通过父进程 IPC 反投 server；无父 IPC 时只计数 / 日志并丢弃。测试才允许本地写。 | 删除 db-service 双向 IPC；Go writer 只有一个明确的运行时 owner。 |
| SQLite ingest worker | 本地内存 queue，500ms 调度，最多 1000 条或 8 MiB 一批；queue 上限 10,000 条 / 64 MiB，超量或超大新记录直接丢弃。shutdown 最多 drain 100 批。 | SQLite 专有缓解逻辑不迁移。Go + PostgreSQL 的队列背压应通过 durable queue、数据库连接池与有界 worker 控制。 |
| PostgreSQL ingest worker | 本地 queue 可并发 flush，最大并发取 `postgres.writeMaxConcurrency` 并夹到 `1..100`。 | 只迁移吞吐目标与有界并发原则，不复制 JS promise / 定时器结构。 |
| SQLite child writer pool | 仅 `sqlite + worker + ingest-worker + enabled + shardCount>1` 启用；按 shard 路由 child。child 写 shard 时 `registerLocation=false`，父 ingest worker 单写 catalog 与账户副作用。 | 不迁移。PostgreSQL 不需要 SQLite shard writer pool；Go 版本应采用事务、幂等键和单一 catalog / 分区元数据 owner。 |

## 3. 不可改变的业务事实

1. Node producer 在异步边界前确定 usage ID、规范化 `createdAt` 并清理 request/response snapshot；只有 Redis Stream 路径会在 enqueue 前显式执行 `freezeUsageRecordPricingFactsAsync`。本地 queue / IPC 路径仍可能在消费写库时补全定价，这是当前跨驱动语义差异，不是 Go 应复制的目标行为。
2. 账户和分组作用域是原子事实。`accountId` 必须与 owner、access type 以及需要时的 authorization source 同时有效；`groupId` 同理。任一元组不完整时 Node 入队规范化会整体清空该元组；`group_authorized` 账户还必须同时具有完整的 authorized group 事实。Go 不得保存孤立 ID 或自行猜测 owner。
3. 写入必须按 usage ID 幂等。SQLite 现有回归证明重复投递不会重复记录，且后续投递可补回账户 `last_used_at` 副作用。
4. durable Redis 模式只能在持久化成功后 ACK；失败消息必须可 claim/retry，切流前须有 backlog / pending drain 证据。
5. 一个业务部署内只能有一个 usage writer owner。Go reader 在 W6 共存期可以读取 Node 单写数据，但不可并行启动 Go writer。
6. shutdown 不能宣称无损：Node 本地模式明确最多 drain 100 批，容量拒绝与 IPC 不可用也会丢失。Go 接管设计必须单独定义 shutdown deadline、未完成任务交接和可观测告警。

## 4. 已确认缺陷与 Go 修复要求

| 缺陷 | Node 事实 | Go 修复要求 | 验收证据 |
| --- | --- | --- | --- |
| IPC 投递失败静默丢失 | server/db-service 的 IPC 不可用只增加 `droppedDispatchCount` 并写 warning，调用链仍可继续。 | 使用 durable enqueue / transactional outbox；不能把使用记录可靠性建立在进程父子关系上。 | 断开 consumer、重启 producer、重复消费、最终行数 / 幂等键 / backlog 均可核验。 |
| 本地 queue 过载丢新记录 | 10,000 条、64 MiB 或单条超过上限即丢弃新记录。 | 以持久队列背压、请求准入策略或显式降级指标替代 silent drop；业务方可见的状态必须明确。 | 满队列、超大 payload、恢复后的 backlog 和告警测试。 |
| shutdown drain 有固定截断 | Node 仅尝试 100 批，未完成数据留在内存时会随进程退出丢失。 | 实现可配置 deadline 与 durable handoff；停止时禁止接新、等待已取任务或归还队列。 | SIGTERM / crash/restart 真实 Redis + PostgreSQL smoke。 |
| queue driver 定价冻结时点不同 | Redis Stream 在 enqueue 前冻结请求时定价；本地 / IPC 路径可能在 consumer 写库时补价。模型目录在排队期间变化时，两个驱动可能得到不同历史事实。 | producer 统一冻结使用记录所需的模型、service tier、mapping 与成本快照；consumer 只验证并持久化，不读未来目录重新解释。 | enqueue 后修改模型目录，再消费并比对两种 producer 的 golden / PostgreSQL 结果。 |
| SQLite child-pool/catalog 双 owner 风险 | child 只写 shard，父进程补 catalog / account 副作用，必须严格维持该拆分。 | 不复制此结构；PostgreSQL 统一在事务中维护事实与必要副作用。 | 并发重复投递、账户副作用、分区 / catalog 一致性测试。 |

## 5. W7 实施边界

后续实现应先选择 PostgreSQL 事实表与 Redis durable queue 的唯一 owner，再按独立切片实现 producer、consumer、幂等持久化、stats dirty/aggregation、shutdown/recovery 和观测。不得先把 Node 的 `setTimeout`、IPC、child process pool 或 SQLite shard catalog 逐行搬到 Go。

在 Go writer 的 Redis/PostgreSQL 真实 smoke、Node writer -> Go reader 双向切流演练、backlog drain、崩溃恢复、指标和回滚记录完成前，本文只作为 Node 对照基线，Node writer 不得删除。
