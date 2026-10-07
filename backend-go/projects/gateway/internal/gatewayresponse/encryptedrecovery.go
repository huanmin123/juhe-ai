package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
)

// BUG-0289：codex 加密上下文 200 流内失败恢复臂（finalize.go 消费）。
//
// 生产形态：Codex 客户端 /v1/responses 流式请求被路由到非生成上游，上游以
// 200 SSE 首事件 response.failed(code=encrypted_context_invalid) 拒绝加密
// 续上下文。既有两个服务端重试分支对该拦截结果都判否（retry_no_avoidance
// 无 ReplayAuthority/AccountSwitch；ResponseInspection != nil 使 pre-commit
// 分支互斥短路），失败以"请重试"文案交回客户端 → Codex 自动重试但上下文
// 不变 → 死循环。本臂复用非 2xx 失败面的同源清理核心
// （gatewaycodex.BuildCodexEncryptedContentRecoveryRetry），信号改从结构化
// 决策字段分类（ClassifyCodexEncryptedContentFailureParts），重放体基于
// input.RequestBody（引擎实际发送的已映射体，非客户端 RawBody）。

// codexOpenAIProtocolCode 是恢复臂的协议门控取值：default_openai_response_error
// 系统默认规则为 scopeType=protocol / protocolCode=openai（sqlinspection.go:242），
// 决策投影经 buildPolicyDecision → PolicyProtocolCode 原样携带。
const codexOpenAIProtocolCode = "openai"

// codexEncryptedContentRecoveryArm 在既有服务端重试分支之前执行：命中返回
// (verdict, true)；未命中返回 (零值, false) 且不改动 pipeResult。不可恢复
// （无可清理内容）时改写 pipeResult 的客户端文案后返回 false，落入既有
// 客户端交接尾——错误码归因通道（inspectionUpstreamErrorCode / CompleteAttempt）
// 不经过 Message，不受改写影响。
func (input *HandleUpstreamResponseInput) codexEncryptedContentRecoveryArm(pipeResult *StreamPipeResult) (UpstreamResponseHandlingResult, bool) {
	if pipeResult.SemanticCommitted || pipeResult.ResponseInspection == nil {
		return UpstreamResponseHandlingResult{}, false
	}
	decision := pipeResult.ResponseInspection
	// 门控（全部满足才进入）：预提交（!SemanticCommitted，上方已判）；
	// 命中策略为 openai 协议；请求为 /v1/responses 族；信号分类非空。
	if decision.PolicyProtocolCode != codexOpenAIProtocolCode || !gatewaycodex.IsOpenAIResponsesRequest(input.Req) {
		return UpstreamResponseHandlingResult{}, false
	}
	signal := gatewaycodex.ClassifyCodexEncryptedContentFailureParts(
		decision.UpstreamErrorCode,
		firstNonEmpty(decision.UpstreamErrorMessage, pipeResult.Message))
	if signal == "" {
		return UpstreamResponseHandlingResult{}, false
	}
	recovery := gatewaycodex.BuildCodexEncryptedContentRecoveryRetry(input.RequestBody, signal)
	switch recovery.Action {
	case gatewaycodex.RecoveryActionRetryWithBodyVariant:
		input.AuditCapture.AddGatewayMetadata("codex_encrypted_content_recovery_retry", codexStreamRecoveryRetryMetadata(input, recovery))
		return UpstreamResponseHandlingResult{
			RetryUpstream: true,
			RetryReason:   StreamServerRetryCodexEncryptedContentRecovery,
			// 恢复重放钉回同账户（chain 面 SameAccountRetry + RequestBodyOverride），
			// 不走换号排除，也不走引擎内 SameAccountRetryEligible 通道。
			SameAccountRetryEligible:    false,
			ExcludeCurrentAccount:       false,
			CompatibilityRecoverySignal: signal,
			RecoveryBody:                recovery.Body,
			RecoverySemanticRetryID:     recovery.SemanticRetryID,
			RecoveryMetadata:            recovery.Metadata,
			ResponseInspection:          decision,
			Message:                     pipeResult.Message,
			ErrorCode:                   pipeResult.ErrorCode,
			UncommittedResponseBody:     pipeResult.UncommittedResponseBody,
			TransportFailure:            pipeResult.TransportFailure,
		}, true
	case gatewaycodex.RecoveryActionNotRecoverable:
		// 无可清理内容：同账户重放同体必然同样失败，不再重试。客户端文案改
		// 为恢复终态（请新建会话），不再诱导客户端重试；上游错误码归因保持
		// encrypted_context_invalid（audit Finalize 的
		// inspectionUpstreamErrorCode 通道取 decision.UpstreamErrorCode，与本
		// 文案改写正交；usage 失败记录在 finalizeStreamFailure 更早处已落账）。
		input.AuditCapture.AddGatewayMetadata("codex_encrypted_content_recovery_skipped", map[string]any{
			"accountId":   input.Account.GetID(),
			"upstreamUrl": input.UpstreamURL,
			"transport":   "http",
			"signal":      signal,
			"reason":      recovery.Reason,
		})
		pipeResult.Message = gatewaycodex.CodexEncryptedContentRecoveryExhaustedMessage
		// 该失败面的预提交缓冲只可能是网关自己改写的旧失败副本（上游原始失败
		// 帧已被检查拦截替换）；保留会把"请重试"旧文案带回客户端，与恢复终态
		// 契约冲突，显式丢弃。
		pipeResult.UncommittedResponseBody = nil
		return UpstreamResponseHandlingResult{}, false
	default:
		// 防御分支：核心构造器 BuildCodexEncryptedContentRecoveryRetry 实际只会
		// 返回 retry_with_body_variant / not_recoverable（nil body、解析失败、
		// 无可清理项都归 not_recoverable，分别走上方重试臂与文案改写臂），本
		// 分支当前不可达；保留作 switch 完整性兜底——语义为不重试、不改文案、
		// 完全维持既有行为（BUG-0289 复查轮注释纠正）。
		return UpstreamResponseHandlingResult{}, false
	}
}

