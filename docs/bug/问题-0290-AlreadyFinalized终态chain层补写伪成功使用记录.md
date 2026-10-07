# BUG-0290 AlreadyFinalized 终态被 chain 层补写伪成功使用记录

## 基本信息

- 编号：BUG-0290
- 状态：已修复（待发布验证）
- 严重程度：P2
- 发现时间：2026-10-07
- 发现方式：用户反馈（使用日志页该批请求 Token 用量全 0、首 token 空，而请求显示"成功"）
- 模块：后端 / 网关
- 关联计划：无
- 关联 bug：BUG-0289（正交——0289 治加密上下文恢复面，本 bug 治记账双写；0289 的 `RetryUpstream` 臂在 chain 层本就短路、不经本缺陷路径）

## 问题概述

- 现象：上游 HTTP 200 但流内首个语义事件即失败（生产为 `event: error` + `thinking_signature_invalid`，2026-10-07 vsllm/chengtingkj 中转爆发暴露）时，同一请求在 `juhe_usage.usage_records` 落两条记录：一条 `success=0`（正确，由 `finalizeStreamFailure` 写）+ 一条伪 `success=1`（无 usage、无 first_token、无错误码）。使用日志页该请求 Token 用量全 0、首 token 空，且被计为成功。
- 期望：响应终态已写入下游（`AlreadyFinalized=true`）后，chain 层不再补写任何完成行——每请求恰一条失败记录。
- 实际：chain 层只短路 `RetryUpstream`，对 `AlreadyFinalized` 结果继续调用 `FinalizeHandledUpstreamResponse`，凭上游 2xx 再写一条成功行。
- 影响范围：四处 `AlreadyFinalized` 生产者对应的所有响应面——流式提交后失败/流式预提交检查失败不可服务端重试、非流式协议失败（200 失败终态体）、SSE 心跳下游冲突、非流式检查兜底。成功计数与质量分被污染（失败请求虚计成功、用量指标缺真实 usage）；计费金额不受影响（伪成功行 usage 全空，不产生金额，正确的失败行照常落库）。

## 复现步骤

1. 以 Codex 画像客户端（或携带 `x-juhe-client-profile: codex` 的请求，`InterpretSemantics=true`）发起流式 chat/responses 请求。
2. mock 上游返回 HTTP 200 `text/event-stream`：先发一个内容 delta（提交下游语义），再发 `data: {"error":{"code":"thinking_signature_invalid",...}}` 帧，随后 EOF。
3. 检查 `usage_records`：同 traceId 出现两条——`success=false, error_code=thinking_signature_invalid` 与 `success=true, error_code 空, input_tokens/output_tokens/first_token_ms 全 NULL`。

（流式路径注意：generic 客户端对 SSE 保留不透明语义——干净 EOF 即视为成功、`error` 帧原样转发（`gatewayresponse/pipefinal.go:361` 臂），不进入本缺陷路径，这是既有契约不是本 bug。）

## 环境信息

- 分支 / 版本：生产国内单机 Docker go-only（103.36.63.105），2026-10-07。
- 数据状态：不涉及数据修复（存量伪成功行不回溯，与 BUG-0269 存量口径一致）。
- 浏览器 / 系统 / Node 版本：后端缺陷，客户端形态不限（需命中 `AlreadyFinalized` 生产者）。
- 是否稳定复现：是（端到端回归测试红/绿闭环锁定）。

## 根因分析

- 表象：失败请求出现两条 usage 记录，其中一条伪成功。
- 真实根因：`AlreadyFinalized` 语义在 chain 层断链——
  1. `gatewayresponse/finalize.go` 的 `finalizeStreamFailure`（:444-541）先自写失败行（:452-468，`Success=false` + `ErrorCode`），审计 finalize 后返回 `UpstreamResponseHandlingResult{AlreadyFinalized: true, ...}`（:535-540）；
  2. chain 层 `cmd/juhe-ai-gateway/chain_v1.go` 的 finalize 短路块（修复前 :733/:739）只判断 `handling.RetryUpstream`，对 `AlreadyFinalized` 无条件调用 `gatewayresponse.FinalizeHandledUpstreamResponse`（`gatewayresponse/nonstream.go:1132`）；
  3. 该函数无 `AlreadyFinalized` 守卫，`forwardedResponseSuccessful = input.UpstreamResponse.OK() = true`（:1140），以 `Success=true / Stream=true / Usage 空 / FirstTokenMs=nil / ErrorCode 空` 再写一行（:1181-1198）。
- 契约依据：`internal/gatewayresponse/handlingresult.go:11`——"`AlreadyFinalized=true` → 终态已写入下游"，与 `RetryUpstream` 同为 chain 层必须消费的三态之一。
- 四处同构双写生产者（全部"已自写失败行后被 chain 层补伪成功行"）：
  1. `gatewayresponse/finalize.go:536`——流式失败终态（`finalizeStreamFailure`，含提交后失败与预提交检查不可重试失败）；
  2. `gatewayresponse/nonstream.go:970`——非流式协议失败（`finalizeBufferedJSONProtocolFailure`，200 失败终态体按 502 渲染）；
  3. `gatewayresponse/nonstream.go:1035`——SSE 心跳下游冲突（`finalizeNonStreamResponseAfterSseHeartbeat`）；
  4. `gatewayresponse/nonstreaminspection.go:192`——非流式检查兜底。
