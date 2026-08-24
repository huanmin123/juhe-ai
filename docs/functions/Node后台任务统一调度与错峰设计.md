# Node 后台任务统一调度与错峰设计

## 1. 定位

本文定义当前 Node 过渡阶段周期任务的统一调度契约。目标是减少固定相位碰撞、限制单轮工作量、保证多部署单 owner 正确性，并让运行态准确表达超时、跳过、合并补跑和租约状态。本文不改变 Go 目标架构，也不把当前 Node scheduler 扩展成通用工作流平台。

## 2. 任务分类

| 分类 | 调度语义 | 典型任务 |
| --- | --- | --- |
| 高频观测 / 控制 | `fixedRate`、稳定秒槽、禁止每轮随机漂移 | system sample、状态同步、熔断维护 |
| 低频轻任务 | 稳定初始相位、小范围实例相位 | reconcile、expiry sweep |
| 新鲜度驱动重统计 | `fixedRate`、`coalesceOne`、资源 lane、租约 | 排行、趋势、overview、范围窗口 |
| 纯存储维护 | `fixedDelay`、资源 lane、租约 | 表监控、retention、日志索引 |
| 对象到期任务 | 持久化 `due_at`、全局被动偏移、短事务 claim | 健康检查、OAuth、余额 |
| 用户控制周期 | 保持用户周期，只治理 claim / 并发 / timeout | 模型质量检查 |
| 事件驱动微批 | 不加周期 jitter，使用队列上限和背压 | usage、audit、operation |

## 3. Scheduler 契约

- `scheduleMode`：`fixedRate` 以计划时间为锚点；`fixedDelay` 在上轮结束后再等待周期。
- `stablePhaseWindowMs`：使用 `jobName + workerRole + stableInstanceId` 派生启动相位，只用于初始分散；每轮被动执行还必须通过全局分级偏移重新采样。`production + performance` 未显式设置 `JUHE_AI_INSTANCE_ID` 时启动直接失败；standalone / 非生产环境保留 PID fallback。
- `overlapPolicy`：轻任务可 `skip`；重任务使用 `coalesceOne`，运行期间多次到期最多保留一个尾随执行。
- `timeoutMs`：scheduler 创建 `AbortController` 并传给 task；task 必须在外部请求、批次边界和数据库阶段响应取消。
- `failureBackoff`：失败使用有上限的 full-jitter backoff；成功后回归正常周期，不永久漂移。
- `resourceLane`：同一进程内按 lane 限制跨 Job 并发，等待 lane 的时间计入 overdue，不计入业务执行时间。
- `shutdown`：停止创建新 tick，取消可取消任务，并等待在途任务到关闭 deadline。

运行态至少包含：`nextRunAt`、`runningSince`、`overdueMs`、`pending`、`skippedCount`、`coalescedCount`、`timedOutCount`、连续失败、上次 outcome、lane 和 lease 状态。

## 4. 多实例语义

- 进程内 lane 只解决单部署资源竞争。
- PostgreSQL 单 owner 周期任务使用“短事务 `pg_try_advisory_xact_lock` + `background_job_leases` TTL + fencing token”。事务锁只保护 claim；任务本身在事务外运行，兼容 PgBouncer transaction pooling。
- claim、续期、释放和独立校验都通过带 `SET LOCAL statement_timeout / lock_timeout / idle_in_transaction_session_timeout` 的短事务执行。
- 会覆盖或推进权威状态的写事务在开头按 `leaseKey + ownerId + fencingToken` 校验租约，并使用 `SELECT ... FOR UPDATE` 把 lease 行锁持有到该短写事务提交，避免校验后被新 owner 接管、旧事务晚提交。
- 当前强制写栅栏覆盖 usage 聚合 cursor 与桶、quota window、排行/热窗口及范围窗口各 stage，以及 Chat retention 的状态推进。F2 表监控 cursor、快照写入和 retention 已由 Go 独立 owner 负责，不再进入 Node scheduler。固定 cutoff 的幂等 data-retention 删除保持 job lease + 有界批次，不额外引入跨 Redis/PG outbox。
- 短队列 / 到期对象使用 `FOR UPDATE SKIP LOCKED` 在短事务中 claim，外部 HTTP 调用必须在事务外执行。
- 租约失败是正常 `skipped`，不是错误；运行态记录当前 lease key 和未获得次数。
- SQLite 继续依赖单 owner 进程和幂等写入，不增加伪分布式锁表。

## 5. PostgreSQL 执行约束

- 重统计事务使用 `SET LOCAL statement_timeout` 和 `SET LOCAL lock_timeout`。
- 外部网络请求、sleep 和 scheduler lane 等待不得发生在数据库事务内。
- 大范围重建按 range / scope / batch 拆成短事务，保持统一锁顺序。
- 列表和预热使用 keyset cursor，不使用深 OFFSET。
- 批量写使用多行 INSERT / UPSERT；禁止 per-row transaction 和 N+1 查询。

## 6. 资源 lane

| lane | 默认并发 | 说明 |
| --- | ---: | --- |
| `stats-sample` | 1 | 高优先级短采样，不等待重窗口 |
| `stats-heavy` | 1 | 排行、趋势、overview、scope、authorization |
| `storage-maintenance` | 1 | Chat/data retention、未迁移的日志/数据集维护；F1/F2 已由 Go 独立进程负责，不进入 Node lane |
| `external-diagnostics` | 现有诊断上限 | 健康、恢复探针等共享上游并发 |
| `external-account-maintenance` | 1 | 代理、OAuth、余额等后台批任务；任务内部再使用各自的小并发上限 |

