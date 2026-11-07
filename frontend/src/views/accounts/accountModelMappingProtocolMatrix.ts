import type { ProviderDefinition } from '@/types/domain'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY,
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'
import type { AccountFormModel } from './accountFormTypes'

export type AccountModelMappingSourceEndpointFamily = AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
export type AccountModelMappingUpstreamEndpointFamily = AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
export type AccountModelMappingProviderProfile = ProviderDefinition | ProviderDefinition['protocolProfiles'][number] | {
  id?: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
}

export type AccountModelMappingProtocolContext = {
  providerProfile?: AccountModelMappingProviderProfile
  supportedEndpointModes?: AccountFormModel['supportedEndpointModes']
}

type ProtocolProfileKind = 'openai' | 'anthropic' | 'gemini'

type ProtocolConversionRule = {
  source: AccountModelMappingSourceEndpointFamily
  upstream: AccountModelMappingUpstreamEndpointFamily
  upstreamProfile: ProtocolProfileKind
  requiresNativeResponses?: boolean
}

// isMediaMappingEndpointFamily 报告端点族是否媒体映射族（M4b，镜像后端
// model_mapping_protocol_matrix.go）：媒体族映射是同族模型名改写，上游面恒为
// OpenAI 形态媒体端点（/v1/videos、/v1/audio/speech），只允许 OpenAI 兼容
// 协议档案声明。
export function isMediaMappingEndpointFamily(
  value: AccountModelMappingSourceEndpointFamily | AccountModelMappingUpstreamEndpointFamily
): boolean {
  return value === OPENAI_VIDEO_GENERATION_FAMILY || value === OPENAI_TTS_FAMILY
}