- 不受影响面（守卫安全性）：正常成功流不设置 `AlreadyFinalized`；`gatewayresponse` 包内成功路径没有任何 `RecordCompletedUpstreamAttempt` 调用点——chain 层该点就是正常流的唯一写行点，守卫后成功路径行为不变（绿卫兵用例锁定）。`FinalizeHandledUpstreamResponse` 全仓唯一生产调用方就是 chain 层该点。

## 修复方案

- 修改点（一处）：`chain_v1.go` finalize 短路块 `if handling.RetryUpstream` → `if handling.RetryUpstream || handling.AlreadyFinalized`，并按仓库注释风格补中文注释说明约束（`AlreadyFinalized` 生产者已自写 usage 行并 finalize 审计，chain 层再走 `FinalizeHandledUpstreamResponse` 会二次落库伪成功行；契约见 `gatewayresponse/handlingresult.go`）。不改 `gatewayresponse` 包内任何代码。
- 行为影响：命中四处生产者的请求从"两条记录"收敛为"恰一条失败记录"；成功流、`RetryUpstream` 重试流、首字截止 cutover 臂（返回自建结果非 AlreadyFinalized 完成分支）均不变。
- 回归测试：新增 `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_usage_alreadyfinalized_dedup_test.go`（修复前必红）——①流式提交后协议失败恰 1 条失败行（核心红测）；②正常成功流恰 1 条 `Success=true` 且 usage/`FirstTokenMs` 保留（绿卫兵）；③非流式 200 失败终态体恰 1 条失败行（覆盖 `nonstream.go:970` 生产者）。测试经真实组合链（`composeGatewayChain` + httptest 上游 + spool 落盘解析），两条竞争写入都在请求处理器返回前同步入队，`shutdown()` 排空后断言，无时钟/睡眠依赖。
- 发布异常处理：纯记账收敛，无 schema/env/部署变更，发布即生效。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元验证（红） | 修复前三用例 | `cd backend-go && go test ./projects/gateway/cmd/juhe-ai-gateway/ -run 'SingleUsageRow' -count=1 -v` | 双写用例必红：`usage records = 2, want 1`（第二行为伪 success=1）；成功守卫绿 | `--- FAIL: TestGatewayChainCommittedStreamFailureRecordsSingleUsageRow`、`--- FAIL: TestGatewayChainNonStreamProtocolFailureRecordsSingleUsageRow`（均 `usage records = 2, want 1`）、`--- PASS: TestGatewayChainSuccessfulStreamKeepsSingleUsageRow`、`FAIL ... 3.386s` | 已执行（红） |
| 单元验证（绿） | 修复后三用例 | 同上 | 恰 1 条记录、失败行 `Success=false` 且 `ErrorCode` 非空、成功行 usage/FirstTokenMs 保留 | 三用例 `--- PASS`，`ok ... 5.563s` | 已执行（绿） |
| 回归验证 | `gatewayresponse` 包 | `go test ./projects/gateway/internal/gatewayresponse/... -count=1` | 全绿（本批未改该包） | `ok ... 7.251s` | 已执行 |
| 回归验证 | gateway cmd 整包 | `go test ./projects/gateway/cmd/juhe-ai-gateway/... -count=1` | 全绿 | 见下方"整包运行说明" | 部分执行 |
| 生产验证 | 使用日志页失败请求不再显示成功/双记录 | 发布后命中 thinking_signature_invalid 爆发面再核对该 trace 的 usage_records | 每请求恰 1 条 `success=0` 失败行 | 未执行 | 待发布验证 |

整包运行说明：修复后首轮 `go test ./projects/gateway/cmd/juhe-ai-gateway/... -count=1` 在 600s 默认超时处 FAIL，goroutine 栈指向 `gatewaybody.NewJSONParser`（测试挂起形态）；该现象与本次改动面（chain 层 finalize 短路 + 新增 3 个端到端用例）的因果归属尚在取证，结论与原始输出回填于下轮验证记录，不顺手修改与本次改动无关的挂起测试。

## 复发记录

- 时间：
- 环境：
- 现象：
- 关联处理：

## 下次遇到

- 先查什么：同 traceId 在 `usage_records` 是否双行；伪行特征是 `success=true` + usage 全空 + `error_code` 空 + `first_token_ms` 空。先确认该请求走的响应面命中哪个 `AlreadyFinalized` 生产者（流式失败 / 非流式协议失败 / SSE 心跳冲突 / 非流式检查兜底）。
- 重点看什么：chain 层 finalize 短路块是否同时消费 `RetryUpstream` 与 `AlreadyFinalized`（`handlingresult.go` 三态契约）；新增 `AlreadyFinalized` 生产者时必须确认 chain 层短路覆盖。
- 如何避免误判：复现流式路径必须用 precise 客户端画像（`x-juhe-client-profile: codex` 等使 `InterpretSemantics=true`）；generic 客户端对 SSE 是"干净 EOF 即成功、`error` 帧原样转发"的既有契约（`pipefinal.go:361`），不要把该形态误判为本 bug 或修复无效。

## 完成总结

- 完成时间：2026-10-07
- 结论：chain 层 finalize 短路补 `AlreadyFinalized` 守卫（一处最小 diff + 约束注释），四处同构生产者的双写面全部收敛为恰一条失败行；成功流唯一写行点不受影响（绿卫兵锁定）；与 BUG-0289 正交。本文档验证记录在发布后回填生产验证行。
- 后续建议：发布后用 thinking_signature_invalid 爆发面的真实 trace 核对 `usage_records` 单行失败形态；观察使用日志页成功计数与质量分回归正常。
