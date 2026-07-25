// Package gatewayapikeyobservation freezes the typed, sanitized boundary for a
// future API-key runtime observer. It intentionally does not write the shared
// runtime-state table while Node remains its production owner.
package gatewayapikeyobservation

import (
	"fmt"
	"strings"
	"time"
)

type TrafficSource string

const (
	TrafficSourceGateway              TrafficSource = "gateway"
	TrafficSourceAccountHealthCheck   TrafficSource = "account_health_check"
	TrafficSourceRuntimeRecoveryProbe TrafficSource = "runtime_recovery_probe"
	TrafficSourceCooldownRetest       TrafficSource = "cooldown_retest"
)

type Outcome string

const (
	OutcomeCompleteSuccess        Outcome = "complete_success"
	OutcomeUpstreamFailure        Outcome = "upstream_failure"
	OutcomeFramingCompleteNeutral Outcome = "framing_complete_neutral"
	OutcomeProbeTaskFailure       Outcome = "probe_task_failure"
	OutcomeExplicitPolicyFailure  Outcome = "explicit_policy_failure"
)

type Eligibility string

const (
	EligibilityForbidden          Eligibility = "forbidden"
	EligibilityDeferOnly          Eligibility = "defer_only"
	EligibilityPersistentEligible Eligibility = "persistent_eligible"
)

type SelectedKey struct {
	Fingerprint string
	Index       int
	Status      string
}

// Candidate deliberately contains no credential value or credentials JSON.
// LocalAccountID is the authorized instance; ResourceAccountID is the physical
// credential source that a future writer must fence independently.
type Candidate struct {
	LocalAccountID         string
	ResourceAccountID      string
	SystemAccountID        string
	AuthorizationBindingID string
	ResourceAccountType    string
	TargetConfigRevision   int
	TargetDispatchRevision int64
	SourceConfigRevision   int
	SourceDispatchRevision int64
	SelectedKey            SelectedKey
	TrafficSource          TrafficSource
	Outcome                Outcome
	TraceID                string
	AttemptID              string
	Diagnostic             string
	ObservedAt             time.Time
}

type Result struct {
	Eligibility Eligibility
	Candidate   Candidate
}

// Qualify validates identity, selected-key and revision fences before it
// classifies the event. A valid-but-not-persistable event is forbidden rather
// than an error, so callers can retain bounded local diagnostics safely.
func Qualify(candidate Candidate) (Result, error) {
	if err := validate(candidate); err != nil {
		return Result{}, err
	}
	eligibility := EligibilityForbidden
	switch candidate.TrafficSource {
	case TrafficSourceGateway:
		if candidate.Outcome == OutcomeExplicitPolicyFailure {
			eligibility = EligibilityPersistentEligible
		}
	case TrafficSourceAccountHealthCheck, TrafficSourceRuntimeRecoveryProbe, TrafficSourceCooldownRetest:
		switch candidate.Outcome {
		case OutcomeCompleteSuccess, OutcomeUpstreamFailure:
			eligibility = EligibilityPersistentEligible
		case OutcomeFramingCompleteNeutral, OutcomeProbeTaskFailure:
			eligibility = EligibilityDeferOnly
		}
	}
	return Result{Eligibility: eligibility, Candidate: candidate}, nil
}

func validate(candidate Candidate) error {
	for label, value := range map[string]string{"local account": candidate.LocalAccountID, "resource account": candidate.ResourceAccountID, "system account": candidate.SystemAccountID, "authorization binding": candidate.AuthorizationBindingID, "key fingerprint": candidate.SelectedKey.Fingerprint, "trace": candidate.TraceID, "attempt": candidate.AttemptID} {
		if err := validateText(value, 256, label, label == "trace" || label == "attempt"); err != nil {
			return err
		}
	}
	if !validFingerprint(candidate.SelectedKey.Fingerprint) {
		return fmt.Errorf("API key observation fingerprint is invalid")
	}
	if !strings.EqualFold(strings.TrimSpace(candidate.ResourceAccountType), "api_key") {
		return fmt.Errorf("API key observation resource type must be api_key")
	}
	if candidate.SelectedKey.Index < 0 || !knownAvailableStatus(candidate.SelectedKey.Status) {
		return fmt.Errorf("API key observation selected key is invalid")
	}
	if candidate.TargetConfigRevision < 1 || candidate.TargetDispatchRevision < 1 || candidate.SourceConfigRevision < 1 || candidate.SourceDispatchRevision < 1 {
		return fmt.Errorf("API key observation revision fence is invalid")
	}
	if candidate.ObservedAt.IsZero() || candidate.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return fmt.Errorf("API key observation time is invalid")
	}
	if err := validateText(candidate.Diagnostic, 512, "diagnostic", true); err != nil {
		return err
	}
	switch candidate.TrafficSource {
	case TrafficSourceGateway, TrafficSourceAccountHealthCheck, TrafficSourceRuntimeRecoveryProbe, TrafficSourceCooldownRetest:
	default:
		return fmt.Errorf("API key observation traffic source is invalid")
	}
	switch candidate.Outcome {
	case OutcomeCompleteSuccess, OutcomeUpstreamFailure, OutcomeFramingCompleteNeutral, OutcomeProbeTaskFailure, OutcomeExplicitPolicyFailure:
	default:
		return fmt.Errorf("API key observation outcome is invalid")
	}
	return nil
}

func knownAvailableStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "temporary_unavailable", "rate_limited", "error":
		return true
	default:
		return false
	}
}

func validateText(value string, limit int, label string, optional bool) error {
	value = strings.TrimSpace(value)
	if value == "" && optional {
		return nil
	}
	if value == "" || len(value) > limit || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("API key observation %s is invalid", label)
	}
	return nil
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') && !(char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}
