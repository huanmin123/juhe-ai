export type GptServiceTierOverride = string
export type GptReasoningEffortOverride = string
export type GptRequestOverrideEndpointFamily = 'chat_completions' | 'responses'

export interface GptAccountRequestOverrides {
  serviceTier?: GptServiceTierOverride
  reasoningEffort?: GptReasoningEffortOverride
}

export interface ApplyGptAccountRequestOverridesInput {
  credentials?: Record<string, unknown>
  endpointFamily?: GptRequestOverrideEndpointFamily
  compact?: boolean
  modelCapabilities?: GptRequestOverrideModelCapabilities
}

export interface GptRequestOverrideModelCapabilities {
  supportedServiceTiers: readonly string[]
  supportedReasoningEfforts: readonly string[]
}

export class GptAccountRequestOverrideError extends Error {
  readonly code = 'account_request_override_unsupported'
}

export function readGptAccountRequestOverrides(
  credentials: Record<string, unknown> | undefined
): GptAccountRequestOverrides {
  return {
    serviceTier: optionalCredentialToken(credentials, 'service_tier_override'),
    reasoningEffort: optionalCredentialToken(credentials, 'reasoning_effort_override')
  }
}

export function applyGptAccountRequestOverrides(
  inputBody: Readonly<Record<string, unknown>>,
  input: ApplyGptAccountRequestOverridesInput
): Record<string, unknown> {
  const overrides = readGptAccountRequestOverrides(input.credentials)
  assertGptAccountRequestOverrideValues(overrides)
  const effectiveOverrides = effectiveGptAccountRequestOverrides(overrides, input.modelCapabilities)
  if (!hasApplicableGptAccountRequestOverrides(effectiveOverrides, input.endpointFamily, input.compact === true)) {
    return inputBody as Record<string, unknown>
  }

  const body: Record<string, unknown> = { ...inputBody }
  if (effectiveOverrides.serviceTier === 'default') {
    delete body.service_tier
  } else if (effectiveOverrides.serviceTier) {
    body.service_tier = effectiveOverrides.serviceTier
  }

  if (input.compact || !effectiveOverrides.reasoningEffort) {
    return body
  }
  if (input.endpointFamily === 'responses') {
    const reasoning = isPlainObject(body.reasoning) ? body.reasoning : {}
    body.reasoning = {
      ...reasoning,
      effort: effectiveOverrides.reasoningEffort
    }
    delete body.reasoning_effort
  } else if (input.endpointFamily === 'chat_completions') {
    body.reasoning_effort = effectiveOverrides.reasoningEffort
    delete body.reasoning
  }
  return body
}

export function assertGptAccountRequestOverrideValues(overrides: GptAccountRequestOverrides): void {
  if (overrides.serviceTier && !new Set(['default', 'priority', 'flex']).has(overrides.serviceTier)) {
    throw new GptAccountRequestOverrideError('GPT 账户请求覆盖字段 service_tier_override 无效')
  }
  if (overrides.reasoningEffort && !new Set(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']).has(overrides.reasoningEffort)) {
    throw new GptAccountRequestOverrideError('GPT 账户请求覆盖字段 reasoning_effort_override 无效')
  }
}

export function hasApplicableGptAccountRequestOverrides(
  overrides: GptAccountRequestOverrides,
  endpointFamily: GptRequestOverrideEndpointFamily | undefined,
  compact: boolean
): boolean {
  if (!endpointFamily) return false
  return Boolean(overrides.serviceTier || (!compact && overrides.reasoningEffort))
}

export function effectiveGptAccountRequestOverrides(
  overrides: GptAccountRequestOverrides,
  capabilities: GptRequestOverrideModelCapabilities | undefined
): GptAccountRequestOverrides {
  if (!overrides.serviceTier && !overrides.reasoningEffort) return {}
  if (!capabilities) return {}
  const effective: GptAccountRequestOverrides = {}
  if (overrides.serviceTier) {
    const supported = overrides.serviceTier === 'default'
      ? capabilities.supportedServiceTiers.length > 0
      : capabilities.supportedServiceTiers.includes(overrides.serviceTier)
    if (supported) effective.serviceTier = overrides.serviceTier
  }
  if (overrides.reasoningEffort && capabilities.supportedReasoningEfforts.includes(overrides.reasoningEffort)) {
    effective.reasoningEffort = overrides.reasoningEffort
  }
  return effective
}

function optionalCredentialToken(
  credentials: Record<string, unknown> | undefined,
  key: string
): string | undefined {
  const raw = credentials?.[key]
  if (raw === undefined || raw === null || raw === '') {
    return undefined
  }
  if (typeof raw === 'string' && raw === raw.trim() && /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(raw)) {
    return raw
  }
  throw new GptAccountRequestOverrideError(`账户请求覆盖字段 ${key} 无效`)
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
