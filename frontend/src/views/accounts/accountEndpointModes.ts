import type { AccountClientCompatibility, AccountSupportedEndpointMode, AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  normalizeProviderToken
} from '@/shared/providerProtocol'
import {
  type AccountProviderProfileLike,
  allAccountEndpointModes,
  anthropicAccountEndpointModes,
  defaultEndpointModesForAccount,
  profileSupportsCodexResponsesChatBridge,
  responsesEndpointModes
} from './accountProviderCapabilities'
import { FALLBACK_PROVIDERS } from './accountOptions'

export type AccountEndpointModeLabelContext = AccountProviderProfileLike | ProviderProtocolProfileDefinition | ProviderDefinition | undefined

export const accountEndpointModeOptions: Array<{ label: string; value: AccountSupportedEndpointMode }> = [
  { label: 'Chat Completions (JSON)', value: 'chat_json' },
  { label: 'Chat Completions (Streaming)', value: 'chat_sse' },
  { label: 'Responses API (JSON)', value: 'responses_json' },
  { label: 'Responses API (Streaming)', value: 'responses_sse' },
  { label: 'Messages API (JSON)', value: 'messages_json' },
  { label: 'Messages API (Streaming)', value: 'messages_sse' },
  { label: 'Count tokens', value: 'message_token_counting' }
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
      return `当前供应商协议不支持接口能力：${unsupportedModes.map((mode) => accountEndpointModeLabel(mode, input.profile)).join('、')}`
    }
  }
  const hasAnthropicMode = input.modes.some((mode) => anthropicAccountEndpointModes.includes(mode))
  const hasOpenAIMode = input.modes.some((mode) => !anthropicAccountEndpointModes.includes(mode))
  if (hasAnthropicMode && hasOpenAIMode) {
    return 'Anthropic Messages 能力不能与 OpenAI Chat/Responses 能力混选'
  }
  if (hasAnthropicMode && !input.modes.includes('messages_json') && !input.modes.includes('messages_sse')) {
    return `Anthropic API Key 至少需要启用 ${accountEndpointModeLabel('messages_json', input.profile)} 或 ${accountEndpointModeLabel('messages_sse', input.profile)}`
  }
  if (input.type === 'oauth') {
    if (input.modes.some((mode) => !responsesEndpointModes.includes(mode))) {
      return `OAuth 账户接口能力只能选择 ${accountEndpointModeLabel('responses_json', input.profile)} 或 ${accountEndpointModeLabel('responses_sse', input.profile)}`
    }
    if (!input.modes.includes('responses_sse')) {
      return `OAuth 账户必须支持 ${accountEndpointModeLabel('responses_sse', input.profile)}`
    }
  }
  if (input.clientCompatibility === 'codex_responses' && hasOpenAIMode && profileSupportsCodexResponsesChatBridge(input.profile)) {
    if (!input.modes.includes('chat_sse')) {
      return `Codex Responses 桥接能力必须启用 ${accountEndpointModeLabel('chat_sse', input.profile)}`
    }
    return undefined
  }
  if (input.clientCompatibility === 'codex_responses' && hasOpenAIMode && !input.modes.includes('responses_sse')) {
    return `Codex Responses 兼容能力必须启用 ${accountEndpointModeLabel('responses_sse', input.profile)}`
  }
  return undefined
}

export function endpointModesEqual(left: AccountSupportedEndpointMode[], right: AccountSupportedEndpointMode[]): boolean {
  return stableEndpointModeKey(left) === stableEndpointModeKey(right)
}

export function accountEndpointModeText(value: unknown, context?: AccountEndpointModeLabelContext): string {
  const modes = normalizeAccountEndpointModes(value, [])
  if (!modes.length) return '-'
  return modes.map((mode) => accountEndpointModeLabel(mode, context)).join('、')
}

export function accountEndpointModeOptionsForProfile(context?: AccountEndpointModeLabelContext): Array<{ label: string; value: AccountSupportedEndpointMode }> {
  return accountEndpointModeOptions.map((option) => ({
    ...option,
    label: accountEndpointModeLabel(option.value, context)
  }))
}

export function accountEndpointModeLabel(mode: AccountSupportedEndpointMode, context?: AccountEndpointModeLabelContext): string {
  switch (mode) {
    case 'chat_json':
      return `${chatCapabilityName(context)} (JSON)`
    case 'chat_sse':
      return `${chatCapabilityName(context)} (Streaming)`
    case 'messages_json':
      return 'Messages API (JSON)'
    case 'messages_sse':
      return 'Messages API (Streaming)'
    case 'message_token_counting':
      return 'Count tokens'
    default:
      return accountEndpointModeOptions.find((item) => item.value === mode)?.label ?? mode
  }
}

function chatCapabilityName(context?: AccountEndpointModeLabelContext): string {
  const providerCode = normalizeProviderToken(contextProviderCode(context))
  const profileId = contextProfileId(context)
  if (providerCode === DEEPSEEK_PROVIDER_CODE || profileId === DEEPSEEK_OPENAI_V1_PROFILE_ID) {
    return 'Chat Completion'
  }
  if (providerCode === GLM_PROVIDER_CODE || profileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID || profileId === GLM_CODING_OPENAI_V1_PROFILE_ID) {
    return profileId === GLM_CODING_OPENAI_V1_PROFILE_ID ? 'OpenAI Chat Completions' : '对话补全'
  }
  return 'Chat Completions'
}

function contextProviderCode(context?: AccountEndpointModeLabelContext): string | undefined {
  if (!context) return undefined
  if ('providerCode' in context) return context.providerCode
  if ('code' in context) return context.code
  return undefined
}

function contextProfileId(context?: AccountEndpointModeLabelContext): string | undefined {
  if (!context) return undefined
  if ('providerProtocolProfileId' in context) return context.providerProtocolProfileId
  if ('id' in context) return context.id
  return undefined
}

function stableEndpointModeKey(value: AccountSupportedEndpointMode[]): string {
  return [...new Set(value)].sort().join('\n')
}
