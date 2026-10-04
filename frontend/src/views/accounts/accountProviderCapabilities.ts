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
  isGeminiProviderCode,
  isXaiProviderCode,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'

export type AccountProviderProtocolKind = 'openai_v1' | 'anthropic_v1' | 'gemini_v1beta' | 'unknown'
export type ManagedOAuthProviderKind = 'openai' | 'anthropic' | 'gemini' | 'grok'
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
// images_json 是 openai 族的合法可表达能力（/v1/images 图像 lane 派发依赖它），
// 词表与后端 OpenAIEndpointModeValues 同构（images_json 居首）；但它只能显式
// 开启——defaultEndpointModesForAccount 会在默认集中过滤掉它。
// M1 同步音频新增 audio_speech（POST /v1/audio/speech）与
// audio_transcription_json（POST /v1/audio/transcriptions|translations），与
// accounts.health_check_endpoint_mode CHECK 及 gatewaypreauth 端点模式词表同步
// （音频设计 §11.1/§11.6）；M2 视频新增 video_create（POST /v1/videos）、
// video_get（GET /v1/videos|/v1/videos/{id}）、video_content
// （GET /v1/videos/{id}/content）、video_cancel（DELETE /v1/videos/{id}），同步
// 口径同上（媒体设计 §11.6）；同样只能显式开启，不进默认集（视频不做自动
// 真实生成探针，媒体设计 §11.9）。
export const openAIEndpointModes: AccountSupportedEndpointMode[] = [
  'images_json',
  'audio_speech',
  'audio_transcription_json',
  'video_create',
  'video_get',
  'video_content',
  'video_cancel',
  ...chatEndpointModes,
  ...responsesEndpointModes
]
export const anthropicAccountEndpointModes: AccountSupportedEndpointMode[] = ['messages_json', 'messages_sse', 'message_token_counting']
// gemini 族含 audio_speech（gemini TTS 经混合账户跨协议转换服务
// /v1/audio/speech，契约 §5.1）：与 openai 族的 audio 两值同样只能显式开启
//（defaultEndpointModesForAccount 过滤，不进默认集）。audio_speech 是跨协议
// 共享 token（同时属于 openai/gemini 词表），协议互斥校验按非共享 token 判定。
export const geminiAccountEndpointModes: AccountSupportedEndpointMode[] = ['generate_content_json', 'generate_content_sse', 'count_tokens', 'embed_content', 'interactions_json', 'interactions_sse', 'audio_speech']
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

export function canManageNativeOAuthAccount(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): boolean {
  return managedOAuthProviderKind(input) !== undefined
}

export function managedOAuthProviderKind(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): ManagedOAuthProviderKind | undefined {
  const providerCode = providerCodeForOAuthFlow(input)
  const protocolKind = accountProviderProtocolKind(input.profile ?? input.provider)
  if (isGptVendorCode(providerCode)
    && protocolKind === 'openai_v1'
    && providerProfileSupportsAccountType('oauth', input.provider, input.profile)) {
    return 'openai'
  }
  if (providerCode === 'anthropic'
    && protocolKind === 'anthropic_v1'
    && providerProfileSupportsAccountType('oauth', input.provider, input.profile)) {
    return 'anthropic'
  }
  if (isGeminiProviderCode(providerCode)
    && protocolKind === 'gemini_v1beta'
    && providerProfileSupportsAccountType('google_oauth', input.provider, input.profile)) {
    return 'gemini'
  }
  if (isXaiProviderCode(providerCode)
    && protocolKind === 'openai_v1'
    && providerProfileSupportsAccountType('oauth', input.provider, input.profile)) {
    return 'grok'
  }
  return undefined
}

export function supportsOAuthAccountType(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition | AccountProviderProfileLike
}): boolean {
  return providerProfileSupportsAccountType('oauth', input.provider, input.profile)
}

