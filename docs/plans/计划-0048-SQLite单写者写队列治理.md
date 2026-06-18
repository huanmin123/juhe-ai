# PLAN-0048 SQLite 单写者写队列治理

## 基本信息

- 编号：PLAN-0048
- 状态：已完成
- 创建时间：2026-06-18
- 需求来源：用户反馈多 worker 下频繁 `database is locked`，提出“凡是数据库写操作先进入对应写队列”
- 执行者：Codex
- 关联模块：后端 / SQLite / DB service / 后台 worker / 存储 / 统计 / 日志 / 维护清理 / 文档 / 验证

## 需求目标

在保持 SQLite 和单机轻量部署的前提下，把当前多 worker 并发写库治理成“按 SQLite 文件单写者”的架构：同一个 SQLite 文件的所有运行时写入只能通过唯一 writer owner / typed write command 串行提交，避免多个 worker 同时抢写导致频繁 `database is locked`。

## 范围边界

本次做：

- 建立长期设计文档，明确业务库、数据集目录库、统计结果库和 usage shard 的写 owner。
- 盘点所有运行时写入路径，按目标库、worker role 和任务类型分类。
- 优先收口高风险直接写路径：业务库写入走 DB service，统计库写入走 stats writer owner，数据集目录库写入走 ingest / log writer owner，usage shard 同 shard 串行。
- 补充 writer 运行态指标和静态边界回归。
- 增加锁竞争回归，确认 locked 时按业务语义进入 retry / blocked / 快速失败。

本次不做：

- 不引入 PostgreSQL、Redis、Kafka、BullMQ 或外部持久队列。
- 不保留旧 schema、旧字段或旧部署兼容分支。
- 不把事实明细队列改成 last-write-wins。
- 不把所有库的写入塞进一个全局 FIFO 队列。
- 不一次性实现分布式多节点写锁治理；分布式模式另走独立设计。

## 关联文档

- [SQLite 单写者写队列治理设计](../functions/SQLite单写者写队列治理设计.md)
- [SQLite 存储说明](../functions/SQLite存储说明.md)
- [整体架构设计](../architecture/架构总览.md)
- [后端架构设计](../architecture/backend/README.md)
- [后台任务使用说明](../architecture/backend/后台任务使用说明.md)
- [后台 Worker 多角色拆分设计](../architecture/backend/后台Worker多角色拆分设计.md)
- [数据集库分片写入设计](../functions/数据集库分片写入设计.md)
- [统计数据集与结果库拆分设计](../functions/统计数据集与结果库拆分设计.md)
- [问题修复指导](../architecture/问题修复指导.md)

## 方案概述

SQLite 的 WAL 和 `busy_timeout` 只能缓冲短冲突，不能让同一个文件拥有多个并发 writer。本计划按数据库文件划分写 owner：

| 目标文件 | 目标 owner | 初始治理重点 |
| --- | --- | --- |
| 业务库 | DB service | worker 里的账号状态、API Key 状态、授权状态、测试任务状态和 OAuth 凭据写回不能直接写业务库。 |
| 数据集目录库 | ingest / log writer | 审计、操作日志、公开接口日志、运行日志索引和模型检测写入串行；维护清理低优先级短事务。 |
| 统计结果库 | stats writer | `stats-worker` 作为唯一统计写 owner；`metrics-worker`、`snapshot-worker`、`maintenance-worker` 保留拓扑 / 调度但不能直接抢写 stats。 |
| usage shard | ingest / per-shard writer | 同一 shard 只由 ingest 写入；统计聚合只读取 shard，不再把估算字段回写到原始 shard。 |

writer 接收 typed command，不接收任意 SQL 或闭包。状态 / 快照类 command 可合并，事实明细 append-only，维护清理可 blocked 重试。

常规运行时默认开启 writer boundary strict 模式：非 owner 进程打开非所属 SQLite 文件时启用 `PRAGMA query_only = ON`，生产环境不能关闭；离线脚本作为停机 / 回归 / 造数边界例外，不进入常驻运行路径。DB service 只拥有业务库写锁，内部 system API 触发的数据集目录库或统计结果库写入必须通过 dataset writer / stats writer 转发。

