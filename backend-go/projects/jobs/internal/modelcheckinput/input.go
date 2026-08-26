// Package modelcheckinput defines the immutable J3b work unit that a future
// jobs runtime will persist before it can contact an upstream provider.
package modelcheckinput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

type Trigger string

const (
	TriggerManual          Trigger = "manual"
	TriggerScheduled       Trigger = "scheduled"
	TriggerQualityRecovery Trigger = "quality_recovery"
)

type AccountSnapshot struct {
	ID                        string `json:"id"`
	ConfigRevision            string `json:"configRevision"`
	ProviderCode              string `json:"providerCode"`
	ProtocolProfileID         string `json:"protocolProfileId"`
	ProtocolProfileRevision   string `json:"protocolProfileRevision"`
	EndpointFingerprint       string `json:"endpointFingerprint"`
	MappedUpstreamModel       string `json:"mappedUpstreamModel"`
	CredentialEnvelopeRef     string `json:"credentialEnvelopeRef"`
	ProxyConfigurationVersion string `json:"proxyConfigurationVersion"`
}

type PolicySnapshot struct {
	Revision string `json:"revision"`
	Digest   string `json:"digest"`
}

type Draft struct {
	InputID              string
	SystemAccountID      string
	ActorSystemAccountID string
	Target               AccountSnapshot
	Comparison           *AccountSnapshot
	Model                string
	Profile              string
	Trigger              Trigger
	ScheduleID           string
	TrustedComparison    bool
	ProbeSetVersion      string
	Policy               PolicySnapshot
	IssuedAt             time.Time
	DeadlineAt           time.Time
}

type IssuedInput struct {
	SchemaVersion        int              `json:"schemaVersion"`
	InputVersion         int64            `json:"inputVersion"`
	InputID              string           `json:"inputId"`
	SystemAccountID      string           `json:"systemAccountId"`
	ActorSystemAccountID string           `json:"actorSystemAccountId"`
	Target               AccountSnapshot  `json:"target"`
	Comparison           *AccountSnapshot `json:"comparison,omitempty"`
	Model                string           `json:"model"`
	Profile              string           `json:"profile"`
	Trigger              Trigger          `json:"trigger"`
	ScheduleID           string           `json:"scheduleId,omitempty"`
	TrustedComparison    bool             `json:"trustedComparison"`
	ProbeSetVersion      string           `json:"probeSetVersion"`
	Policy               PolicySnapshot   `json:"policy"`
	IssuedAt             time.Time        `json:"issuedAt"`
	DeadlineAt           time.Time        `json:"deadlineAt"`
	InputDigest          string           `json:"inputDigest"`
}

// Issue normalizes a draft and returns deterministic, credential-free bytes
// for durable input storage. CredentialEnvelopeRef is an opaque alias only;
// raw API keys, tokens, cookies, proxy passwords, and response bodies are not
// fields in this type and therefore cannot enter its payload.
func Issue(draft Draft) (IssuedInput, error) {
	return issue(draft, 0)
}

// IssueVersioned creates the durable form after the store has allocated the
// next monotonic version for this identity.
func IssueVersioned(draft Draft, version int64) (IssuedInput, error) {
	if version < 1 {
		return IssuedInput{}, errors.New("model check input version must be positive")
	}
	return issue(draft, version)
}

func issue(draft Draft, version int64) (IssuedInput, error) {
	input := IssuedInput{
		SchemaVersion:        SchemaVersion,
		InputVersion:         version,
		InputID:              clean(draft.InputID),
		SystemAccountID:      clean(draft.SystemAccountID),
		ActorSystemAccountID: clean(draft.ActorSystemAccountID),
		Target:               normalizeAccount(draft.Target),
		Model:                clean(draft.Model),
		Profile:              clean(draft.Profile),
		Trigger:              draft.Trigger,
		ScheduleID:           clean(draft.ScheduleID),
		TrustedComparison:    draft.TrustedComparison,
		ProbeSetVersion:      clean(draft.ProbeSetVersion),
		Policy:               normalizePolicy(draft.Policy),
		IssuedAt:             draft.IssuedAt.UTC(),
		DeadlineAt:           draft.DeadlineAt.UTC(),
	}
	if draft.Comparison != nil {
		comparison := normalizeAccount(*draft.Comparison)
		input.Comparison = &comparison
	}
	if err := validate(input); err != nil {
		return IssuedInput{}, err
	}
	digest, err := digest(input)
	if err != nil {
		return IssuedInput{}, err
	}
	input.InputDigest = digest
	return input, nil
}

