# BUG-0265：遗留 running 检测运行永不收尾

- 状态：已修复（2026-10-02，待发布）
- 定性：Go 实现收口缺口——run 终态的唯一收口是发起进程内的 CAS，进程崩溃/重启后该 run 永久停留 running，包内无任何启动/周期收尾扫描
- 发现方式：生产数据取证（2026-09-28 一条 running 滞留）+ 代码取证（`modelcheckowner/run.go` / `host.go` / `scheduler.go`）

## 1. 现象

gateway 进程在 `CreateRun` 之后、终态投影之前崩溃或重启，该 run 在 `juhe_j3b.model_check_runs` 永久停留 `status='running'`（无 finished_at/终态字段），管理面一直显示进行中。生产现存一条（2026-09-28）。

## 2. 根因

run 生命周期的全部写点都在发起进程内：

- `Runtime.Run` 创建 running 行后，靠进程内 goroutine 完成 `Store.ProjectOutcome`（`run.go` 的 `UPDATE ... WHERE id=? AND status='running'` CAS）收终态；
- `AppendItem`/`AppendObservation` 虽走 `beginRunning` 事务校验，但**不推进 run 行**；
- lease 心跳（`RenewClaim`，lease/3 周期）只推进 `model_check_execution_claims.updated_at`，**不推进 run 行**；
- `Host`（`host.go` OpenHost/Run）与 `Scheduler` 装配期均不扫描 run 表。

因此进程一旦在执行中丢失，没有任何路径再触达该行——`model_check_runs.updated_at` 的实际推进点只有四处：`CreateRun`（=started_at）、`ProjectOutcome`（终态）、`MergeRunQualityDecision` / `MarkHealthSync`（终态后补写），运行期间不推进。

## 3. 修复

`modelcheckowner` 增加启动收尾（新增 `stale_runs.go` + `host.go` OpenHost 在 CheckSchema 后一次调用）：

```sql
UPDATE juhe_j3b.model_check_runs
SET status='failed', error_code='owner_lost', error_message='检测执行进程中断，运行未完成', updated_at=<now RFC3339>
WHERE status='running' AND updated_at::timestamptz < <now-阈值>::timestamptz
```

- **幂等**：条件含 `status='running'`，重复执行自然空转；语句只标记 updated_at 超过阈值的行，不波及本次启动新发起的 run。
- **阈值 30 分钟**（契约原定 15 分钟，经核实改为保守值）：run 表 updated_at 在运行期不推进（见根因），阈值必须覆盖"存活的长 run"——代码内最长 run 预算为 run-now 默认 10 分钟（`ScheduleRunNowService.execute`）、计划/恢复执行 `ScheduleRunBudget(6 分钟 lease)`=5.5 分钟；手动 SSE run 无代码内墙钟预算，仅受每跳重试边界（10/20/30 秒）与客户端连接约束，带大题库的 full 手动 run 可合法超过 15 分钟而 updated_at 不动。30 分钟为最大代码内预算（10 分钟）的 3 倍，误收存活 run 的代价是该 run 终态写以显式错误失败（ProjectOutcome 的 status='running' CAS），不会静默污染数据。
- **方言**：比较用 `::timestamptz` 双侧 cast（updated_at 为 text 时间戳，避免文本字典序在零分数秒边界的误序）；j3b 生产为 PG-only，SQLite 模式（本地开发夹具）不执行收尾并静默返回 0。
- **失败语义**：收尾失败使 OpenHost fail-closed（与 CheckSchema 同级）——同一张表的 UPDATE 不可用意味着本 owner 也无法写任何 run 终态。
- **生产存量**：2026-09-28 那条 running 记录在新代码上线启动时自动收尾（updated_at 已远超 30 分钟阈值），无需手工 SQL。

## 4. 验证

- 单测（`stale_runs_test.go`，包内 PG 测试基建为 opt-in 且需外部 DSN，故按 SQL 构造与判定逻辑覆盖）：
  - `TestStaleRunSweepSQLConstruction`：语句含 schema 限定表名、`status='failed'`/`owner_lost` 字段序、`WHERE status='running'`、双侧 `::timestamptz` cast，PG bind 后参数序 `$1..$4`；
  - `TestSweepStaleRunsSkipsSQLiteMode`：SQLite 夹具静默不执行，遗留 running 行保持原状；
  - `TestSweepStaleRunsInputGuards`：nil store / 零时间入参拒绝；
  - `TestStaleRunSweepThresholdCoversBoundedBudgets`：阈值 > run-now 10 分钟预算且 ≥2 倍余量（对 ScheduleRunBudget 的预算推导一并钉死）。
- `go build ./...` 通过；`go test ./internal/modelcheckowner/ -count=1` 全绿（60.0s，ok）；gofmt 无差异。
- **未做 PG 集成验证**：收尾语句未在真实 PG 实例上执行过（opt-in smoke 需运维 DSN）；上线后首次启动观察日志 `J3b 遗留 running 检测运行已收尾`（count=1）与该 run 的 failed/owner_lost 终态即可确认。

## 5. 关联

- 生命周期：`docs/functions/模型检测设计.md`（run 终态与 lease 契约）
- 同域：[BUG-0263](问题-0263-模型检测多密钥账户凭据白名单缺失.md)、[BUG-0264](问题-0264-题库未执行误报满分.md)
- 预算依据代码：`scheduler_executor.go` `ScheduleRunBudget`/`BusinessLeaseExecutionMargin`、`business_scheduler.go` run-now 默认预算

2026-10-02
