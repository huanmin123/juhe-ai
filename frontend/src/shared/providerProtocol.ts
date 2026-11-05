import type { ProviderDefinition } from '@/types/domain'

export const OPENAI_PROTOCOL_CODE = 'openai'
export const OPENAI_PROTOCOL_VERSION = 'v1'
export const GPT_VENDOR_CODE = 'gpt'
export const XAI_PROVIDER_CODE = 'xai'
export const DEEPSEEK_PROVIDER_CODE = 'deepseek'
export const GLM_PROVIDER_CODE = 'glm'
export const MINIMAX_PROVIDER_CODE = 'minimax'
export const VOLCENGINE_PROVIDER_CODE = 'volcengine'
export const QWEN_PROVIDER_CODE = 'qwen'
export const ANTHROPIC_PROTOCOL_CODE = 'anthropic'
export const ANTHROPIC_PROTOCOL_VERSION = 'v1'
export const ANTHROPIC_PROVIDER_CODE = 'anthropic'
export const GEMINI_PROTOCOL_CODE = 'gemini'
export const GEMINI_PROTOCOL_VERSION = 'v1beta'
export const GEMINI_PROVIDER_CODE = 'gemini'
export const GPT_OPENAI_V1_PROFILE_ID = 'profile_gpt_openai_v1'
export const XAI_OPENAI_V1_PROFILE_ID = 'profile_xai_openai_v1'
export const DEEPSEEK_OPENAI_V1_PROFILE_ID = 'profile_deepseek_openai_v1'
export const DEEPSEEK_ANTHROPIC_V1_PROFILE_ID = 'profile_deepseek_anthropic_v1'
export const GLM_GENERAL_OPENAI_V1_PROFILE_ID = 'profile_glm_general_openai_v1'
export const GLM_CODING_OPENAI_V1_PROFILE_ID = 'profile_glm_coding_openai_v1'
export const GLM_CODING_ANTHROPIC_V1_PROFILE_ID = 'profile_glm_coding_anthropic_v1'
export const MINIMAX_OPENAI_V1_PROFILE_ID = 'profile_minimax_openai_v1'
export const VOLCENGINE_OPENAI_V1_PROFILE_ID = 'profile_volcengine_openai_v1'
// T102：火山 OpenSpeech 音频连接（registry preset volcengine_openspeech，契约 §9.2）
// 的前端表单档案。后端写链 pin 把该 preset 与 ark 同指 profile_volcengine_openai_v1
//（accounts/protocol_context.go presetProfileID），但编辑保存链按本表正向导出连接
// preset——共用 ark 档案会把 OpenSpeech 连接行导出成 volcengine_ark，被后端 audio
// 域组合校验拒绝（volcengine+ark 仅允许 chat/video/image）。档案协议对沿用后端 pin
// 语义（openai/v1）；草稿链收到词表外档案 id 时按 vendor 默认回落同一协议对
//（draftProtocolContext），行为不变。
export const VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID = 'profile_volcengine_openspeech_v1'
export const QWEN_OPENAI_V1_PROFILE_ID = 'profile_qwen_openai_v1'
export const ANTHROPIC_ANTHROPIC_V1_PROFILE_ID = 'profile_anthropic_anthropic_v1'
export const GEMINI_NATIVE_V1BETA_PROFILE_ID = 'profile_gemini_native_v1beta'
export const GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID = 'profile_gemini_openai_chat_v1beta'
export const OPENAI_CHAT_COMPLETIONS_FAMILY = 'chat_completions'
export const OPENAI_RESPONSES_FAMILY = 'responses'
// M3 媒体新增（媒体设计 §9/契约 §8）：openai 协议族的厂商原生媒体端点族——
// minimax 档案（profile_minimax_openai_v1）只挂这两个族。
export const OPENAI_VIDEO_GENERATION_FAMILY = 'video_generation'
export const OPENAI_TTS_FAMILY = 'tts'
export const OPENAI_AUDIO_TRANSCRIPTION_FAMILY = 'audio_transcription'
export const ANTHROPIC_MESSAGES_FAMILY = 'messages'
export const ANTHROPIC_MODELS_FAMILY = 'models'
export const ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY = 'message_token_counting'
export const GEMINI_GENERATE_CONTENT_FAMILY = 'generate_content'
export const GEMINI_STREAM_GENERATE_CONTENT_FAMILY = 'stream_generate_content'
export const GEMINI_INTERACTIONS_FAMILY = 'interactions'
export const GEMINI_COUNT_TOKENS_FAMILY = 'count_tokens'
export const GEMINI_EMBED_CONTENT_FAMILY = 'embed_content'

export function normalizeProviderToken(value: unknown): string {
  return String(value ?? '').trim().toLowerCase()
}

export function isOpenAIProtocolProvider(provider?: Pick<ProviderDefinition, 'protocolCode' | 'protocolVersion'>): boolean {
  return normalizeProviderToken(provider?.protocolCode) === OPENAI_PROTOCOL_CODE
    && normalizeProviderToken(provider?.protocolVersion) === OPENAI_PROTOCOL_VERSION
}

