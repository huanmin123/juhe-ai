package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// gatewayStreamClientRetryErrorCode 已在 gatewaypreauth 冻结为
// GatewayStreamClientRetryErrorCode；本包通过别名使用。
const gatewayStreamClientRetryErrorCode = gatewaypreauth.GatewayStreamClientRetryErrorCode

// gatewayStreamClientRetryMessage 对齐 gatewayStreamClientRetryMessage。
const GatewayStreamClientRetryMessage = gatewaypreauth.GatewayStreamClientRetryMessage

// serverRetryableSystemDefaultResponseInspectionPolicyIdsPreWrite 对齐
// stream-retry-decision.ts 的集合（不含 context_window）。
var serverRetryableSystemDefaultResponseInspectionPolicyIdsPreWrite = map[string]bool{
	"default_codex_compaction_contract": true,
	"default_gemini_cli_retryable_error": true,
}

// serverRetryableSystemDefaultResponseInspectionPolicyIds 对齐
// stream-finalization-retry-decision.ts 的集合。
var serverRetryableSystemDefaultResponseInspectionPolicyIds = map[string]bool{
	"default_codex_compaction_contract":   true,
	"default_openai_context_window_error": true,
	"default_gemini_cli_retryable_error":  true,
}

// transientPrecommitUpstreamPolicyIds 对齐 transientPrecommitUpstreamPolicyIds。
var transientPrecommitUpstreamPolicyIds = map[string]bool{
	"default_openai_transient_precommit_error":    true,
	"default_anthropic_transient_precommit_error": true,
	"default_gemini_transient_precommit_error":    true,
}

// StreamServerRetryReason 对齐 StreamServerRetryReason union。
const (
	StreamServerRetryResponseInspection          = "response_inspection"
	StreamServerRetryUpstreamProtocolFailure     = "upstream_protocol_failure"
	StreamServerRetryPreCommitStreamFailure      = "pre_commit_stream_failure"
	StreamServerRetryCodexEncryptedContentRecovery = "codex_encrypted_content_recovery"
	StreamServerRetryNormalRouteFirstByteTimeout = "normal_route_first_byte_timeout"
	StreamServerRetryHybridQuality               = "hybrid_quality"
)

// StreamClientFailureCode 对齐 streamClientFailureCode。
func StreamClientFailureCode(errorCode string, outputReceived bool, clientRetryEnabled bool, downstreamBytesWritten int64) string {
	if clientRetryEnabled && (!outputReceived || downstreamBytesWritten > 0) {
		return gatewayStreamClientRetryErrorCode
	}
	return errorCode
}

// ShouldReturnResponseInspectionBeforeDownstreamWrite 对齐
// shouldReturnResponseInspectionBeforeDownstreamWrite。
func ShouldReturnResponseInspectionBeforeDownstreamWrite(decision *ResponseInspectionDecision, response PreCommitResponseState, totalResponseBytes int64) bool {
	if decision == nil {
		return false
	}
	serverRetryableSystemDefault := isServerRetryableSystemDefaultResponseInspectionDecisionPreWrite(decision)
	return (decision.Reason == "configured_response_policy" || serverRetryableSystemDefault) &&
		(decision.PolicySource != "system_default" || serverRetryableSystemDefault) &&
		totalResponseBytes == 0 &&
		!response.HeadersSent &&
		!response.WritableEnded &&
		!response.Destroyed
}

func isServerRetryableSystemDefaultResponseInspectionDecisionPreWrite(decision *ResponseInspectionDecision) bool {
	return decision != nil &&
		decision.PolicySource == "system_default" &&
		serverRetryableSystemDefaultResponseInspectionPolicyIdsPreWrite[decision.PolicyID]
}

func isServerRetryableSystemDefaultResponseInspectionDecision(decision *ResponseInspectionDecision) bool {
	return decision != nil &&
		decision.PolicySource == "system_default" &&
		serverRetryableSystemDefaultResponseInspectionPolicyIds[decision.PolicyID]
}

// ShouldInterruptCommittedGenericStream 对齐
// shouldInterruptCommittedGenericStream。
func ShouldInterruptCommittedGenericStream(protocolFailureEventEnabled bool, downstreamBytesWritten int64) bool {
	return !protocolFailureEventEnabled && downstreamBytesWritten > 0
}

