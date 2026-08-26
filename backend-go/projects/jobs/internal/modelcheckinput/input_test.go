package modelcheckinput

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestIssueNormalizesAndDigestsImmutableInput(t *testing.T) {
	issuedAt := time.Date(2026, 8, 26, 8, 0, 0, 123456789, time.FixedZone("local", 8*60*60))
	draft := validDraft(issuedAt)
	draft.InputID = "  input-1  "
	draft.Model = "  gpt-5.6-sol  "
	first, err := Issue(draft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Issue(validDraft(issuedAt))
	if err != nil {
		t.Fatal(err)
	}
	if first.InputID != "input-1" || first.Model != "gpt-5.6-sol" || first.IssuedAt.Location() != time.UTC || first.InputDigest != second.InputDigest {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	payload, err := first.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("raw-api-key")) || !bytes.Contains(payload, []byte(`"credentialEnvelopeRef":"credential-alias-1"`)) {
		t.Fatalf("unexpected credential payload: %s", payload)
	}
}

func TestVerifyRejectsMutationAndIdentitySeparatesComparison(t *testing.T) {
	issued, err := Issue(validDraft(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	mutated := issued
	mutated.Target.ConfigRevision = "config-revision-2"
	if err := mutated.Verify(); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mutation verify error=%v", err)
	}
	other, err := Issue(validDraft(issued.IssuedAt))
	if err != nil || !issued.SameIdentity(other) {
		t.Fatalf("same identity other=%#v err=%v", other, err)
	}
	comparison := other.Target
	comparison.ID = "comparison-account"
	other.Comparison = &comparison
	other.TrustedComparison = true
	other.InputDigest = ""
	refreshed, err := Issue(Draft{InputID: other.InputID, SystemAccountID: other.SystemAccountID, ActorSystemAccountID: other.ActorSystemAccountID, Target: other.Target, Comparison: other.Comparison, Model: other.Model, Profile: other.Profile, Trigger: other.Trigger, TrustedComparison: true, ProbeSetVersion: other.ProbeSetVersion, Policy: other.Policy, IssuedAt: other.IssuedAt, DeadlineAt: other.DeadlineAt})
	if err != nil || issued.SameIdentity(refreshed) {
		t.Fatalf("comparison identity refreshed=%#v err=%v", refreshed, err)
	}
}

func TestIssueRejectsInvalidScheduleDeadlineAndComparison(t *testing.T) {
	base := validDraft(time.Now().UTC())
	base.Trigger = TriggerScheduled
	if _, err := Issue(base); err == nil || !strings.Contains(err.Error(), "scheduleId") {
		t.Fatalf("schedule error=%v", err)
	}
	base = validDraft(time.Now().UTC())
	base.DeadlineAt = base.IssuedAt
	if _, err := Issue(base); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("deadline error=%v", err)
	}
	base = validDraft(time.Now().UTC())
	comparison := base.Target
	base.Comparison = &comparison
	base.TrustedComparison = true
	if _, err := Issue(base); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("comparison error=%v", err)
	}
}

func validDraft(issuedAt time.Time) Draft {
	return Draft{
		InputID:              "input-1",
		SystemAccountID:      "system-account",
		ActorSystemAccountID: "actor-account",
		Target: AccountSnapshot{
			ID:                        "target-account",
			ConfigRevision:            "config-revision-1",
			ProviderCode:              "openai",
			ProtocolProfileID:         "profile-openai-responses",
			ProtocolProfileRevision:   "profile-revision-1",
			EndpointFingerprint:       "endpoint-hmac-1",
			MappedUpstreamModel:       "gpt-5.6-sol",
			CredentialEnvelopeRef:     "credential-alias-1",
			ProxyConfigurationVersion: "proxy-revision-1",
		},
		Model:           "gpt-5.6-sol",
		Profile:         "quick",
		Trigger:         TriggerManual,
		ProbeSetVersion: "probe-v1",
		Policy:          PolicySnapshot{Revision: "policy-revision-1", Digest: "policy-digest-1"},
		IssuedAt:        issuedAt,
		DeadlineAt:      issuedAt.Add(time.Minute),
	}
}