资源 lane 不允许长期持有数据库连接；任务取得 lane 后再开始业务阶段，并在每个有界阶段后检查 abort。

## 7. 具体任务规则

- 账户健康检查保持 `1h` 基础周期并按全局被动策略每轮重新偏移，AI 健康监控只读其结果。
- 模型质量检查保持用户周期；scheduled / recovery / health-sync retry 使用独立 claim 和真实 batch outcome。
- 系统趋势只刷新受最新小时变化影响的范围；封口历史窗口不反复删除重建。
- PostgreSQL quota、usage overview、AI 性能摘要使用“事实写事务内标 dirty + generation CAS + `SKIP LOCKED` 小批领取”。在线刷新只删除或 upsert 本轮领取 scope 的目标行，不再全表删除。
- quota 配置集合变化使用独立 keyset seed cursor 分批标记已有额度 scope；整点过期也按离开窗口的小时桶标 dirty。即使本轮没有新 usage，聚合任务仍执行一次轻量 quota drain，避免安静账户保留陈旧值。
- overview 每轮最多领取 8 个系统账户；热窗口与 30 分钟窗口任务共用 dirty 队列。dirty 未排空时不能因 source watermark 未变化而跳过。
- AI 性能摘要从 TopN 组合任务拆出为独立 5 分钟错峰任务，每轮最多领取 10 个系统账户、只重算与 `min_stat_date..max_stat_date` 相交的窗口，并用 upsert 发布。SQLite 暂时保留原组合刷新语义。
- 首页预热只处理近期活跃、最近使用账户，并使用跨重启确定性的时间槽轮转、批次和总时间预算；performance 多 usage-worker 时仅 replica 0 注册，避免每个副本重复读取候选和预热相同账户。
- 首页预热正常路径读取既有 7 日 `usage_overview_summary_windows` 排序索引；窗口尚未生成时最多执行 7 个单日 Top-512 查询，候选池 128、每轮热门 8 + 轮转 24，PostgreSQL 候选事务使用 1.5 秒 statement timeout。
- F2 表监控由唯一 `juhe-ai-go-sidecar` 内部组件直接异步并发采样；SQLite 使用只读源和精确 `COUNT(*)`，PostgreSQL 使用 catalog/relation size，快照与 retention 均由 Go owner 负责，不进入 Node scheduler 或中央清理任务。
- Codex Context 的 cursor 模式连 shard 目录查询和 PRAGMA 访问也受 pair budget 约束；局部扫描不伪装成全库汇总，数据库级未知值写 `NULL`。
- performance process publisher 使用实例、角色和 replica 派生 0～5 秒稳定相位；sampler 放在 publisher 波次之间。
- performance process publisher 的 Redis key 包含 `instanceId + processRole`，读侧保留公共前缀扫描以兼容滚动升级，退出实例仍由 TTL 清理。
- system metrics 趋势水位使用最大更新时间加同毫秒行指纹，空变更不退化全量；同轮系统样本和进程事件循环样本在 PostgreSQL 中合并为一个短事务。
- 代理、OAuth、余额任务使用总时间预算、有限并发和跨轮 cursor / claim，不允许理论最长执行时间长期超过周期。
- scheduler 取消信号到达后，代理、OAuth 和模型质量 scanner 停止领取新候选；已开始的 OAuth token rotation 按 refresh-token 轮换安全要求完成并写回。
- Redis cache/state 操作使用端到端绝对 deadline；超时或取消会销毁并移除共享 client，避免半开连接让 prewarm/OAuth 永久占用 lane。
- model-trust 与 account-quality 进入 `stats-online` lane，具备稳定相位、timeout/backoff、PG job lease 和写事务 fencing；account-quality dirty marker 使用行版本 CAS，避免并发 usage 标记被误删。
- data-retention 在每个新批次、阶段和 pause 前响应 scheduler signal；Codex Context 删除过期索引时会在同一数据库事务内把 storage key 写入持久清理队列，文件不存在按幂等成功确认，删除失败记录次数、错误和指数退避时间。scheduler signal 只阻止领取下一批，已返回的 storage keys 必须完成本批文件删除与成功/失败确认后再退出，避免索引已删但文件线索永久丢失。

`background-job-registry` 的 `leaseRequired / singleOwner` 是治理清单元数据，不等于自动执行约束。运行时正确性必须由以下机制之一提供：job 级 PostgreSQL lease、对象级 claim/lease、明确的单 worker 拓扑 owner；仅登记但没有运行时机制的任务不得在文档中宣称已经具备分布式单 owner。

## 8. 验证

- scheduler 使用 fake clock 验证相位、fixed-delay、coalesce、timeout、backoff 和 shutdown。
- PostgreSQL 使用两个独立客户端验证 lease claim、TTL 接管、fencing 和释放，确认不存在 session advisory lock 泄漏；对象队列继续用短事务验证 `SKIP LOCKED` claim。
- Redis 使用隔离 prefix 验证 publisher 相位和 key TTL，不读取或删除现有业务 key。
- 统计和表监控使用规模化 fixture 验证 SQL 数量、事务时长、cursor 推进和结果一致性。
- 真实环境 smoke 必须创建唯一临时数据库并在 `finally` 精确删除；Redis 只扫描本任务前缀。直连 PostgreSQL 与 PgBouncer transaction-pooling 入口都必须通过租约 smoke。
