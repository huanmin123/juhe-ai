# 存储目标与 SQLite 移除

## 1. 结论

Go 迁移完成后的正式存储目标只有一套：

- PostgreSQL：保存系统账户、会话、供应商、AI 账户、分组、API Key、路由策略、授权、设置、公告、使用记录、审计、日志索引、统计窗口、监控采样、模型检测和所有可恢复事实。
- Redis：保存短 TTL cache、运行态 state、原子计数、限流窗口、调度临时状态，以及 Asynq 使用的可靠任务队列数据。

SQLite 不再作为 Go 后端运行模式存在。项目不再维护 standalone SQLite / PostgreSQL performance 两套模式，不再维护 SQLite schema、SQLite adapter、SQLite writer owner、usage shard 文件写入和 SQLite read worker。

## 2. 为什么删除 SQLite

- 两套存储模式会让 schema、repository、测试、部署、排障和性能边界长期翻倍。
- SQLite 单写者、文件级锁、usage shard、数据集目录库、统计结果库和 DB service 隔离已经消耗过多维护成本。
- Go 迁移的目标是减少复杂度；继续保留 SQLite 会把旧复杂度带进新后端。
- PostgreSQL + Redis 已经是高并发、统计、队列、运行态和后续部署扩展的更稳定边界。

## 3. 删除范围

迁移完成后必须删除或停止维护：

- SQLite runtime driver 和相关依赖。
- `JUHE_AI_DATABASE_PATH`、`JUHE_AI_DATASET_DATABASE_PATH`、`JUHE_AI_USAGE_CATALOG_DATABASE_PATH`、`JUHE_AI_STATS_DATABASE_PATH`、`JUHE_AI_USAGE_SHARD_ROOT` 等 SQLite 路径配置。
- standalone / performance 模式开关和所有模式分支。
- 业务库、数据集目录库、使用记录目录库、统计结果库和 usage shard 文件结构。
- SQLite schema、seed、repository helper、writer owner、read worker、busy timeout、WAL 和单写者文档约束。
- DB service 中只为隔离 SQLite 同步访问和 Node 事件循环存在的代理层。
- 只验证 SQLite 行为的回归测试、压测脚本和部署说明。

## 4. 保留的业务边界

删除 SQLite 不等于删除这些业务约束：

- 业务事实、统计事实、审计事实和日志索引仍要有明确表结构、索引、保留期和权限边界。
- 使用记录、审计、操作日志、公开接口日志和运行日志索引仍要异步批量写入，不能阻塞网关返回。
- 统计、额度、TopN、趋势和摘要仍读取预聚合窗口，不在 API 请求里实时扫描明细。
- 敏感字段仍要加密、哈希、脱敏和权限隔离。
- Redis 不能承载长期账务、权限、授权、账号健康或审计事实。

## 5. PostgreSQL 数据域目标

| 数据域 | PostgreSQL 目标 |
| --- | --- |
| 业务事实 | 系统账户、会话、供应商、账户、分组、API Key、路由策略、团队、授权、设置、公告 |
| 请求事实 | 使用记录、上游尝试、命中账号、授权归属、请求结果和 usage |
| 审计与日志 | 原始审计索引、payload 引用、操作日志、公开接口日志、运行日志索引 |
| 统计与监控 | 统计窗口、额度窗口、TopN、趋势、账号质量、系统指标、表监控 |
| 维护状态 | job state、游标、重建进度、保留期清理目标 |

## 6. Redis 数据域目标

| 数据域 | Redis 目标 |
| --- | --- |
| cache | API Key 校验、公开设置、供应商目录、模型目录、管理 options、统计摘要短缓存 |
| state | 短 TTL 账号屏蔽、IP 级回避、来源熔断、会话亲和、并发占用、调度临时状态 |
| queue | 使用记录、审计、操作日志、公开接口日志、运行日志索引、维护清理和后台任务队列 |
| counter | 限流窗口、短 TTL 失败计数、队列指标和运行时采样辅助计数 |

Redis cache / state 数据默认可重建或可过期。Asynq queue 在任务副作用落入 PostgreSQL 前属于待处理事实，不能按普通 cache 处理。

Redis queue 必须满足：

- 使用独立 `redis-queue` 或等效隔离 DB / namespace，避免被 cache 淘汰策略影响。
- 生产建议使用 `noeviction` 和 AOF；如果环境不满足，部署文档必须标注可靠性降级。
- Go 目标默认使用 Asynq，不在业务模块里手写 Redis Streams / list / sorted set 队列。
- 任务必须定义 task type、payload version、幂等 key、timeout、retry、dead / archived 处理和 trace ID。
- handler 成功写入 PostgreSQL 或完成业务副作用后才返回成功；失败交给 Asynq 重试或 dead / archived。
- worker 重启后任务必须可恢复，重复投递必须幂等。
- 监控 queue depth、in progress、retry、dead / archived、失败率、最老任务年龄和 handler 延迟。

## 7. 旧 SQLite 数据处理

迁移期间如需要保留既有本地数据，只允许做一次性离线处理：

- 停止旧服务。
- 备份旧 SQLite 数据文件和 `.env`。
- 使用离线导出脚本读取旧 SQLite 当前结构。
- 清洗为当前 PostgreSQL schema。
- 导入 PostgreSQL 并执行校验 SQL。
- 启动 Go 后端验证业务、统计和网关主流程。

离线脚本只有在用户明确要求时再生成，不并入正常请求路径、启动路径、worker 或 repository。

## 8. 验证门禁

存储迁移模块不能只通过功能测试，必须额外证明：

- 新库初始化和 seed 成功。
- PostgreSQL schema、索引、唯一约束、外键或软约束符合当前模型。
- PostgreSQL 连接池预算、PgBouncer、事务超时、锁等待、分区查询窗口、前缀检索 collation 和慢查询观测清晰。
- Redis cache / state / queue 的 TTL、容量、隔离、Asynq retry、dead / archived、任务恢复、幂等和降级策略清晰。
- Go 代码没有 SQLite driver、SQLite 路径配置、standalone / performance 模式分支。
- 旧 SQLite 数据导出如果需要，已作为离线步骤记录，不进入运行路径。
- 部署文档只描述 PostgreSQL + Redis 正式运行方式。
