# 问题-0281：mockdata 当天样本未来时间戳致派生聚合排空中止

- 编号：BUG-0281
- 状态：未修复（2026-10-04 登记，含根因定位与修复方向）
- 发现：本地零配置数据根 `.local/dev/data` 执行 `pnpm mockdata`（默认参数，2026-10-04 00:43 +08:00）失败。business/chat/usage 明细域已写入库，管线在派生聚合排空步骤中止，覆盖校验未执行。

## 现象

排空循环跑满 200 轮后中止：`派生聚合排空超过 200 轮仍有 156 行未消费，中止`（`scripts/mockdata.mjs:414`）。每轮 `juhe-ai-jobs -run-jobs-once` 均报 `outcome=success` 且无告警，但剩余行数始终为 156。

证据（stats.sqlite3 只读查询）：

- 两个游标型任务（`usage_stats_aggregation` / `client_ip_stats_aggregation`）游标停在 `2026-10-03T16:35:23.000Z`（= 北京时间 04 日 00:35）。
- 游标后剩余 156 行，`created_at` 分布 `2026-10-03T16:47:07Z .. 2026-10-04T00:00:00Z`（= 北京时间 04 日 00:47 .. 08:00）——**相对执行时刻全部是未来时间戳**。

## 根因（两侧谓词不对称）

1. **生成面写出未来行**：mockdata 按"近 N 天（含当天）"生成样本，时间戳以 UTC（`Z` 后缀）落库；本地日界含当天意味着执行时刻之后的"当天剩余小时"样本的 `created_at` 晚于运行时刻（未来行）。
2. **聚合器不消费未来行（设计正确）**：消费谓词为 `created_at <= safeCreatedBefore AND (created_at > cursor OR (created_at = cursor AND id > cursor))`（`backend-go/projects/jobs/internal/statsagg/aggregate.go:94-95`），`safeCreatedBefore` 基于当前时刻——未来行被有意跳过。
3. **排空计数谓词缺上界**：`statsAggregationRemainingRows`（`scripts/mockdata.mjs:483-486`）只按游标比较统计，没有聚合器的 `created_at <= safeCreatedBefore` 上界——未来行被计入"未消费"，而聚合器永远不消费它们，排空判定永假，200 轮后中止。

即：只要生成窗口含执行时刻所在的当天（默认参数必然含），本 bug 必现；与数据根是否干净无关。

## 影响

- `pnpm mockdata` 退出码 1，`--verify-mockdata-coverage` 未执行；
- 明细域（business / chat / usage 分片 / observability）已写入，派生聚合只重建到游标处——统计/用量页面对当天尾部样本的聚合口径偏少；重跑会先按清理标识幂等清理再重写，无数据污染累积。

## 修复方向（未实施）

- 生成面：样本时间戳截断到运行时刻（或聚合器 safe 水位）之前，不产出未来行；这是治本点。
- 或排空判定：计数谓词补齐聚合器同款 `created_at <= safeCreatedBefore` 上界，未来行不计入剩余（与"聚合器同款谓词"的注释声明对齐）。
- 两处择一或同改；修复后需在含当天的默认参数下完整跑通 `pnpm mockdata`（含 coverage 校验）验证。
