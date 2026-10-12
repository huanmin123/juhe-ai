# 问题-0310：ensure-schema 六表 success_cost_usd 回填全扫与迟到补正缺口

- **定性**：性能缺陷 + 数据口径缺陷复合；`docs/bug/问题-0307-J1租约续约热路径反复全量EnsureSchema致PG锁队列风暴.md` 同窗观察项 2 的根治结案。
- **状态**：已修复（2026-10-12 本地代码落地并验收）；生产验证待维护窗口执行（见下"剩余动作"）。
- **来源**：[信封差异与慢查询处置复核报告](../reports/信封差异与慢查询处置复核报告-2026-10-11.md) §4；[成功成本历史回填治理计划](../plans/计划-20261011T225418-成功成本历史回填治理.md)（用户三项裁决：受控维护窗口 / 扣减事务内补正 / 根治原则）。

## 缺陷本体

1. **重复全表扫描**：六条 `UPDATE usage_stats_{totals,minute,hourly,daily,weekly,monthly} SET success_cost_usd = total_cost_usd WHERE error_count = 0 AND success_cost_usd <> total_cost_usd`（`pg_schema_stats.go`）作为 schema statements 随**每次** `--ensure-schema` 遍历执行；SQLite `ensureSQLiteStatsSuccessCostColumns` 与 jobs `statsverify.ensureStatsSuccessCostBackfill` 同款——三处均无完成标记，"数据已相等时重复执行不改值"成立但"无扫描成本"不成立：`usage_stats_minute` 24 万行全扫生产实测 max 234s（0307 观察项 2）。
2. **迟到补正缺口**：删失败记录会把历史混合行 `(1,10,0)` 扣成 `(0,8,0)`，此后只能等下一次 ensure-schema 的全表回填补成 `(0,8,8)`；若单独加 marker 停掉回填，该行将永停 0——一次性迁移与运行中补正必须同时交付。

## 根治（三项裁决的落地形态）

- **结构保证与数据迁移分离**：三处回填扫描全部卸载，`ensure` 只幂等建新表 `backfill_progress`（marker：status running/done + 主键 tuple 游标，PG juhe_stats 与 SQLite stats 库同构）；ensure 遇缺失或非 done 的目标 marker 仅在摘要提示，不自动迁移。
- **一次性迁移**：maintenance `--apply-success-cost-backfill`（`--dry-run` 只读预检；六表按主键游标分批 5000，批内谓词在行锁后当前值重判，批 UPDATE 与 cursor 推进同事务提交，CAS 防御；可中断续跑、幂等可重入；marker 缺失显式报错指引先 `--ensure-schema`，不静默建表）。
- **运行中补正根治**：删除失败记录的扣减事务内，`success_cost_usd` 改按扣减后值重判 CASE——`error_count` 扣减后归零且成功/总成本不一致时补正为扣减后总成本；SQLite（`MAX`）与 PostgreSQL（`GREATEST`）两条扣减路径语义一致，与扣减同提交同回滚。权威口径已落《统计指标与分层聚合设计.md》"成功口径成本语义"。
- **维护窗口**：先 `stop jobs` 并备份 → 只更新新版 maintenance → `--ensure-schema` 建 marker → `--dry-run` → 子命令及六 marker 核验 → 最后启动新版 jobs；中途不得用全量发布启动写者。已持久化的明细/spool 由 jobs 重启后从游标追平；进程内尚未落盘的 buffer 不属于该持久保证。完整命令序列见 `docs/deploy/部署指南.md` 与 `docker/single-server/README.md`。旧写者回滚运行后，再升级须先停写、重置六 marker 并完整重跑。

## 测试与验证

- 四形态（新库/存量未回填/已回填无 marker/中断续跑）子命令测试；`(0,10,0)→(0,10,10)`、混合行不动、done 短路零批秒完；扣减补正元组回归 `(1,10,0)→(0,8,8)`、混合行不补正、幂等、全零行 DELETE 不回归；PG 侧语句形态逐 token 契约断言（CASE 结构/参数序）。
- 全仓唯一性验收：`rg "SET success_cost_usd = total_cost_usd"` 仅命中子命令与测试夹具。
- maintenance schema/cmd、jobs cleanuprepo/statsverify 全绿；golden 随 statements 数联动。

## 剩余动作（生产）

1. 维护窗口按部署文档序列执行（一次性回填 + `backfill_progress` 六行 done 复核 + spool 积压归零）。
2. 隔离 PG 多批回填及扣减已通过，分钟表 240000 行、48 批、最长批事务 530ms；验收条件与限制见[三线验收报告](../reports/信封与成功成本治理验收报告-2026-10-12.md)。生产批时长仍须在维护窗口核验。
3. 生产 `pg_stat_statements` 中六表回填 UPDATE 归零复核（marker 短路生效）。
