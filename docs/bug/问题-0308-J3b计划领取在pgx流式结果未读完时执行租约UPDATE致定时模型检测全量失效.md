# 问题-0308：J3b 计划领取在 pgx 流式结果未读完时执行租约 UPDATE 致定时模型检测全量失效

- **日期**：2026-10-11 用户报障（管理页定时检查恒挂"排队中"数小时不动）；同日定位、修复入库
- **严重级**：P1（功能级：定时模型检测自该功能投产起 0 次成功；质量隔离恢复链与 account-health 写入臂同雷一并修复；/v1 主链、数据与计费零影响）
- **状态**：已修复（代码待提交，待发布；发布后调度器首个 tick 自愈，无需数据修复）

## 现象（2026-10-11 取证链）

1. **用户可见**：管理页"模型检测 → 定时检查"计划恒挂**排队中**，"下次运行 12:24:51"过期 3 小时+不跑；"上次结果 已完成·100 分 12:14:51"实为用户 12:13 手点"立即执行"（`schedule_now`，不走租约领取）所留，**不是定时运行**。
2. **gateway 日志**：自 `12:24:52.061`（首条计划到期后的第一个调度 tick）起每秒 1 条 `J3b scheduler task operation failed operation=claim kind=scheduled err="driver: bad connection"`，至取证时 1.1 万+条；**此前全天零报错**。
3. **运行记录**：`juhe_j3b.model_check_runs` 全历史 `trigger_kind` 仅 `manual`(32)/`schedule_now`(2)——`scheduled` 定时触发 **0 次**：定时检测从未成功过。两条计划均为 10-11 当日创建（12:13/13:00），12:24:52 是生产第一次有计划到期、第一次踩中本 bug。

## 取证

- **pg_stat_statements**：claim 的 SELECT（`... FOR UPDATE OF mqs SKIP LOCKED`）30.7 万次调用、mean 0.4ms 全部正常；**claim 的租约 UPDATE 0 次调用——从未到达 PG 服务端**。
- **PG 服务端**：12:24:52 前后日志无任何 backend 错误/终止；71 个 idle 连接健康 → 错误在客户端驱动层。
- **kind 分布**：仅 `scheduled` 报错；`quality_recovery`/`health_retry` 每秒同样对业务库开事务却静默 → 锁定"SELECT 命中到期行之后"的循环体路径。
- **驱动确认**：业务库句柄 `sql.Open("pgx", ...)`（`business_source.go:86`），`pgx/v5 v5.10.0`。

## 根因

`claimSchedules`/`claimRecoveries` 在 `for rows.Next()` 循环体内对**同一事务**执行租约 UPDATE。pgx 的查询结果是流式读取（stdlib `QueryContext` 只预读第一行，其余行与 CommandComplete/ReadyForQuery 留在连接上），结果集未读完时连接处于 busy；此时同事务 Exec：

```
pgconn.lock() → connLockError{"conn busy"}（SafeToRetry()=true）
  → stdlib ExecContext 转换为 driver.ErrBadConn（文本恰为 "driver: bad connection"）
  → database/sql 事务内不重试，原样冒出 → 事务回滚、租约不落库
  → 下一 tick SELECT 再命中到期行 → 再炸，永久循环
```

**存在到期行即 100% 失败**；零到期行时循环体不执行、连接走完读流程正常归还——这就是投产 3.5 天、30 万次 claim 静默无事，直到首条计划到期才暴露的原因；`quality_recovery` 无到期恢复、`health_retry` 的 `SQLSchedulerSource.Claim` 本就是"先读完再改"的安全写法，故二者静默。

**SQLite 驱动全量缓冲结果集**，同写法无此约束——现有测试全部基于 SQLite 挂载，天然拦不住。

## 同型排查（全仓）

17 个启发式命中文件逐一核验：唯一另一处真雷 = `settings/store.go reserveAccountHealthInputVersions`（`upsertSystemSettings` 的 account-health 分区在存在 openai 账户时，外层 accounts 流式 rows 未读完即在同事务做版本预留 QueryRow/Exec，必炸）；其余为安全写法（rows 已读完并 Close 再 Exec / Exec 在不同句柄 / 仅 SQLite 路径）。

## 修复

三处统一改为"**先把外层 SELECT 全部读入内存并关闭 rows，再循环执行租约/预留语句**"：

1. `modelcheckowner/business_scheduler.go claimSchedules`（定时计划领取，生产正在炸）；
2. `modelcheckowner/business_scheduler.go claimRecoveries`（质量隔离恢复领取，潜伏雷）；
3. `settings/store.go reserveAccountHealthInputVersions`（account-health 版本预留，潜伏雷）。

事务语义、CAS 条件、payload 冻结字段逐字保留；`scheduler_store.go SQLSchedulerSource.Claim`（健康重试）核对为安全写法，不改。

## 验证

- 新增 PG 门禁回归：`w15b_pg_streaming_claim_test.go`（scheduled 双到期行领取 + 租约落库 + 未来/停用账户不领取 + 租约期内不重领 + 零到期行不报错；recovery 到期恢复领取 + 租约落库）与 `settings/w15b_pg_account_health_reserve_test.go`（account-health 写入版本预留 + 二次写入自增）。一次性子库隔离（DROP IF EXISTS FORCE + CREATE，主库零接触；shared.env 读配置、直连语义同生产；env 缺失或 PG 不可达一律 skip；输出不带凭据）。
- **红绿闭环**：暂存修复跑新测试 = 三臂全部逐字复现 `driver: bad connection`；恢复修复 = 三臂全绿。
- settings 全包 ok；modelcheckowner 失败集合与干净树基线完全一致（2 个既有失败 `TestQBQuestionBankMountsAndGlobalVisibility`、`TestCheckBusinessSQLiteSchemaFailsClosedForConstraintDrift`，均与本次无关）；go vet / gofmt 绿。

## 上线与存量

发布后**无需任何数据修复**：逾期计划 `next_run_at` 已远过期，gateway 上线后首个调度 tick 即领取并恢复正常节奏（10 分钟/60 分钟计划）；被质量隔离账户的自动恢复随首个 recovery 扫描恢复。

## 教训（固化）

1. **database/sql + pgx 下，同事务内"rows 未读完就执行下一条语句"是确定性 bug**：错误文本 `driver: bad connection` 极具误导性（像连接池故障，实为应用层使用约束）。循环内需要写库时，先读完并 Close rows 再执行。
2. **SQLite 测试对 pgx 流式语义零覆盖**：凡 PG 专属路径必须落真实 pgx 门禁测试（w10b/w15b 先例），SQLite 全绿不代表 PG 可用。
3. 排障口径：`driver: bad connection` + `pg_stat_statements` 中目标写语句 0 次调用 + PG 服务端日志干净 = "客户端驱动层失败三件套"，先查连接使用方式，再查连接池。
