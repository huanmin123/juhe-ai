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
  if (!hasApplicableGptAccountRequestOverrides(overrides, input.endpointFamily, input.compact === true)) {
    return inputBody as Record<string, unknown>
  }

  const body: Record<string, unknown> = { ...inputBody }
  if (overrides.serviceTier === 'default') {
    delete body.service_tier
  } else if (overrides.serviceTier) {
    body.service_tier = overrides.serviceTier
  }

  if (input.compact || !overrides.reasoningEffort) {
    return body
  }
  if (input.endpointFamily === 'responses') {
    const reasoning = isPlainObject(body.reasoning) ? body.reasoning : {}
    body.reasoning = {
      ...reasoning,
      effort: overrides.reasoningEffort
    }
    delete body.reasoning_effort
  } else if (input.endpointFamily === 'chat_completions') {
    body.reasoning_effort = overrides.reasoningEffort
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
