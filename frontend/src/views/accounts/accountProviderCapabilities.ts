import type {
  AccountClientCompatibility,
  AccountSupportedEndpointMode,
  AccountType,
  ProviderDefinition,
  ProviderProtocolProfileDefinition
} from '@/types/domain'
import {
  ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
  ANTHROPIC_MESSAGES_FAMILY,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_RESPONSES_FAMILY,
  isGptVendorCode,
  isXaiProviderCode,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'

export type AccountProviderProtocolKind = 'openai_v1' | 'anthropic_v1' | 'gemini_v1beta' | 'unknown'
export type ClientCompatibilityCapability = 'openai_standard' | 'codex_responses' | 'anthropic_native' | 'claude_code'

export type AccountProviderProfileLike = {
  code?: string
  providerCode?: string
  id?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  accountTypes?: AccountType[]
  capabilities?: string[]
  endpointFamilies?: Array<{ code?: string } | string>
  type?: AccountType
  clientCompatibility?: AccountClientCompatibility
}

export const clientCompatibilityCapabilityOptions: Array<{ label: string; value: ClientCompatibilityCapability }> = [
  { label: 'OpenAI-compatible', value: 'openai_standard' },
  { label: 'Codex Responses', value: 'codex_responses' },
  { label: 'Anthropic API', value: 'anthropic_native' },
  { label: 'Claude Code', value: 'claude_code' }
]

export const chatEndpointModes: AccountSupportedEndpointMode[] = ['chat_json', 'chat_sse']
export const responsesEndpointModes: AccountSupportedEndpointMode[] = ['responses_json', 'responses_sse']
export const openAIEndpointModes: AccountSupportedEndpointMode[] = [...chatEndpointModes, ...responsesEndpointModes]
export const anthropicAccountEndpointModes: AccountSupportedEndpointMode[] = ['messages_json', 'messages_sse', 'message_token_counting']
export const geminiAccountEndpointModes: AccountSupportedEndpointMode[] = ['generate_content_json', 'generate_content_sse', 'count_tokens', 'embed_content', 'interactions_json', 'interactions_sse']
export const allAccountEndpointModes: AccountSupportedEndpointMode[] = [
  ...openAIEndpointModes,
  ...anthropicAccountEndpointModes,
  ...geminiAccountEndpointModes
]

export function accountProviderProtocolKind(profile?: AccountProviderProfileLike): AccountProviderProtocolKind {
  if (isOpenAIProtocolProfile(profile)) return 'openai_v1'
  if (isAnthropicProtocolProfile(profile)) return 'anthropic_v1'
  if (isGeminiProtocolProfile(profile)) return 'gemini_v1beta'
  return 'unknown'
}

export function providerProfileSupportsAccountType(
  type: AccountType,
  provider?: ProviderDefinition,
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
): boolean {
  if (profile && 'type' in profile && profile.type === type) return true
  const accountTypes = profile?.accountTypes?.length ? profile.accountTypes : provider?.accountTypes ?? []
  return accountTypes.includes(type)
}

export function canCreateOAuthAccount(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): boolean {
  return providerProfileSupportsAccountType('oauth', input.provider, input.profile)
    && isGptVendorCode(providerCodeForOAuthFlow(input))
    && accountProviderProtocolKind(input.profile ?? input.provider) === 'openai_v1'
}

export function defaultAccountClientCompatibilityForProvider(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition
}): AccountClientCompatibility {
  return canCreateOAuthAccount(input) ? 'codex_responses' : 'openai_standard'
}

export function effectiveAccountTestClientCompatibility(
  account: AccountProviderProfileLike,
  clientCompatibility: 'account_default' | AccountClientCompatibility
): AccountClientCompatibility {
  if (account.type === 'oauth' && accountProviderProtocolKind(account) === 'openai_v1') {
    return 'codex_responses'
  }
  if (clientCompatibility !== 'account_default') return clientCompatibility
  return 'openai_standard'
}

export function isGatewayTestableAccountProfile(profile?: AccountProviderProfileLike): boolean {
  return accountProviderProtocolKind(profile) !== 'unknown'
}

export function canSelectClientCompatibility(account: AccountProviderProfileLike): boolean {
  return account.type === 'api_key'
    && accountProviderProtocolKind(account) === 'openai_v1'
    && (
      isGptVendorCode(account.providerCode)
      || isXaiProviderCode(account.providerCode)
      || profileSupportsCodexResponsesChatBridge(account)
    )
}

export function accountClientCompatibilityCapabilities(account: AccountProviderProfileLike): ClientCompatibilityCapability[] {
  const protocolKind = accountProviderProtocolKind(account)
  if (protocolKind === 'anthropic_v1') {
    return account.type === 'api_key'
      ? ['anthropic_native', 'claude_code']
      : ['anthropic_native']
  }
  if (protocolKind === 'gemini_v1beta') {
    return ['openai_standard']
  }
  if (protocolKind !== 'openai_v1') {
    return ['openai_standard']
  }
  if (account.type === 'oauth') {
    return ['codex_responses']
  }
  return isGptVendorCode(account.providerCode)
    || isXaiProviderCode(account.providerCode)
    || profileSupportsCodexResponsesChatBridge(account)
    ? ['openai_standard', 'codex_responses']
    : ['openai_standard']
}

