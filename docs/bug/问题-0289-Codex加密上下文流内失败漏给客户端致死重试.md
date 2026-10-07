# BUG-0289 Codex 加密上下文流内失败漏给客户端致死重试

## 基本信息

- 编号：BUG-0289
- 状态：已修复（待发布验证）
- 严重程度：P1
- 发现时间：2026-10-07
- 发现方式：用户反馈
- 模块：后端 / 网关
- 关联计划：无
- 关联 bug：BUG-0267（§3.3 留观项"codex encrypted-content 恢复的链面路径：2xx-body 期/pre-commit 触发面仍缺"，本修复收口）

## 问题概述

- 现象：生产 Codex 客户端在会话中出现"正在重新连接 13/100"式自动重试死循环，每个回合都是空回合提示，请求永不成功。
- 期望：上游以 HTTP 200 SSE 返回 `event: response.failed` 且 `response.error.code` 命中加密上下文信号、下游语义输出未提交时，网关应在服务端完成恢复（清理加密上下文并重放）或给出不可重试的恢复终态文案，而不是把可重试失败交给客户端。
- 实际：Codex 会话跨请求被路由到不同上游账户，`encrypted_content` 只有生成它的上游能解密；其他账户返回 200 流内 `response.failed(encrypted_context_invalid)`。网关响应检查拦截该失败后，两条既有服务端恢复路径均够不着——加密上下文兼容恢复只接在"上游非 2xx 失败响应"面、未接 200 流内预提交面，且信号白名单缺 `encrypted_context_invalid` 精确码；最终网关把可重试文案"上游流式响应在输出前失败，请重试"下发给客户端。加密上下文不随客户端重试变化，重试必然再次命中同一信号，形成确定性死循环。
- 影响范围：携带跨账户 `encrypted_content` 的 Codex `/v1/responses` 会话流式请求。非流式 JSON 失败面、语义已提交后的失败、Anthropic/Gemini 协议不在本恢复范围。

## 复现步骤

1. 用 Codex 客户端建立会话，产生带 `encrypted_content`（reasoning / 工具输出 / compaction）的历史。
2. 让该会话的后续请求被路由到与生成 `encrypted_content` 不同的上游账户，连续请求 `/v1/responses`。
3. 观察：上游返回 200 流内 `response.failed(encrypted_context_invalid)`，客户端收到可重试文案后自动重试，反复出现"正在重新连接 N/100"与空回合，且每次重试仍失败。

## 环境信息

- 分支 / 版本：生产国内单机 Docker go-only（103.36.63.105），2026-10-07。
- 数据状态：不涉及数据修复。
- 浏览器 / 系统 / Node 版本：Codex 客户端（任意版本），后端缺陷。
- 是否稳定复现：是（携带跨账户加密上下文时必现）。

## 根因分析

- 表象：Codex 客户端死重试、回合全部为空。
- 真实根因：三分缺口叠加——
  1. **触发面缺口**：加密上下文兼容恢复（一次性清理 + 同账户重放）只接在"上游非 `2xx` 失败响应"面（`gatewaydispatch/attemptoutcomes.go` 的 `retry_with_compatibility_recovery` 语义重放臂，见 BUG-0267 登记）；本场景上游是 HTTP 200 SSE 流内 `response.failed`，不进入该面。
  2. **信号识别缺口**：恢复信号 allowlist 只有 `thinking_signature_invalid` / `invalid_encrypted_content` / `encrypted_content_decryption_failed` 三个精确码（`gatewaycodex/encryptedcontent.go`），生产实际返回的 `encrypted_context_invalid` 不在其中；消息启发式也缺 "could not validate"。
  3. **分支互斥短路**：`gatewayresponse/finalize.go` 的预提交失败分支互斥——200 流内 `response.failed` 命中响应检查拦截后，`retry_no_avoidance` 动作只返回客户端可重试失败、无重放权限，兼容恢复又不在该触发面，两条服务端恢复路径都判否，失败直接交接客户端。
- 动机事实：`encrypted_content` 只有生成它的上游账户能解密，跨账户重放该上下文必然再次命中同一信号；把可重试文案交给客户端等于制造确定性死循环。

## 修复方案

