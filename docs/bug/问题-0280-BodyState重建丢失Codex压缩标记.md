# BUG-0280 BodyState 重建丢失 Codex 压缩标记（跨协议桥场景压缩免超时识别断线）

## 基本信息

- 编号：BUG-0280
- 状态：已修复（2026-10-03）
- 严重程度：P2
- 发现时间：2026-10-03
- 发现方式：生产报障排查（804298280@qq.com Codex 压缩卡住，定性为上游问题）后对 BUG-0266 登记遗留项的代码核查
- 模块：网关（`gatewaybody`）
- 关联 bug：BUG-0266（第 34 行登记的「另行加固」项，本档收口）
- 责任人：agent

## 问题概述

- 现象：请求体被 `gatewaybody.ReplaceGatewayJSONBody` 重写后（跨协议桥转换、模型映射改 `model`、图像强制等路径全部汇聚于此），`BodyState.CodexCompactionTrigger` 标记被无条件清除。
- 期望：压缩标记是请求进入时流式扫描置位的请求级事实（请求体确实含过 `"type":"compaction_trigger"`），在请求生命周期内不应因 body 重写丢失。
- 实际：`ReplaceGatewayJSONBody` 用 `CreateBodyState` 全量重建 `req.State` 时，`BodyStateInput` 未携带旧标记 → 重建后标记恒为 `false`。
- 影响范围：**跨协议桥场景**——桥把 Responses body 转换为其他协议形态后，`compaction_trigger` 字面 item 不再存在于新 body，`gatewaycodex.CodexCompactionExpectedForRequest` 的三层检测（新 body JSON 递归 → bodyState 标记 → 原始 body 正则首尾兜底）全部落空 → 压缩请求不进免超时通道，被 120s/270s 预算掐断（BUG-0266 症状在桥场景复发）。不跨协议桥的 Responses 直通请求不受影响（重写后 body 仍含 trigger 字面 item，第一层 JSON 递归兜住）；`/responses/compact` 路径形态不受影响（路径恒判定，不依赖标记）。

## 复现步骤

1. 构造 `POST /v1/responses` 请求，body 含 `"type":"compaction_trigger"`，经 gatewaybody middleware 流式扫描置位 `CodexCompactionTrigger=true`；
2. 调用 `ReplaceGatewayJSONBody(req, newBody)`（`newBody` 不含 trigger item，模拟跨协议桥转换产物）；
3. 观察 `req.State.CodexCompactionTrigger == false`，`CodexCompactionExpectedForRequest` 返回 `false`——压缩免超时识别丢失。

## 根因

`backend-go/projects/gateway/internal/gatewaybody/request.go` 的 `ReplaceGatewayJSONBody` 在 `req.State = CreateBodyState(BodyStateInput{...})` 重建 BodyState 时，`BodyStateInput` 只传 `RawBody/ContentType/JSONParseStatus/ParsedBody` 四个字段。`BodyStateInput.CodexCompactionTrigger`（`*bool`）字段存在且 `CreateBodyState` 支持恢复（`body.go:179-181`），但重建点未从旧 state 继承。

`CreateBodyState` 全部 7 个调用点核查结论：

- `middleware.go:368/388/432/439`：请求进入时初始构建（其中 `metadataStateInput` 路径携带流式扫描的扫描元数据，`middleware.go:488` 置位标记），为标记的唯一合法置位来源，不涉丢失；
- `compactpreflight.go:480`（`BuildSyntheticChatCompletionsRequest`）：合成的 chat 桥请求路径为 `/v1/chat/completions`，压缩判定谓词（`NormalizedOpenAIRequestPath` 只认 `/responses`、`/responses/compact`）对其恒 false，标记无消费点，不需要继承；
- `request.go:256`：唯一丢失点。

## 修复

`ReplaceGatewayJSONBody` 重建 BodyState 前继承旧 state 的压缩标记：

```go
next := BodyStateInput{ ...原有四字段... }
if req.State != nil && req.State.CodexCompactionTrigger {
    trigger := true
    next.CodexCompactionTrigger = &trigger
}
req.State = CreateBodyState(next)
```

语义：**标记一旦置位即请求级事实，body 重写不清除**。只继承 `true`（`false` 为零值，显式传 `&false` 与不传等价，无意义）。`req.State` 为 nil 时（既有行为：`fresh Request` 直接重写）保持零值，不引入新路径。消费端 `requestBodyHasCompactionTrigger`（`gatewaycodex/compactioncontract.go`）无需改动——其第二层本就读 `bodyState.CodexCompactionTrigger`。

不改动：`CreateBodyState`/`BodyStateInput` 结构（本就支持）；`middleware.go` 置位链；`compactpreflight.go` synthetic 构建（见根因核查）。

说明：`BodyState.CodexCompactionTrigger` 为 Go 迁移时的流式扫描产物（Node 侧判定每次直接解析 body，无对应标记机制，未逐行核对），本修复不依赖 Node 对照，正当性为 Go 自身一致性缺陷——标记丢失直接改变压缩识别的可见行为。

## 验证

- 新增 `gatewaybody/compaction_trigger_preserve_test.go`：
  - 置位后重写为不含 trigger 的 body → 标记保持 true；
  - `ReplaceGatewayJSONBodyModel`（模型映射路径）传导：标记保持 true；
  - 对照：未置位请求重写后标记保持 false（防误报）；
  - nil state 直接重写不 panic（既有行为锁定）。
- 新增 `gatewaycodex` 传导用例（`compaction_trigger_preserve_test.go`）：桥重写形态（state 带标记 + 新 body 无 trigger item）下 `CodexCompactionExpectedForRequest` 返回 true；标记 false 的同形态请求返回 false。
- 回归：`gatewaybody`、`gatewaycodex` 两包全量 `-count=1` 绿。
