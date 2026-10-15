# BUG-0315 Gemini Interaction 亲和写入未接线

## 基本信息

- 编号：BUG-0315
- 状态：已修复（2026-10-14，待发布）
- 严重程度：P1（功能契约缺失：Interactions 资源族创建后不可访问、删除不清理）
- 发现时间：2026-10-14
- 发现方式：性能/缺陷审计复审核验（`InteractionAffinity` 写方法生产零调用的指控逐点取证）
- 模块：后端 / 网关 / Gemini interactions 资源族 / 账号亲和
- 关联计划：无
- 关联 bug：BUG-0316（同批集成验证暴露的 DELETE 空成功体缺陷）、BUG-0314（同批复审）
- 责任人：AI

## 问题概述

- 现象：`gatewaygemini.InteractionAffinity` 的 `Remember` / `Delete` 在迁移后的 Go 生产链路上零调用（仅 `Resolve` 接在 preflight 亲和解析处），创建成功不写亲和映射、DELETE 成功不移除映射。
- 期望（《Gemini账号接入.md》:191-192 契约）：创建成功后提取 Interaction ID 写入亲和映射（按系统账户 + API Key 隔离）；`GET` / `cancel` / `DELETE` 必须命中创建该资源的账号；DELETE 成功后移除映射。
- 实际：映射从未写入 → 创建后的 `GET` / `cancel` / `DELETE` 在 preflight 亲和解析处恒 `404 interaction_affinity_not_found`（performance 模式 Redis 中已有历史映射时除外）；DELETE 成功也不清理（有映射时悬挂至 TTL 到期）。
- 影响范围：Gemini native `interactions_json` / `interactions_sse` 端点模式的全部资源族访问；创建请求本身的转发不受影响。
- 附带缺口：Gemini inspector 已从 `interaction.*` 事件提取的 `ResponseResourceID` 未在 `convertGeminiInspection` 回带到 `StreamInspection`，流式创建即使接线也拿不到 ID。

## 复现步骤

1. 以 `interactions_json` 端点模式账户创建 Interaction（`POST /v1beta/interactions`，上游 2xx）。
2. 用同一 API Key `GET /v1beta/interactions/{id}`。
3. 修复前：404 `interaction_affinity_not_found`（映射未写入）；修复后：200，命中创建账号。

## 根因分析

- 表象：资源访问恒 404；`Remember` / `Delete` 无生产调用点。
- 真实根因：Node→Go 迁移只接线了读路径（preflight 的 `Resolve`，含 TTL 续期），写路径（创建成功 remember、DELETE 成功 remove）未接入响应处理链；`ResponseResourceID` 回带链在 `convertGeminiInspection` 处断开。
- Node 语义（归档 `response/finalization.ts:1667/:1697` 与 `:1675/:1705`）：创建（2xx）与 DELETE（2xx）在**下游提交前**写亲和；写失败记录 `gateway_gemini_interaction_affinity_update_failed` 日志与 `gemini_interaction_account_affinity_update_failed` 审计元数据（元数据仅 `operation`/`accountId`，日志带原始存储错误）后 rethrow `GeminiInteractionAffinityUnavailableError`（routes.ts:1621）→ 503、响应不提交；成功记录 `gemini_interaction_account_affinity_update` 元数据（`commitPhase: before_downstream_commit`）。`updateGeminiInteractionAffinityAfterSuccessAsync` 在 Node 亦无调用点（生产走 remember/delete 直连），Go 保持同构不接线。
- 触发链路：POST 创建 → 上游 2xx → 响应处理（流式 `BeforeDownstreamCommit` / 非流式 `res.send` 前）→ 修复前无写入；GET → preflight `Resolve` → 未命中 404。

## 修复方案

