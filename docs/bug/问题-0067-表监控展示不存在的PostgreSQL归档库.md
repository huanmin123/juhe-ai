# BUG-0067 表监控展示不存在的 PostgreSQL 归档库

## 现象

生产表监控长期显示 `postgres:juhe_archive`，但 PostgreSQL 中实际没有 `juhe_archive` schema 或归档表，管理员容易误以为存在独立归档数据库或已保存历史数据。

## 根因

- PostgreSQL 表监控把 `archive -> juhe_archive` 写成固定目标；catalog 查询在 schema 不存在时返回空集合而不报错，代码仍写入 0 表、0 容量快照。
- 使用记录保留任务虽然能把整日分区移动到同库归档 schema，但没有查询、导出、自动恢复、保留期和自动删除消费方，无法形成完整生命周期，也不能释放 PostgreSQL 总磁盘。

## 修复

- 从后端监控角色、路由参数、PostgreSQL 采样目标和前端类型/文案中删除 `archive`；概览查询只允许五个当前真实角色，历史 archive 快照不再返回。
- 使用记录统计安全游标追平后，整日分区改为事务内 `DETACH` + `DROP`；边界日仍按现有有界行删除。
- 删除归档 manifest repository/schema、归档恢复 smoke 和压力脚本中的归档字段，替换为真实 PostgreSQL 分区直接删除 smoke。
- 生产确认 archive schema 不存在、归档表为 0、manifest 行数为 0 后，发布迁移显式删除空 manifest 表和空 schema，不重建非业务数据库。

## 验证

- `test:postgres-schema-sql` 和 `test:table-monitor-display` 通过。
- 测试 PostgreSQL `test:usage-record-partition-drop-postgres-smoke` 通过，2 条样本随整日分区删除且两个 schema 无残留。
- 前后端类型检查通过；完整构建和生产发布验证待本轮发布补充。

## 下次遇到

- 表监控目标必须来自当前真实存储角色，预留或不存在的 schema 不能无条件生成占位快照。
- 归档功能必须同时具备读取/导出、保留期、恢复和最终删除生命周期；只有“移动到同库另一个 schema”不能视为完整归档。
- 删除非业务结构优先使用经过数据为空确认的显式迁移，不能为了删一张空表重建整个非业务数据库。
