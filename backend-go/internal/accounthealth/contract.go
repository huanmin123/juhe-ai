// Package accounthealth defines the pure account-health state-machine rules
// shared by health checks, cooldown recovery probes, and gateway prechecks.
//
// It deliberately has no transport, queue, or database dependency. Callers
// keep ownership of persistence and side effects after classifying a probe.
package accounthealth

import (
	"net/url"
	"strings"
	"time"
)

type Status string

const (
	StatusActive               Status = "active"
	StatusPendingTest          Status = "pending_test"
	StatusDisabled             Status = "disabled"
	StatusError                Status = "error"
	StatusRateLimited          Status = "rate_limited"
	StatusTemporaryUnavailable Status = "temporary_unavailable"
)

type ProbeOutcome string

const (
	// ProbeOutcomeCompleteSuccess permits the caller's success transition.
	ProbeOutcomeCompleteSuccess ProbeOutcome = "complete_success"
	// ProbeOutcomeFramingCompleteNeutral means the probe received and fully
	// framed an upstream response, but the diagnostic result itself was not a
	// success. It is intentionally not evidence for an account-state mutation.
	ProbeOutcomeFramingCompleteNeutral ProbeOutcome = "framing_complete_neutral"
	// ProbeOutcomeUpstreamFailure is the only failed outcome that is attributable
	// to an account and may drive its persistent health state.
	ProbeOutcomeUpstreamFailure ProbeOutcome = "upstream_failure"
	// ProbeOutcomeTaskFailure has no completed real upstream response and must
	// not be converted into an account-failure state transition.
	ProbeOutcomeTaskFailure ProbeOutcome = "probe_task_failure"
)

// probeTransportKind records whether a real upstream exchange completed its
// response framing, failed during transport, or produced no account-attributable
// transport evidence. It deliberately does not infer completion from response
// headers: a response body can still time out or be interrupted after headers.
type probeTransportKind uint8

const (
	probeTransportFramingComplete probeTransportKind = iota + 1
	probeTransportIncomplete
	probeTransportUnknown
)

// ProbeTransportFailureKind contains only failures that can be attributed to a
// real upstream attempt. Cancellation and executor failures use the separate
// ProbeTaskFailureKind type so callers cannot pass them to IncompleteTransport.
type ProbeTransportFailureKind uint8

const (
	ProbeTransportFailureTimeout ProbeTransportFailureKind = iota + 1
	ProbeTransportFailureConnection
	ProbeTransportFailureRead
)

// ProbeTaskFailureKind identifies non-account-attributable probe termination.
type ProbeTaskFailureKind uint8

const (
	ProbeTaskFailureCanceled ProbeTaskFailureKind = iota + 1
	ProbeTaskFailureExecutor
)

// ProbeTransportEvidence is the bounded transport fact produced by the probe
// executor. Completed framing carries a status code; incomplete transport is
// attributed from its typed failure rather than from whether headers arrived.
type ProbeTransportEvidence struct {
	kind             probeTransportKind
	transportFailure ProbeTransportFailureKind
	taskFailure      ProbeTaskFailureKind
	statusCode       int
}

// FramingCompleteTransport records a fully consumed upstream response.
func FramingCompleteTransport(statusCode int) ProbeTransportEvidence {
	return ProbeTransportEvidence{kind: probeTransportFramingComplete, statusCode: statusCode}
}

// IncompleteTransport records a real upstream transport failure.
func IncompleteTransport(failureKind ProbeTransportFailureKind) ProbeTransportEvidence {
	return ProbeTransportEvidence{kind: probeTransportIncomplete, transportFailure: failureKind}
}

// UnknownTransport records cancellation or a local probe-executor failure.
func UnknownTransport(failureKind ProbeTaskFailureKind) ProbeTransportEvidence {
	return ProbeTransportEvidence{kind: probeTransportUnknown, taskFailure: failureKind}
}