// codexStreamRecoveryRetryMetadata 对齐非 2xx 失败面 codexRecoveryMetadataOf
// 的键形状（cmd/juhe-ai-gateway/chain_ports.go）：accountId / upstreamUrl /
// transport / strategy / signal / 各 removed 计数 / BodyBytesBefore/After。
func codexStreamRecoveryRetryMetadata(input *HandleUpstreamResponseInput, recovery gatewaycodex.CodexEncryptedContentRecoveryResult) map[string]any {
	metadata := recovery.Metadata
	if metadata == nil {
		return map[string]any{
			"accountId":   input.Account.GetID(),
			"upstreamUrl": input.UpstreamURL,
			"transport":   "http",
			"signal":      recovery.Signal,
		}
	}
	return map[string]any{
		"accountId":                             input.Account.GetID(),
		"upstreamUrl":                           input.UpstreamURL,
		"transport":                             "http",
		"strategy":                              metadata.Strategy,
		"signal":                                metadata.Signal,
		"removedReasoningEncryptedContentCount": metadata.RemovedReasoningEncryptedContentCount,
		"removedFunctionOutputEncryptedContentCount": metadata.RemovedFunctionOutputEncryptedContentCount,
		"removedAgentMessageEncryptedContentCount":   metadata.RemovedAgentMessageEncryptedContentCount,
		"removedCompactionEncryptedContentCount":     metadata.RemovedCompactionEncryptedContentCount,
		"removedReasoningItemCount":                  metadata.RemovedReasoningItemCount,
		"removedAgentMessageItemCount":               metadata.RemovedAgentMessageItemCount,
		"removedCompactionItemCount":                 metadata.RemovedCompactionItemCount,
		"preservedPreviousResponseID":                metadata.PreservedPreviousResponseID,
		"bodyBytesBefore":                            metadata.BodyBytesBefore,
		"bodyBytesAfter":                             metadata.BodyBytesAfter,
	}
}
