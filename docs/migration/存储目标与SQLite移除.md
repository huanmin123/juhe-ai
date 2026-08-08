# 双模式存储目标（保留历史文件名）

> 文件名为历史兼容保留。本文自 2026-08-08 起不再主张 SQLite 移除；此前“PostgreSQL + Redis 为 Go 唯一模式、删除 SQLite / DB service / standalone”是已被取代的历史规则，不是当前执行目标。

## 1. 结论

Go 的正式目标有两种部署模式：

- **SQLite 模式**：保留 SQLite 文件、DB service、按文件单 writer、typed command 和既有本地运行态边界。Go 功能在 Node 共存期只能经 owner bridge 写入，或在完整功能冻结、drain 与 handoff 后独占目标文件。
- **PostgreSQL/Redis 模式**：以 PostgreSQL 承载可恢复事实，Redis adapter 按需承载 cache、state 和 counter。连接池、事务、lease、幂等和完整功能单 owner 仍是必需约束。

部署模式只选择 Store、Runtime 和直接异步执行方式，绝不决定 Go 是否可用；SQLite 模式默认不要求 Redis。Store Port 必须屏蔽 schema、SQL 方言和事务差异，业务规则不能因 driver 分叉。当前没有受版本控制的 Go 源码或 `go.mod`，SQLite driver / owner bridge、直接异步和资源维度并发都是 B0 PoC 待定事项。

## 2. 单 owner 与直接异步契约

- 每个 SQLite 文件、PostgreSQL 写入表、完整功能和外部副作用在任一时刻只有一个正式 owner；双模式不等于双写。
- SQLite 中，Node 当前拥有的业务库、数据集 / usage writer 和统计 writer 不得被 Go 并行直接写入。非 owner 只能 query-only、生成 typed command，或在 handoff 后成为唯一 owner。
- PostgreSQL/Redis 中，以事务、lease、幂等键和 drain 实现完整功能唯一 owner。当前 Node Redis Streams 是 Node 实现，新 Go 功能默认直接异步，不可混称或假定互通。
- 直接异步必须定义版本化输入、取消、失败、幂等、提交结果和重启重新发现；启动时 driver、异步执行方式、owner 不匹配必须 fail-fast，禁止静默 fallback。
- 验证期的结果只能写隔离存储，不能写共享业务表、发布运行态或触发生产副作用；正式接管必须是完整功能唯一 owner，而非长期影子。

## 3. 数据域与部署边界

| 数据域 | SQLite 模式 | PostgreSQL/Redis 模式 |
| --- | --- | --- |
| 可恢复业务、使用、审计和统计事实 | 保留现有业务库、数据集 / usage / stats 文件和各自 owner | PostgreSQL 表、事务、约束、索引与分区 |
| 短期运行态和缓存 | 当前本地 adapter；不因缺 Redis 停止 Go | Redis cache / state adapter，按专用配置与容量边界运行 |
| 后台异步 | 直接等待唯一 file owner；按文件 / writer 串行 | goroutine 直接写 PostgreSQL，由 pool / 事务 / context 收口；当前 Node Streams 不等同 Go 执行模型 |

Redis 不得承载长期账务、权限、授权、账户健康或审计事实；SQLite 也不因 Go 迁移降格为一次性导出来源。

## 4. 切换、恢复与非目标

任何完整功能接管须先冻结完整文件和契约、完成 Go 验证、Node drain、Go readiness、单 owner 观察和双模式验证。回滚恢复整个功能的 Node owner；只修改 owner manifest 或迁移文件的一部分不构成接管。

本策略不声明 Go 已实现、已运行或已接管功能。某完整功能接管后，其 Node 文件退出活跃路径并归档；SQLite、DB service、schema、测试或部署入口是否保留由未接管功能决定。首期范围和阶段见 [完整功能接管与 Node 归档迁移规则](完整功能接管与Node归档迁移规则.md)。

## 5. 历史规则说明

本文件旧标题中的“SQLite 移除”、旧的 PostgreSQL/Redis-only 部署、Asynq-only 队列和离线 SQLite 导出要求，都是 2026-08-08 前的规划快照。保留该文件名是为了不破坏既有链接；后续文档和实施必须以本页与双模式方案为准。
