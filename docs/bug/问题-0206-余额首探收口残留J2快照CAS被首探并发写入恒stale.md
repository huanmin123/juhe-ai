# BUG-0206：余额首探收口残留——补偿任务 outcome CAS 被 J2 首探并发写入恒 stale

## 现象

2026-09-27 23:19 发布（含 BUG-0204 修复）后观察：

- J2 首探循环 `seen` 从 16 降至 8 后不再下降，剩余候选仍每约 10 秒被重探（`executed` 全成功、`errors:0`，上游 429 未复现）；
- 补偿任务 `account-balance-auto-detect-recovery` 每轮 `selectedCount:2 staleCount:2 enabledCount:0 outcome:partial`，持续不收口；
- 业务库候选 due 全部停留 `2026-09-26T15:26~15:31Z`（纳秒精度存量值），发布后零推进；候选集（`balance_query_enabled=0 && config_json='{}' && due 非空`）剩余 12+ 行。

## 根因（方向已定位，待修复设计）

0204 修复了业务库 TEXT due 列的围栏格式失配（发布后 8 个候选成功收口证明该层已生效），但暴露并发层残留：

- J2 首探循环对未收口候选每 10 秒写一次 `juhe_jobs.account_balance_snapshots`（`input_version` 每次递增）；
- 补偿任务（recovery）从业务库读候选构造 outcome，携带读取时刻的 `inputVersion` 围栏；
- `store.go writeSnapshotTx` 的 CAS（`currentInput > inputVersion → 拒绝`）在补偿任务 AppendOutcome 时，快表现值已被其间的一轮首探超越 → 恒拒绝 → `ErrOutcomeStale` → `runStateExecutedStale`（补偿日志的 staleCount）；
- 补偿任务的 enable（`EnableDetectedQuery`）只在 AppendOutcome 成功后执行 → 恒不执行 → 候选业务库 due 永不推进 → 首探永不收口。

即：**首探循环自身的高速写入使补偿任务的快照 CAS 围栏结构性失配**。发布前 16 候选 18 小时死循环是该问题与 0204 格式失配的叠加；0204 修掉格式层后剩余候选受此并发层阻塞。

## 影响

- 剩余 12+ 候选每 10 秒被首探（当前上游接受、errors:0，429 风险随上游状态波动）；
- 补偿任务意图永不收口（`outcome:partial` 持续）；
- 不影响已 enable 账户的余额刷新与 0203 投影链路（`relay_balance` 投影正常增长）。

## 待修复方向（供设计裁决，未实施）

1. 补偿任务对 first_probe 候选改走无 `ExpectedSnapshotInput` 围栏的写入路径（以业务库 due 围栏为唯一收口语义）；
2. 或 J2 首探循环自身在 outcome fresh 时推进业务库 due（打通"探测即收口"，绕开补偿任务串行链）；
3. 或补偿任务与首探循环按账户 lease 串行化（`account_balance_account_leases` 已有基础设施，评估租约粒度与 TTL）。

## 状态

已登记，待修复。发布后证据：`docker compose logs jobs`（23:22~23:24 三轮 recovery 恒 stale）、业务库候选 due 分布查询（2026-09-27 23:25）。
