package gatewayaccounteffects

import (
	"testing"
)

func TestAuthorizeAccountApiKeyPersistentMutation(t *testing.T) {
	gateway := func(authority string) *AccountApiKeyPersistentMutationContext {
		return &AccountApiKeyPersistentMutationContext{Authority: authority, TrafficSource: TrafficSourceGateway}
	}
	tests := []struct {
		name        string
		mutation    AccountApiKeyPersistentMutationKind
		context     *AccountApiKeyPersistentMutationContext
		wantAllowed bool
		wantReason  string
	}{
		{name: "缺少授权上下文", mutation: MutationKindFailure, context: nil, wantAllowed: false, wantReason: AuthReasonMissingAuthority},
		{name: "显式用户策略允许失败", mutation: MutationKindFailure, context: gateway(MutationAuthorityExplicitUserPolicy), wantAllowed: true},
		{name: "显式用户策略禁止成功", mutation: MutationKindSuccess, context: gateway(MutationAuthorityExplicitUserPolicy), wantAllowed: false, wantReason: AuthReasonInvalidPolicyMutation},
		{name: "系统配额策略允许失败", mutation: MutationKindFailure, context: gateway(MutationAuthoritySystemQuotaPolicy), wantAllowed: true},
		{name: "确认轮换策略允许失败", mutation: MutationKindFailure, context: gateway(MutationAuthorityConfirmedSameAccountKeyRotation), wantAllowed: true},
		{name: "策略授权但流量来源不符", mutation: MutationKindFailure,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityExplicitUserPolicy, TrafficSource: "account_health_check"},
			wantAllowed: false, wantReason: AuthReasonUnauthorizedTrafficSource},
		{name: "未知授权", mutation: MutationKindFailure, context: gateway("unknown"), wantAllowed: false, wantReason: AuthReasonInvalidAuthority},
		{name: "探针 upstream_failure 允许失败", mutation: MutationKindFailure,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceRuntimeRecoveryProbe), ProbeOutcome: ProbeOutcomeUpstreamFailure},
			wantAllowed: true},
		{name: "探针 framing neutral 无配额模式禁止失败", mutation: MutationKindFailure,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceAccountHealthCheck), ProbeOutcome: ProbeOutcomeFramingCompleteNeutral},
			wantAllowed: false, wantReason: AuthReasonInvalidProbeOutcome},
		{name: "探针 framing neutral 带配额模式允许失败", mutation: MutationKindFailure,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceCooldownRetest), ProbeOutcome: ProbeOutcomeFramingCompleteNeutral, QuotaRecoveryMode: QuotaRecoveryModeGeneric},
			wantAllowed: true},
		{name: "探针 complete_success 允许成功", mutation: MutationKindSuccess,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceAccountHealthCheck), ProbeOutcome: ProbeOutcomeCompleteSuccess},
			wantAllowed: true},
		{name: "探针 task failure 允许 defer", mutation: MutationKindDefer,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceAccountHealthCheck), ProbeOutcome: ProbeOutcomeProbeTaskFailure},
			wantAllowed: true},
		{name: "探针 upstream_failure 带配额模式允许 defer", mutation: MutationKindDefer,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceAccountHealthCheck), ProbeOutcome: ProbeOutcomeUpstreamFailure, QuotaRecoveryMode: QuotaRecoveryModeExplicitReset},
			wantAllowed: true},
		{name: "探针流量来源不符", mutation: MutationKindFailure,
			context:     &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: TrafficSourceGateway, ProbeOutcome: ProbeOutcomeUpstreamFailure},
			wantAllowed: false, wantReason: AuthReasonUnauthorizedTrafficSource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := AuthorizeAccountApiKeyPersistentMutation(tt.mutation, tt.context)
			if decision.Allowed != tt.wantAllowed || decision.Reason != tt.wantReason {
				t.Fatalf("decision = %+v, want allowed=%v reason=%q", decision, tt.wantAllowed, tt.wantReason)
			}
		})
	}
}

func TestAuthorizeForTrafficSourceRejectsContextMismatch(t *testing.T) {
	context := &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: string(TrafficSourceRuntimeRecoveryProbe), ProbeOutcome: ProbeOutcomeUpstreamFailure}
	decision := AuthorizeAccountApiKeyPersistentMutationForTrafficSource(MutationKindFailure, TrafficSourceGateway, context)
	if decision.Allowed || decision.Reason != AuthReasonUnauthorizedTrafficSource {
		t.Fatalf("mismatched traffic source must be rejected: %+v", decision)
	}
	same := AuthorizeAccountApiKeyPersistentMutationForTrafficSource(MutationKindFailure, string(TrafficSourceRuntimeRecoveryProbe), context)
	if !same.Allowed {
		t.Fatalf("matching traffic source must pass: %+v", same)
	}
	withoutContext := AuthorizeAccountApiKeyPersistentMutationForTrafficSource(MutationKindFailure, TrafficSourceGateway, nil)
	if withoutContext.Allowed || withoutContext.Reason != AuthReasonMissingAuthority {
		t.Fatalf("nil context must be missing_authority: %+v", withoutContext)
	}
}
