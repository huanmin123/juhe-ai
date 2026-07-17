# 计划-0126 PostgreSQL 普通查询去隐式事务

## 基本信息

- 状态：已完成
- 创建时间：2026-07-17
- 需求来源：生产管理端性能排障与优化速度专题
- 影响范围：PostgreSQL DatabaseClient、数据库级超时默认值、回归与部署

## 目标

普通 `query`、`one` 和单条 `execute` 直接使用 pool query，不再为每条 SQL 自动执行 `connect -> BEGIN -> SET LOCAL -> SQL -> COMMIT`。真实多语句业务不变量继续使用显式 `transaction()`，并保留独占连接、事务内超时、提交与回滚。

## 安全边界

- 不机械删除 repository 中的显式事务。
- 生产切代码前必须把数据库默认 `statement_timeout`、`lock_timeout` 和 `idle_in_transaction_session_timeout` 设置为当前运行配置等价值。
- migration、verify、rollback 必须同时覆盖临时数据库与生产数据库；回滚恢复上线前默认值。
- 不改变 CRUD、权限、统计、审计、额度或网关业务语义。

## 执行清单

- [x] 新增普通 SQL / 显式事务边界红绿回归。
- [x] 移除普通 PostgreSQL SQL 的隐式事务包装。
- [x] 保留显式事务的 `BEGIN + SET LOCAL + COMMIT/ROLLBACK`。
- [x] 完成目标回归、类型检查、构建和发布包。
- [x] 候选数据库应用超时默认值并通过 PgBouncer / PostgreSQL smoke。
- [x] 临时接管上线并完成 60 秒统一生产验收。
- [x] 确认生产新连接超时与主进程稳定性；长期性能趋势继续由后续性能阶段跟踪。

## 完成结果

- 发布提交：`a4bdcfce4`
- 生产 release：`20260717-PgDirectQuery-a4bdcfce4`
- 候选与生产 PgBouncer 门禁：`STAGE3_POSTGRES_QUERY_BOUNDARY_OK`
- 数据库默认 timeout：`30s / 2s / 30s`
- 生产统一验收：`VERIFY_SUCCESS`，固定 PID `54605`，1 server + 1 DB service + 3 worker。
