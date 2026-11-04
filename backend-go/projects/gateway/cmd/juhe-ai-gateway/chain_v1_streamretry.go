package main

// 流式服务端重试的耗尽契约与候选窗口投影：RetryUpstream verdict 的终端
// 渲染（stream server-retry exhausted / client handoff）、pre-commit 失败
// 信号提取与排除集候选过滤。自 chain_v1.go 按职责拆出（REFACTOR-0007
// 文件内拆分）；被移动的函数体逐字节保持。

import (
	"net/http"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// streamServerRetryExhaustedInput mirrors sendStreamServerRetryExhaustedResponse's
// consumed input (routes.ts:2966-2982). retryReason / errorCode carry the
// actual verdict values the caller received (Node: input.retryReason /
// input.errorCode).
type streamServerRetryExhaustedInput struct {
	message        string
	retryReason    string
	errorCode      string
	decision       *gatewayresponse.ResponseInspectionDecision
	usageContext   gatewaypreauth.GatewayFailureUsageContext
	clientStrategy *gatewaypreauth.ClientStrategyContext
}

// sendStreamServerRetryExhaustedResponse renders the stream server-retry
// exhausted / client-handoff contract (routes.ts:2966 →
// sendPreCommitStreamRetryExhaustedResponse:3109-3133).
//
// Client-payload rule (V6 fix): input.message is the internal strategy /
// pipeline copy and only feeds the audit errorMessage — with the
// '服务端流式重试未找到可用账号' empty-input fallback (routes.ts:2984), which is
// an audit-only string, never the client copy. The client payload is one of
// the two fixed copies, chosen by the pre-commit failure signal:
//
//   - retryCoordination.preCommitFailureSignal === 'protocol_error_event' →
//     gatewayStreamClientRetryMessage '上游流式响应在输出前失败，请重试'
//     (responses.ts:190, via sendPreCommitStreamRetryExhaustedResponse's
//     clientVisibleMessage). Node renders it as a 200 SSE response.failed
//     event; the Go frozen surface carries no committed-SSE channel across
//     the dispatch boundary, so the same copy renders as the pre-commit HTTP
//     error with the sendPreCommit audit shape (stream_failed / 'stream').
//   - otherwise → '上游暂时不可用，请重试' (routes.ts:3036-3040) with the
//     pre_commit_http_error audit shape (upstream_failed / 'dispatch').
//
// 非终态交接（锁死 hold / 预算耗尽 / temporarily_blocked）不再走本函数的
// 503 传输：由 renderClientHandoff 先真断连，断连成功时只复用同一审计/usage
// 记录（recordGatewayFailureWithoutResponse），不向客户端写错误。
func (l *v1DispatchLoop) sendStreamServerRetryExhaustedResponse(input streamServerRetryExhaustedInput) {
	l.c.preauth.Responses.SendGatewayFailureResponse(l.streamServerRetryExhaustedFailureInput(input, "pre_commit_http_error"))
}

// streamServerRetryExhaustedFailureInput 构造交接/耗尽共用的失败记账输入：
// 审计 outcome/phase/code、usage 语义与客户端 payload 与 503 契约同源，
// responseMode 区分真实 HTTP 渲染（pre_commit_http_error）与断连交接
// （client_handoff）。
func (l *v1DispatchLoop) streamServerRetryExhaustedFailureInput(input streamServerRetryExhaustedInput, responseMode string) gatewaypreauth.FailureResponseInput {
	auditMessage := input.message
	if auditMessage == "" {
		auditMessage = "服务端流式重试未找到可用账号"
	}
	protocolErrorEvent := clientStrategyPreCommitFailureSignal(input.clientStrategy) == gatewaycodex.FailureSignalProtocolErrorEvent
	clientMessage := "上游暂时不可用，请重试"
	auditOutcome := gatewaypreauth.AuditOutcomeUpstreamFailed
	auditErrorPhase := "dispatch"
	usageErrorMessage := auditMessage
	metadata := map[string]any{
		"retryReason":  input.retryReason,
		"responseMode": responseMode,
	}
	if protocolErrorEvent {
		clientMessage = "上游流式响应在输出前失败，请重试"
		auditOutcome = gatewaypreauth.AuditOutcomeStreamFailed
		auditErrorPhase = "stream"
		usageErrorMessage = clientMessage
		errorCodeValue := input.errorCode
		if errorCodeValue == "" {
			errorCodeValue = gatewaypreauth.GatewayStreamClientRetryErrorCode
		}
		metadata["errorCode"] = errorCodeValue
	} else if input.errorCode != "" {
		metadata["upstreamErrorCode"] = input.errorCode
	}
	// routes.ts:3052-3057: the response-inspection decision rides the
	// pre-commit HTTP-error metadata (the protocol branch drops it).
	if !protocolErrorEvent && input.decision != nil {
		metadata["policyId"] = input.decision.PolicyID
		metadata["policyName"] = input.decision.PolicyName
		metadata["accountSwitch"] = input.decision.AccountSwitch
		metadata["retryEnabled"] = input.decision.RetryEnabled
		metadata["matchedField"] = input.decision.MatchedField
		metadata["matchedValue"] = input.decision.MatchedValue
	}
	if input.clientStrategy != nil {
		metadata["clientProfile"] = input.clientStrategy.ClientProfile
		metadata["downstreamProtocol"] = input.clientStrategy.DownstreamProtocol
	}
	l.auditCapture.AddGatewayMetadata("stream_server_retry_exhausted", metadata)
	return gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             l.res,
		AuditCapture:    l.auditCapture,
		UsageContext:    input.usageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(clientMessage, "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      auditOutcome,
			ErrorPhase:   auditErrorPhase,
			ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
			ErrorMessage: auditMessage,
		},
		FailureScope:      "upstream",
		RecordUsage:       boolPtr(false),
		UsageErrorMessage: usageErrorMessage,
	}
}