// ProbeUpstreamAttempt identifies an exchange that was actually handed to the
// real HTTP(S) upstream transport. Its zero value means no real attempt
// happened, even if a URL was constructed locally.
type ProbeUpstreamAttempt struct {
	url     string
	present bool
}

// NewProbeUpstreamAttempt records that execution handed this URL to the
// upstream transport. URL validation remains part of classification so a
// malformed or synthetic attempt still cannot affect account state.
func NewProbeUpstreamAttempt(rawURL string) ProbeUpstreamAttempt {
	return ProbeUpstreamAttempt{url: rawURL, present: true}
}

// ProbeEvidence records the minimum evidence needed for a background probe
// decision. A local synthetic URL or an absent attempt is never account-
// attributable.
type ProbeEvidence struct {
	Success         bool
	UpstreamAttempt ProbeUpstreamAttempt
	Transport       ProbeTransportEvidence
}

// ClassifyAutomaticProbeOutcome matches the Node runtime attribution contract.
// A completed real upstream response may succeed or remain neutral on a
// business-level failure. A real connection, timeout, or interrupted-read
// failure is account-attributable even when framing never completed.
// Cancellation, local synthetic errors, missing real attempts, and malformed or
// contradictory evidence remain task failures.
func ClassifyAutomaticProbeOutcome(evidence ProbeEvidence) ProbeOutcome {
	if !evidence.UpstreamAttempt.present || !IsRealHTTPUpstreamURL(evidence.UpstreamAttempt.url) {
		return ProbeOutcomeTaskFailure
	}

	switch evidence.Transport.kind {
	case probeTransportIncomplete:
		if evidence.Transport.taskFailure == 0 && isAccountAttributableTransportFailure(evidence.Transport.transportFailure) {
			return ProbeOutcomeUpstreamFailure
		}
	case probeTransportFramingComplete:
		if evidence.Transport.transportFailure != 0 || evidence.Transport.taskFailure != 0 || !isHTTPStatusCode(evidence.Transport.statusCode) {
			return ProbeOutcomeTaskFailure
		}
		if evidence.Success {
			return ProbeOutcomeCompleteSuccess
		}
		return ProbeOutcomeFramingCompleteNeutral
	case probeTransportUnknown:
		return ProbeOutcomeTaskFailure
	}

	return ProbeOutcomeTaskFailure
}

func isAccountAttributableTransportFailure(kind ProbeTransportFailureKind) bool {
	switch kind {
	case ProbeTransportFailureTimeout, ProbeTransportFailureConnection, ProbeTransportFailureRead:
		return true
	default:
		return false
	}
}

func isHTTPStatusCode(statusCode int) bool {
	// HTTP status codes are three digits. Keeping the syntactic upper bound
	// accepts registered and extension codes without treating a zero value as
	// proof that response framing completed.
	return statusCode >= 100 && statusCode <= 999
}

func IsRealHTTPUpstreamURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	if parsed.Host != "" {
		return true
	}

	// WHATWG URL, used by Node, accepts special-scheme URLs with zero or one
	// slash (for example https:/api.example.test/v1). net/url leaves their host
	// empty, so normalize only this HTTP(S) form before checking it again.
	separator := strings.IndexByte(rawURL, ':')
	if separator < 0 {
		return false
	}
	remainder := strings.TrimLeft(rawURL[separator+1:], "/")
	if remainder == "" {
		return false
	}
	normalized, err := url.Parse(parsed.Scheme + "://" + remainder)
	return err == nil && normalized.Host != ""
}

// CooldownCandidate is the runtime state needed to decide whether a cooling
// account may consume one recovery probe slot.
type CooldownCandidate struct {
	Status        Status
	Schedulable   bool
	BoundGroupID  string
	CooldownUntil *time.Time
	ExpiresAt     *time.Time
}

func CooldownRetestEligible(candidate CooldownCandidate, now time.Time) bool {
	if candidate.Status != StatusTemporaryUnavailable && candidate.Status != StatusRateLimited {
		return false
	}
	if !candidate.Schedulable || strings.TrimSpace(candidate.BoundGroupID) == "" || candidate.CooldownUntil == nil {
		return false
	}
	if candidate.CooldownUntil.After(now) {
		return false
	}
	return candidate.ExpiresAt == nil || candidate.ExpiresAt.After(now)
}

