# PLAN-0048 SQLite 单写者写队列治理

## 基本信息

- 编号：PLAN-0048
- 状态：进行中
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
| 统计结果库 | stats writer | `metrics-worker`、`stats-worker`、`snapshot-worker`、`maintenance-worker` 都不能直接抢写 stats。 |
| usage shard | per-shard writer | 同一 shard 串行，不同 shard 文件允许并行。 |

writer 接收 typed command，不接收任意 SQL 或闭包。状态 / 快照类 command 可合并，事实明细 append-only，维护清理可 blocked 重试。

## 执行拆解

- [x] 新增长期设计文档，写清单写者边界、优先级、事务规则和隐患。
- [x] 同步架构、SQLite、后台任务和 functions / plans 索引。
- [~] 盘点运行时写库路径，生成按库和 worker role 的风险清单。
- [~] 业务库写入收口：新增或扩展 DB service write operations，替换 worker 直接业务库写入。
- [ ] 统计库写入收口：设计 stats writer command / queue，迁移系统采样、增量聚合、窗口刷新、表监控和任务状态写入。
- [ ] 数据集目录库写入收口：确认 ingest / log writer 为唯一 owner，维护清理改低优先级 command 或临时 worker 短批提交。
- [ ] usage shard 写入收口：补 per-shard writer 边界和同 shard 串行回归。
- [ ] 增加 writer runtime snapshot 和队列健康展示字段。
- [ ] 增加静态边界回归：非 owner 运行时代码不得直接写目标库。
- [ ] 增加锁竞争回归：业务库、数据集目录库、统计库和 usage shard 分别模拟 locked。
- [ ] 运行类型检查和相关回归，更新验证记录。

## 当前隐患清单

| 隐患 | 影响 | 初步处理方向 |
| --- | --- | --- |
| `probe-worker`、`maintenance-worker` 等角色直接写业务库 | 与 DB service 管理写、网关状态写抢同一业务库 writer | 通过 DB service typed operation 写账号、API Key、授权和任务状态。 |
| `metrics-worker`、`stats-worker`、`snapshot-worker`、`maintenance-worker` 都可能写统计库 | 系统采样、聚合、快照、表监控互相抢锁 | 引入 stats writer owner，按优先级串行提交。 |
| 数据集目录库同时承载 append-only 写和清理删除 | 日志 / 审计高频写与保留期清理互相抢锁 | ingest / log writer 高优先级，维护清理低优先级、小批、blocked 重试。 |
| 长窗口刷新事务过大 | 压住统计库，导致采样、聚合和 quota 窗口滞后 | staged refresh，短事务发布，阶段之间让出事件循环。 |
| 跨库维护流程等待锁时扩大持锁范围 | 一个库 locked 传导到其他库，导致维护任务失败 | 严格分库短事务，幂等账本衔接，locked 转 blocked。 |
| 队列缺少优先级 | 低价值清理排队阻塞高优先级状态写 | command priority + aging，禁止维护任务长期占队首。 |
| 直接写路径缺少回归约束 | 后续新增 repository 调用可能绕过 owner | 加静态脚本扫描 import / role / SQL 写入边界。 |
| locked 可观测性不足 | 只能看到错误，看不到哪个 writer 或 command 导致 | writer snapshot 暴露当前 command、事务耗时、oldest wait 和 locked count。 |

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 备注 |
| --- | --- | --- | --- | --- | --- |
| 文档验证 | 文档索引和链接 | 人工检查 / markdown 链接检查 | 计划、functions、架构和后台文档互相链接正确 | 未执行 | 文档落地后检查 |
| 静态边界 | 主库 owner 判定 | `pnpm --filter juhe-ai-backend test:sqlite-writer-boundary` | 业务库 / 数据集库 / 统计库 owner 判定和严格模式入口符合设计 | 通过 | 后续继续扩展为直接写扫描 |
| 启动边界 | 入口懒打开 SQLite | `pnpm --filter juhe-ai-backend test:sqlite-entrypoint-lazy-open` | DB service、worker、临时维护 worker 不再启动即预热打开非 owner 主库 | 通过 | ingest-worker 仍可按 owner 预热 dataset 队列 |
| 静态边界 | worker 业务库写回转发 | `pnpm --filter juhe-ai-backend test:background-db-service-write-boundary` | 账户内 API Key 冷却复测写回通过 worker -> server -> DB service，不能直写业务库 | 通过 | 第一条业务库写 owner 收口样板 |
| 业务库锁竞争 | DB service 写 owner | 新增锁竞争回归 | worker 写业务状态不直接抢业务库锁，locked 返回可读错误或进入重试 | 待实现 | 覆盖账号状态 / API Key 状态 |
| 统计库锁竞争 | stats writer owner | 新增锁竞争回归 | metrics / stats / snapshot / maintenance 写 stats 串行，低优先级任务不阻塞采样 | 待实现 | 覆盖系统采样和窗口刷新 |
| 数据集库锁竞争 | ingest / log writer owner | 新增锁竞争回归 | append-only 写保留队首批次，清理 blocked 重试 | 待实现 | 覆盖 audit / runtime logs / retention |
| usage shard 边界 | per-shard writer | 新增 usage shard writer 回归 | 同一 shard 串行，不同 shard 可并行；同 shard 不出现多 writer 抢锁 | 待实现 | 结合 PLAN-0030 |
| 运行态 | writer runtime snapshot | 新增队列健康回归 | 展示 pending、oldest wait、locked、当前 command，不把 unknown 当 0 | 待实现 | 对齐 runtime snapshot contract |
| 类型检查 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 未执行 | 实现后执行 |

