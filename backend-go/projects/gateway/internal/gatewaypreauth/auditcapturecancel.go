package gatewaypreauth

// Additive audit-capture lifecycle surface for the /v1 chain orchestrator
// (chain_v1.go). The frozen AuditCaptureContext interface (types.go) stays
// untouched; the orchestrator discovers the cancel half through this optional
// interface, mirroring audit/capture.service.ts cancel():
//
// - routes.ts:515  preflight throw  -> auditCapture.cancel(); rethrow
// - routes.ts:527  preflight reject -> auditCapture.cancel(); return
// - routes.ts:2645 request finally  -> auditCapture.cancel()
//
// Implementations must be idempotent: a capture that already finalized (or
// already canceled) is a no-op, so the finally-path call after an explicit
// failure-path call is safe. Cancel releases the active-capture slot of an
// un-finalized capture (gatewayusage.AuditCaptureContext.Cancel).

// AuditCaptureCanceller is the optional cancel surface of the audit capture.
type AuditCaptureCanceller interface {
	// Cancel mirrors capture.cancel(): an un-finalized capture is discarded
	// and its active-capture registration released; a finalized capture
	// no-ops.
	Cancel()
}

// CancelAuditCapture cancels the capture when its concrete type exposes the
// lifecycle; plain captures (test fakes, disabled audit) pass through.
func CancelAuditCapture(capture AuditCaptureContext) {
	if canceller, ok := capture.(AuditCaptureCanceller); ok {
		canceller.Cancel()
	}
}

// Audit outcome values the routes.ts failure exits render beyond the frozen
// gateway_failed / success pair.
const (
	// AuditOutcomeStreamFailed mirrors outcome: 'stream_failed' (the stream
	// server-retry-exhausted / client-handoff exits).
	AuditOutcomeStreamFailed = "stream_failed"
	// AuditOutcomeUpstreamFailed mirrors outcome: 'upstream_failed' (the
	// top-level dispatch-exhaustion / unexpected-dispatch-error exit).
	AuditOutcomeUpstreamFailed = "upstream_failed"
)

// Route coordination outcome value carried by RouteActionCoordination.Outcome
// for the client-handoff exit (routes.ts:398-411); the frozen preflight only
// produces temporarily_blocked / hard_exhausted.
const (
	// RouteOutcomeClientHandoff mirrors outcome === 'client_handoff'.
	RouteOutcomeClientHandoff = "client_handoff"
)
