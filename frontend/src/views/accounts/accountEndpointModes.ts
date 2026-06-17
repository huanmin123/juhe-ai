import {
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  normalizeProviderToken
} from '@/shared/providerProtocol'
import type { AccountClientCompatibility, AccountSupportedEndpointMode, AccountType } from '@/types/domain'

export const accountEndpointModeOptions: Array<{ label: string; value: AccountSupportedEndpointMode }> = [
  { label: 'Chat JSON', value: 'chat_json' },
  { label: 'Chat SSE', value: 'chat_sse' },
  { label: 'Responses JSON', value: 'responses_json' },
  { label: 'Responses SSE', value: 'responses_sse' }
]

const allEndpointModes = accountEndpointModeOptions.map((item) => item.value)
const chatEndpointModes: AccountSupportedEndpointMode[] = ['chat_json', 'chat_sse']
const responsesEndpointModes: AccountSupportedEndpointMode[] = ['responses_json', 'responses_sse']

export function defaultAccountEndpointModes(
  providerCode: string,
  type: AccountType,
  clientCompatibility?: AccountClientCompatibility
): AccountSupportedEndpointMode[] {
  if (type === 'oauth') return [...responsesEndpointModes]
  const provider = normalizeProviderToken(providerCode)
  if (provider === GPT_VENDOR_CODE || clientCompatibility === 'codex_responses') return [...allEndpointModes]
  if (provider === OPENAI_COMPATIBLE_PROVIDER_CODE) return [...chatEndpointModes]
  return [...allEndpointModes]
}

export function normalizeAccountEndpointModes(
  value: unknown,
  fallback: AccountSupportedEndpointMode[]
): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) return [...fallback]
  const allowed = new Set<AccountSupportedEndpointMode>(allEndpointModes)
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!allowed.has(item) || seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  return output.length ? output : [...fallback]
}

export function validateAccountEndpointModes(input: {
  modes: AccountSupportedEndpointMode[]
  type: AccountType
  clientCompatibility: AccountClientCompatibility
}): string | undefined {
  if (!input.modes.length) return '请至少选择一项接口能力'
  if (input.type === 'oauth') {
    if (input.modes.some((mode) => !responsesEndpointModes.includes(mode))) {
      return 'OAuth 账户接口能力只能选择 Responses JSON 或 Responses SSE'
    }
    if (!input.modes.includes('responses_sse')) {
      return 'OAuth 账户必须支持 Responses SSE'
    }
  }
  if (input.clientCompatibility === 'codex_responses' && !input.modes.includes('responses_sse')) {
    return 'Codex Responses 兼容模式必须启用 Responses SSE'
  }
  return undefined
}

export function endpointModesEqual(left: AccountSupportedEndpointMode[], right: AccountSupportedEndpointMode[]): boolean {
  return stableEndpointModeKey(left) === stableEndpointModeKey(right)
}

export function accountEndpointModeText(value: unknown): string {
  const modes = normalizeAccountEndpointModes(value, [])
  if (!modes.length) return '-'
  const labels = new Map(accountEndpointModeOptions.map((item) => [item.value, item.label]))
  return modes.map((mode) => labels.get(mode) ?? mode).join('、')
}

function stableEndpointModeKey(value: AccountSupportedEndpointMode[]): string {
  return [...new Set(value)].sort().join('\n')
}