## 执行拆解

- [x] 新增长期设计文档，写清单写者边界、优先级、事务规则和隐患。
- [x] 同步架构、SQLite、后台任务和 functions / plans 索引。
- [x] 盘点运行时写库路径，生成按库和 worker role 的风险清单。
- [x] 业务库写入收口：新增或扩展 DB service write operations，替换 worker 直接业务库写入。
- [x] 统计库写入收口：stats writer command / queue 承接系统采样、增量聚合、窗口刷新、表监控、任务状态和统计清理。
- [x] 数据集目录库写入收口：ingest / log writer 为唯一运行时 owner，记录维护和保留期清理由 ingest 小批执行。
- [x] usage shard 写入收口：补 per-shard owner 边界，统计聚合不再回写原始 shard。
- [x] DB service 父进程 IPC 请求增加优先级队列：管理 / 网关可感知同步写入高优先级，定时维护类写入低优先级，低优先级请求不能连续占用事件循环。
- [x] 增加静态边界回归：非 owner 运行时代码不得直接写目标库。
- [x] 增加锁竞争回归：业务库、统计库和 deleted-record 清理锁场景进入转发 / blocked / retry 语义。
- [x] 运行类型检查和相关回归，更新验证记录。

## 当前隐患清单

| 隐患 | 影响 | 初步处理方向 |
| --- | --- | --- |
| `probe-worker`、`maintenance-worker` 等角色直接写业务库 | 与 DB service 管理写、网关状态写抢同一业务库 writer | 通过 DB service typed operation 写账号、API Key、授权和任务状态。 |
| `metrics-worker`、`stats-worker`、`snapshot-worker`、`maintenance-worker` 都可能写统计库 | 系统采样、聚合、快照、表监控互相抢锁 | 已收口到 `stats-worker` typed operation；metrics / snapshot 不写 stats。 |
| DB service system API 直接写数据集目录库 | DB service 只拥有业务库写锁，直接写模型检测、日志或维护表会和 ingest 抢 dataset 锁 | 模型检测创建 run / items / finish 统一改为 dataset writer typed operation，经 server 转发给 ingest-worker。 |
| 数据集目录库同时承载 append-only 写和清理删除 | 日志 / 审计高频写与保留期清理互相抢锁 | ingest / log writer 高优先级，维护清理低优先级、小批、blocked 重试。 |
| DB service 维护类 IPC 连续执行 | 过期清理、dirty 标记等低优先级业务库写入可能让后台管理操作体感卡顿 | DB service 父进程 IPC 高 / 常规 / 低优先级队列，维护类写入低优先级；每个 IPC 请求后让出事件循环。 |
| 长窗口刷新事务过大 | 压住统计库，导致采样、聚合和 quota 窗口滞后 | staged refresh，短事务发布，阶段之间让出事件循环。 |
| 跨库维护流程等待锁时扩大持锁范围 | 一个库 locked 传导到其他库，导致维护任务失败 | 严格分库短事务，幂等账本衔接，locked 转 blocked。 |
| 队列缺少优先级 | 低价值清理排队阻塞高优先级状态写 | command priority + aging，禁止维护任务长期占队首。 |
| 直接写路径缺少回归约束 | 后续新增 repository 调用可能绕过 owner | 加静态脚本扫描 import / role / SQL 写入边界。 |
| locked 可观测性不足 | 只能看到错误，看不到哪个 writer 或 command 导致 | writer snapshot 暴露当前 command、事务耗时、oldest wait 和 locked count。 |

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 备注 |
| --- | --- | --- | --- | --- | --- |
| 文档验证 | 文档索引和链接 | 人工检查 | 计划、functions、架构和后台文档互相链接正确 | 通过 | 已同步计划、设计、SQLite 存储、后台任务和测试说明 |
| 静态 / 动态边界 | 主库 / usage shard owner 判定 | `pnpm --filter juhe-ai-backend test:sqlite-writer-boundary` | 业务库 / 数据集库 / 统计库 / usage shard owner 判定、默认 strict、非 owner `query_only`、dataset writer 源码和 server -> ingest 动态转发边界符合设计 | 通过 | 包含 usage shard writer owner、模型检测 dataset writer guard 和 fake ingest-worker 回包 |
| 启动边界 | 入口懒打开 SQLite | `pnpm --filter juhe-ai-backend test:sqlite-entrypoint-lazy-open` | DB service、worker、临时维护 worker 不再启动即预热打开非 owner 主库 | 通过 | ingest-worker 仍可按 owner 预热 dataset 队列 |
| 静态边界 | worker 业务库写回转发 | `pnpm --filter juhe-ai-backend test:background-db-service-write-boundary` | 账户内 API Key 冷却复测写回通过 worker -> server -> DB service，不能直写业务库 | 通过 | 第一条业务库写 owner 收口样板 |
| 业务库写边界 | DB service 写 owner | `pnpm --filter juhe-ai-backend test:background-db-service-write-boundary` / `test:sqlite-busy-timeout-boundary` | worker 写业务状态不直接抢业务库锁，运行库锁等待保持有界 | 通过 | 覆盖账户内 API Key 冷却复测写回和运行库 busy timeout |
| 统计库锁竞争 | deleted-record 统计扣减 | `pnpm --filter juhe-ai-backend test:deleted-record-cleanup-lock` | stats locked 时 deleted account / API Key 清理 blocked，锁释放后继续 | 通过 | 覆盖统计库维护扣减和最终清理 |
| 数据集库 / usage shard | record maintenance 队列 | `pnpm --filter juhe-ai-backend test:record-maintenance-queue` | server / DB service 只投递，ingest 执行 usage / dataset 清理，stats-only 快照由 stats-worker 执行 | 通过 | 非 ingest worker 不落本地维护队列 |
| 数据集库队列 | DB service 运行日志 / 模型检测边界 | `pnpm --filter juhe-ai-backend test:background-ipc-protected-queue` / `test:sqlite-writer-boundary` | DB service 产生的数据集写入不回退本地 SQLite 队列，模型检测通过 dataset writer 转发；ingest 内 `recordMaintenance` 与高优先级 regular 分组限流 | 通过 | 防止 DB service 误写 dataset，避免维护任务压住日志 / 同步写请求 |
| 业务库队列优先级 | DB service 父进程 IPC 优先级 | `pnpm --filter juhe-ai-backend test:db-service-system-api-boundary` | DB service 父进程请求通过优先级队列 drain；后台管理 / 网关可感知同步写默认高优先级，维护写低优先级且处理后让出事件循环 | 通过 | 避免低优先级维护写连续占用 DB service 事件循环 |
| usage shard 边界 | deleted record shard 清理 | `pnpm --filter juhe-ai-backend test:deleted-api-key-aggregation-cleanup` / `test:deleted-account-related-cleanup` | 清理按 shard 目录定位有限 shard，统计扣减幂等，物理删除两阶段推进 | 通过 | stats-worker 不回写原始 shard |
| 运行态 | worker 拓扑 smoke | `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke` | 七类常驻 worker 与 DB service 都可启动并区分 PID | 通过 | metrics / snapshot 保留拓扑但不写 stats |
| 类型检查 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 通过 | 实现后执行 |