export function isOpenAIProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return normalizeProviderToken(profile?.protocolCode) === OPENAI_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === OPENAI_PROTOCOL_VERSION
}

export function isAnthropicProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return normalizeProviderToken(profile?.protocolCode) === ANTHROPIC_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === ANTHROPIC_PROTOCOL_VERSION
}

export function isGeminiProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return normalizeProviderToken(profile?.protocolCode) === GEMINI_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === GEMINI_PROTOCOL_VERSION
}

export function isGatewaySupportedProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return isOpenAIProtocolProfile(profile) || isAnthropicProtocolProfile(profile) || isGeminiProtocolProfile(profile)
}

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}

export function isXaiProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === XAI_PROVIDER_CODE
}

export function isDeepSeekProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === DEEPSEEK_PROVIDER_CODE
}

export function isGlmProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GLM_PROVIDER_CODE
}

export function isMinimaxProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === MINIMAX_PROVIDER_CODE
}

export function isVolcengineProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === VOLCENGINE_PROVIDER_CODE
}

export function isQwenProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === QWEN_PROVIDER_CODE
}

export function isGeminiProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GEMINI_PROVIDER_CODE
}

// 账户创建载荷的 connections[] 使用连接预设 code（设计 §1.1/§9.3）；前端表单仍以
// 协议档案 id 驱动，二者一一对应。
const CONNECTION_PRESET_BY_PROFILE_ID: Record<string, string> = {
  [GPT_OPENAI_V1_PROFILE_ID]: 'gpt_openai_v1',
  [XAI_OPENAI_V1_PROFILE_ID]: 'xai_openai_v1',
  [DEEPSEEK_OPENAI_V1_PROFILE_ID]: 'deepseek_openai_v1',
  [DEEPSEEK_ANTHROPIC_V1_PROFILE_ID]: 'deepseek_anthropic_v1',
  [ANTHROPIC_ANTHROPIC_V1_PROFILE_ID]: 'anthropic_v1',
  [GLM_GENERAL_OPENAI_V1_PROFILE_ID]: 'glm_general_openai_v1',
  [GLM_CODING_OPENAI_V1_PROFILE_ID]: 'glm_coding_openai_v1',
  [GLM_CODING_ANTHROPIC_V1_PROFILE_ID]: 'glm_coding_anthropic_v1',
  [GEMINI_NATIVE_V1BETA_PROFILE_ID]: 'gemini_native_v1beta',
  [GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID]: 'gemini_openai_chat_v1beta',
  [MINIMAX_OPENAI_V1_PROFILE_ID]: 'minimax_media',
  [VOLCENGINE_OPENAI_V1_PROFILE_ID]: 'volcengine_ark',
  [VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID]: 'volcengine_openspeech',
  [QWEN_OPENAI_V1_PROFILE_ID]: 'qwen_dashscope'
}

export function connectionPresetCodeForProfile(profileId: string | undefined): string | undefined {
  const key = String(profileId ?? '').trim()
  return key ? CONNECTION_PRESET_BY_PROFILE_ID[key] : undefined
}

// 连接 preset → 表单供应商身份的反查表（T102）：编辑/克隆详情的
// providerProtocolProfileId 由后端探针表（exactkeyprobe.connectionPresetProfiles，
// 仅 13 条）反查派生，minimax_media / volcengine_ark / volcengine_openspeech /
// qwen_dashscope 四档媒体 preset 不在表内返回空串——这些账户的编辑保存被前端
// 校验拦死。本表在后端读面派生缺失时按连接 preset（编辑详情 connectionPresetCode
// 字段，即健康检查连接行）反查补齐供应商身份。取值依据（逐条）：
//   - 文本 preset 与 anthropic_v1：后端迁移映射 legacybusinessimport/mapping.go
//     legacyProfilePresets 的双射逆向，与上方正向表逐条互逆；
//   - minimax_media / volcengine_ark / qwen_dashscope：同上双射逆向；
//   - volcengine_openspeech → OpenSpeech 专用档案：registry PresetVolcengineOpenSpeech，
//     档案 id 见 VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID 注释。
// generic_http 三档 preset 已随连接器退役，不再入表；词表外的 preset 由
// resolveEditAccountProviderIdentity 保持 fail-closed。
const PROVIDER_IDENTITY_BY_CONNECTION_PRESET: Record<string, ProviderProtocolIdentity> = {
  gpt_openai_v1: { providerCode: GPT_VENDOR_CODE, providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID },
  xai_openai_v1: { providerCode: XAI_PROVIDER_CODE, providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID },
  deepseek_openai_v1: { providerCode: DEEPSEEK_PROVIDER_CODE, providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID },
  deepseek_anthropic_v1: { providerCode: DEEPSEEK_PROVIDER_CODE, providerProtocolProfileId: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID },
  anthropic_v1: { providerCode: ANTHROPIC_PROVIDER_CODE, providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID },
  glm_general_openai_v1: { providerCode: GLM_PROVIDER_CODE, providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID },
  glm_coding_openai_v1: { providerCode: GLM_PROVIDER_CODE, providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID },
  glm_coding_anthropic_v1: { providerCode: GLM_PROVIDER_CODE, providerProtocolProfileId: GLM_CODING_ANTHROPIC_V1_PROFILE_ID },
  gemini_native_v1beta: { providerCode: GEMINI_PROVIDER_CODE, providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID },
  gemini_openai_chat_v1beta: { providerCode: GEMINI_PROVIDER_CODE, providerProtocolProfileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID },
  minimax_media: { providerCode: MINIMAX_PROVIDER_CODE, providerProtocolProfileId: MINIMAX_OPENAI_V1_PROFILE_ID },
  volcengine_ark: { providerCode: VOLCENGINE_PROVIDER_CODE, providerProtocolProfileId: VOLCENGINE_OPENAI_V1_PROFILE_ID },
  volcengine_openspeech: { providerCode: VOLCENGINE_PROVIDER_CODE, providerProtocolProfileId: VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID },
  qwen_dashscope: { providerCode: QWEN_PROVIDER_CODE, providerProtocolProfileId: QWEN_OPENAI_V1_PROFILE_ID }
}

