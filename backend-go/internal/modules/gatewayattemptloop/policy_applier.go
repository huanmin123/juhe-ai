package gatewayattemptloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

const policyTransitionPrefix = "gateway-policy:v1:"

type StorePolicyApplier struct {
	writer port.GatewayAccountPolicyWriter
}

func NewStorePolicyApplier(writer port.GatewayAccountPolicyWriter) (*StorePolicyApplier, error) {
	if writer == nil {
		return nil, fmt.Errorf("gateway account policy writer is required")
	}
	return &StorePolicyApplier{writer: writer}, nil
}

func (a *StorePolicyApplier) Apply(ctx context.Context, mutation PolicyMutation) (PolicyApplyResult, error) {
	if a == nil || a.writer == nil {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy writer is required")
	}
	if ctx == nil {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy context is required")
	}
	if mutation.AppliedAt.IsZero() {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy applied time is required")
	}
	transitionID, err := stableInputID(mutation.TransitionID, 256)
	if err != nil || transitionID != mutation.TransitionID || !validPolicyTransitionID(transitionID) {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy transition ID is invalid")
	}
	traceID, err := optionalStableInputID(mutation.TraceID, 200)
	if err != nil || traceID != mutation.TraceID {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy trace ID is invalid")
	}
	if mutation.Reason == "" || len(mutation.Reason) > 1000 || strings.ToValidUTF8(mutation.Reason, "") != mutation.Reason {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy reason is invalid")
	}
	if err := validatePolicyMutationIdentity(mutation.Target, mutation.Source); err != nil {
		return PolicyApplyResult{}, err
	}
	if mutation.Target.AccountRuntimeKey != policyAccountRuntimeKey(mutation.Target) {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy runtime key is invalid")
	}
	decision, err := normalizePolicyDecision(mutation.Decision, mutation.AppliedAt)
	if err != nil || (decision.Action != PolicyActionCooldown && decision.Action != PolicyActionDisable) {
		return PolicyApplyResult{}, fmt.Errorf("gateway account policy mutation decision is invalid")
	}
	action := port.GatewayAccountPolicyAction(decision.Action)
	status := port.GatewayAccountPolicyCooldownStatus(decision.CooldownStatus)
	var cooldownUntil *time.Time
	if decision.CooldownUntil != nil {
		value := decision.CooldownUntil.UTC()
		cooldownUntil = &value
	}
	return a.writer.ApplyGatewayAccountPolicy(ctx, port.GatewayAccountPolicyWriteInput{
		TransitionID:   mutation.TransitionID,
		Target:         mutation.Target,
		Source:         mutation.Source,
		Action:         action,
		CooldownStatus: status,
		CooldownUntil:  cooldownUntil,
		Reason:         mutation.Reason,
		TraceID:        mutation.TraceID,
		AppliedAt:      mutation.AppliedAt,
	})
}

func newPolicyMutation(
	mutationID string,
	traceID string,
	attemptIndex int,
	candidate gatewaycandidatewindow.Candidate,
	decision PolicyDecision,
	failure FailureFacts,
	appliedAt time.Time,
) (PolicyMutation, error) {
	projection := candidate.Projection
	target := port.GatewayAccountPolicyTarget{
		GatewayAccountPolicyRevisionFence: port.GatewayAccountPolicyRevisionFence{
			AccountID:                projection.AccountID,
			ExpectedConfigRevision:   projection.ConfigRevision,
			ExpectedDispatchRevision: projection.DispatchRevision,
		},
		SystemAccountID:                   projection.SystemAccountID,
		GroupID:                           projection.GroupID,
		AccountAuthorizationID:            projection.AccountAuthorizationID,
		AuthorizationSourceAccountID:      projection.AuthorizationSourceAccountID,
		AuthorizationOwnerSystemAccountID: projection.AuthorizationOwnerSystemAccountID,
		ExpectedStatus:                    projection.Status,
	}
	source := port.GatewayAccountPolicyRevisionFence{
		AccountID:                projection.ResourceAccountID,
		ExpectedConfigRevision:   projection.ResourceConfigRevision,
		ExpectedDispatchRevision: projection.ResourceDispatchRevision,
	}
	if strings.TrimSpace(source.AccountID) == "" {
		source = target.GatewayAccountPolicyRevisionFence
	}
	if source.AccountID != target.AccountID && strings.TrimSpace(projection.AuthorizationID) != target.AccountAuthorizationID {
		return PolicyMutation{}, fmt.Errorf("authorized policy binding identity is inconsistent")
	}
	if err := validatePolicyMutationIdentity(target, source); err != nil {
		return PolicyMutation{}, err
	}
	target.AccountRuntimeKey = policyAccountRuntimeKey(target)
	if len(target.AccountRuntimeKey) > 1024 {
		return PolicyMutation{}, fmt.Errorf("policy account runtime key exceeds limit")
	}
	transitionID := policyTransitionID(mutationID, attemptIndex, target, source, decision)
	if failure.StatusCode < 100 || failure.StatusCode > 599 {
		return PolicyMutation{}, fmt.Errorf("policy failure status code is invalid")
	}
	reason := policyMutationReason(decision, failure)
	return PolicyMutation{
		TransitionID: transitionID,
		Target:       target,
		Source:       source,
		Decision:     decision,
		Reason:       reason,
		TraceID:      traceID,
		AppliedAt:    appliedAt.UTC(),
	}, nil
}