// renderClientHandoff 统一 pre-commit 客户端交接：流式请求先提交 SSE 头再
// 断开（避免反向代理把 pre-commit 断开合成 502），非流式请求 Hijack+Close，
// 都不可用时才回退既有 503 client retry 契约。调用方必须是 pre-commit（尚未
// 写出响应）路径；返回 true 表示请求已终结。
//
// 断连成功时不写任何 HTTP 响应，但失败审计与 usage 仍按同源契约记入
// （GatewayFailureRecorder），避免交接路径丢审计/计费行。
func (l *v1DispatchLoop) renderClientHandoff(input streamServerRetryExhaustedInput) bool {
	return l.renderClientHandoffWithMetadata(input, "", nil)
}

// renderClientHandoffWithMetadata 是 renderClientHandoff 的审计扩展入口：
// label 非空时在交接前登记调用方语义元数据，并把实际传输结果写入
// metadata["handoff"]（stream_committed_disconnect / connection_interrupt /
// http_503_fallback）。
func (l *v1DispatchLoop) renderClientHandoffWithMetadata(input streamServerRetryExhaustedInput, label string, metadata map[string]any) bool {
	return l.renderClientHandoffFailure(
		l.streamServerRetryExhaustedFailureInput(input, "client_handoff"),
		input.retryReason, input.errorCode, label, metadata)
}

// renderClientHandoffFailure 是交接传输与记账的唯一实现：failure 既是交接
// 断开不可用时的 503 回退契约，也是断开成功时的审计/usage 记账来源——两条
// 臂的 outcome / errorCode / failureAttribution / payload 完全同源。调用方
// 用自定义 failure 保留各自出口的历史 503 语义（墙钟码、capacity 归因等）。
func (l *v1DispatchLoop) renderClientHandoffFailure(failure gatewaypreauth.FailureResponseInput, retryReason, errorCode, label string, metadata map[string]any) bool {
	handoff := "http_503_fallback"
	interrupted, headersCommitted := interruptStreamingHandoff(l.res, l.req)
	switch {
	case interrupted:
		handoff = "stream_committed_disconnect"
	case headersCommitted:
		// SSE 头已提交但 Hijack 被底层拒绝：无法再改状态码，只能正常收尾；
		// 审计必须记录该降级，不能声称写入了 503。
		handoff = "stream_headers_committed_hijack_failed"
	default:
		interrupted, headersCommitted = interruptDownstreamConnection(l.res)
		switch {
		case interrupted:
			handoff = "connection_interrupt"
		case headersCommitted:
			handoff = "response_headers_committed_hijack_failed"
		}
	}
	if label != "" {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["handoff"] = handoff
		l.auditCapture.AddGatewayMetadata(label, metadata)
	}
	l.auditCapture.AddGatewayMetadata("gateway_client_handoff_transport", map[string]any{
		"retryReason": retryReason,
		"errorCode":   errorCode,
		"handoff":     handoff,
	})
	if handoff == "stream_committed_disconnect" || handoff == "connection_interrupt" {
		l.recordGatewayFailureWithoutResponse(failure)
		return true
	}
	l.c.preauth.Responses.SendGatewayFailureResponse(failure)
	return true
}