## 进度记录

### 2026-06-18

- 用户确认希望先落地文档，再查看其他隐患和问题，之后按目标开始做。
- 建立本计划，目标从“所有写操作入对应队列”修正为“按 SQLite 文件单写者 owner / writer 队列”。
- 新增 [SQLite 单写者写队列治理设计](../functions/SQLite单写者写队列治理设计.md)，明确 owner 划分、command 语义、优先级、事务规则、locked 处理和隐患清单。
- 第一批实现收掉 DB service、常驻 worker 和临时维护 worker 的启动入口非 owner 数据库预热，避免多 worker 重启时同时初始化业务库 / 统计库放大锁竞争。
- 新增主库 writer owner 判定和可选严格模式入口；后续迁移可用 `JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT` 辅助发现非 owner 写入。
- 新增 worker -> server -> DB service typed operation 转发桥，并把 `probe-worker` 的账户内 API Key 冷却复测成功 / 失败写回迁移到 DB service operation，作为业务库写 owner 收口样板。

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-18 | 按 SQLite 文件划分单写者，而不是按 worker 或业务类型各自建队列 | SQLite 锁在数据库文件级生效，同库多队列仍会互相抢写 | 业务库、数据集目录库、统计库、usage shard 分别定义 owner |
| 2026-06-18 | writer 接收 typed command，不接收任意 SQL / 闭包 | 任意 SQL 队列无法做幂等、合并、优先级和静态边界回归 | 后续新增写入需要先定义 command 类型和语义 |
| 2026-06-18 | 事实明细不做 LWW | 使用记录、审计、操作日志需要完整追溯 | 只对状态、快照、dirty 标记等可覆盖数据合并 |
| 2026-06-18 | `busy_timeout` 只作为缓冲，不作为根治方案 | 长事务和持续多 writer 抢锁会超过 timeout | 后续重点治理 owner、短事务、优先级和 blocked 重试 |

## 验收标准

- [ ] 长期设计文档、计划文档和索引更新完成。
- [ ] 所有运行时写库路径已按业务库、数据集目录库、统计库和 usage shard 分类。
- [ ] 同一个 SQLite 文件只有一个运行时写 owner。
- [ ] 非 owner 直接写路径有静态回归约束。
- [ ] locked 场景按业务语义进入可读错误、重试、blocked 或跳过本轮。
- [ ] writer runtime 能观测 pending、oldest wait、locked、失败、丢弃和当前 command。
- [ ] 后端类型检查和相关回归通过。

## 验证记录

- `pnpm --filter juhe-ai-backend test:sqlite-entrypoint-lazy-open`：通过。
- `pnpm --filter juhe-ai-backend test:sqlite-writer-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:background-db-service-write-boundary`：通过。
- `pnpm --filter juhe-ai-backend test:background-worker-topology-smoke`：通过。
- `pnpm --filter juhe-ai-backend typecheck`：通过。

## 风险与注意事项

- 第一阶段迁移写 owner 会碰到大量 repository 调用，必须先盘点再分批迁移，不能一次性大改所有存储层。
- DB service 承接业务库写入后，必须保持 typed operation 粒度，避免把 DB service 变成任意 SQL 执行器。
- stats writer 如果只做 FIFO，可能让低优先级窗口刷新阻塞系统采样；必须有优先级和短事务。
- append-only 事实队列的重试必须避免数组复制和无界内存增长。
- 运行态指标增加后，页面和后端都要保留 unknown 语义，不能把不可观测状态显示成健康。

## 完成总结

- 待完成后补充。