// IdentityKey is a stable, credential-free key used to allocate versions.
// It deliberately excludes actor, revisions and timestamps so retries of the
// same target/model/trigger identity share one sequence.
func (input IssuedInput) IdentityKey() (string, error) {
	identity := struct {
		SystemAccountID string  `json:"systemAccountId"`
		TargetID        string  `json:"targetId"`
		Model           string  `json:"model"`
		Profile         string  `json:"profile"`
		Trigger         Trigger `json:"trigger"`
		ScheduleID      string  `json:"scheduleId,omitempty"`
		ComparisonID    string  `json:"comparisonId,omitempty"`
	}{input.SystemAccountID, input.Target.ID, input.Model, input.Profile, input.Trigger, input.ScheduleID, comparisonID(input.Comparison)}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("marshal model check identity: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (input IssuedInput) Verify() error {
	if err := validate(input); err != nil {
		return err
	}
	digest, err := digest(input)
	if err != nil {
		return err
	}
	if input.InputDigest != digest {
		return errors.New("model check input digest mismatch")
	}
	return nil
}

func (input IssuedInput) Payload() ([]byte, error) {
	if err := input.Verify(); err != nil {
		return nil, err
	}
	return json.Marshal(input)
}

func (input IssuedInput) SameIdentity(other IssuedInput) bool {
	return input.SystemAccountID == other.SystemAccountID &&
		input.Target.ID == other.Target.ID &&
		input.Model == other.Model &&
		input.Profile == other.Profile &&
		input.Trigger == other.Trigger &&
		input.ScheduleID == other.ScheduleID &&
		comparisonID(input.Comparison) == comparisonID(other.Comparison)
}

func validate(input IssuedInput) error {
	if input.SchemaVersion != SchemaVersion {
		return errors.New("model check input schema version is invalid")
	}
	for name, value := range map[string]string{
		"inputId":              input.InputID,
		"systemAccountId":      input.SystemAccountID,
		"actorSystemAccountId": input.ActorSystemAccountID,
		"model":                input.Model,
		"profile":              input.Profile,
		"probeSetVersion":      input.ProbeSetVersion,
		"policyRevision":       input.Policy.Revision,
		"policyDigest":         input.Policy.Digest,
	} {
		if value == "" {
			return fmt.Errorf("model check input %s is required", name)
		}
	}
	if input.Profile != "quick" && input.Profile != "full" {
		return errors.New("model check input profile is invalid")
	}
	if input.Trigger != TriggerManual && input.Trigger != TriggerScheduled && input.Trigger != TriggerQualityRecovery {
		return errors.New("model check input trigger is invalid")
	}
	if input.Trigger == TriggerScheduled && input.ScheduleID == "" {
		return errors.New("scheduled model check input requires scheduleId")
	}
	if input.Trigger != TriggerScheduled && input.ScheduleID != "" {
		return errors.New("non-scheduled model check input must not include scheduleId")
	}
	if input.TrustedComparison != (input.Comparison != nil) {
		return errors.New("model check trusted comparison snapshot is inconsistent")
	}
	if err := validateAccount("target", input.Target); err != nil {
		return err
	}
	if input.Comparison != nil {
		if err := validateAccount("comparison", *input.Comparison); err != nil {
			return err
		}
		if input.Comparison.ID == input.Target.ID {
			return errors.New("model check comparison account must differ from target")
		}
	}
	if input.IssuedAt.IsZero() || input.DeadlineAt.IsZero() || !input.DeadlineAt.After(input.IssuedAt) {
		return errors.New("model check input deadline must follow issue time")
	}
	return nil
}

func validateAccount(name string, account AccountSnapshot) error {
	for field, value := range map[string]string{
		"id":                        account.ID,
		"configRevision":            account.ConfigRevision,
		"providerCode":              account.ProviderCode,
		"protocolProfileId":         account.ProtocolProfileID,
		"protocolProfileRevision":   account.ProtocolProfileRevision,
		"endpointFingerprint":       account.EndpointFingerprint,
		"mappedUpstreamModel":       account.MappedUpstreamModel,
		"credentialEnvelopeRef":     account.CredentialEnvelopeRef,
		"proxyConfigurationVersion": account.ProxyConfigurationVersion,
	} {
		if value == "" {
			return fmt.Errorf("model check %s snapshot %s is required", name, field)
		}
	}
	return nil
}

func digest(input IssuedInput) (string, error) {
	input.InputDigest = ""
	payload, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal model check input digest: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeAccount(account AccountSnapshot) AccountSnapshot {
	return AccountSnapshot{
		ID:                        clean(account.ID),
		ConfigRevision:            clean(account.ConfigRevision),
		ProviderCode:              clean(account.ProviderCode),
		ProtocolProfileID:         clean(account.ProtocolProfileID),
		ProtocolProfileRevision:   clean(account.ProtocolProfileRevision),
		EndpointFingerprint:       clean(account.EndpointFingerprint),
		MappedUpstreamModel:       clean(account.MappedUpstreamModel),
		CredentialEnvelopeRef:     clean(account.CredentialEnvelopeRef),
		ProxyConfigurationVersion: clean(account.ProxyConfigurationVersion),
	}
}

func normalizePolicy(policy PolicySnapshot) PolicySnapshot {
	return PolicySnapshot{Revision: clean(policy.Revision), Digest: clean(policy.Digest)}
}

func comparisonID(snapshot *AccountSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.ID
}

func clean(value string) string {
	return strings.TrimSpace(value)
}
