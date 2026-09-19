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
// The committed-disconnect branch (routes.ts:3013-3034) stays unreachable for
// the same frozen-surface reason; every Go call site renders the 503.
func (l *v1DispatchLoop) sendStreamServerRetryExhaustedResponse(input streamServerRetryExhaustedInput) {
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
		"responseMode": "pre_commit_http_error",
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
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
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
	})
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
// (routes.ts:2845-2853).
func streamServerRetryFallbackReason(retryReason string) string {
	switch retryReason {
	case gatewayresponse.StreamServerRetryResponseInspection:
		return "response_inspection_server_retry_exhausted"
	case gatewayresponse.StreamServerRetryUpstreamProtocolFailure:
		return "upstream_protocol_server_retry_exhausted"
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
