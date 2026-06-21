import type { AccountClientCompatibility, AccountSupportedEndpointMode, AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  type AccountProviderProfileLike,
  allAccountEndpointModes,
  anthropicAccountEndpointModes,
  defaultEndpointModesForAccount,
  profileSupportsCodexResponsesChatBridge,
  responsesEndpointModes
} from './accountProviderCapabilities'
import { FALLBACK_PROVIDERS } from './accountOptions'

export const accountEndpointModeOptions: Array<{ label: string; value: AccountSupportedEndpointMode }> = [
  { label: '对话 JSON', value: 'chat_json' },
  { label: '对话流式', value: 'chat_sse' },
  { label: 'Responses JSON', value: 'responses_json' },
  { label: 'Responses 流式', value: 'responses_sse' },
  { label: 'Messages JSON', value: 'messages_json' },
  { label: 'Messages 流式', value: 'messages_sse' },
  { label: 'Token 计数', value: 'message_token_counting' }
]

export function defaultAccountEndpointModes(
  providerCode: string,
  type: AccountType,
  clientCompatibility?: AccountClientCompatibility,
  context: {
    provider?: ProviderDefinition
    protocolProfile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
  } = {}
): AccountSupportedEndpointMode[] {
  const provider = context.provider ?? FALLBACK_PROVIDERS.find((item) => item.code === providerCode)
  const protocolProfile = context.protocolProfile
    ?? provider?.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
    ?? provider?.protocolProfiles.find((item) => item.enabled)
    ?? provider?.protocolProfiles[0]
  return defaultEndpointModesForAccount({
    provider,
    profile: protocolProfile ?? provider,
    type,
    clientCompatibility
  })
}

export function normalizeAccountEndpointModes(
  value: unknown,
  fallback: AccountSupportedEndpointMode[]
): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) return [...fallback]
  const allowed = new Set<AccountSupportedEndpointMode>(allAccountEndpointModes)
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
  allowedModes?: AccountSupportedEndpointMode[]
  profile?: AccountProviderProfileLike
}): string | undefined {
  if (!input.modes.length) return '请至少选择一项接口能力'
  if (input.allowedModes?.length) {
    const allowedModes = new Set(input.allowedModes)
    const unsupportedModes = input.modes.filter((mode) => !allowedModes.has(mode))
    if (unsupportedModes.length) {
      return `当前供应商协议不支持接口能力：${unsupportedModes.map(accountEndpointModeLabel).join('、')}`
    }
  }
  const hasAnthropicMode = input.modes.some((mode) => anthropicAccountEndpointModes.includes(mode))
  const hasOpenAIMode = input.modes.some((mode) => !anthropicAccountEndpointModes.includes(mode))
  if (hasAnthropicMode && hasOpenAIMode) {
    return 'Anthropic Messages 能力不能与 OpenAI Chat/Responses 能力混选'
  }
  if (hasAnthropicMode && !input.modes.includes('messages_json') && !input.modes.includes('messages_sse')) {
    return 'Anthropic API Key 至少需要启用 Messages JSON 或 Messages SSE'
  }
  if (input.type === 'oauth') {
    if (input.modes.some((mode) => !responsesEndpointModes.includes(mode))) {
      return 'OAuth 账户接口能力只能选择 Responses JSON 或 Responses SSE'
    }
    if (!input.modes.includes('responses_sse')) {
      return 'OAuth 账户必须支持 Responses SSE'
    }
  }
  if (input.clientCompatibility === 'codex_responses' && hasOpenAIMode && profileSupportsCodexResponsesChatBridge(input.profile)) {
    if (!input.modes.includes('chat_sse')) {
      return 'Codex Responses 桥接能力必须启用 Chat SSE'
    }
    return undefined
  }
  if (input.clientCompatibility === 'codex_responses' && hasOpenAIMode && !input.modes.includes('responses_sse')) {
    return 'Codex Responses 兼容能力必须启用 Responses SSE'
  }
  return undefined
}

export function endpointModesEqual(left: AccountSupportedEndpointMode[], right: AccountSupportedEndpointMode[]): boolean {
  return stableEndpointModeKey(left) === stableEndpointModeKey(right)
}

export function accountEndpointModeText(value: unknown): string {
  const modes = normalizeAccountEndpointModes(value, [])
  if (!modes.length) return '-'
  return modes.map(accountEndpointModeLabel).join('、')
}

function accountEndpointModeLabel(mode: AccountSupportedEndpointMode): string {
  return accountEndpointModeOptions.find((item) => item.value === mode)?.label ?? mode
}

function stableEndpointModeKey(value: AccountSupportedEndpointMode[]): string {
  return [...new Set(value)].sort().join('\n')
}
