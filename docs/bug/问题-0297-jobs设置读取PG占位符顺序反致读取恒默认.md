# BUG-0297 jobs 设置读取 PG 占位符顺序反致读取恒默认

## 基本信息

- 编号：BUG-0297
- 状态：已修复（2026-10-08 入库，待发布验证）
- 严重程度：P1（生产 PG 下 jobs 进程全部经该读模型的设置读取自引入起恒回默认值，管理端保存的设置对 jobs 侧从未生效）
- 发现时间：2026-10-08
- 发现方式：客户端版本自动跟版批次的独立复审（阻断项 B1；同批复审另发现写面缺口，见本档案「同批复审项」）
- 模块：后端 / jobs（`internal/jobssettings` 设置读模型）
- 关联计划：PLAN-20261007T164058012Z（客户端版本自动跟版，发现载体；缺陷本身非该批次引入）
- 关联 bug：无（同族占位符错配先例：BUG-0235、BUG-0239，均为 `Bind` 双层包裹撞号，形态不同）
- 责任人：待定

## 问题概述

- 现象：PostgreSQL 模式下，jobs 进程经 `jobssettings.Source` 读取 `system_settings` 的全部设置恒返回 `DEFAULT_SYSTEM_SETTINGS` 默认值，管理端保存的真实值从未被读到。SQLite 模式正常。
- 期望：按 `system_account_id='sys_admin' AND key=<目标键>` 命中存储行，返回 `value_json` 解码值；缺行才回退默认。
- 实际：PG 查询条件与实参错配，存储行永远不命中，`sql.ErrNoRows` 走缺行回退分支，静默返回默认值（无错误、无告警）。
- 影响范围：jobs 进程所有经 `readValue`（`Number`）与 `readValueStrict`（版本覆盖直读）的设置读取。gateway 侧读模型使用 `?` → `$n` 顺序转换器，不受影响；SQLite 部署不受影响。

## 复现步骤

1. PG 库 `juhe_business.system_settings` 写入 `('sys_admin', 'statsAggregationBatchSize', '3500')`。
2. 以 `Mode: Postgres` 构造 `jobssettings.Source`，调用 `Number(ctx, "statsAggregationBatchSize", 100, 10000)`。
3. 修复前返回默认值 2000（`ErrNoRows` 回退）；修复后返回 3500。
4. SQLite 同构用例（`settings_test.go` `TestSourceStoredValueWins`）修复前后均通过——这正是缺陷被掩盖的原因。

## 环境信息

- 分支 / 版本：自 `4354b10b6`（2026-09-06，jobs 设置读模型随迁移里程碑引入）起的全部已发布版本；HEAD `048049317` 仍携带该缺陷。
- 数据状态：无数据损坏（写入侧 gateway 一直正确落库，只是 jobs 读不到）。
- 是否稳定复现：是（结构性缺陷，PG 模式下 100% 复现）。

## 根因分析