export function defaultAccountClientCompatibilityForProvider(input: {
  provider?: ProviderDefinition
  profile?: ProviderProtocolProfileDefinition
}): AccountClientCompatibility {
  if (accountProviderProtocolKind(input.profile ?? input.provider) === 'anthropic_v1') {
    return 'anthropic_native'
  }
  return canCreateOAuthAccount(input) ? 'codex_responses' : 'openai_standard'
}

export function effectiveAccountTestClientCompatibility(
  account: AccountProviderProfileLike,
  clientCompatibility: 'account_default' | AccountClientCompatibility
): AccountClientCompatibility {
  if (account.type === 'oauth'
    && isGptVendorCode(account.providerCode)
    && accountProviderProtocolKind(account) === 'openai_v1') {
    return 'codex_responses'
  }
  if (clientCompatibility !== 'account_default') return clientCompatibility
  if (accountProviderProtocolKind(account) === 'anthropic_v1') {
    return 'anthropic_native'
  }
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
    return isGptVendorCode(account.providerCode) ? ['codex_responses'] : ['openai_standard']
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
  if (protocolKind === 'anthropic_v1') return endpointModesForProfile(input.profile ?? input.provider)
  // gemini 新账户默认集保持 generateContent/interactions 推导结果；audio_speech
  // 与 openai 族的 audio 两值同样只能显式开启。
  if (protocolKind === 'gemini_v1beta') {
    return endpointModesForProfile(input.profile ?? input.provider)
      .filter((mode) => mode !== 'audio_speech')
  }
  if (input.type === 'oauth' && protocolKind === 'openai_v1') return [...responsesEndpointModes]
  if (protocolKind === 'openai_v1') {
    // 新账户默认集保持 chat/responses 推导结果；images_json、M1 音频模式与
    // M2 视频模式只能显式开启。
    return endpointModesForProfile(input.profile ?? input.provider)
      .filter((mode) => mode !== 'images_json' && mode !== 'audio_speech' && mode !== 'audio_transcription_json'
        && mode !== 'video_create' && mode !== 'video_get' && mode !== 'video_content' && mode !== 'video_cancel')
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
  if (protocolKind === 'gemini_v1beta') {
    const familyModes = endpointModesForFamilies(profile, geminiAccountEndpointModes, [
      { family: GEMINI_GENERATE_CONTENT_FAMILY, modes: ['generate_content_json'] },
      { family: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, modes: ['generate_content_sse'] },
      { family: GEMINI_COUNT_TOKENS_FAMILY, modes: ['count_tokens'] },
      { family: GEMINI_EMBED_CONTENT_FAMILY, modes: ['embed_content'] },
      { family: 'interactions', modes: ['interactions_json', 'interactions_sse'] }
    ])
    // audio_speech 是 gemini 族的显式可选能力（gemini TTS 经混合转换派发），
    // 不进默认集（defaultEndpointModesForAccount 过滤），照 openai 族先例。
    const selectable: AccountSupportedEndpointMode[] = [...familyModes, 'audio_speech']
    return [...new Set(selectable)]
  }
  if (protocolKind === 'openai_v1') {
    const familyModes = endpointModesForFamilies(
      profile,
      profileSupportsCodexResponsesChatBridge(profile) ? chatEndpointModes : openAIEndpointModes,
      [
        { family: OPENAI_CHAT_COMPLETIONS_FAMILY, modes: chatEndpointModes },
        { family: OPENAI_RESPONSES_FAMILY, modes: responsesEndpointModes }
      ]
    )
    // images_json 与 M1 音频模式、M2 视频模式是 openai 族的显式可选能力：
    // 允许勾选（媒体 lane 派发依赖），不进默认集（defaultEndpointModesForAccount
    // 过滤）。
    const selectable: AccountSupportedEndpointMode[] = [
      ...familyModes,
      'images_json',
      'audio_speech',
      'audio_transcription_json',
      'video_create',
      'video_get',
      'video_content',
      'video_cancel'
    ]
    return [...new Set(selectable)]
  }
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