// 2026-11-07 跨协议模型映射退役（计划 PLAN-20261106T162344524Z）：唯一允许方向
// 是协议等价——同族改名，外加 Gemini stream_generate_content → generate_content
// 归一；responses → chat_completions bridge 已随 Chat bridge 全链删除。与后端
// entry_mappings.go / entry_mapping_pairs.json golden 全等（回归脚本守护）。
export const accountModelMappingProtocolRules: readonly ProtocolConversionRule[] = [
  { source: OPENAI_CHAT_COMPLETIONS_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: OPENAI_RESPONSES_FAMILY, upstream: OPENAI_RESPONSES_FAMILY, upstreamProfile: 'openai', requiresNativeResponses: true },
  { source: ANTHROPIC_MESSAGES_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: GEMINI_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  // M4b 媒体映射（媒体设计 §9）：video_generation / tts 同族模型名改写，
  // 上游面是 OpenAI 形态媒体端点，仅 openai 档案供应商可声明（能力门要求
  // 账户启用 video_create / audio_speech 端点模式）。
  { source: OPENAI_VIDEO_GENERATION_FAMILY, upstream: OPENAI_VIDEO_GENERATION_FAMILY, upstreamProfile: 'openai' },
  { source: OPENAI_TTS_FAMILY, upstream: OPENAI_TTS_FAMILY, upstreamProfile: 'openai' }
] as const

// 单协议下拉选项（前端变化契约）：每行映射只有一个协议选择，选项即协议等价
// pair——source 唯一标识选项，target 由选项表派生后同时写入两个持久化字段。
// 选项直接由规则表投影生成，保证下拉集合与协议矩阵/golden 不漂移。
export type AccountModelMappingProtocolOption = {
  /** 下拉值：即该协议等价 pair 的 source 端点族（选项唯一标识）。 */
  value: AccountModelMappingSourceEndpointFamily
  label: string
  source: AccountModelMappingSourceEndpointFamily
  upstream: AccountModelMappingUpstreamEndpointFamily
}

const accountModelMappingProtocolOptionLabels: Record<AccountModelMappingSourceEndpointFamily, string> = {
  [OPENAI_CHAT_COMPLETIONS_FAMILY]: 'Chat Completions',
  [OPENAI_RESPONSES_FAMILY]: 'Responses',
  [ANTHROPIC_MESSAGES_FAMILY]: 'Messages',
  [GEMINI_GENERATE_CONTENT_FAMILY]: 'Gemini GenerateContent',
  [GEMINI_STREAM_GENERATE_CONTENT_FAMILY]: 'Gemini StreamGenerateContent',
  [OPENAI_VIDEO_GENERATION_FAMILY]: '视频生成',
  [OPENAI_TTS_FAMILY]: '语音合成'
}

export const accountModelMappingProtocolOptions: readonly AccountModelMappingProtocolOption[] = (
  accountModelMappingProtocolRules.map((rule) => ({
    value: rule.source,
    label: accountModelMappingProtocolOptionLabels[rule.source],
    source: rule.source,
    upstream: rule.upstream
  }))
)

// 单协议下拉的 target 派生：修改协议时同时写入 source/target 两个持久化字段。
export function accountModelMappingTargetForSource(
  source: AccountModelMappingSourceEndpointFamily
): AccountModelMappingUpstreamEndpointFamily {
  const option = accountModelMappingProtocolOptions.find((item) => item.source === source)
  if (option) return option.upstream
  // 表外 source（UI 不可达）按同族回落：stream 归一目标为 generate_content，
  // 其余 source 本身就是合法上游族；保证返回值始终落在上游词表内。
  return source === GEMINI_STREAM_GENERATE_CONTENT_FAMILY ? GEMINI_GENERATE_CONTENT_FAMILY : source
}

// 协议选项过滤（profile + 账户 endpoint modes）：结构门（协议档案支持的协议等价
// pair）与目标族能力门（账户至少启用一种对应上游接口形态）都满足才出现。
export function isAccountModelMappingProtocolOptionAllowed(
  option: AccountModelMappingProtocolOption,
  context: AccountModelMappingProtocolContext
): boolean {
  return !accountModelMappingProtocolStructureValidationMessage({
    sourceEndpointFamily: option.source,
    upstreamEndpointFamily: option.upstream,
    context
  }) && hasAccountModelMappingUpstreamEndpointFamilyCapability(option.upstream, context.supportedEndpointModes)
}

// 默认协议按账户支持模式选择：优先 Chat，其次 Responses / Messages / Gemini
// GenerateContent / Gemini StreamGenerateContent / 媒体族对应项（选项表顺序即
// 优先级）；无任何匹配时回退 Chat（首个选项）。
export function defaultAccountModelMappingProtocol(
  context: AccountModelMappingProtocolContext
): AccountModelMappingProtocolOption {
  return accountModelMappingProtocolOptions.find((option) => (
    isAccountModelMappingProtocolOptionAllowed(option, context)
  )) ?? accountModelMappingProtocolOptions[0]
}

type AccountModelMappingProtocolStructureInput = {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  context: AccountModelMappingProtocolContext
}

export function accountModelMappingProtocolValidationMessage(
  input: AccountModelMappingProtocolStructureInput & { enabled?: boolean }
): string | undefined {
  const structuralValidation = accountModelMappingProtocolStructureValidationMessage(input)
  if (structuralValidation || input.enabled === false) return structuralValidation
  if (hasAccountModelMappingUpstreamEndpointFamilyCapability(
    input.upstreamEndpointFamily,
    input.context.supportedEndpointModes
  )) {
    return undefined
  }
  const rule = accountModelMappingProtocolRules.find((item) => (
    item.source === input.sourceEndpointFamily && item.upstream === input.upstreamEndpointFamily
  ))
  return rule?.requiresNativeResponses
    ? 'Responses 模型别名只能用于账号真实支持 Responses API 的原生上游'
    : missingUpstreamEndpointFamilyCapabilityMessage(input.upstreamEndpointFamily)
}

function accountModelMappingProtocolStructureValidationMessage(
  input: AccountModelMappingProtocolStructureInput
): string | undefined {
  const { sourceEndpointFamily, upstreamEndpointFamily, context } = input
  const providerProfile = context.providerProfile
  const openAIProfile = isOpenAIProtocolProfile(providerProfile)
  const anthropicProfile = isAnthropicProtocolProfile(providerProfile)
  const geminiProfile = isGeminiProtocolProfile(providerProfile)
  const profileId = accountModelMappingProviderProfileId(providerProfile)
  const geminiOpenAIChatProfile = profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
  const geminiNativeProfile = profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID

  if (!openAIProfile && !anthropicProfile && !geminiProfile) {
    return '当前供应商协议不支持模型映射'
  }
  // 2026-11-07 bridge 退役：Gemini OpenAI Chat 档案不再承接 Responses 入站，
  // 只允许 Chat Completions 同协议映射（原「Responses 到 Chat bridge」结构门删除）。
  if (geminiOpenAIChatProfile
    && (sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY || upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY)) {
    return 'Gemini OpenAI Chat 账号模型别名只能使用 Chat Completions 同协议映射'
  }
  if (geminiProfile && !geminiNativeProfile && !geminiOpenAIChatProfile) {
    return '当前 Gemini 协议档案暂不支持模型映射账号别名'
  }

  const rule = accountModelMappingProtocolRules.find((item) => (
    item.source === sourceEndpointFamily && item.upstream === upstreamEndpointFamily
  ))
  if (!rule) {
    return unsupportedProtocolConversionMessage(sourceEndpointFamily, upstreamEndpointFamily)
  }

  // M4b：媒体映射族的上游面恒为 OpenAI 形态媒体端点，仅 openai 档案供应商
  // 可声明（anthropic/gemini 档案无对应执行面）。
  if (isMediaMappingEndpointFamily(sourceEndpointFamily) && !openAIProfile) {
    return '当前供应商协议不支持媒体模型映射账号别名'
  }
  if (openAIProfile && upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY && upstreamEndpointFamily !== OPENAI_RESPONSES_FAMILY && !isMediaMappingEndpointFamily(upstreamEndpointFamily)) {
    return '模型映射 OpenAI 协议账号只能使用 Chat Completions、Responses 或媒体族同协议映射'
  }
  if (anthropicProfile && upstreamEndpointFamily !== ANTHROPIC_MESSAGES_FAMILY) {
    return '模型映射 Anthropic 协议账号只能使用 Messages 同协议映射'
  }
  if (geminiNativeProfile && upstreamEndpointFamily !== GEMINI_GENERATE_CONTENT_FAMILY) {
    return '模型映射 Gemini native 账号只能使用 GenerateContent 同族映射'
  }
  if (!openAIProfile && !anthropicProfile && !geminiNativeProfile) {
    return '当前供应商协议不支持模型映射账号别名'
  }
  return undefined
}

export function isAccountModelMappingProtocolAllowed(input: {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  enabled?: boolean
  context: AccountModelMappingProtocolContext
}): boolean {
  return !accountModelMappingProtocolValidationMessage(input)
}

export function accountModelMappingEndpointFamilyText(
  value: AccountModelMappingSourceEndpointFamily | AccountModelMappingUpstreamEndpointFamily
): string {
  if (value === ANTHROPIC_MESSAGES_FAMILY) return 'Messages'
  if (value === GEMINI_GENERATE_CONTENT_FAMILY) return 'Gemini GenerateContent'
  if (value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) return 'Gemini StreamGenerateContent'
  if (value === OPENAI_VIDEO_GENERATION_FAMILY) return '视频生成'
  if (value === OPENAI_TTS_FAMILY) return '语音合成'
  return value === OPENAI_RESPONSES_FAMILY ? 'Responses' : 'Chat Completions'
}

export function hasNativeResponsesEndpointMode(value: AccountFormModel['supportedEndpointModes']): boolean {
  return hasAccountModelMappingUpstreamEndpointFamilyCapability(OPENAI_RESPONSES_FAMILY, value)
}

export function hasAccountModelMappingUpstreamEndpointFamilyCapability(
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily,
  value: AccountFormModel['supportedEndpointModes'] | undefined
): boolean {
  const modes = value ?? []
  if (upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return modes.some((mode) => mode === 'chat_json' || mode === 'chat_sse')
  }
  if (upstreamEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    return modes.some((mode) => mode === 'responses_json' || mode === 'responses_sse')
  }
  if (upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return modes.some((mode) => mode === 'messages_json' || mode === 'messages_sse')
  }
  if (upstreamEndpointFamily === OPENAI_VIDEO_GENERATION_FAMILY) {
    // M4b：视频映射上游面要求账户可承接 /v1/videos 创建（候选过滤的
    // opt-in 模式，与 dispatch 侧 requiredSupportedEndpointMode 同词表）。
    return modes.some((mode) => mode === 'video_create')
  }
  if (upstreamEndpointFamily === OPENAI_TTS_FAMILY) {
    // M4b：语音映射上游面要求账户可承接 /v1/audio/speech。
    return modes.some((mode) => mode === 'audio_speech')
  }
  return modes.some((mode) => mode === 'generate_content_json' || mode === 'generate_content_sse')
}

function accountModelMappingProviderProfileId(providerProfile?: AccountModelMappingProviderProfile): string | undefined {
  if (providerProfile && 'id' in providerProfile && typeof providerProfile.id === 'string') {
    return providerProfile.id
  }
  if (providerProfile && 'providerProtocolProfileId' in providerProfile && typeof providerProfile.providerProtocolProfileId === 'string') {
    return providerProfile.providerProtocolProfileId
  }
  return undefined
}

// 跨协议映射已整体退役：矩阵外组合一律按「协议一致」口径拒绝（与后端 400
// 文案同语义）；消息携带「映射」关键词，供创建向导校验失败回跳第 3 步。
function unsupportedProtocolConversionMessage(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  return `模型映射客户端协议与上游协议必须一致：不支持 ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 到 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 的跨协议映射`
}

function missingUpstreamEndpointFamilyCapabilityMessage(
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  return `启用的模型映射上游协议 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 要求账户至少启用一种对应的上游接口能力`
}