export interface ProviderProtocolIdentity {
  providerCode: string
  providerProtocolProfileId: string
}

// 前端表单可识别的档案 id 全集：正向表键（保存链 preset 导出的驱动 id）∪ 反查表
// 值（派生落地的 id）。探针目录专用 id（profile_generic_openai_v1 等，后端探针表
// 对 generic 连接的内部目录 ID）不在其中——详情返回这类 id 时必须经 preset 反查
// 换成前端档案，否则供应商选择/保存链 preset 导出均无法工作。
const FRONTEND_PROTOCOL_PROFILE_IDS: ReadonlySet<string> = new Set([
  ...Object.keys(CONNECTION_PRESET_BY_PROFILE_ID),
  ...Object.values(PROVIDER_IDENTITY_BY_CONNECTION_PRESET).map((identity) => identity.providerProtocolProfileId)
])

export function providerIdentityForConnectionPreset(preset: string | undefined | null): ProviderProtocolIdentity | undefined {
  const key = normalizeProviderToken(preset)
  return key ? PROVIDER_IDENTITY_BY_CONNECTION_PRESET[key] : undefined
}

/**
 * 编辑/克隆装载的供应商身份派生（T102）：详情档案缺失（媒体四档 preset 无探针
 * 目录）或为探针专用 id（generic_*）时，按连接 preset 反查补齐；无 preset 或
 * preset 词表外时退回账户行 providerCode（vendor 账户兜底）；供应商与档案都不可
 * 派生时保持 fail-closed（空 providerCode 由保存校验给出可操作文案）。
 */
export function resolveEditAccountProviderIdentity(input: {
  providerCode?: string | null
  providerProtocolProfileId?: string | null
  connectionPresetCode?: string | null
}): ProviderProtocolIdentity {
  const profileId = String(input.providerProtocolProfileId ?? '').trim()
  const presetIdentity = providerIdentityForConnectionPreset(String(input.connectionPresetCode ?? ''))
  // 详情档案前端可识别：原样保留；vendor 投影为空（迁移中间态）时按 preset 补供应商。
  if (profileId && FRONTEND_PROTOCOL_PROFILE_IDS.has(profileId)) {
    return {
      providerCode: String(input.providerCode ?? '').trim() || presetIdentity?.providerCode || '',
      providerProtocolProfileId: profileId
    }
  }
  if (presetIdentity) return { ...presetIdentity }
  const providerCode = String(input.providerCode ?? '').trim()
  if (providerCode) return { providerCode, providerProtocolProfileId: profileId }
  return { providerCode: '', providerProtocolProfileId: profileId }
}

export function preferredDefaultProvider(providers: ProviderDefinition[]): ProviderDefinition | undefined {
  const enabledProviders = providers.filter((provider) => provider.enabled)
  return enabledProviders.find((provider) => isGptVendorCode(provider.code)) ?? enabledProviders[0]
}

export function preferredDefaultProviderCode(providers: ProviderDefinition[]): string {
  return preferredDefaultProvider(providers)?.code ?? ''
}

export function defaultProviderProtocolProfileId(provider?: Pick<ProviderDefinition, 'defaultProtocolProfileId' | 'protocolProfiles'>): string {
  if (!provider) return ''
  return provider.protocolProfiles.find((profile) => profile.id === provider.defaultProtocolProfileId)?.id
    || provider.protocolProfiles.find((profile) => profile.enabled)?.id
    || provider.protocolProfiles[0]?.id
    || provider.defaultProtocolProfileId
    || ''
}
