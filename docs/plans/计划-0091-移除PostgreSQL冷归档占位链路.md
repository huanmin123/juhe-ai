# PLAN-0091 移除 PostgreSQL 冷归档占位链路

## 基本信息

- 编号：PLAN-0091
- 状态：已完成并生产验证
- 创建时间：2026-07-12
- 需求来源：Codex 会话 `019f548b-5364-7223-be51-2237a2a82caf`
- 关联模块：PostgreSQL / 使用记录保留 / maintenance worker / 表监控 / 统计 schema / 前端
- 关联计划：PLAN-0080

## 现状与决策

`juhe_archive` 是同一 PostgreSQL 数据库中的冷归档 schema。当前只会把统计游标安全后的整日 `usage_records` 分区移入该 schema 并写 `data_archive_manifests`，但没有页面查询、自动恢复、导出、归档保留期或自动删除消费方；它不释放 PostgreSQL 总磁盘，表监控还会在 schema 不存在时长期显示一个空“归档库”。

生产核实结果：`juhe_archive` schema 不存在、归档表为 0，`juhe_stats.data_archive_manifests` 行数为 0。决定删除这条不完整生命周期，保留期到期且两个统计安全游标追平后，整日分区直接在事务中 `DETACH` 并 `DROP`；边界日继续走现有有界行删除。

## 实施项

- [x] 修改 PostgreSQL 分区清理为事务内 `DETACH PARTITION` + `DROP TABLE`，保留行数、批次上限和安全游标门禁。
- [x] 删除 `archivePostgresUsageRecordPartition`、归档 manifest repository、`archivedPartitions` 返回字段和 manifest schema 定义。
- [x] 删除归档恢复 smoke，替换为分区直接删除 smoke；压力脚本改为验证目标分区已删除。
- [x] 从后端路由、监控角色、PostgreSQL schema 目标和前端类型/文案中移除 `archive`。
- [x] 更新 PLAN-0080、数据治理、表监控、PostgreSQL 高性能模式、SQLite 存储和测试文档；历史压测报告保留并注明方案已被 PLAN-0091 替换。
- [x] 发布迁移定向删除 manifest、空归档 schema、25 条历史 archive 监控快照和对应游标；普通部署保持非业务数据库不重建、Redis 不清理。

## 验证

- `test:postgres-schema-sql`：当前 schema 不再生成 manifest 表/索引，分区清理源码不再引用归档 schema。
- `test:usage-record-partition-drop-postgres-smoke`：统计游标安全后整日分区被删除，父表和两个 schema 都查不到样本表，manifest 不写入。
- `test:table-monitor-display`：前端监控角色不再包含归档库。
- `test:data-retention-sql-guards`、类型检查和完整构建继续通过。
- 测试 PostgreSQL `test:usage-record-partition-drop-postgres-smoke` 已通过：2 条样本随整日分区删除，热 schema 和冷归档 schema 均无目标表残留。
- 生产发布确认 `juhe_archive`、manifest 表、archive 数据库/表监控快照均为 0；新代码不再产生 archive 角色采样。

## 发布与回滚

- 发布前再次确认生产归档 schema 和 manifest 表为空。
- 迁移只删除空的非业务 manifest 表和空 schema，不重建 `juhe_stats`，不触碰业务表和 `juhe_usage.usage_records` 当前分区。
- 回滚代码不自动重建归档链路；如果未来确有长期取证需求，先设计外部对象存储导出、保留期、恢复和容量告警的完整生命周期，再作为新功能实施。