## 进度记录

### 2026-06-18

- 用户确认希望先落地文档，再查看其他隐患和问题，之后按目标开始做。
- 建立本计划，目标从“所有写操作入对应队列”修正为“按 SQLite 文件单写者 owner / writer 队列”。
- 新增 [SQLite 单写者写队列治理设计](../functions/SQLite单写者写队列治理设计.md)，明确 owner 划分、command 语义、优先级、事务规则、locked 处理和隐患清单。
- 第一批实现收掉 DB service、常驻 worker 和临时维护 worker 的启动入口非 owner 数据库预热，避免多 worker 重启时同时初始化业务库 / 统计库放大锁竞争。
- 新增主库 writer owner 判定和 strict 模式入口；常规运行时默认开启，非 owner 连接启用 `query_only`，生产环境不能关闭，离线脚本作为停机 / 回归边界例外。
- 新增 worker -> server -> DB service typed operation 转发桥，并把 `probe-worker` 的账户内 API Key 冷却复测成功 / 失败写回迁移到 DB service operation，作为业务库写 owner 收口样板。
- 完成 stats writer typed operation：系统采样、用量 / IP 聚合、分组账号统计、账号质量、TopN / 概览 / 范围 / 授权 / 系统趋势窗口、表监控、统计保留期、client IP policy、账号用量快照和 deleted-record 统计扣减统一由 `stats-worker` 串行提交。
- 完成 record-maintenance owner 收口：usage shard / dataset 清理和 deleted account / API Key 明细清理由 `ingest-worker` 本地队列执行；stats 部分投递 stats-writer；`temporary-maintenance-worker` 不再写 usage shard 或 stats。
- 完成过期逻辑删除账号两阶段物理清理：DB service 只做业务库扫描和物理删除，发现关联记录未清空时返回清理目标，由维护 worker 投递 ingest；记录清空后再删除业务库行。
- 完成 usage shard 写边界：严格模式下只有 `ingest-worker` 创建 / 登记 / 写 shard；统计聚合只读取 shard，并且不再把估算 cache read cost 回写原始 shard。
- 完成加固复查：DB service system API 下的模型检测不再直接写数据集目录库，创建检测 run、写入检测项和完成状态统一通过 dataset writer 转发给 `ingest-worker`。
- 完成运行日志索引补边界：DB service 产生运行日志索引消息时只发父进程转发到 ingest-worker，不回退到 DB service 本地 dataset SQLite 队列。
- 强化 `test:sqlite-writer-boundary`：除 owner 判定外，增加非 owner `PRAGMA query_only` 写入拒绝、模型检测 dataset writer 源码 guard，以及 server -> ingest-worker dataset writer 动态转发验证。
- 强化 ingest IPC 队列保护：`recordMaintenance` 继续由 ingest 单写者执行，但在 IPC 容量上与运行日志、审计、操作日志、公开接口日志和 dataset writer 同步请求分组隔离；出队时优先发送非维护任务。
- 强化 DB service 父进程 IPC 请求保护：业务库 owner 内按高 / 常规 / 低优先级 drain，管理面和网关可感知写入优先于后台维护写入，低优先级维护请求不能连续占住 DB service 事件循环。

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-18 | 按 SQLite 文件划分单写者，而不是按 worker 或业务类型各自建队列 | SQLite 锁在数据库文件级生效，同库多队列仍会互相抢写 | 业务库、数据集目录库、统计库、usage shard 分别定义 owner |
| 2026-06-18 | writer 接收 typed command，不接收任意 SQL / 闭包 | 任意 SQL 队列无法做幂等、合并、优先级和静态边界回归 | 后续新增写入需要先定义 command 类型和语义 |
| 2026-06-18 | 事实明细不做 LWW | 使用记录、审计、操作日志需要完整追溯 | 只对状态、快照、dirty 标记等可覆盖数据合并 |
| 2026-06-18 | `busy_timeout` 只作为缓冲，不作为根治方案 | 长事务和持续多 writer 抢锁会超过 timeout | 后续重点治理 owner、短事务、优先级和 blocked 重试 |
| 2026-06-18 | 正常运行时默认开启 SQLite writer boundary strict 模式 | 只靠约定不能阻止后续新增路径误写非所属库 | 非 owner 连接只读；离线脚本是停机 / 回归例外 |
| 2026-06-18 | DB service 父进程 IPC 写请求区分优先级 | 业务库虽然只有 DB service 一个 owner，但低优先级维护写入仍可能让管理面操作体感卡顿 | 后台管理 / 网关可感知同步写默认高优先级；维护类写入低优先级且每次处理后让出事件循环 |

