import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'

export type AccountApiKeyPersistentMutationKind = 'failure' | 'success' | 'defer'

export type AccountApiKeyAutomaticProbeTrafficSource = Extract<
  OpenAIGatewayTrafficSource,
  'account_health_check' | 'runtime_recovery_probe' | 'cooldown_retest'
>

export type AccountApiKeyAutomaticProbeOutcome =
  | 'complete_success'
  | 'framing_complete_neutral'
  | 'upstream_failure'
  | 'probe_task_failure'

export type AccountApiKeyPersistentMutationContext =
  | {
      authority: 'explicit_user_policy'
      trafficSource: 'gateway'
    }
  | {
      authority: 'system_quota_policy'
      trafficSource: 'gateway'
    }
  | {
      authority: 'confirmed_same_account_key_rotation'
      trafficSource: 'gateway'
    }
  | {
      authority: 'automatic_probe'
      trafficSource: AccountApiKeyAutomaticProbeTrafficSource
      probeOutcome: AccountApiKeyAutomaticProbeOutcome
      quotaRecoveryMode?: 'generic' | 'explicit_reset'
    }

export type AccountApiKeyPersistentMutationAuthorization =
  | { allowed: true }
  | {
      allowed: false
      reason:
        | 'missing_authority'
        | 'invalid_authority'
        | 'unauthorized_traffic_source'
        | 'invalid_policy_mutation'
        | 'invalid_probe_outcome'
    }

export function authorizeAccountApiKeyPersistentMutation(
  mutation: AccountApiKeyPersistentMutationKind,
  context: AccountApiKeyPersistentMutationContext | undefined
): AccountApiKeyPersistentMutationAuthorization {
  if (!context) {
    return { allowed: false, reason: 'missing_authority' }
  }
  if (context.authority === 'explicit_user_policy') {
    if (context.trafficSource !== 'gateway') {
      return { allowed: false, reason: 'unauthorized_traffic_source' }
    }
    return mutation === 'failure'
      ? { allowed: true }
      : { allowed: false, reason: 'invalid_policy_mutation' }
  }
  if (context.authority === 'system_quota_policy') {
    if (context.trafficSource !== 'gateway') {
      return { allowed: false, reason: 'unauthorized_traffic_source' }
    }
    return mutation === 'failure'
      ? { allowed: true }
      : { allowed: false, reason: 'invalid_policy_mutation' }
  }
  if (context.authority === 'confirmed_same_account_key_rotation') {
    if (context.trafficSource !== 'gateway') {
      return { allowed: false, reason: 'unauthorized_traffic_source' }
    }
    return mutation === 'failure'
      ? { allowed: true }
      : { allowed: false, reason: 'invalid_policy_mutation' }
  }
  if (context.authority !== 'automatic_probe') {
    return { allowed: false, reason: 'invalid_authority' }
  }
  if (!isAutomaticProbeTrafficSource(context.trafficSource)) {
    return { allowed: false, reason: 'unauthorized_traffic_source' }
  }
  if (
    mutation === 'failure'
    && (context.probeOutcome === 'upstream_failure'
      || (context.probeOutcome === 'framing_complete_neutral' && context.quotaRecoveryMode !== undefined))
  ) {
    return { allowed: true }
  }
  if (mutation === 'success' && context.probeOutcome === 'complete_success') {
    return { allowed: true }
  }
  if (
    mutation === 'defer'
    && (
      context.probeOutcome === 'framing_complete_neutral'
      || context.probeOutcome === 'probe_task_failure'
      || (context.probeOutcome === 'upstream_failure' && context.quotaRecoveryMode !== undefined)
    )
  ) {
    return { allowed: true }
  }
  return { allowed: false, reason: 'invalid_probe_outcome' }
}

export function authorizeAccountApiKeyPersistentMutationForTrafficSource(
  mutation: AccountApiKeyPersistentMutationKind,
  trafficSource: OpenAIGatewayTrafficSource | undefined,
  context: AccountApiKeyPersistentMutationContext | undefined
): AccountApiKeyPersistentMutationAuthorization {
  if (context && context.trafficSource !== trafficSource) {
    return { allowed: false, reason: 'unauthorized_traffic_source' }
  }
  return authorizeAccountApiKeyPersistentMutation(mutation, context)
}

function isAutomaticProbeTrafficSource(value: unknown): value is AccountApiKeyAutomaticProbeTrafficSource {
  return value === 'account_health_check'
    || value === 'runtime_recovery_probe'
    || value === 'cooldown_retest'
}
