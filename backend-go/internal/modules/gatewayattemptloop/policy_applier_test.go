package gatewayattemptloop

import (
	"context"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestNewPolicyMutationFreezesAuthorizedTargetAndSourceWithoutDiagnostics(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	candidate := gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{
		AccountID: "instance-1", SystemAccountID: "consumer-1", GroupID: "group-1",
		AccountAuthorizationID: "authorization-1", AuthorizationID: "authorization-1",
		AuthorizationSourceAccountID: "source-1", AuthorizationOwnerSystemAccountID: "owner-1",
		ResourceAccountID: "source-1", Status: "active",
		ConfigRevision: 7, DispatchRevision: 11, ResourceConfigRevision: 13, ResourceDispatchRevision: 17,
	}}
	decision := PolicyDecision{Action: PolicyActionCooldown, RuleName: "rate rule", CooldownStatus: CooldownRateLimited, CooldownUntil: &until}
	failure := FailureFacts{StatusCode: 429, ErrorCode: "rate_limit", ErrorType: "quota", BodyText: "body-secret", Message: "message-secret"}

	first, err := newPolicyMutation("request-1", "trace-1", 2, candidate, decision, failure, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newPolicyMutation("request-1", "trace-1", 2, candidate, decision, failure, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	changedCandidate := candidate
	changedCandidate.Projection.GroupID = "group-2"
	changed, err := newPolicyMutation("request-1", "trace-1", 2, changedCandidate, decision, failure, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.TransitionID != second.TransitionID || len(first.TransitionID) > 256 {
		t.Fatalf("transitions = %q / %q", first.TransitionID, second.TransitionID)
	}
	if first.TransitionID == changed.TransitionID {
		t.Fatal("transition did not bind the authorized group identity")
	}
	if first.Target.AccountID != "instance-1" || first.Target.ExpectedStatus != "active" || first.Target.ExpectedConfigRevision != 7 || first.Target.ExpectedDispatchRevision != 11 || first.Target.AccountAuthorizationID != "authorization-1" {
		t.Fatalf("target = %+v", first.Target)
	}
	if first.Source != (port.GatewayAccountPolicyRevisionFence{AccountID: "source-1", ExpectedConfigRevision: 13, ExpectedDispatchRevision: 17}) {
		t.Fatalf("source = %+v", first.Source)
	}
	if first.Target.AccountRuntimeKey != "instance-1" {
		t.Fatalf("runtime key = %q", first.Target.AccountRuntimeKey)
	}
	if strings.Contains(first.Reason, "body-secret") || strings.Contains(first.Reason, "message-secret") {
		t.Fatalf("reason leaked raw diagnostics: %q", first.Reason)
	}
}

func TestNewPolicyMutationRejectsInconsistentAuthorizationIdentity(t *testing.T) {
	now := time.Now().UTC()
	until := now.Add(time.Hour)
	candidate := gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{
		AccountID: "instance-1", SystemAccountID: "consumer-1", GroupID: "group-1",
		AccountAuthorizationID: "binding-auth", AuthorizationID: "instance-auth",
		AuthorizationSourceAccountID: "source-1", AuthorizationOwnerSystemAccountID: "owner-1",
		ResourceAccountID: "source-1", Status: "active",
		ConfigRevision: 1, DispatchRevision: 1, ResourceConfigRevision: 1, ResourceDispatchRevision: 1,
	}}
	decision := PolicyDecision{Action: PolicyActionCooldown, RuleName: "rule", CooldownStatus: CooldownRateLimited, CooldownUntil: &until}
	if _, err := newPolicyMutation("request-1", "", 0, candidate, decision, FailureFacts{StatusCode: 429}, now); err == nil {
		t.Fatal("inconsistent authorization error = nil")
	}
}

func TestStorePolicyApplierMapsTypedWriterContract(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	writer := &policyWriterStub{result: port.GatewayAccountPolicyWriteResult{
		Status: port.GatewayAccountPolicyWriteApplied, TransitionID: policyTransitionPrefix + strings.Repeat("a", 64), TargetDispatchRevision: 4, OutboxEventID: "event-1",
	}}
	applier, err := NewStorePolicyApplier(writer)
	if err != nil {
		t.Fatal(err)
	}
	mutation := PolicyMutation{
		TransitionID: policyTransitionPrefix + strings.Repeat("a", 64),
		Target: port.GatewayAccountPolicyTarget{
			GatewayAccountPolicyRevisionFence: port.GatewayAccountPolicyRevisionFence{AccountID: "account-1", ExpectedConfigRevision: 2, ExpectedDispatchRevision: 3},
			SystemAccountID:                   "system-1", GroupID: "group-1", AccountRuntimeKey: "account-1", ExpectedStatus: "active",
		},
		Source:   port.GatewayAccountPolicyRevisionFence{AccountID: "account-1", ExpectedConfigRevision: 2, ExpectedDispatchRevision: 3},
		Decision: PolicyDecision{Action: PolicyActionCooldown, RuleName: "rule", CooldownStatus: CooldownRateLimited, CooldownUntil: &until},
		Reason:   "bounded reason", TraceID: "trace-1", AppliedAt: now,
	}
	result, err := applier.Apply(context.Background(), mutation)
	if err != nil || result.Status != PolicyApplyApplied || len(writer.inputs) != 1 {
		t.Fatalf("result=%+v err=%v inputs=%+v", result, err, writer.inputs)
	}
	input := writer.inputs[0]
	if input.Action != port.GatewayAccountPolicyCooldown || input.CooldownStatus != port.GatewayAccountPolicyRateLimited || input.CooldownUntil == nil || !input.CooldownUntil.Equal(until) || input.Reason != "bounded reason" || input.TraceID != "trace-1" {
		t.Fatalf("writer input = %+v", input)
	}
}

func TestStableInputIDBoundsTraceAtPersistenceLimit(t *testing.T) {
	if _, err := optionalStableInputID(strings.Repeat("x", 200), 200); err != nil {
		t.Fatal(err)
	}
	if _, err := optionalStableInputID(strings.Repeat("x", 201), 200); err == nil {
		t.Fatal("oversized trace ID error = nil")
	}
}

type policyWriterStub struct {
	inputs []port.GatewayAccountPolicyWriteInput
	result port.GatewayAccountPolicyWriteResult
	err    error
}

func (s *policyWriterStub) ApplyGatewayAccountPolicy(_ context.Context, input port.GatewayAccountPolicyWriteInput) (port.GatewayAccountPolicyWriteResult, error) {
	s.inputs = append(s.inputs, input)
	return s.result, s.err
}