func validatePolicyMutationIdentity(target port.GatewayAccountPolicyTarget, source port.GatewayAccountPolicyRevisionFence) error {
	if strings.TrimSpace(target.AccountID) == "" || strings.TrimSpace(target.SystemAccountID) == "" || strings.TrimSpace(target.GroupID) == "" {
		return fmt.Errorf("policy target identity is incomplete")
	}
	if target.ExpectedStatus != "active" && target.ExpectedStatus != "rate_limited" && target.ExpectedStatus != "temporary_unavailable" {
		return fmt.Errorf("policy target status is invalid")
	}
	if target.ExpectedConfigRevision < 1 || target.ExpectedDispatchRevision < 1 {
		return fmt.Errorf("policy target revisions are invalid")
	}
	if strings.TrimSpace(source.AccountID) == "" || source.ExpectedConfigRevision < 1 || source.ExpectedDispatchRevision < 1 {
		return fmt.Errorf("policy source revisions are invalid")
	}
	if source.AccountID != target.AccountID {
		if strings.TrimSpace(target.AccountAuthorizationID) == "" ||
			strings.TrimSpace(target.AuthorizationSourceAccountID) == "" ||
			strings.TrimSpace(target.AuthorizationOwnerSystemAccountID) == "" ||
			target.AuthorizationSourceAccountID != source.AccountID {
			return fmt.Errorf("authorized policy target identity is incomplete")
		}
	}
	return nil
}

func policyAccountRuntimeKey(target port.GatewayAccountPolicyTarget) string {
	return target.AccountID
}

func policyTransitionID(
	mutationID string,
	attemptIndex int,
	target port.GatewayAccountPolicyTarget,
	source port.GatewayAccountPolicyRevisionFence,
	decision PolicyDecision,
) string {
	parts := []string{
		mutationID,
		strconv.Itoa(attemptIndex),
		target.AccountRuntimeKey,
		target.SystemAccountID,
		target.GroupID,
		target.AccountAuthorizationID,
		target.AuthorizationSourceAccountID,
		target.AuthorizationOwnerSystemAccountID,
		strconv.Itoa(target.ExpectedConfigRevision),
		strconv.FormatInt(target.ExpectedDispatchRevision, 10),
		target.ExpectedStatus,
		source.AccountID,
		strconv.Itoa(source.ExpectedConfigRevision),
		strconv.FormatInt(source.ExpectedDispatchRevision, 10),
		string(decision.Action),
		string(decision.CooldownStatus),
		decision.RuleName,
	}
	var canonical strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&canonical, "%d:%s|", len(part), part)
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return fmt.Sprintf("%s%x", policyTransitionPrefix, digest[:])
}

func validPolicyTransitionID(value string) bool {
	suffix, ok := strings.CutPrefix(value, policyTransitionPrefix)
	if !ok || len(suffix) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func policyMutationReason(decision PolicyDecision, failure FailureFacts) string {
	parts := []string{fmt.Sprintf("账户错误策略「%s」命中", decision.RuleName), fmt.Sprintf("上游调用失败：HTTP %d", failure.StatusCode)}
	if value := boundedText(failure.ErrorCode, 256); value != "" {
		parts = append(parts, "code="+value)
	}
	if value := boundedText(failure.ErrorType, 256); value != "" {
		parts = append(parts, "type="+value)
	}
	return boundedText(strings.Join(parts, "；"), 1000)
}

func validatePolicyApplyResult(result PolicyApplyResult, transitionID string) error {
	switch result.Status {
	case PolicyApplyApplied, PolicyApplyIdempotent:
		if result.TargetDispatchRevision < 1 || strings.TrimSpace(result.OutboxEventID) == "" {
			return fmt.Errorf("applied policy result is incomplete")
		}
	case PolicyApplyStaleTarget, PolicyApplyStaleSource, PolicyApplyIneligible:
		if strings.TrimSpace(result.OutboxEventID) != "" {
			return fmt.Errorf("non-applied policy result cannot contain an outbox event")
		}
	default:
		return fmt.Errorf("unknown policy apply status %q", result.Status)
	}
	if result.TransitionID != transitionID {
		return fmt.Errorf("policy transition ID mismatch")
	}
	return nil
}

func clonePolicyApplyResult(result PolicyApplyResult) *PolicyApplyResult {
	copy := result
	return &copy
}

func stableInputID(value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("is required")
	}
	if len(value) > limit || strings.ToValidUTF8(value, "") != value {
		return "", fmt.Errorf("is invalid")
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", fmt.Errorf("contains a control character")
		}
	}
	return value, nil
}

func optionalStableInputID(value string, limit int) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return stableInputID(value, limit)
}