- 修改点：
  - `internal/gatewayproto/stream.go`：`StreamInspection` 新增 `ResponseResourceID`（inspector 从 `interaction.*` 事件提取的协议内部元数据，其余协议恒空）。
  - `internal/gatewayresponse/convert.go`：`convertGeminiInspection` 回带 `ResponseResourceID`。
  - `internal/gatewayresponse/pipehelpers.go`：`publishInspection` 在快照汇聚点把 ID 写入 `p.responseResourceId`（对齐 Node `stream.ts:389` 的 `??=` 语义）。
  - `internal/gatewayresponse/finalize.go`：`HandleUpstreamResponseInput` 新增 `InteractionAffinityRemember`，接线为 `StreamPipeOptions.BeforeDownstreamCommit`。
  - `internal/gatewayresponse/nonstream.go`：完整缓冲发送前（`res.send` 前）调用 `InteractionAffinityRemember(InteractionIDFromParsedResponse(...))`，失败阻断发送。
  - `internal/gatewaygemini/affinity.go`：`AffinityUnavailableError` 新增 `Code()`（`interaction_affinity_unavailable`）。
  - `cmd/juhe-ai-gateway/chain_interaction_affinity.go`（新）：链面写面接线——创建提交前 remember 回调（门：interactions create + 上游 2xx + gemini 账户 + 亲和已装配）、DELETE 2xx 提交前 remove（ID 取自请求路径）、审计元数据与失败日志逐字段对齐 Node。
  - `cmd/juhe-ai-gateway/chain_v1.go`：装配 `InteractionAffinityRemember`；DELETE 亲和移除先于响应处理；`AffinityUnavailableError` 映射 503 + `interaction_affinity_unavailable`（仅响应未提交时）。
- 语义说明：写失败 = 请求失败（503、响应不提交、不降级透传），与 Node 一致；创建 2xx 但响应体无 Interaction ID 时按 `action=none` 跳过，不写空键；日志级别沿用 Go 观测面既有的 `Warn` 收敛（`gatewaypreauth.Logger` 只暴露 warn 面）。
- 行为影响：Interactions 资源族创建后可正常访问（命中创建账号）；DELETE 成功即清理映射；亲和存储故障时创建/DELETE 以 503 失败而非静默成功。
- 回归测试：gatewayresponse 新增 4 用例（流式提交前钩子命中/失败阻断提交、非流式发送前钩子命中/失败阻断发送）；chain 新增 2 集成用例（生命周期 create→GET→DELETE→GET 404、写失败 503）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元验证 | 响应层钩子四用例 | `go test ./internal/gatewayresponse/ -run 'InteractionAffinity' -count=1` | 全绿 | 全绿 | 通过 |
| 集成验证 | 链生命周期 + 写失败 503 | `go test ./cmd/juhe-ai-gateway/ -run 'TestChainInteractionAffinity' -count=1` | 全绿 | 全绿（create 200 / GET 200 / DELETE 204 / GET 404 `interaction_affinity_not_found`；写失败 503 `interaction_affinity_unavailable`） | 通过 |
| 包测试 | gatewayresponse / gatewaygemini / gatewayproto 全量 | `go test ./internal/gatewayresponse/ ./internal/gatewaygemini/ ./internal/gatewayproto/ -count=1` | 全绿 | 全绿 | 通过 |
| 静态检查 | go vet + gofmt | `go vet ./cmd/juhe-ai-gateway/`；`gofmt -l` | 干净 | 干净 | 通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：创建/DELETE 的审计记录里是否出现 `gemini_interaction_account_affinity_update` 元数据；失败时 `gateway_gemini_interaction_affinity_update_failed` 日志。
- 重点看什么：`ResponseResourceID` 是否全链回带（流式经 inspector 事件、非流式经解析体）；写失败时响应是否确实未提交。
- 如何避免误判：普通模式下进程重启后 404 `interaction_affinity_not_found` 是预期行为（进程内 LRU 丢失）；performance 模式重启后仍 404 才可疑。

## 完成总结

- 完成时间：2026-10-14
- 结论：亲和写面（创建 remember / 删除 remove）按 Node 语义接线完成——提交前写、写失败 503 `interaction_affinity_unavailable` 且响应不提交；`ResponseResourceID` 全链回带；响应层 4 单测 + 链面 2 集成用例 + 相关包全量回归绿。
- 后续建议：发布后在生产核验一次 interactions 创建→GET 命中（审计元数据 `gemini_interaction_account_affinity_update` 出现）。
