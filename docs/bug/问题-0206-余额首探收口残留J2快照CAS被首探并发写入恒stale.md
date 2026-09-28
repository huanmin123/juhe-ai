# BUG-0206：余额首探收口残留——due 等值围栏微秒舍入失配恒 stale

- 编号：BUG-0206
- 状态：已修复（待发布；本文件 2026-09-28 根因改判并替换原「快照 CAS 被首探并发写入恒超越」定性——该机制不成立，见下）

## 现象

2026-09-27 23:19 发布（含 BUG-0204 修复）后观察：

- J2 首探循环 `seen` 从 16 降至 8 后不再下降，剩余候选仍每约 10 秒被重探（`executed` 全成功、`errors:0`，上游 429 未复现）；
- 补偿任务 `account-balance-auto-detect-recovery` 每轮 `selectedCount:2 staleCount:2 enabledCount:0 outcome:partial`，持续不收口；
- 业务库候选 due 全部停留 `2026-09-26T15:26~15:31Z`（**9 位纳秒精度存量文本**，如 `2026-09-26T15:26:25.690411968Z`），发布后零推进；候选集剩余 12+ 行。

## 根因（2026-09-28 改判定案）

`balance_query_next_refresh_at` 在 PG 是 **text 列**，等值围栏写作 `balance_query_next_refresh_at::timestamptz = $n`。失配发生在等值两侧的**微秒舍入方向不一致**：

- **列侧**：PG 把 text cast 到 timestamptz 时对亚微秒位**四舍五入**——`.690411968` → `.690412`（生产实测 `'...690411968Z'::timestamptz` = `...25.690412`）；
- **参数侧**：围栏绑定 `time.Time`，pgx 二进制编码**截断**到微秒——`.690411968` → `.690411`；

两侧恒差 1μs → `EnableDetectedQuery`（及 `CommitDetectionDue`）等值围栏恒 0 行 → recovery 对纳秒存量候选恒 stale、enable 永不执行 → due 永不推进 → J2 首探永不收口。

**受影响范围精确锁定**：只有 due 文本带 7~9 位小数且舍入方向与截断不一致的**存量行**（2026-09-26 0204 修复前 Go 写入值）受影响。0204 修复后所有新写入都是毫秒截断文本（cast 与 pgx 绑定对毫秒值恒相等），这正是发布后 8 个候选成功收口、剩余 12+ 行恒 stale 的分界。0204 修的是「写入格式」层；本缺陷是「存量精度 × 等值比较」层，两层叠加构成完整因果链。

原定性（快照 CAS 被首探 10 秒写入恒超越）不成立：recovery 链路（`EnableDetectedQuery`/`ReplaceSnapshotIfCurrent`）不经过 J2 快照 CAS，`ReplaceSnapshotIfCurrent` 的围栏是 config_revision + 配置 JSON 等值，与 input_version 无关；补偿 stale 计数唯一来源是业务库 enable 围栏 0 行。

## 修复

等值围栏改为**绑定库内读回的 RFC3339Nano 原文文本 + 参数侧同 cast**（`balance_query_next_refresh_at::timestamptz = $n::timestamptz`）——等值两侧同过 PG 同一处 cast 舍入，纳秒存量与毫秒新值都能精确命中；SQLite 保持规范文本等值（Go RFC3339Nano 往返恒等）。三处调用点统一走新 `balanceDueCompare`：

- `worker_balance_detect.go` `CommitDetectionDue` / `EnableDetectedQuery` 围栏；
- `worker_balance_detect.go` `ListDueCandidates` 游标 `>` / `=` 比较（无游标占位绑定零值时间文本，防 `''::timestamptz` 求值报错）；
- `shared/platform/accountbalance` `AdvancePeriodicDue` 周期 due 推进围栏（同族预防，当前存量 enabled 账户 due 均为毫秒文本未实际触发）。

**零数据迁移、零 schema 变更、零 env 变更**：不需要生产数据回填，存量 9 位文本在首次 enable/commit 命中后被毫秒文本覆写自然归一。

## 验收

- 录制驱动 PG 契约测试（`wfix_balance_due_fence_test.go`）：fence SQL 为 `::timestamptz = ?::timestamptz`（pgpool 改写后 `$5::timestamptz`）、fence 参数为原文文本、游标空占位为可解析时间文本。
- SQLite 实测（纳秒/毫秒双精度种入）：扫描、Commit 收口/推进、二次围栏、Enable 全命中（`TestBalanceRuntimeFenceHitsNanoAndMillisecondStoredText`）。
- `go test ./cmd/juhe-ai-jobs/`、`shared/platform/accountbalance` 全绿。
- 发布后生产验证：recovery 日志 `enabledCount` 开始推进、`staleCount` 归零；候选集（`balance_query_enabled=0 AND config_json='{}' AND due 非空`）清零；J2 首探 `seen` 收敛到 0；业务库残余候选 due 被毫秒文本覆写或清 NULL。

## 状态

- 2026-09-27：登记（当时定性为快照 CAS 并发残留，三个修复方向待裁决，未实施）。
- 2026-09-28：根因改判为 due 等值围栏微秒舍入失配并实施修复（原文三方向作废）；随 08:30 发布上线。注意：围栏修复上线后 recovery 仍一度不轮转，根因是 BUG-0209 jobsched lane 交接自死锁（独立缺陷），09:05 jobs 修复发布后 recovery 恢复轮转，候选集 12+→0 全部收口（enabled/unsupported 混合，staleCount 恒 0），J2 首探风暴随之消失。
