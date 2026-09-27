# BUG-0203：relay_balance 投影缺失，余额列表与明细读端恒空

## 现象

生产核对（2026-09-27）：

- `juhe_stats.account_usage_snapshots` 中 `kind='relay_balance'` 行数为 **0**；
- `juhe_jobs.account_balance_snapshots` 98 行、秒级新鲜（J2 余额服务每周期写入）；
- gateway 余额明细接口（`FindBalanceDetails`/`LoadBalanceSnapshotRecord`）与列表投影（BUG-0200 修复后的读端）都读 stats 表——**读端永远为空**；多 Key 账户的逐 Key 明细（`keyBalances`）同样无处可读。

## 根因

归档 Node 的 `account-balance-jobs-projector` 负责"jobs 已结算 outcome → `juhe_stats.account_usage_snapshots` relay_balance 行"投影，Go 迁移未移植该投影器；J2（`shared/platform/accountbalance`）只写 `juhe_jobs` 快照与 outcome。旧的 opsjobs 写入路径（`ReplaceSnapshotIfCurrent`）是唯一 stats 写者，但受 BUG-0204 围栏失配与 BUG-0202 lane 停摆影响从未成功写入。

## 修复

`backend-go/projects/jobs/`：

- 新增 wired scheduled job `account-balance-stats-projection`（间隔 1 分钟、独立 lane `balance-projection`、InitialDelay 47s、Timeout 45s、OverlapCoalesce、LeaseTTL 2min；driver=sqlite 登记 disabled）：全量读 `juhe_jobs.account_balance_snapshots` → join 业务库账户（900 分块，缺失/已删除跳过计数）→ UPSERT `juhe_stats.account_usage_snapshots`（冲突键 `(system_account_id, account_id, kind)`，列集合与既有 `ReplaceSnapshotIfCurrent` 一致，幂等）。
- 快照 JSON 映射为下游读端 camelCase 契约：`status/configRevision（取 J2 行 config_revision 列）/remainingUsd/rawRemaining/rawUnit/basis/errorMessage/lastAttemptAt/lastSuccessAt` + 多 Key 字段 `keyCount/queriedKeyCount/scope/aggregation/keyBalances` 透传（明细端按 keyFingerprint join，列表端白名单剥离 keyBalances）。
- 探测链路接入多 Key：`QueryBuiltin`/`buildQueryInput` 执行入口改 `ExecuteAccountBalanceQuery`（shared 多 Key 逐 Key + 合计，单 Key 原样委托）；`balanceSnapshotPersist` 扩展多 Key 字段透传（单 Key JSON 形状零变化）。

## 验证

- 新增 10 项测试：投影映射（单/多 Key）、非法输入 fail closed、UPSERT 幂等、孤儿账户跳过、PG SQL 契约（录制驱动）、契约校验分支、多 Key sum 运行时、装配全链路持久化、单 Key 窄投影回归；`go test ./cmd/juhe-ai-jobs/ -run 'TestBalance|TestWorkerBalanceDetect|TestEnsure'` 28 项 PASS。
- 发布后预期：`relay_balance` 行数增长至 ≈J2 快照数，列表/明细读端出现数据。

## 状态

已修复，2026-09-27 23:19 发布；发布后投影任务 `scannedCount:98 projectedCount:97 skippedCount:1`（孤儿跳过），relay_balance 行数持续增长，符合预期。
