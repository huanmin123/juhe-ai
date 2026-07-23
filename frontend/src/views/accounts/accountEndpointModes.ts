import type { AccountClientCompatibility, AccountHealthCheckEndpointMode, AccountSupportedEndpointMode, AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  isHybridProviderCode,
  normalizeProviderToken
} from '@/shared/providerProtocol'
import {
  type AccountProviderProfileLike,
  allAccountEndpointModes,
  anthropicAccountEndpointModes,
  accountProviderProtocolKind,
  defaultEndpointModesForAccount,
  geminiAccountEndpointModes,
  openAIEndpointModes,
  responsesEndpointModes
} from './accountProviderCapabilities'
import { FALLBACK_PROVIDERS } from './accountOptions'

export type AccountEndpointModeLabelContext = AccountProviderProfileLike | ProviderProtocolProfileDefinition | ProviderDefinition | undefined
export type AccountTestEndpointModeSource = AccountProviderProfileLike & {
  credentials?: Record<string, unknown>
  healthCheckEndpointMode?: AccountHealthCheckEndpointMode
}
export type AccountTestEndpointModeDraftSource = {
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  type?: unknown
  credentials?: Record<string, unknown>
  clientCompatibility?: AccountClientCompatibility
  healthCheckEndpointMode?: AccountHealthCheckEndpointMode
}

export const accountEndpointModeOptions: Array<{ label: string; value: AccountSupportedEndpointMode }> = [
  { label: 'Chat Completions (JSON)', value: 'chat_json' },
  { label: 'Chat Completions (Streaming)', value: 'chat_sse' },
  { label: 'Responses API (JSON)', value: 'responses_json' },
  { label: 'Responses API (Streaming)', value: 'responses_sse' },
  { label: 'Messages API (JSON)', value: 'messages_json' },
  { label: 'Messages API (Streaming)', value: 'messages_sse' },
  { label: 'Count tokens', value: 'message_token_counting' },
  { label: 'generateContent (JSON)', value: 'generate_content_json' },
  { label: 'streamGenerateContent (SSE)', value: 'generate_content_sse' },
  { label: 'countTokens', value: 'count_tokens' },
  { label: 'embedContent', value: 'embed_content' },
  { label: 'Interactions API (JSON)', value: 'interactions_json' },
  { label: 'Interactions API (SSE)', value: 'interactions_sse' }
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
  allowedModes?: AccountSupportedEndpointMode[]
  profile?: AccountProviderProfileLike
}): string | undefined {
  if (!input.modes.length) return '请至少选择一项上游接口能力'
  if (input.allowedModes?.length) {
    const allowedModes = new Set(input.allowedModes)
    const unsupportedModes = input.modes.filter((mode) => !allowedModes.has(mode))
    if (unsupportedModes.length) {
      return `当前供应商协议不支持上游接口能力：${unsupportedModes.map((mode) => accountEndpointModeLabel(mode, input.profile)).join('、')}`
    }
  }
  const hasAnthropicMode = input.modes.some((mode) => anthropicAccountEndpointModes.includes(mode))
  const hasGeminiMode = input.modes.some((mode) => geminiAccountEndpointModes.includes(mode))
  const hasOpenAIMode = input.modes.some((mode) => openAIEndpointModes.includes(mode))
  const protocolModeCount = [hasOpenAIMode, hasAnthropicMode, hasGeminiMode].filter(Boolean).length
  if (protocolModeCount > 1 && !isHybridEndpointModeContext(input.profile)) {
    return '不同协议的上游接口能力不能混选'
  }
  if (hasAnthropicMode && !input.modes.includes('messages_json') && !input.modes.includes('messages_sse')) {
    return `Anthropic 账户上游接口能力至少需要启用 ${accountEndpointModeLabel('messages_json', input.profile)} 或 ${accountEndpointModeLabel('messages_sse', input.profile)}`
  }
  if (hasGeminiMode) {
    if (input.type !== 'api_key' && input.type !== 'google_oauth') return 'Gemini 原生协议当前仅支持 API Key 或 Google OAuth 账户'
    if (!input.modes.includes('generate_content_json') && !input.modes.includes('generate_content_sse')
      && !input.modes.includes('interactions_json') && !input.modes.includes('interactions_sse')) {
      return `Gemini 上游接口能力至少需要启用 ${accountEndpointModeLabel('generate_content_json', input.profile)}、${accountEndpointModeLabel('generate_content_sse', input.profile)} 或 Interactions`
    }
    return undefined
  }
  if (input.type === 'oauth' && hasOpenAIMode) {
    if (input.modes.some((mode) => !responsesEndpointModes.includes(mode))) {
      return `OAuth 账户上游接口能力只能选择 ${accountEndpointModeLabel('responses_json', input.profile)} 或 ${accountEndpointModeLabel('responses_sse', input.profile)}`
    }
    if (!input.modes.includes('responses_sse')) {
      return `OAuth 账户上游接口能力必须启用 ${accountEndpointModeLabel('responses_sse', input.profile)}`
    }
  }
  if (!hasOpenAIMode) return undefined
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
    case 'images_json':
      return 'Images API'
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
    case 'generate_content_json':
      return 'generateContent (JSON)'
    case 'generate_content_sse':
      return 'streamGenerateContent (SSE)'
    case 'count_tokens':
      return 'countTokens'
    case 'embed_content':
      return 'embedContent'
    case 'interactions_json':
      return 'Interactions API (JSON)'
    case 'interactions_sse':
      return 'Interactions API (SSE)'
    default:
      return accountEndpointModeOptions.find((item) => item.value === mode)?.label ?? mode
  }
}