// IsTransientPrecommitUpstreamFailureDecision 对齐
// isTransientPrecommitUpstreamFailureDecision。
func IsTransientPrecommitUpstreamFailureDecision(decision *ResponseInspectionDecision) bool {
	return decision != nil &&
		decision.PolicySource == "system_default" &&
		decision.Reason == "before_downstream_write_response_failure" &&
		decision.TriggerPhase == "before_downstream_write" &&
		!decision.DownstreamWritten &&
		transientPrecommitUpstreamPolicyIds[decision.PolicyID]
}

// ShouldRetryResponseInspectionDecisionOnServer 对齐
// shouldRetryResponseInspectionDecisionOnServer。
func ShouldRetryResponseInspectionDecisionOnServer(decision *ResponseInspectionDecision, response PreCommitResponseState) bool {
	if decision == nil {
		return false
	}
	serverRetryableSystemDefault := isServerRetryableSystemDefaultResponseInspectionDecision(decision)
	transientPrecommitUpstreamFailure := IsTransientPrecommitUpstreamFailureDecision(decision)
	replayAuthorityOK := transientPrecommitUpstreamFailure ||
		decision.ReplayAuthority == "explicit_user_policy" ||
		decision.ReplayAuthority == "system_default_retry_next_account"
	accountSwitchOK := transientPrecommitUpstreamFailure ||
		decision.AccountSwitch == "request_next_account" ||
		decision.AccountSwitch == "avoid_account_ttl" ||
		decision.AccountSwitch == "avoid_upstream_bucket_ttl"
	reasonOK := decision.Reason == "configured_response_policy" || serverRetryableSystemDefault || transientPrecommitUpstreamFailure
	policySourceOK := transientPrecommitUpstreamFailure ||
		decision.PolicySource != "system_default" ||
		serverRetryableSystemDefault
	return replayAuthorityOK && accountSwitchOK && reasonOK && policySourceOK &&
		!response.WritableEnded &&
		!response.Destroyed
}

// ShouldRetryPreCommitStreamFailureOnServer 对齐
// shouldRetryPreCommitStreamFailureOnServer。
func ShouldRetryPreCommitStreamFailureOnServer(result StreamPipeResult, response PreCommitResponseState) bool {
	// A stream with no semantic event is replayable whether it has written a
	// transport-only heartbeat or has not committed any downstream bytes yet.
	// The downstream byte/state pair is only evidence that a transport
	// heartbeat was actually written; HTTP headers alone never enter this
	// decision.
	return !result.Completed &&
		!result.SemanticCommitted &&
		!result.GatewayLocalFailure &&
		result.ErrorCode != "" &&
		!response.WritableEnded &&
		!response.Destroyed
}

// PreCommitStreamServerRetryErrorCode 对齐 preCommitStreamServerRetryErrorCode。
// clientStrategyPreCommitProtocolError 由调用方从 client strategy 读出
//（retryCoordination.preCommitFailureSignal === 'protocol_error_event'）。
func PreCommitStreamServerRetryErrorCode(result StreamPipeResult, clientStrategyPreCommitProtocolError bool) string {
	if clientStrategyPreCommitProtocolError {
		return gatewayStreamClientRetryErrorCode
	}
	return gatewaypreauth.GatewayStreamFailureCode(result.Message)
}

// ShouldExcludeCurrentAccountForStreamServerRetry 对齐
// shouldExcludeCurrentAccountForStreamServerRetry。
func ShouldExcludeCurrentAccountForStreamServerRetry(decision *ResponseInspectionDecision) bool {
	if decision == nil {
		return false
	}
	return decision.AccountSwitch == "request_next_account" ||
		decision.AccountSwitch == "avoid_account_ttl" ||
		decision.AccountSwitch == "avoid_upstream_bucket_ttl" ||
		decision.AccountState == "runtime_avoidance"
}

// ShouldRememberGatewayClientSourceFailure 对齐
// shouldRememberGatewayClientSourceFailure：allowClientSourceAccountAvoidance
// 由调用方从 client strategy 读出。
func ShouldRememberGatewayClientSourceFailure(result StreamPipeResult, allowClientSourceAccountAvoidance bool) bool {
	return !result.Completed &&
		!result.GatewayLocalFailure &&
		allowClientSourceAccountAvoidance &&
		(result.ErrorCode == gatewayStreamClientRetryErrorCode ||
			(result.ResponseInspection != nil && result.ResponseInspection.RewriteErrorCode == gatewayStreamClientRetryErrorCode))
}
