package gatewayaccounteffects

// AccountApiKeyPersistentMutationKind mirrors AccountApiKeyPersistentMutationKind.
type AccountApiKeyPersistentMutationKind string

// Mutation kinds.
const (
	MutationKindFailure AccountApiKeyPersistentMutationKind = "failure"
	MutationKindSuccess AccountApiKeyPersistentMutationKind = "success"
	MutationKindDefer   AccountApiKeyPersistentMutationKind = "defer"
)

// AccountApiKeyAutomaticProbeTrafficSource mirrors the Extract<> subset of
// OpenAIGatewayTrafficSource authorized for automatic probes.
type AccountApiKeyAutomaticProbeTrafficSource string

// Automatic probe traffic sources.
const (
	TrafficSourceAccountHealthCheck  AccountApiKeyAutomaticProbeTrafficSource = "account_health_check"
	TrafficSourceRuntimeRecoveryProbe AccountApiKeyAutomaticProbeTrafficSource = "runtime_recovery_probe"
	TrafficSourceCooldownRetest      AccountApiKeyAutomaticProbeTrafficSource = "cooldown_retest"
)

// AccountApiKeyAutomaticProbeOutcome mirrors AccountApiKeyAutomaticProbeOutcome.
type AccountApiKeyAutomaticProbeOutcome string

// Automatic probe outcomes.
const (
	ProbeOutcomeCompleteSuccess      AccountApiKeyAutomaticProbeOutcome = "complete_success"
	ProbeOutcomeFramingCompleteNeutral AccountApiKeyAutomaticProbeOutcome = "framing_complete_neutral"
	ProbeOutcomeUpstreamFailure      AccountApiKeyAutomaticProbeOutcome = "upstream_failure"
	ProbeOutcomeProbeTaskFailure     AccountApiKeyAutomaticProbeOutcome = "probe_task_failure"
)

// QuotaRecoveryMode mirrors the 'generic' | 'explicit_reset' union.
type QuotaRecoveryMode string

// Quota recovery modes.
const (
	QuotaRecoveryModeGeneric       QuotaRecoveryMode = "generic"
	QuotaRecoveryModeExplicitReset QuotaRecoveryMode = "explicit_reset"
)

// AccountApiKeyPersistentMutationContext mirrors
// AccountApiKeyPersistentMutationContext. The union collapses to one struct:
// Authority selects the branch and the remaining fields only matter for
// automatic_probe.
type AccountApiKeyPersistentMutationContext struct {
	Authority         string // explicit_user_policy | system_quota_policy | confirmed_same_account_key_rotation | automatic_probe
	TrafficSource     string
	ProbeOutcome      AccountApiKeyAutomaticProbeOutcome
	QuotaRecoveryMode QuotaRecoveryMode // '' when unset
}

// Mutation authority values.
const (
	MutationAuthorityExplicitUserPolicy            = "explicit_user_policy"
	MutationAuthoritySystemQuotaPolicy             = "system_quota_policy"
	MutationAuthorityConfirmedSameAccountKeyRotation = "confirmed_same_account_key_rotation"
	MutationAuthorityAutomaticProbe                = "automatic_probe"
)

// TrafficSourceGateway mirrors the 'gateway' traffic source.
const TrafficSourceGateway = "gateway"

// AccountApiKeyPersistentMutationAuthorization mirrors the authorization
// union: Allowed with empty Reason, or blocked with one of the reasons.
type AccountApiKeyPersistentMutationAuthorization struct {
	Allowed bool
	Reason  string
}

// Authorization denial reasons.
const (
	AuthReasonMissingAuthority          = "missing_authority"
	AuthReasonInvalidAuthority          = "invalid_authority"
	AuthReasonUnauthorizedTrafficSource = "unauthorized_traffic_source"
	AuthReasonInvalidPolicyMutation     = "invalid_policy_mutation"
	AuthReasonInvalidProbeOutcome       = "invalid_probe_outcome"
)

// AuthorizeAccountApiKeyPersistentMutation mirrors
// authorizeAccountApiKeyPersistentMutation.
func AuthorizeAccountApiKeyPersistentMutation(mutation AccountApiKeyPersistentMutationKind, context *AccountApiKeyPersistentMutationContext) AccountApiKeyPersistentMutationAuthorization {
	if context == nil {
		return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonMissingAuthority}
	}
	switch context.Authority {
	case MutationAuthorityExplicitUserPolicy, MutationAuthoritySystemQuotaPolicy, MutationAuthorityConfirmedSameAccountKeyRotation:
		if context.TrafficSource != TrafficSourceGateway {
			return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonUnauthorizedTrafficSource}
		}
		if mutation == MutationKindFailure {
			return AccountApiKeyPersistentMutationAuthorization{Allowed: true}
		}
		return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonInvalidPolicyMutation}
	case MutationAuthorityAutomaticProbe:
		if !isAutomaticProbeTrafficSource(context.TrafficSource) {
			return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonUnauthorizedTrafficSource}
		}
		if mutation == MutationKindFailure &&
			(context.ProbeOutcome == ProbeOutcomeUpstreamFailure ||
				(context.ProbeOutcome == ProbeOutcomeFramingCompleteNeutral && context.QuotaRecoveryMode != "")) {
			return AccountApiKeyPersistentMutationAuthorization{Allowed: true}
		}
		if mutation == MutationKindSuccess && context.ProbeOutcome == ProbeOutcomeCompleteSuccess {
			return AccountApiKeyPersistentMutationAuthorization{Allowed: true}
		}
		if mutation == MutationKindDefer &&
			(context.ProbeOutcome == ProbeOutcomeFramingCompleteNeutral ||
				context.ProbeOutcome == ProbeOutcomeProbeTaskFailure ||
				(context.ProbeOutcome == ProbeOutcomeUpstreamFailure && context.QuotaRecoveryMode != "")) {
			return AccountApiKeyPersistentMutationAuthorization{Allowed: true}
		}
		return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonInvalidProbeOutcome}
	default:
		return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonInvalidAuthority}
	}
}

// AuthorizeAccountApiKeyPersistentMutationForTrafficSource mirrors
// authorizeAccountApiKeyPersistentMutationForTrafficSource.
func AuthorizeAccountApiKeyPersistentMutationForTrafficSource(mutation AccountApiKeyPersistentMutationKind, trafficSource string, context *AccountApiKeyPersistentMutationContext) AccountApiKeyPersistentMutationAuthorization {
	if context != nil && context.TrafficSource != trafficSource {
		return AccountApiKeyPersistentMutationAuthorization{Allowed: false, Reason: AuthReasonUnauthorizedTrafficSource}
	}
	return AuthorizeAccountApiKeyPersistentMutation(mutation, context)
}

func isAutomaticProbeTrafficSource(value string) bool {
	switch AccountApiKeyAutomaticProbeTrafficSource(value) {
	case TrafficSourceAccountHealthCheck, TrafficSourceRuntimeRecoveryProbe, TrafficSourceCooldownRetest:
		return true
	default:
		return false
	}
}
