# SQLite 单写者写队列治理设计

> 面向后端实现、后台 worker 和数据库维护者。
> 本文定义当前 Node standalone 模式下 SQLite 多进程写入治理的过渡边界。执行计划见 [PLAN-20260618T024133000Z SQLite 单写者写队列治理](../plans/计划-20260618T024133000Z-SQLite单写者写队列治理.md)。PostgreSQL performance 模式的并发写入边界见 [PostgreSQL 与 Redis 高性能模式设计](PostgreSQL与Redis高性能模式设计.md)。
> 迁移方向更新（2026-08-08）：SQLite 单写者是 Go 可运行于 SQLite 模式的前提，而非待删除对象。B0 兼容验证与未迁 Node 功能可经既有 typed command / owner bridge 写入；完成 F3 的 Go 完整功能必须独占目标 file owner，不能继续依赖 Node bridge。每个 SQLite 文件继续只有一个 writer owner。SQLite 与 PostgreSQL/Redis 均为正式目标模式，完整规则见 [完整功能接管与 Node 归档迁移规则](../migration/完整功能接管与Node归档迁移规则.md)。

## 背景

当前系统已经拆出 DB service 和多角色后台 worker，但 SQLite 的基础约束没有改变：同一个 SQLite 文件同一时间只能有一个 writer。WAL 可以让读写更友好，`busy_timeout` 可以吸收短暂冲突，但不能把一个库变成多 writer 数据库。

生产出现频繁 `database is locked` 时，根因通常不是某一个 SQL 报错，而是多个进程同时写同一个 SQLite 文件，或低优先级长事务占住写锁，导致高优先级写入也被拖住。

因此后续治理目标不是“每个 worker 内部各自加队列”，而是按 SQLite 文件建立单写者边界：凡是写同一个数据库文件的操作，必须经过同一个 owner / writer 队列串行提交。

## 设计目标

- 每个 SQLite 文件只有一个明确写 owner。
- 所有写入都通过 typed command 或明确 repository 入口提交，禁止散落的跨进程直接写。
- 写 owner 支持优先级、批量、合并、重试、blocked 降级和运行态指标。
- 保持 standalone 轻量单机部署，不在 SQLite 模式引入 Redis、Kafka、PostgreSQL 或外部分布式锁。
- 业务强一致写入同步确认；记录类和统计类写入按语义允许异步、延迟或 best-effort。

## 非目标

- 不把 SQLite 包装成分布式数据库。
- 不为旧 schema 或旧数据结构写运行时兼容分支。
- 不把所有写操作粗暴放进一个全局 FIFO 队列。
- 不把事实明细改成 last-write-wins。
- 不为了消除锁等待牺牲审计、使用记录、授权和账号状态语义。

## 写 owner 划分

| 数据库文件 | 写 owner | 可并行边界 | 说明 |
| --- | --- | --- | --- |
| 业务库 `JUHE_AI_DATABASE_PATH` | DB service | 无，同一业务库串行写 | 管理 CRUD、账户状态、API Key、授权、会话、账号测试任务状态等核心事实写入。 |
| 数据集目录库 `JUHE_AI_DATASET_DATABASE_PATH` | ingest / log writer | 无，同一数据集目录库串行写 | 审计、操作日志、公开接口日志、运行日志索引、模型检测、清理目标等记录类写入。 |
| 统计结果库 `JUHE_AI_STATS_DATABASE_PATH` | stats writer | 无，同一统计库串行写 | 统计聚合、窗口刷新、系统采样、表监控、任务状态、维护扣减账本等结果写入。 |
| usage shard 文件 | per-shard writer | 不同 shard 文件可并行，同一 shard 串行写 | `usage_records` 按日期与 shard key 路由；每个 shard 文件独立队列。 |

owner 是写锁归属，不等于业务角色。`stats-worker` 负责生产并提交系统采样和统计 command；`ops-worker` 可以生产账号状态 command，但业务库写入仍由 DB service owner 提交。

## 运行时强制边界