export function accountTestEndpointModesForAccount(
  account?: AccountTestEndpointModeSource,
  draftAccount?: AccountTestEndpointModeDraftSource
): AccountSupportedEndpointMode[] {
  if (!account && !draftAccount) return []
  const source = {
    ...(account ?? {}),
    ...(draftAccount ?? {}),
    type: normalizedAccountType(draftAccount?.type ?? account?.type),
    credentials: draftAccount?.credentials ?? account?.credentials
  }
  const type = normalizedAccountType(source.type)
  const fallback = defaultAccountEndpointModes(
    source.providerCode ?? source.code ?? '',
    type,
    source.clientCompatibility,
    { protocolProfile: source }
  )
  const supportedModes = normalizeAccountEndpointModes(source.credentials?.supported_endpoint_modes, fallback)
  const supportedSet = new Set(supportedModes)
  return prioritizeAccountTestEndpointModes(
    accountTestEndpointModeOrder(source).filter((mode) => supportedSet.has(mode)),
    source.healthCheckEndpointMode
  )
}

export function prioritizeAccountTestEndpointModes(
  modes: readonly AccountSupportedEndpointMode[],
  healthCheckMode?: AccountHealthCheckEndpointMode
): AccountSupportedEndpointMode[] {
  const normalized = [...new Set(modes)]
  if (!healthCheckMode) return normalized
  return normalized.includes(healthCheckMode)
    ? [healthCheckMode, ...normalized.filter((mode) => mode !== healthCheckMode)]
    : normalized
}

export function defaultAccountTestEndpointModeForSelection(
  selection?: AccountTestEndpointModeSource | AccountTestEndpointModeSource[],
  draftAccount?: AccountTestEndpointModeDraftSource
): AccountSupportedEndpointMode | undefined {
  const accounts = Array.isArray(selection) ? selection : selection ? [selection] : []
  if (!accounts.length) return undefined
  const firstModes = accountTestEndpointModesForAccount(accounts[0], draftAccount)
  if (accounts.length === 1) return firstModes[0]
  const shared = accounts.slice(1).reduce((current, account) => {
    const modes = new Set(accountTestEndpointModesForAccount(account))
    return current.filter((mode) => modes.has(mode))
  }, firstModes)
  return shared[0]
}

export function accountTestEndpointModeOrder(account?: AccountProviderProfileLike): AccountSupportedEndpointMode[] {
  if (isHybridProviderCode(contextProviderCode(account))) {
    return [
      'chat_json',
      'chat_sse',
      'responses_json',
      'responses_sse',
      'messages_json',
      'messages_sse',
      'generate_content_json',
      'generate_content_sse'
    ]
  }
  const protocolKind = accountProviderProtocolKind(account)
  if (protocolKind === 'anthropic_v1') return ['messages_json', 'messages_sse']
  if (protocolKind === 'gemini_v1beta') return ['generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse']
  if (account?.type === 'oauth') return ['responses_json', 'responses_sse']
  return ['chat_json', 'responses_json', 'chat_sse', 'responses_sse']
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

function isHybridEndpointModeContext(context?: AccountEndpointModeLabelContext): boolean {
  return isHybridProviderCode(contextProviderCode(context))
}

function contextProfileId(context?: AccountEndpointModeLabelContext): string | undefined {
  if (!context) return undefined
  if ('providerProtocolProfileId' in context) return context.providerProtocolProfileId
  if ('id' in context) return context.id
  return undefined
}

function normalizedAccountType(value: unknown): AccountType {
  return typeof value === 'string' && value.trim() ? value.trim() : 'api_key'
}

function stableEndpointModeKey(value: AccountSupportedEndpointMode[]): string {
  return [...new Set(value)].sort().join('\n')
}
