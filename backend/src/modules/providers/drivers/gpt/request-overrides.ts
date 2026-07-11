import type {
  GptServiceTier,
  GptWireReasoningEffort
} from '../../../model-pricing/provider-driver.types.js'

export type GptServiceTierOverride = 'default' | GptServiceTier
export type GptReasoningEffortOverride = GptWireReasoningEffort
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
  supportedServiceTiers: readonly GptServiceTier[]
  supportedReasoningEfforts: readonly GptWireReasoningEffort[]
}

export class GptAccountRequestOverrideError extends Error {
  readonly code = 'account_request_override_unsupported'
}

export function readGptAccountRequestOverrides(
  credentials: Record<string, unknown> | undefined
): GptAccountRequestOverrides {
  return {
    serviceTier: optionalCredentialEnum(
      credentials,
      'service_tier_override',
      gptServiceTierOverrides
    ),
    reasoningEffort: optionalCredentialEnum(
      credentials,
      'reasoning_effort_override',
      gptReasoningEffortOverrides
    )
  }
}

export function applyGptAccountRequestOverrides(
  inputBody: Readonly<Record<string, unknown>>,
  input: ApplyGptAccountRequestOverridesInput
): Record<string, unknown> {
  const overrides = readGptAccountRequestOverrides(input.credentials)
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
  if (!capabilities) return {}
  return {
    serviceTier: downgradedServiceTier(overrides.serviceTier, capabilities.supportedServiceTiers),
    reasoningEffort: downgradedReasoningEffort(overrides.reasoningEffort, capabilities.supportedReasoningEfforts)
  }
}

function optionalCredentialEnum<TValue extends string>(
  credentials: Record<string, unknown> | undefined,
  key: string,
  allowedValues: ReadonlySet<TValue>
): TValue | undefined {
  const raw = credentials?.[key]
  if (raw === undefined || raw === null || raw === '') {
    return undefined
  }
  if (typeof raw === 'string' && allowedValues.has(raw as TValue)) {
    return raw as TValue
  }
  throw new GptAccountRequestOverrideError(`GPT 账户请求覆盖字段 ${key} 无效`)
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

const gptServiceTierOverrides = new Set<GptServiceTierOverride>([
  'default',
  'priority',
  'flex'
])

const gptReasoningEffortOverrides = new Set<GptReasoningEffortOverride>([
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max'
])

const gptServiceTierOrder: readonly GptServiceTierOverride[] = ['flex', 'default', 'priority']
const gptReasoningEffortOrder: readonly GptReasoningEffortOverride[] = [
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max'
]

function downgradedServiceTier(
  configured: GptServiceTierOverride | undefined,
  supported: readonly GptServiceTier[]
): GptServiceTierOverride | undefined {
  if (!configured) return undefined
  return highestSupportedAtOrBelow(configured, ['default', ...supported], gptServiceTierOrder)
}

function downgradedReasoningEffort(
  configured: GptReasoningEffortOverride | undefined,
  supported: readonly GptWireReasoningEffort[]
): GptReasoningEffortOverride | undefined {
  if (!configured) return undefined
  return highestSupportedAtOrBelow(configured, supported, gptReasoningEffortOrder)
}

function highestSupportedAtOrBelow<TValue extends string>(
  configured: TValue,
  supported: readonly TValue[],
  order: readonly TValue[]
): TValue | undefined {
  const configuredIndex = order.indexOf(configured)
  if (configuredIndex < 0) return undefined
  const supportedSet = new Set(supported)
  for (let index = configuredIndex; index >= 0; index -= 1) {
    const candidate = order[index]
    if (candidate && supportedSet.has(candidate)) return candidate
  }
  return undefined
}