// HealthCheckCandidate is intentionally separate from CooldownCandidate:
// periodic health checks operate on active and pending-test accounts, while
// cooldown recovery owns temporary-unavailable and rate-limited accounts.
type HealthCheckCandidate struct {
	Status       Status
	Schedulable  bool
	BoundGroupID string
	ExpiresAt    *time.Time
	NextCheckAt  *time.Time
}

func HealthCheckEligible(candidate HealthCheckCandidate, now time.Time) bool {
	if strings.TrimSpace(candidate.BoundGroupID) == "" {
		return false
	}
	if candidate.Status != StatusActive && candidate.Status != StatusPendingTest {
		return false
	}
	if candidate.Status == StatusActive && !candidate.Schedulable {
		return false
	}
	if candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(now) {
		return false
	}
	return candidate.NextCheckAt == nil || !candidate.NextCheckAt.After(now)
}

// RetestTaskVersion is the optimistic-concurrency guard captured when a
// cooldown retest is queued. Both values must still match before persistence.
type RetestTaskVersion struct {
	ConfigRevision       int
	DispatchRevision     int
	ObservationStartedAt *time.Time
	Generation           string
	SourceConfigRevision *int
}

// NormalizeCooldownRetestGeneration mirrors ECMAScript String.prototype.trim,
// which is the canonicalization contract used by the Node cooldown owner.
func NormalizeCooldownRetestGeneration(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch {
		case r >= '\u0009' && r <= '\u000d':
			return true
		case r == '\u0020', r == '\u00a0', r == '\u1680',
			r >= '\u2000' && r <= '\u200a',
			r == '\u2028', r == '\u2029', r == '\u202f', r == '\u205f', r == '\u3000', r == '\ufeff':
			return true
		default:
			return false
		}
	})
}

func CooldownRetestTaskVersionValid(version RetestTaskVersion) bool {
	generation := NormalizeCooldownRetestGeneration(version.Generation)
	return version.ConfigRevision > 0 && version.DispatchRevision > 0 &&
		version.ObservationStartedAt != nil && !version.ObservationStartedAt.IsZero() &&
		generation != "" && generation == version.Generation &&
		(version.SourceConfigRevision == nil || *version.SourceConfigRevision > 0)
}

func CooldownRetestTaskCurrent(queued, current RetestTaskVersion) bool {
	if !CooldownRetestTaskVersionValid(queued) || !CooldownRetestTaskVersionValid(current) ||
		queued.ConfigRevision != current.ConfigRevision ||
		queued.DispatchRevision != current.DispatchRevision ||
		queued.Generation != current.Generation ||
		!sameOptionalInt(queued.SourceConfigRevision, current.SourceConfigRevision) {
		return false
	}
	return queued.ObservationStartedAt.Equal(*current.ObservationStartedAt)
}

func sameOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type RetestAction string

const (
	// RetestActionRestore clears cooling state and restores the candidate.
	RetestActionRestore RetestAction = "restore"
	// RetestActionDefer retains the existing account state and schedules a short
	// retry because no account-attributable upstream response was observed.
	RetestActionDefer RetestAction = "defer"
	// RetestActionRecordFailure delegates bounded backoff or terminal-state
	// mutation to the cooldown outcome repository.
	RetestActionRecordFailure RetestAction = "record_failure"
)

func CooldownRetestActionFor(outcome ProbeOutcome) (RetestAction, bool) {
	switch outcome {
	case ProbeOutcomeCompleteSuccess:
		return RetestActionRestore, true
	case ProbeOutcomeTaskFailure, ProbeOutcomeFramingCompleteNeutral:
		return RetestActionDefer, true
	case ProbeOutcomeUpstreamFailure:
		return RetestActionRecordFailure, true
	default:
		return "", false
	}
}
