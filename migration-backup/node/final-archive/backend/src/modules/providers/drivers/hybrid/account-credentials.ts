import { ANTHROPIC_ENDPOINT_MODE_VALUES } from '../../../../domain/anthropic-endpoint-modes.js'
import { GEMINI_ENDPOINT_MODE_VALUES } from '../../../../domain/gemini-endpoint-modes.js'
import { OPENAI_ENDPOINT_MODE_VALUES } from '../../../../domain/openai-endpoint-modes.js'
import type { AccountSupportedEndpointMode } from '../../../../domain/types.js'
import { HYBRID_PROVIDER_CODE, isHybridProviderCode } from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

const HYBRID_ENDPOINT_MODE_VALUES: readonly AccountSupportedEndpointMode[] = [
  ...OPENAI_ENDPOINT_MODE_VALUES,
  ...ANTHROPIC_ENDPOINT_MODE_VALUES,
  ...GEMINI_ENDPOINT_MODE_VALUES
] as const

const hybridEndpointModeSet = new Set<string>(HYBRID_ENDPOINT_MODE_VALUES)

export const hybridAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'hybrid',
  providerCode: HYBRID_PROVIDER_CODE,
  supportsContext(context) {
    return isHybridProviderCode(context.providerCode)
  },
  normalizeEndpointModesForWrite(value, context) {
    void context
    return normalizeHybridEndpointModesForWrite(value)
  }
}

function normalizeHybridEndpointModesForWrite(value: unknown, label = '上游接口能力'): AccountSupportedEndpointMode[] {
  if (value === undefined) {
    return [...HYBRID_ENDPOINT_MODE_VALUES]
  }
  if (!Array.isArray(value)) {
    throw new Error(`${label}必须是数组`)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isHybridEndpointMode(item)) {
      throw new Error(`${label}包含不支持的能力：${String(item)}`)
    }
    if (seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  if (!output.length) {
    throw new Error(`${label}至少选择一项`)
  }
  return output
}

export function normalizeHybridEndpointModesForRuntime(value: unknown): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) {
    return [...HYBRID_ENDPOINT_MODE_VALUES]
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isHybridEndpointMode(item) || seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  return output.length ? output : [...HYBRID_ENDPOINT_MODE_VALUES]
}

function isHybridEndpointMode(value: unknown): value is AccountSupportedEndpointMode {
  return typeof value === 'string' && hybridEndpointModeSet.has(value)
}