- 真实根因：`backend-go/projects/jobs/internal/jobssettings/settings.go` 的 PG 分支查询串占位符与实参顺序错配。两处（`readValue` 与 `readValueStrict`）均写为：

  ```go
  query = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = $2 AND key = $1 LIMIT 1`
  // ...
  s.db.QueryRowContext(ctx, query, SystemSettingsAccountID, key)
  ```

  实参顺序是 `(system_account_id, key)`，即 `$1 = system_account_id`、`$2 = key`，而 SQL 把两者对调：实际执行的条件是 `system_account_id = <目标键> AND key = 'sys_admin'`，恒不命中。
- 为什么会发生：PG 占位符是显式编号，写反后编译期与运行期都不报错（参数数量一致，只是语义互换），失败形态是"正常的缺行"。SQLite 分支用位置参数 `?`（顺序天然正确），而 jobssettings 包测试全部基于 SQLite 内存库，缺陷自引入起从未被任何测试触达。
- 引入时点（git 事实）：`4354b10b6`（2026-09-06，`migrate(里程碑 2026-09-06)`）引入 `readValue` 时 PG 分支即写反；`7d859599e`（2026-10-02，日志与审计设置批次）新增 `readValueStrict` 时原样复制了同一错误串。HEAD 即携带，**非本次客户端版本自动跟版批次引入**。

## 影响面

凡 PG 生产上经 `jobssettings.Source` 的设置读取全部恒回默认值（已知消费方）：

| 消费方 | 路径 | 受影响的键 |
| --- | --- | --- |
| 调度间隔解析 | `cmd/juhe-ai-jobs/worker_schedule_settings.go`（启动期一次） | `systemMetricsSampleIntervalSeconds`、`statsAggregationIntervalSeconds`、`groupAccountStatsRefreshIntervalSeconds`、`usageHotWindowRefreshIntervalSeconds`、`cooldownAccountRetestIntervalSeconds`、`oauthAccessTokenRefreshIntervalSeconds`、`accountQualityRefreshIntervalSeconds` |
| 统计聚合批次 | `worker_settings.go` `dbSettingsSource` | `statsAggregationBatchSize`、`statsAggregationMaxBatchesPerRun` |
| 探针设置 | `worker_settings.go` `probeSettingsSource`（`accountquality.SettingsNumber`） | `accountQualityWindowMinutes`、`cooldownAccountRetestMaxBackoffHours` |
| retention 策略 | `worker_retention.go` `retentionSettingsRuntime.settings` | `publicApiLogRetentionDays`、`usageRecordRetentionDays`、`usageStatsMinuteRetentionHours`、`usageStatsHourlyRetentionDays`、`usageStatsDailyRetentionDays`、`usageStatsWeeklyRetentionWeeks`、`usageStatsMonthlyRetentionMonths`、`usageRankSnapshotRetentionDays`、`systemMetricsRetentionDays`、`systemMetricsHourlyRetentionDays` |
| 上游客户端版本覆盖 | `worker_settings.go` `refreshUpstreamClientVersionOverrides`（启动 + 60s） | `upstreamClientVersionOverrides`（手动应急覆盖）、`upstreamClientVersionAutoOverrides`（自动跟版层，本批次新增消费方，上线前即被本缺陷阻断） |

行为后果：管理端修改上述任一设置后，jobs 侧继续按代码默认值运行（调度间隔、聚合批次、retention 保留期、探针窗口、版本覆盖全部如此）。因为默认值与 seed 初值一致，**从未改过这些设置的实例无可见差异**；改过的实例其修改对 jobs 侧静默无效。

不受影响的读取：`worker_retention.go` 的 `usageStatsTimezone` 走独立查询（字面量内嵌 `system_account_id = 'sys_admin' AND key = 'usageStatsTimezone'`，无占位符），不经本读模型。gateway 侧 settings 读模型用 `?` 顺序转换器生成 `$n`，占位符与实参天然对齐。

## 修复方案

- 修改点（`backend-go/projects/jobs/internal/jobssettings/settings.go`）：
  - 两处查询提升为包级常量 `systemSettingSelectSQLite` / `systemSettingSelectPostgres`，经 `systemSettingSelectQuery(mode)` 单一出口取用，调用点不再内联 SQL。
  - PG 查询改为 `WHERE system_account_id = $1 AND key = $2`，与既有实参顺序 `(SystemSettingsAccountID, key)` 对齐；SQLite 查询与实参顺序不变。
- 回归测试（`settings_pg_placeholder_test.go`，新增）：脚本化 `database/sql` driver 捕获实际下发的 SQL 与实参，断言 PG 查询串占位符映射为 `system_account_id=$1`、`key=$2`，且实参顺序为 `(SystemSettingsAccountID, key)`——覆盖 `readValue`（经 `Number`）与 `readValueStrict`（经手动键/自动键直读）三条路径；另钉桩包级常量形状，防止调用点绕过统一出口。
- 未加 PG 集成用例：jobs 下既有 `*_postgres_integration_test.go` 均为"专用空库 smoke + 自有环境变量门控"（如 `JUHE_AI_TABLE_MONITOR_POSTGRES_SMOKE_URL`），没有可复用的业务库 DSN 门控；占位符映射由脚本化 driver 已完整锁定，新增 smoke 门控不增加缺陷覆盖，故不引入。
- 复核结论：同文件内全部 `$n` 占位符仅上述一处（修复后 `$1`/`$2`），无其他反向占位符。
- 行为影响：PG 下 jobs 设置读取恢复真实值；SQLite 行为不变；无 schema/env 变化。
- 存量处置：无需数据修复。存储行一直由 gateway 正确写入，发布后 jobs 进程重启即按真实值读取（调度间隔在启动期解析，必须重启生效；版本覆盖与 retention 为周期读取，重启后首轮即对齐）。

## 同批复审项（B2，设计 §7 写面收口）

同一次复审发现：`upstreamClientVersionAutoOverrides` 加入 settings 白名单与 spec 后，legacy 全量 `PATCH /settings`（`normalizeSystemSettingsInput` → `normalizeSystemSetting`）可写入该键，与《客户端版本自动跟版设计》§7「管理端 v1 不可写、只读语义」冲突（前端表单不发该键，UX 未变，但 API 可写面被打开）。

- 修复（`backend-go/projects/gateway/internal/settings/store.go`）：写路径显式拒绝自动键——`normalizeSystemSettingsInput`（`Update` 全量写入口）与 `UpdateSection` 分区写循环均先经 `rejectSystemManagedSettingWrite`，返回 `&ValidationError{Message: "客户端版本自动覆盖由系统任务维护，不支持通过管理接口写入"}`。拒绝放在写入口而非 `normalizeSystemSetting`：后者被 `Load`/`LoadSection` 读路径共用（`loadFromDatabase` 经其归一化存储行），读路径必须继续正常返回该键值。
- 测试（`auto_overrides_write_guard_test.go`，新增）：`Update` 与 `UpdateSection` 携带自动键 → 校验错误且不改动既有行；`Load` 与直读 `UpstreamClientVersionAutoOverrides` 正常返回存储值；手动键 `upstreamClientVersionOverrides` 写入行为不变。`UpdateSection` 用例临时注入含自动键的分区（当前目录无此分区），锁定"目录扩张后守卫仍生效"。
- 既有测试无冲突：全库无"逐键断言所有 SystemSettingKeys 可写"的断言（既有断言均为 GET 快照含全键或单键写入，均不受影响）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 构建 | jobs 全模块 | `cd backend-go/projects/jobs && go build ./...` | 通过 | 通过（无输出） | 通过 |
| 单元 | jobssettings 全包 | `go test -count=1 ./internal/jobssettings/...` | 全绿 | `ok .../jobssettings 0.408s` | 通过 |
| 单元 | 新增占位符回归（定向） | `go test -count=1 -v -run 'TestPostgresSettingSelectPlaceholderOrder\|TestSystemSettingSelectQueryConstants' ./internal/jobssettings/...` | 全绿 | 4 用例 PASS（三条读取路径 + 常量钉桩） | 通过 |
| 构建 | gateway 全模块 | `cd backend-go/projects/gateway && go build ./...` | 通过 | 通过（无输出） | 通过 |
| 单元 | settings 全包 | `go test -count=1 ./internal/settings/...` | 全绿 | `ok .../settings 2.146s`（含既有偶发失败用例 TestW11FStoreFaults，本轮一次通过） | 通过 |
| 单元 | 新增写拒绝回归（定向） | `go test -count=1 -v -run 'TestAutoOverridesKey*' ./internal/settings/...` | 全绿 | 4 用例 PASS（Update 拒绝 / UpdateSection 拒绝 / Load+直读正常 / 手动键不受影响） | 通过 |

## 复发记录

- 无（首次登记）。

## 下次遇到

- 先查什么：PG 占位符必须显式编号，"参数数量一致"不代表"参数语义正确"；缺行回退类路径会把错配伪装成正常默认值。
- 重点看什么：同一查询在 SQLite（`?`）与 PG（`$n`）双分支手写时，两分支是否经同一转换器生成（gateway `Store.bind` 即该模式）；手写双份 SQL 是本缺陷的结构温床。
- 如何避免误判：只跑 SQLite 测试不能为 PG 查询串背书；占位符映射类缺陷要用捕获实际 SQL 的脚本化 driver（或真实 PG）钉桩，字符串断言与实参顺序断言需同时存在（防"改了串、调了参"双错仍绿）。

## 完成总结

- 完成时间：2026-10-08
- 结论：PG 占位符错配修复（两处 SQL 收敛为包级常量 + 回归测试锁定）；自动键写面按设计 §7 收口（写路径拒绝、读路径不受影响）。发布后 jobs 重启即恢复真实设置读取，无需数据修复。
- 后续建议：发布后抽查一个曾在管理端修改过的 jobs 侧设置（如 retention 天数或调度间隔），确认 jobs 日志/行为与库内存储值一致，作为生产验证闭环。