- 正常 server、DB service 和 worker 运行时默认启用 SQLite writer boundary strict 模式；非 owner 进程打开业务库、数据集目录库、统计库或 usage shard 时必须进入 `PRAGMA query_only = ON`，只允许读取，任何写 SQL 都应被 SQLite 拒绝。
- 生产环境不能关闭 strict 模式；开发环境只有显式设置 `JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT=false` 才允许临时关闭，且仅用于定位或离线验证。
- `src/scripts/` 和 `dist/scripts/` 下的维护 / 回归 / 造数脚本默认按离线脚本处理，可以直接打开当前 schema 写库；这些脚本不能作为常驻运行路径，也不能和生产进程并发抢同一 SQLite 文件。
- DB service 是业务库 owner，不是所有 SQLite 文件的 owner。DB service 内部 system API 可以读取数据集目录库和统计结果库，但任何写入数据集目录库或统计结果库的动作都必须转成 typed command，分别投递 ingest / dataset writer 或 stats writer。
- 模型检测属于 DB service system API 触发但写入数据集目录库的流程；运行记录、检测项和完成状态必须通过 dataset writer 转发给 `ingest-worker`，不能在 DB service 内直接调用数据集写 repository。

## command 语义

写队列只接受明确语义的 command，不接受任意 SQL 字符串或任意闭包。command 至少包含：

- `type`：稳定操作名。
- `targetDatabase`：业务库、数据集目录库、统计库或具体 usage shard。
- `priority`：`critical`、`normal`、`background`、`maintenance`。
- `idempotencyKey`：可重复执行操作必须提供稳定键。
- `coalesceKey`：状态 / 快照类操作可提供合并键。
- `createdAt`：入队时间，用于观测等待时长。
- `sourceRole`：生产该 command 的进程角色。

事实明细类 command 必须 append-only，不能 LWW 覆盖；状态 / 快照 / dirty 标记类 command 才允许按 `coalesceKey` 保留最后一次。

## 优先级

| 优先级 | 例子 | 处理规则 |
| --- | --- | --- |
| `critical` | 管理写请求、账号状态确认、API Key 状态、授权状态、会话安全状态 | 同步等待提交；失败返回可读错误或进入明确重试路径。 |
| `normal` | 使用记录、审计失败事件、操作日志、公开接口日志 | 批量短事务；失败保留队首批次或按安全策略降级。 |
| `background` | 统计增量、系统采样、质量缓存、分组统计刷新 | 固定批次，允许跳过本轮，不能阻塞 critical 写。 |
| `maintenance` | 保留期清理、历史窗口重建、非业务硬清理 | 低优先级、小批次、可 blocked 重试；不能长期占用写 owner。 |

writer 不能让低优先级大任务饥饿高优先级写入。长窗口刷新、保留清理和物理删除必须拆成 staged command 或短批次 command。

当前 ingest-worker 的 IPC 入队按语义分组限流：`usageRecords` 使用独立队列；运行日志、审计、操作日志、公开接口日志、dataset writer 同步请求等进入高优先级 regular 组；`recordMaintenance` 作为低优先级维护组独立计数并在出队时让位给高优先级 regular。这样仍保持数据集目录库 / usage shard 只有 ingest 一个写 owner，但不会让保留期清理压死日志索引或模型检测写入。

DB service 的父进程 IPC 请求同样必须支持优先级：后台管理、网关请求链路、账号状态、API Key、授权、会话等用户可感知同步写入默认进入高优先级；过期会话清理、账号测试任务维护、授权过期扫描、分组统计 dirty 标记和全量刷新游标等定时维护写入进入低优先级。DB service 每处理一个父进程 IPC 写请求后都要让出事件循环，避免低优先级维护请求连续 drain 导致 DB service 内部 system API / 管理操作出现明显卡顿。HTTP 形式进入 DB service 的系统管理 API 不经过父进程 IPC 队列，但仍共享同一个业务库 owner，因此维护类 IPC 必须主动让位给管理面请求。

## 事务规则

- 写事务必须短：读取、计算、网络请求、payload 压缩、大 JSON 处理都放在事务外。
- 需要抢写锁时使用 `BEGIN IMMEDIATE`，不要在读计算期间持有事务。
- 跨库流程必须拆成按库提交的短事务，失败时依靠幂等账本、blocked reason 或后续重试衔接。
- 统计窗口发布优先 staged publish，避免长时间 `DELETE + INSERT` 占住统计库。
- 退出 flush 只允许有限批次，不能在 `exit` / `beforeExit` 中无界同步写库。

