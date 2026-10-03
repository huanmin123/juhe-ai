# 问题-0273：模型检测"Token 诚信"状态恒"证据不足"

- 编号：BUG-0273
- 状态：已修复（2026-10-03，运行期投影批次），待发布
- 发现：BUG-0269 后的全量排查。

## 现象

模型检测详情"Token 诚信"恒显示"证据不足"。生产 `juhe_j3b.model_account_trust_results` 唯一 1 行 = `insufficient_evidence`。token 探针（`modelcheckprobe/token.go`）已按阈值算出 consistent/warning/suspected_padding/unsupported，但从未进入信任报告。

## 根因

- `BuildTrustReport`（`internal/modelcheckowner/trust.go`）初始化 `UsageIntegrityStatus="insufficient_evidence"` 后从不赋值，只追加 `token_integrity_anomaly` reason；
- `trust_store.go` 把常量钉死：UPDATE 字面 `'insufficient_evidence'`、INSERT 绑定字面量、同游标回放冲突比较不含该状态；
- Node 的提升点在聚合 worker（`model-trust.repository.ts:518-529` 窗口回归），Go 无窗口聚合管线——本批落地"运行期投影"，完整窗口管线另立任务（文档已标注待补批次）。

## 修复（运行期投影批次）

- `BuildTrustReport` 从 `token_integrity` 证据按判定表提升状态：passed→consistent；failed 或 reasonCodes 含 proportional_padding→suspected_padding；warning（slope_warning/bucket_rounding）→warning；skipped 且 reported_usage_missing/incompatible→unsupported；其余 skipped/无 item→insufficient_evidence；多决定性 item 只升不降（severity 序 consistent<warning<unsupported<suspected_padding）。
- `upsertTrustLatest`：UPDATE/INSERT 改绑报告真值；"仅结论性状态覆盖"——新值 insufficient 且旧行已结论性时保留旧值（对齐 Node 无证据不覆盖）；usage 状态纳入同游标回放冲突比较（fail-closed）。
- 读侧 `query.go` 合并不改（Node 契约：详情读 stats latest 合并）。

## 有界偏差（文档 `docs/functions/模型检测设计.md` 15.3/15.9 已同步）

单 run 证据（非 Node 的跨 run 窗口累计）；运行期分桶阈值 0.8 vs 窗口 0.5；slope/intercept/baseline/距离等预聚合列 Go 仍无写入者（schema 已预置，待补批次目标表）。

## 测试

判定表 13 子用例（含 []string/[]any reasonCodes 双形态、trusted_comparison.token_integrity 不投影）；真值落库+幂等重放；"新 insufficient + 旧 consistent → 保留"；同游标不同 usage → conflicts。
