package gatewaycircuit

// Local suppression preflight contract (runtime/local-suppression-preflight.ts).
// The orchestration wrapper itself stays with the dispatch slice (G15/G20)
// because it owns the express request/response and route coordinator; the
// client-facing failure contract below must match Node byte for byte.

// LocalSuppressionExhaustedStatusCode mirrors the 503 the preflight returns
// when every candidate account is still suppressed after the recoverable
// wait window.
const LocalSuppressionExhaustedStatusCode = 503

// Failure payload strings mirror completeFailure exactly.
const (
	LocalSuppressionExhaustedMessage    = "所有上游账户正在临时隔离，请稍后重试"
	LocalSuppressionExhaustedErrorType  = "service_unavailable"
	LocalSuppressionExhaustedErrorCode  = "upstream_retryable_error"
	LocalSuppressionExhaustedErrorPhase = "dispatch"
)

// Wait reason used for the recoverable wait scope of the suppression preflight.
const LocalAccountSuppressionWaitReason = "local_account_suppression"

// LocalSuppressionExhaustedFailure mirrors the completeFailure input.
type LocalSuppressionExhaustedFailure struct {
	StatusCode   int
	Message      string
	ErrorType    string
	ErrorCode    string
	ErrorPhase   string
	RetryAfterMs *int64
}

// LocalSuppressionExhaustedFailureResponse mirrors the completeFailure call:
// the wait reason stays internal coordination state (audit metadata above)
// and clients receive one stable retry contract instead of gateway
// scheduling internals.
func LocalSuppressionExhaustedFailureResponse(nextRetryAfterMs *int64) LocalSuppressionExhaustedFailure {
	return LocalSuppressionExhaustedFailure{
		StatusCode:   LocalSuppressionExhaustedStatusCode,
		Message:      LocalSuppressionExhaustedMessage,
		ErrorType:    LocalSuppressionExhaustedErrorType,
		ErrorCode:    LocalSuppressionExhaustedErrorCode,
		ErrorPhase:   LocalSuppressionExhaustedErrorPhase,
		RetryAfterMs: nextRetryAfterMs,
	}
}

// RecoverableSuppressionScopeKey mirrors recoverableSuppressionScopeKey:
// [systemAccountId, apiKeyId ?? '', groupId].join(':').
func RecoverableSuppressionScopeKey(systemAccountID, apiKeyID, groupID string) string {
	if apiKeyID == "" {
		apiKeyID = ""
	}
	return systemAccountID + ":" + apiKeyID + ":" + groupID
}
