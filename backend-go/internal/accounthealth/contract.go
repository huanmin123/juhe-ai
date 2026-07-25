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

// ProbeEvidence records the minimum evidence needed for a background probe
// decision. ResponseObserved means a response header was received; it does not
// merely mean a request was constructed or sent.
type ProbeEvidence struct {
	Success          bool
	UpstreamURL      string
	ResponseObserved bool
	FramingComplete  bool
}

// ClassifyAutomaticProbeOutcome matches the Node runtime contract. A successful
// probe wins. A fully framed diagnostic failure is neutral: it proves neither a
// transport failure nor an account-state transition. Otherwise a failed probe
// is account-attributable only after a real HTTP(S) upstream response has been
// observed. Local synthetic failures, dial errors, malformed URLs, and requests
// with no response evidence stay task failures.
func ClassifyAutomaticProbeOutcome(evidence ProbeEvidence) ProbeOutcome {
	if evidence.Success {
		return ProbeOutcomeCompleteSuccess
	}
	if evidence.FramingComplete {
		return ProbeOutcomeFramingCompleteNeutral
	}
	if evidence.ResponseObserved && IsRealHTTPUpstreamURL(evidence.UpstreamURL) {
		return ProbeOutcomeUpstreamFailure
	}
	return ProbeOutcomeTaskFailure
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
	ObservationStartedAt *time.Time
}

func CooldownRetestTaskCurrent(queued, current RetestTaskVersion) bool {
	if queued.ConfigRevision != current.ConfigRevision {
		return false
	}
	if queued.ObservationStartedAt == nil || current.ObservationStartedAt == nil {
		return queued.ObservationStartedAt == nil && current.ObservationStartedAt == nil
	}
	return queued.ObservationStartedAt.Equal(*current.ObservationStartedAt)
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
	case ProbeOutcomeTaskFailure:
		return RetestActionDefer, true
	case ProbeOutcomeUpstreamFailure:
		return RetestActionRecordFailure, true
	default:
		return "", false
	}
}