- 触发面扩展：Codex/OpenAI Responses 的加密上下文兼容恢复从"上游非 `2xx` 失败响应"面扩展到 **200 流内预提交失败面**——上游以 HTTP 200 SSE 返回 `event: response.failed`、`response.error.code` 命中加密上下文信号、下游语义输出未提交时，网关在响应检查拦截点执行一次性兼容清理：从实际发送的请求体（含模型映射后副本）移除被拒的加密内容（reasoning / function_output / agent_message / compaction 的 `encrypted_content` 族），钉住同一账户重放一次；重放去重预算沿用既有 `(SemanticRetryID, 账户运行态键, 物理凭据键)` 一次语义，SemanticRetryID 为 `codex_encrypted_content_cleanup:<signal>`。
- 信号清单增列：精确码新增 `encrypted_context_invalid`；消息启发式新增 "could not validate"（与既有 "could not be decrypted/decoded/verified/parsed" 并列，仍以含 "encrypted" 为前提）。非 2xx 失败体与 200 流内 `response.failed` 决策字段两个触发面共享同一分类器。
- 终态文案：信号命中但无 Climable 内容可清理，或清理重放再次命中同一信号时，不再下发可重试文案"上游流式响应在输出前失败，请重试"，改为 `gatewaycodex.CodexEncryptedContentRecoveryExhaustedMessage`："上游拒绝了加密上下文，网关已尝试一次兼容性清理但仍然失败。请新建会话，或不要携带上一会话的加密 reasoning、工具输出或 compaction 后重新发送请求。"；错误码归因分通道：audit 以 `inspectionUpstreamErrorCode` 通道保持上游原码（如 `encrypted_context_invalid`）不变；usage 失败行错误码维持既有网关改写码口径（本修复未改变该通道）。流内恢复终态事件的 `response.error.code` 亦保持网关码，仅 message 换恢复终态（与非 2xx 面"第二次命中以 signal 为 code"的口径不同）。
- 审计/日志：恢复重放打审计 metadata 键 `codex_encrypted_content_recovery_retry`、跳过打 `codex_encrypted_content_recovery_skipped`（与既有非 2xx 面同键）；chain 流式服务端重试裁决新增 retryReason `codex_encrypted_content_recovery`（不排除当前账户、钉住同账户）。
- 边界（非目标）：仅 OpenAI 协议 + `/v1/responses` 族 + 下游语义未提交（预提交）生效；非流式 JSON 失败面、语义已提交后的失败、Anthropic/Gemini 协议不在本次恢复范围。恢复重放是服务端行为，成功时客户端拿到正常成功流，无感知。
- 附属发现（BUG-0289 排障中发现，已正式登记 [BUG-0290](问题-0290-AlreadyFinalized终态chain层补写伪成功使用记录.md)，随本批同发布）：chain 响应轮消费处 `AlreadyFinalized` verdict 原本会落进 `FinalizeHandledUpstreamResponse`，按上游 2xx 再落库一条伪成功 usage 行——即生产 usage_records 中与失败行同毫秒成对出现的"成功 200"行（如 2026-10-07 `vsllm.com-3105` gpt-6.1-sol 25517ms 失败 + 25518ms "成功"对）。修复为 `AlreadyFinalized` 与 `RetryUpstream` 同样短路（生产者在返回前已自写 usage 失败行并 finalize 审计），用量记录不再产生伪成功对。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元验证 | 信号分类器 | `go test ./projects/gateway/internal/gatewaycodex/... -count=1`（在 `backend-go/` 下） | `encrypted_context_invalid` 精确码与 "could not validate" 启发命中分类；非信号不触发 | ok 4.4s（TestBug0289Classify* 6 项 + 既有分类回归全绿） | 已验证 |
| 单元验证 | 流内恢复臂 verdict 与终态文案 | `go test ./projects/gateway/internal/gatewayresponse/... -count=1` | 信号命中且语义未提交 → 一次性清理后钉住同账户重放一次；不可恢复 → 恢复终态文案而非可重试文案 | ok 6.1s（TestBug0289StreamEncryptedContext* 3 项 + 既有 finalize/pipefinal 回归全绿） | 已验证 |
| 单元验证 | 重放去重与 chain 钉住重放 | `go test -run 'Bug0289\|FetchFirstAvailableUpstreamRequestBodyOverride' ./internal/gatewaydispatch/ ./cmd/juhe-ai-gateway/`、`go test -run 'TestV1Loop\|TestV1R5\|TestW1V\|HotQuality\|PostVerdict\|ChainV1Recovery\|Avoidance\|StreamRetry\|Exhausted' ./cmd/juhe-ai-gateway/`（在 `backend-go/projects/gateway/` 下） | `(SemanticRetryID, 账户运行态键, 物理凭据键)` 一次去重；retryReason `codex_encrypted_content_recovery` 不排除当前账户 | ok（override 同账户重放 + chain 恢复臂/中性分类/回归类 40.3s 全绿） | 已验证 |
| 生产验证 | Codex 跨账户会话不再死重试 | 发布后用携带跨账户加密上下文的 Codex 会话连续请求 `/v1/responses` | 服务端恢复成功或下发恢复终态文案，客户端不再自动重试死循环 | 未执行 | 待发布验证 |

## 复发记录

- 时间：
- 环境：
- 现象：
- 关联处理：

## 下次遇到

- 先查什么：上游 200 流内 `response.failed` 的 `response.error.code` 是否命中加密上下文信号 allowlist；审计 metadata 是否有 `codex_encrypted_content_recovery_retry` / `codex_encrypted_content_recovery_skipped`。
- 重点看什么：恢复触发面是否覆盖当前失败形态（非 2xx 失败体 vs 200 流内预提交 `response.failed`）；下游 `semanticCommitted` 状态；`(SemanticRetryID, 账户运行态键, 物理凭据键)` 一次重放预算是否已消费。
- 如何避免误判：新增上游加密上下文类错误码时先补分类器 allowlist，并确认非 2xx 失败体与 200 流内 `response.failed` 两个触发面共享同一分类器，不要只在单面生效；客户端"正在重新连接 N/100"式死循环优先怀疑网关下发了可重试文案、且重试确定性失败。

## 完成总结

- 完成时间：2026-10-07
- 结论：Codex/OpenAI Responses 加密上下文兼容恢复扩展到 200 流内预提交失败面（一次性清理实际发送请求体并钉住同账户重放一次），信号清单补 `encrypted_context_invalid` 与 "could not validate" 启发、两个触发面共享同一分类器，不可恢复时下发恢复终态文案而非可重试文案，消除 Codex 客户端自动重试死循环；BUG-0267 §3.3 对应留观项一并收口。本文档验证记录在发布后回填。
- 后续建议：发布后按验证记录表执行单测与生产验证并回填；观察审计 `codex_encrypted_content_recovery_retry` / `codex_encrypted_content_recovery_skipped` 命中情况。