## 验收标准

- [x] 长期设计文档、计划文档和索引更新完成。
- [x] 所有运行时写库路径已按业务库、数据集目录库、统计库和 usage shard 分类。
- [x] 同一个 SQLite 文件只有一个运行时写 owner。
- [x] 非 owner 直接写路径有静态回归约束。
- [x] locked 场景按业务语义进入可读错误、重试、blocked 或跳过本轮。
- [x] 后端类型检查和相关回归通过。

## 验证记录

- `pnpm --filter juhe-ai-backend test:sqlite-entrypoint-lazy-open`：通过。
- `pnpm --filter juhe-ai-backend test:sqlite-writer-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:background-db-service-write-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:db-service-system-api-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:background-metrics-worker-role`：通过。
- `pnpm --filter juhe-ai-backend test:temporary-maintenance-worker`：通过。
- `pnpm --filter juhe-ai-backend test:record-maintenance-queue`：通过。
- `pnpm --filter juhe-ai-backend test:worker-local-queue-limit`：通过。
- `pnpm --filter juhe-ai-backend test:deleted-api-key-aggregation-cleanup`：通过。
- `pnpm --filter juhe-ai-backend test:deleted-account-related-cleanup`：通过。
- `pnpm --filter juhe-ai-backend test:deleted-record-cleanup-lock`：通过。
- `pnpm --filter juhe-ai-backend test:background-ipc-protected-queue`：通过。
- `pnpm --filter juhe-ai-backend test:background-ipc-snapshot-current-only`：通过。
- `pnpm --filter juhe-ai-backend test:background-queue-health`：通过。
- `pnpm --filter juhe-ai-backend test:background-ipc-payload-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:background-parent-ipc-event-loop`：通过。
- `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke`：通过。
- `pnpm --filter juhe-ai-backend test:queue-failure-requeue-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:operation-log-db-service-ipc`：通过。
- `pnpm --filter juhe-ai-backend test:public-api-log-db-service-ipc`：通过。
- `pnpm --filter juhe-ai-backend test:gateway-db-service-append-ipc`：通过。
- `pnpm --filter juhe-ai-backend test:sqlite-busy-timeout-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:usage-record-shard-routing`：通过。
- `pnpm --filter juhe-ai-backend test:usage-stats-shard-fanout-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:usage-stats-batch-statement`：通过。
- `pnpm --filter juhe-ai-backend test:usage-stats-rebuild-shard-cursor`：通过。
- `pnpm --filter juhe-ai-backend test:usage-stats-authorization-shard-context`：通过。
- `pnpm --filter juhe-ai-backend test:model-check-storage-sanitizer`：通过。
- `pnpm --filter juhe-ai-backend typecheck`：通过。

## 风险与注意事项

- 第一阶段迁移写 owner 会碰到大量 repository 调用，必须先盘点再分批迁移，不能一次性大改所有存储层。
- DB service 承接业务库写入后，必须保持 typed operation 粒度，避免把 DB service 变成任意 SQL 执行器。
- stats writer 如果只做 FIFO，可能让低优先级窗口刷新阻塞系统采样；必须有优先级和短事务。
- append-only 事实队列的重试必须避免数组复制和无界内存增长。
- 运行态指标增加后，页面和后端都要保留 unknown 语义，不能把不可观测状态显示成健康。

## 完成总结

- 已按 SQLite 文件 owner 收口主要运行时写路径：业务库写回走 DB service，数据集目录库和 usage shard 由 ingest 执行，统计结果库由 stats-worker typed operation 执行。
- 已删除账号 / API Key 记录清理拆成 usage shard / dataset 与 stats 两阶段，使用统计扣减账本保证重试幂等；过期逻辑删除账号物理清理不再由 DB service 跨库清理记录。
- `metrics-worker`、`snapshot-worker` 保留常驻拓扑和控制响应，不再承载 stats 写入；`temporary-maintenance-worker` 不再作为运行时 usage / stats 写入者。