## 锁冲突处理

SQLite locked 不是统一硬失败：

- 用户可见业务写：返回可读错误，必要时要求重试；不能静默丢弃。
- append-only 事实：保留队首批次等待重试，队列达到上限后按语义丢弃低价值项并计数。
- 维护清理：写入 `last_blocked_reason` 或任务状态，下一轮继续。
- 统计 / 快照：本轮跳过并记录 job state，不能阻塞请求链路。
- 系统采样：允许丢失个别样本，但必须记录 dropped / failed 指标。

`busy_timeout` 只作为短冲突缓冲，不作为主要治理手段。持续 locked 必须回到 owner、长事务、低优先级任务和直接写路径排查。

## 运行态指标

每个 writer 至少暴露：

- 队列长度、估算字节数、最老等待时间。
- 最近 flush 成功 / 失败时间。
- 当前执行 command type、批量大小和事务耗时。
- locked / busy 次数、连续失败次数、丢弃次数。
- 按优先级的 pending 数量。
- owner 进程角色、PID、ready 状态和事件循环延迟。

运行态页面不能把 unknown 展示为 0；writer 不可用时必须明确标记不可观测。

## 实施顺序

1. 静态盘点所有 `getBusinessDatabase()`、`getDatasetDatabase()`、`getStatsDatabase()` 和 usage shard 写入路径，按目标库、角色、任务归属分类。
2. 先收口业务库写入：除 DB service 和测试 / 脚本外，worker 不直接写业务库，改为 DB service typed operation。
3. 收口统计库写入：由 `stats-worker` 作为 stats writer owner 串行提交系统采样、增量聚合、窗口刷新、表监控、任务状态和统计清理；`ingest-worker` 和 `ops-worker` 不直接写统计库。
4. 收口数据集目录库写入：确认 ingest writer 是唯一 owner；维护清理需要通过低优先级 command 短批次提交。
5. 强化 usage shard per-shard writer：同一 shard 只允许 ingest writer 写入；统计聚合只读取 shard，不能把估算字段回写到原始 shard；不同 shard 文件可在 owner 内分批处理。
6. 增加直接写边界回归：禁止非 owner 运行时代码直接写目标库。
7. 增加锁竞争回归：模拟统计库、数据集库、业务库写锁占用，验证 blocked / retry / 快速失败语义。

## 主要隐患

- **worker 绕过 owner 直接写业务库**：探测、冷却复测、时间计划同步、授权到期扫描等路径容易直接改 `accounts`、`api_keys` 或授权表。
- **过渡期 worker -> server -> DB service 转发桥失控**：如果 worker 新增写回只转发 message、不校验 operation、或者 server 继续允许任意 DB service write op，会把写 owner 变成伪单写者。
- **统计库多角色同时写**：系统采样、统计聚合、窗口刷新、表监控 / 清理统一收口到 `stats-worker` typed operation，新增 stats 写入不得落回 `ingest-worker` 或 `ops-worker`。
- **数据集目录库 append-only 与清理抢锁**：ingest 高频写日志时，维护清理如果直接删除数据集表，会放大 locked。
- **长事务窗口刷新**：范围窗口、TopN、概览或授权窗口如果单事务过大，会压住系统采样和增量统计。
- **跨库事务链路**：dataset / stats / usage shard 混在一个流程里等待，会把一个库的 locked 扩散到其他库。
- **队列无优先级**：低价值清理或重建排在前面时，高优先级状态写入会被 FIFO 拖住。
- **command 不幂等**：重试可能重复扣减、重复写日志、重复推进游标。
- **运行态不可观测**：只看到 `database is locked`，看不到哪个 writer、哪个 command、等待多久和持锁阶段。

## 验收标准

- 运行时代码中，同一个 SQLite 文件只有一个写 owner。
- 非 owner 进程不能直接执行目标库写 SQL；静态回归能捕获新增绕行路径。
- 模拟写锁占用时，高优先级业务写、append-only 写、统计写和维护清理分别进入正确的失败 / 重试 / blocked 语义。
- 运行态可看到各 writer 的 pending、oldest wait、locked、失败、丢弃和当前 command。
- 后端类型检查和相关队列 / 锁竞争回归通过。
