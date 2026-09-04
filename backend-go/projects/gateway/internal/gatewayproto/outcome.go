package gatewayproto

// Outcome is the frozen four-way failure attribution contract of the gateway
// chain (§5.2): every upstream attempt must land in exactly one bucket.
type Outcome string

const (
	// OutcomeCompleteSuccess: HTTP framing completed and the protocol
	// semantic evidence (completion terminal, no failure frames) is present.
	OutcomeCompleteSuccess Outcome = "complete_success"
	// OutcomeFramingCompleteNeutral: HTTP framing completed but the response
	// carries no acceptable semantic completion evidence (upstream error
	// status, malformed body, stream without terminal). No retry by itself.
	OutcomeFramingCompleteNeutral Outcome = "framing_complete_neutral"
	// OutcomeUpstreamFailure: the transport could not complete a real
	// upstream exchange (timeout / connection / read failure).
	OutcomeUpstreamFailure Outcome = "upstream_failure"
	// OutcomeProbeTaskFailure: the attempt never became real upstream
	// evidence (canceled or local task failure).
	OutcomeProbeTaskFailure Outcome = "probe_task_failure"
)

// TransportFailureKind mirrors TransportProbeFailureKind.
type TransportFailureKind string

const (
	TransportFailureTimeout    TransportFailureKind = "timeout"
	TransportFailureConnection TransportFailureKind = "connection"
	TransportFailureRead       TransportFailureKind = "read"
	TransportFailureCanceled   TransportFailureKind = "canceled"
	TransportFailureTask       TransportFailureKind = "task_failure"
)

// AttemptEvidence captures how one upstream attempt ended. StatusCode is 0
// when no HTTP framing happened at all.
type AttemptEvidence struct {
	StatusCode       int
	TransportFailure TransportFailureKind
	SemanticSuccess  bool
}

// ClassifyOutcome mirrors automaticAccountProbeOutcome +
// transportProbeOutcomeFromAccountTestResult: transport-incomplete failures
// are upstream failures, canceled/task errors are probe task failures, and a
// completed HTTP framing is complete_success only with semantic success
// evidence, otherwise framing_complete_neutral.
func ClassifyOutcome(evidence AttemptEvidence) Outcome {
	switch evidence.TransportFailure {
	case TransportFailureTimeout, TransportFailureConnection, TransportFailureRead:
		return OutcomeUpstreamFailure
	case TransportFailureCanceled, TransportFailureTask:
		return OutcomeProbeTaskFailure
	}
	if evidence.StatusCode != 0 {
		if evidence.SemanticSuccess {
			return OutcomeCompleteSuccess
		}
		return OutcomeFramingCompleteNeutral
	}
	return OutcomeProbeTaskFailure
}
