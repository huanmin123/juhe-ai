import type { CodexProtocolGuardGlobalMode } from '../../../config/runtime.js'
import type { CodexResponsesGuardMode } from './response-guard.js'

export const codexResponsesSafeRepairCredentialKey = 'codex_responses_safe_repair_enabled'
export const codexResponsesStrictInterceptCredentialKey = 'codex_responses_strict_intercept_enabled'

export interface CodexResponsesAccountPolicy {
  safeRepairEnabled: boolean
  strictInterceptEnabled: boolean
}

export function resolveCodexResponsesAccountPolicy(credentials: unknown): CodexResponsesAccountPolicy {
  const value = plainObject(credentials)
  return {
    safeRepairEnabled: booleanOrDefault(value?.[codexResponsesSafeRepairCredentialKey], true),
    strictInterceptEnabled: booleanOrDefault(value?.[codexResponsesStrictInterceptCredentialKey], false)
  }
}

export function resolveCodexResponsesGuardMode(input: {
  globalMode: CodexProtocolGuardGlobalMode
  credentials: unknown
}): CodexResponsesGuardMode | 'off' {
  if (input.globalMode === 'off') return 'off'
  const accountPolicy = resolveCodexResponsesAccountPolicy(input.credentials)
  if (input.globalMode === 'strict_intercept' || accountPolicy.strictInterceptEnabled) {
    return 'strict_intercept'
  }
  if (input.globalMode === 'safe_repair' || accountPolicy.safeRepairEnabled) {
    return 'safe_repair'
  }
  return 'shadow'
}

function booleanOrDefault(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function plainObject(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
