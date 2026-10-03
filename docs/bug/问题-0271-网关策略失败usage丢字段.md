# 问题-0271：网关策略失败 usage 丢 model/stream/失败归因/响应快照

- 编号：BUG-0271
- 状态：已修复（2026-10-03），待发布
- 发现：BUG-0269 后的全量排查。

## 现象

生产 `usage_records`（gateway 流量、success=0）：`gateway_policy` 1,239 行里只有 23 行有 `model`；下游关闭分支 121 行仅 63 行有 model。失败行缺 model → 不进模型维度统计；容量类失败被压成 `gateway_policy`。

## 根因

- `gatewayresponse.FailureUsageRecordInput` 没有 `Model` / `Stream` 字段，sink 构造时不带请求事实；
- `usageDispatchAdapter.RecordGatewayFailure`（`chain_ports.go`）只转发 5 个字段，把 sink 已算好的 `FailureAttribution`（含 `gateway_capacity`）与 `ResponseSnapshot`（协议渲染后的失败快照）丢弃 → `Service.RecordGatewayFailure` 回落 `gateway_policy`、自建未渲染快照。

## 修复

- `FailureUsageRecordInput` 增加 `Model` / `Stream`，sink 从 `input.Req` 填充（`requestModelHint` / `RequestStream`，均 nil 安全）；
- 适配器透传 Model/Stream/FailureAttribution/ResponseSnapshot（新增 `gatewayFailureResponseSnapshotOf` 机械转换 `UsageResponseSnapshotView` → `gatewayusage.UsageResponseSnapshot`）；
- `Service.RecordGatewayFailure` 本就消费全部字段，未改。

## 测试

`TestSinkFailureUsageCarriesRequestFactsAndCompletion`（sink 侧）、`TestUsageDispatchAdapterRecordGatewayFailureCarriesRequestFacts`（适配器端到端，真实 Service + FinalizationDispatch，快照 body 用专属文案区分透传与回退构造）。
