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
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  OPENAI_AUDIO_TRANSCRIPTION_FAMILY,
  isGptVendorCode,
  isGeminiProviderCode,
  isMinimaxProviderCode,
  isVolcengineProviderCode,
  isQwenProviderCode,
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
  'audio_job_create',
  'audio_job_get',
  'audio_job_content',
  'audio_job_cancel',
  'realtime_session',
  ...chatEndpointModes,
  ...responsesEndpointModes
]
export const anthropicAccountEndpointModes: AccountSupportedEndpointMode[] = ['messages_json', 'messages_sse', 'message_token_counting']
// gemini 族含 audio_speech（gemini TTS 经混合账户跨协议转换服务
// /v1/audio/speech，契约 §5.1）与 M3 视频四值 video_create / video_get /
// video_content / video_cancel（gemini Veo 经 veo adapter 承载
// predictLongRunning 形态，契约 §5.2）：与 openai 族的 audio/video 值同样
// 只能显式开启（defaultEndpointModesForAccount 过滤，不进默认集）。
// audio_speech 与 video_* 是跨协议共享 token（同时属于 openai/gemini 词表），
// 协议互斥校验按非共享 token 判定。
export const geminiAccountEndpointModes: AccountSupportedEndpointMode[] = ['generate_content_json', 'generate_content_sse', 'count_tokens', 'embed_content', 'interactions_json', 'interactions_sse', 'audio_speech', 'video_create', 'video_get', 'video_content', 'video_cancel']
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
  // 与 M3 视频四值（veo adapter 承载）同样只能显式开启。
  if (protocolKind === 'gemini_v1beta') {
    return endpointModesForProfile(input.profile ?? input.provider)
      .filter((mode) => mode !== 'audio_speech'
        && mode !== 'video_create' && mode !== 'video_get' && mode !== 'video_content' && mode !== 'video_cancel')
  }
  if (input.type === 'oauth' && protocolKind === 'openai_v1') return [...responsesEndpointModes]
  if (protocolKind === 'openai_v1') {
    // 新账户默认集保持 chat/responses 推导结果；images_json、M1 音频模式与
    // M2 视频模式只能显式开启。minimax 档案（M3）只挂媒体 families，推导
    // 结果经此过滤后为空集——媒体端点模式全部 opt-in，与 openai/gemini 族
    // 媒体模式同语义；volcengine/qwen 档案（对话批）补 chat_completions
    // family，但其 chat 对不进默认集（对齐后端 DefaultOpenAIEndpointModes
    // 的 video_get 默认——两家无零费用健康检查面，chat 探针真实计费且档案
    // DefaultHealthCheckModel 仍为媒体模型），chat 可显式勾选。
    const modes = endpointModesForProfile(input.profile ?? input.provider)
      .filter((mode) => mode !== 'images_json' && mode !== 'audio_speech' && mode !== 'audio_transcription_json'
        && mode !== 'video_create' && mode !== 'video_get' && mode !== 'video_content' && mode !== 'video_cancel'
        && mode !== 'audio_job_create' && mode !== 'audio_job_get' && mode !== 'audio_job_content' && mode !== 'audio_job_cancel'
        && mode !== 'realtime_session')
    const code = input.provider?.code ?? input.profile?.providerCode ?? ''
    if (isVolcengineProviderCode(code) || isQwenProviderCode(code)) {
      return modes.filter((mode) => mode !== 'chat_json' && mode !== 'chat_sse')
    }
    return modes
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
    // audio_speech 与 M3 视频四值是 gemini 族的显式可选能力（gemini TTS 经
    // 混合转换派发、veo 经 veo adapter 承载），不进默认集
    // （defaultEndpointModesForAccount 过滤），照 openai 族先例。
    const selectable: AccountSupportedEndpointMode[] = [
      ...familyModes,
      'audio_speech',
      'video_create',
      'video_get',
      'video_content',
      'video_cancel'
    ]
    return [...new Set(selectable)]
  }
  if (protocolKind === 'openai_v1') {
    // M3 媒体供应商（媒体设计 §9/契约 §8）：minimax 档案只声明媒体
    // families（video_generation/tts），openai 族推导不回退 chat 词表
    //（MiniMax 聊天端点非 OpenAI Chat 形态，档案不承接聊天流量）；video_*
    // 四值与 audio_speech 照 openai 族先例属显式可选能力（opt-in，不进
    // 默认集——defaultEndpointModesForAccount 过滤）。
    if (isMinimaxProviderCode(profile?.providerCode ?? profile?.code)) {
      const families = new Set(endpointFamilyCodes(profile))
      const modes: AccountSupportedEndpointMode[] = []
      if (families.has(OPENAI_VIDEO_GENERATION_FAMILY)) {
        modes.push('video_create', 'video_get', 'video_content', 'video_cancel')
      }
      if (families.has(OPENAI_TTS_FAMILY)) {
        modes.push('audio_speech')
      }
      return modes
    }
    // M3 媒体供应商（媒体设计 §9/契约 §9.1）：volcengine 档案声明对话、视频
    // 与语音 families（对话批补 chat_completions——ark OpenAI 兼容端点
    // /api/v3/chat/completions；M6 补 tts——契约 §9.2 回填，豆包 TTS 走
    // openspeech /api/v3/tts adapter，凭据双值 speech_appid/speech_token）。
    // chat 可勾选但不进新账户默认集（探针真实计费 + 档案健康检查模型为
    // 媒体模型，见 defaultEndpointModesForAccount）；video_* 四值与
    // audio_speech 同为显式可选能力。
    if (isVolcengineProviderCode(profile?.providerCode ?? profile?.code)) {
      const families = new Set(endpointFamilyCodes(profile))
      const modes: AccountSupportedEndpointMode[] = []
      if (families.has(OPENAI_CHAT_COMPLETIONS_FAMILY)) {
        modes.push(...chatEndpointModes)
      }
      if (families.has(OPENAI_VIDEO_GENERATION_FAMILY)) {
        modes.push('video_create', 'video_get', 'video_content', 'video_cancel')
      }
      if (families.has(OPENAI_TTS_FAMILY)) {
        modes.push('audio_speech')
      }
      return modes
    }
    // M3 媒体供应商（媒体设计 §9/契约 §10.1/§10.2）：qwen 档案声明对话、
    // video_generation 与 audio_transcription（M3f 长转写）families（对话批
    // 补 chat_completions——DashScope OpenAI 兼容模式
    // /compatible-mode/v1/chat/completions；CosyVoice TTS 面 §10.2 未回填）。
    // chat 对照 openai 族惯例进默认集；video_* 与 audio_job_* 属显式可选
    // 能力（opt-in，不进默认集）。
    if (isQwenProviderCode(profile?.providerCode ?? profile?.code)) {
      const families = new Set(endpointFamilyCodes(profile))
      const modes: AccountSupportedEndpointMode[] = []
      if (families.has(OPENAI_CHAT_COMPLETIONS_FAMILY)) {
        modes.push(...chatEndpointModes)
      }
      if (families.has(OPENAI_VIDEO_GENERATION_FAMILY)) {
        modes.push('video_create', 'video_get', 'video_content', 'video_cancel')
      }
      if (families.has(OPENAI_AUDIO_TRANSCRIPTION_FAMILY)) {
        modes.push('audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel')
      }
      return modes
    }
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
      'video_cancel',
      'audio_job_create',
      'audio_job_get',
      'audio_job_content',
      'audio_job_cancel',
      'realtime_session'
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