// recordGatewayFailureWithoutResponse 只落失败审计/usage，不写响应：连接已
// 被交接断开。生产 sink 实现 GatewayFailureRecorder；其他 sink（测试/裸装配）
// 回退为直接 finalize 审计，并不写响应。
func (l *v1DispatchLoop) recordGatewayFailureWithoutResponse(input gatewaypreauth.FailureResponseInput) {
	if recorder, ok := l.c.preauth.Responses.(gatewaypreauth.GatewayFailureRecorder); ok {
		recorder.RecordGatewayFailureWithoutResponse(input)
		return
	}
	l.c.observability.Logger().Warn("gateway_client_handoff_recorder_missing", map[string]any{
		"event":   "gateway_client_handoff_recorder_missing",
		"traceId": l.traceID,
	}, "客户端交接的 response sink 未实现 GatewayFailureRecorder，仅落审计")
	if input.AuditCapture != nil {
		input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
			Outcome:          input.Audit.Outcome,
			Success:          false,
			StatusCode:       input.StatusCode,
			ResponsePartType: "gateway_error",
			ErrorPhase:       input.Audit.ErrorPhase,
			ErrorCode:        input.Audit.ErrorCode,
			ErrorMessage:     input.Audit.ErrorMessage,
		})
	}
}

// clientStrategyPreCommitFailureSignal extracts
// retryCoordination.preCommitFailureSignal from the frozen G18 strategy
// context (Node input.clientStrategy?.retryCoordination.preCommitFailureSignal);
// the archived codex strategy rides the Opaque member.
func clientStrategyPreCommitFailureSignal(strategy *gatewaypreauth.ClientStrategyContext) string {
	if strategy == nil {
		return ""
	}
	resolved, ok := strategy.Opaque.(gatewaycodex.OpenAIGatewayClientStrategyContext)
	if !ok {
		return ""
	}
	return resolved.RetryCoordination.PreCommitFailureSignal
}

// streamRetryDispatchAccounts mirrors streamRetryDispatchAccounts
// (routes.ts:2855-2860): the candidate window minus the stream server-retry
// exclusions.
func streamRetryDispatchAccounts(accounts []gatewaydispatch.AccountCandidate, excluded map[string]struct{}) []gatewaydispatch.AccountCandidate {
	if len(excluded) == 0 {
		return accounts
	}
	remaining := make([]gatewaydispatch.AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if _, isExcluded := excluded[account.ID]; isExcluded {
			continue
		}
		remaining = append(remaining, account)
	}
	return remaining
}

// streamServerRetryFallbackReason mirrors streamServerRetryFallbackReason
// (routes.ts:2845-2853)。
func streamServerRetryFallbackReason(retryReason string) string {
	switch retryReason {
	case gatewayresponse.StreamServerRetryResponseInspection:
		return "response_inspection_server_retry_exhausted"
	case gatewayresponse.StreamServerRetryUpstreamProtocolFailure:
		return "upstream_protocol_server_retry_exhausted"
	case gatewayresponse.StreamServerRetryCodexEncryptedContentRecovery:
		// BUG-0289 防御命名：恢复轮经 settleResponseStreamServerRetry 恢复臂
		// 返回 false，正常流不会走到耗尽出口；若后续改动把恢复轮漏进既有
		// 耗尽分支，fallback reason 仍显式可辨，不再误报为通用 stream 耗尽。
		return "codex_encrypted_content_recovery"
	case gatewayresponse.StreamServerRetryEmptyCompletion:
		// 上游空完成失败（流式提交边界受限重放与异常终局处置设计 §3.2.3，
		// v2.3）：换号耗尽出口与通用 pre_commit 耗尽显式可辨（fallback group
		// 切换审计与耗尽终态审计都消费本 reason）。
		return "upstream_stream_empty_completion_server_retry_exhausted"
	}
	return "stream_server_retry_exhausted"
}

// stringSetKeys returns the set members in sorted order (Node spreads the Set
// in insertion order; the Go audit metadata keeps a deterministic form).
func stringSetKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