export function clientCompatibilityCapabilityLabel(value: ClientCompatibilityCapability): string {
  return clientCompatibilityCapabilityOptions.find((option) => option.value === value)?.label ?? value
}

export function fixedCompatibilityLabel(accounts: AccountProviderProfileLike[]): string {
  return accounts.length ? '测试请求形态' : '客户端兼容'
}

export function fixedCompatibilityText(accounts: AccountProviderProfileLike[]): string {
  if (accounts.some((account) => accountProviderProtocolKind(account) === 'gemini_v1beta')) {
    return 'Gemini API 请求'
  }
  if (accounts.some((account) => accountProviderProtocolKind(account) === 'anthropic_v1')) {
    return 'Anthropic API 请求'
  }
  const capabilities = new Set(accounts.flatMap(accountClientCompatibilityCapabilities))
  if (capabilities.has('codex_responses') && !capabilities.has('openai_standard')) {
    return 'Codex Responses 请求'
  }
  return 'OpenAI-compatible 请求'
}

export function defaultEndpointModesForAccount(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
  type: AccountType
  clientCompatibility?: AccountClientCompatibility
}): AccountSupportedEndpointMode[] {
  if (isHybridProviderProfile(input.profile ?? input.provider)) return [...allAccountEndpointModes]
  const protocolKind = accountProviderProtocolKind(input.profile ?? input.provider)
  if (input.type === 'oauth') return [...responsesEndpointModes]
  if (protocolKind === 'anthropic_v1') return endpointModesForProfile(input.profile ?? input.provider)
  if (protocolKind === 'gemini_v1beta') return endpointModesForProfile(input.profile ?? input.provider)
  if (protocolKind === 'openai_v1') {
    return endpointModesForProfile(input.profile ?? input.provider)
  }
  return [...allAccountEndpointModes]
}

export function profileSupportsCodexResponsesChatBridge(profile?: AccountProviderProfileLike): boolean {
  const profileId = profile?.providerProtocolProfileId ?? profile?.id
  return profileId === OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    || profileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID
    || profileId === GLM_CODING_OPENAI_V1_PROFILE_ID
    || profileId === DEEPSEEK_OPENAI_V1_PROFILE_ID
    || profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
}

export function endpointModesForProfile(profile?: AccountProviderProfileLike): AccountSupportedEndpointMode[] {
  if (isHybridProviderProfile(profile)) return [...allAccountEndpointModes]
  const protocolKind = accountProviderProtocolKind(profile)
  if (protocolKind === 'anthropic_v1') return endpointModesForFamilies(profile, anthropicAccountEndpointModes, [
    { family: ANTHROPIC_MESSAGES_FAMILY, modes: ['messages_json', 'messages_sse'] },
    { family: ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY, modes: ['message_token_counting'] }
  ])
  if (protocolKind === 'gemini_v1beta') return endpointModesForFamilies(profile, geminiAccountEndpointModes, [
    { family: GEMINI_GENERATE_CONTENT_FAMILY, modes: ['generate_content_json'] },
    { family: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, modes: ['generate_content_sse'] },
    { family: GEMINI_COUNT_TOKENS_FAMILY, modes: ['count_tokens'] },
    { family: GEMINI_EMBED_CONTENT_FAMILY, modes: ['embed_content'] },
    { family: 'interactions', modes: ['interactions_json', 'interactions_sse'] }
  ])
  if (protocolKind === 'openai_v1') return endpointModesForFamilies(
    profile,
    profileSupportsCodexResponsesChatBridge(profile) ? chatEndpointModes : openAIEndpointModes,
    [
      { family: OPENAI_CHAT_COMPLETIONS_FAMILY, modes: chatEndpointModes },
      { family: OPENAI_RESPONSES_FAMILY, modes: responsesEndpointModes }
    ]
  )
  return [...allAccountEndpointModes]
}

function endpointModesForFamilies(
  profile: AccountProviderProfileLike | undefined,
  fallback: AccountSupportedEndpointMode[],
  rules: Array<{ family: string; modes: AccountSupportedEndpointMode[] }>
): AccountSupportedEndpointMode[] {
  const families = new Set(endpointFamilyCodes(profile))
  if (!families.size) return [...fallback]
  const output = rules.flatMap((rule) => families.has(rule.family) ? rule.modes : [])
  return output.length ? [...new Set(output)] : [...fallback]
}

function endpointFamilyCodes(profile: AccountProviderProfileLike | undefined): string[] {
  if (!Array.isArray(profile?.endpointFamilies)) return []
  return profile.endpointFamilies
    .map((family) => typeof family === 'string' ? family : family.code)
    .filter((code): code is string => Boolean(code))
}

function providerCodeForOAuthFlow(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): string | undefined {
  return input.profile?.providerCode ?? input.provider?.code
}

function isHybridProviderProfile(profile?: AccountProviderProfileLike): boolean {
  return isHybridProviderCode(profile?.providerCode ?? profile?.code)
}
