import type { AccountSupportedEndpointMode, AccountType, ProviderDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import { GEMINI_NATIVE_V1BETA_PROFILE_ID, isGeminiProviderCode, isGlmProviderCode, isGptVendorCode, isHybridProviderCode, isMinimaxProviderCode, isOpenAICompatibleProviderCode, isQwenProviderCode, isVolcengineProviderCode, isXaiProviderCode } from '@/shared/providerProtocol'
import { defaultEndpointModesForAccount, endpointModesForProfile, type AccountProviderProfileLike } from './accountProviderCapabilities'

// AI 账户能力域拆分（docs/plans/计划-20261011T141004000Z-AI账户能力域拆分.md
// §域枚举与语义 / §D6 三集合模型 / §M1 清单「前端：域常量 + 推导镜像 + 三集合常量」）。
// 本文件是 M1 纯常量层：只承载域枚举、对话 modes 锚集合、域推导镜像与三集合类型，
// 不接线任何 UI/表单消费方（保存契约与勾选 UI 由 M2 接线）。

/** 能力域枚举：`accounts.capability_domain` 列 CHECK 的四值（chat/video/audio/image）。 */
export type AccountCapabilityDomain = 'chat' | 'video' | 'audio' | 'image'

export const ACCOUNT_CAPABILITY_DOMAINS: readonly AccountCapabilityDomain[] = ['chat', 'video', 'audio', 'image']

export const ACCOUNT_CAPABILITY_DOMAIN_LABELS: Record<AccountCapabilityDomain, string> = {
  chat: '对话',
  video: '视频',
  audio: '音频',
  image: '图像'
}

export function accountCapabilityDomainLabel(domain: AccountCapabilityDomain): string {
  return ACCOUNT_CAPABILITY_DOMAIN_LABELS[domain]
}

/** 存量迁移推导规则版本；与迁移阶段记录 `account_capability_domain_migration` 的 ruleVersion 同源。 */
export const ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION = 1

// 「对话 modes 集合」（锚定义，前后端镜像）：命中任一值即视为具备对话能力，
// 规则②③④的前置条件「不含对话 modes」被否决，账户归入对话域。
// v8-A 对话域纯粹化：audio_transcription_json 与 realtime_session 移出对话域
// （归音频域），锚集合 15 值收敛为 13 值；images_json 本就不在锚集且 v8-A 后
// 对话域不再追加共享（存量共享、新增隔离）。
export const DOMAIN_ENDPOINT_MODES_CHAT: readonly AccountSupportedEndpointMode[] = [
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse',
  'interactions_json',
  'interactions_sse',
  'count_tokens',
  'embed_content',
  'message_token_counting'
]

// 规则②③④的媒体触发集合（整组声明语义：视频四值、长音频四值按组出现）。
// v8-A：音频域触发集补短转写 audio_transcription_json 与实时会话
// realtime_session（自对话域移入，只声明其一的包归音频域）。
export const DOMAIN_ENDPOINT_MODES_VIDEO: readonly AccountSupportedEndpointMode[] = [
  'video_create',
  'video_get',
  'video_content',
  'video_cancel'
]
export const DOMAIN_ENDPOINT_MODES_AUDIO: readonly AccountSupportedEndpointMode[] = [
  'audio_speech',
  'audio_job_create',
  'audio_job_get',
  'audio_job_content',
  'audio_job_cancel',
  'audio_transcription_json',
  'realtime_session'
]
export const DOMAIN_ENDPOINT_MODES_IMAGE: readonly AccountSupportedEndpointMode[] = ['images_json']

/**
 * 域→目录 mode 白名单（v8-C 模型选择器按域过滤根治）：四域允许选出的
 * `provider_model_catalog.mode` 六值词表值（与 model_catalog_data_gen.go
 * 种子及服务端 `ProviderCatalogModeValues` 同源）：chat={chat,responses}；
 * video={video}；audio={audio}（含 TTS/转写/realtime，目录统一 mode=audio）；
 * image={image_generation}。embedding 行不属于任何账户域，任何域都不放行。
 * 自定义表 `custom_provider_models.mode` 是另一套四值 UI 分类词表
 * （text/image/audio/video，写入枚举固定），服务端按表翻译后过滤
 * （chat/responses→text、image_generation→image、audio/video 同形，
 * BUG-0324）：调用方始终传本表六值，不做本地换算。
 */
export const DOMAIN_MODEL_MODES: Record<AccountCapabilityDomain, readonly string[]> = {
  chat: ['chat', 'responses'],
  video: ['video'],
  audio: ['audio'],
  image: ['image_generation']
}

/**
 * 按 modes 推导能力域。与 maintenance capabilitydomain 包 ruleVersion=1 镜像，
 * 改动须双侧同步。规则按序命中即停（ruleVersion=1）：
 *
 * ① 授权实例 → 继承源账户域（前端无此场景，镜像省略）；
 * ② 不含对话 modes 且含任一 `video_create/get/content/cancel` → `video`；
 * ③ 不含对话 modes 且含 `audio_speech`、`audio_transcription_json`、
 *    `realtime_session` 或任一 `audio_job_create/get/content/cancel` → `audio`
 *    （v8-A：短转写与实时会话自对话域移入）；
 * ④ 不含对话 modes 且含 `images_json` → `image`；
 * ⑤ 其余 → `chat`（未命中媒体触发集合的 modes 全量保留，域边界以服务端校验为准）。
 *
 * 入参接受原始字符串数组以对齐迁移读取口径；未知模式不命中任何媒体触发集合、
 * 也不做通配符推断（对齐迁移契约「不能用通配符把未知 video_* 自动视为合法能力」），
 * 按规则⑤归入 `chat`。
 */
export function deriveCapabilityDomain(modes: ReadonlyArray<AccountSupportedEndpointMode | string>): AccountCapabilityDomain {
  const declared = new Set(modes)
  const hasChatMode = DOMAIN_ENDPOINT_MODES_CHAT.some((mode) => declared.has(mode))
  if (!hasChatMode && DOMAIN_ENDPOINT_MODES_VIDEO.some((mode) => declared.has(mode))) return 'video'
  if (!hasChatMode && DOMAIN_ENDPOINT_MODES_AUDIO.some((mode) => declared.has(mode))) return 'audio'
  if (!hasChatMode && DOMAIN_ENDPOINT_MODES_IMAGE.some((mode) => declared.has(mode))) return 'image'
  return 'chat'
}

// 三集合模型。allowed =「域 modes 集 × 供应商写侧词表」求交（本文件底部矩阵），
// default = defaultEndpointModesForAccount 按域收窄，declared = 账户当前 modes（必须 ⊆ allowed）。

/** 允许能力集（allowed）：声明集的上限——账户声明集必须 ⊆ 所在域允许集。 */
export type AccountCapabilityAllowedSet = {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  providerProtocolProfileId?: string
  accountType?: AccountType
  modes: readonly AccountSupportedEndpointMode[]
}

/** 默认能力集（default）：新建账户初始勾选 = 现行 defaultEndpointModesForAccount 按域收窄。M1 类型占位。 */
export type AccountCapabilityDefaultSet = {
  capabilityDomain: AccountCapabilityDomain
  modes: readonly AccountSupportedEndpointMode[]
}

/** 实际声明集（declared）：账户当前生效 modes，必须 ⊆ 所在域允许集；域边界以服务端为准。 */
export type AccountCapabilityDeclaredSet = {
  capabilityDomain: AccountCapabilityDomain
  modes: AccountFormModel['supportedEndpointModes']
}

// ---- 档案×域清单表定案（v7；v8-A 对话域纯粹化修订）镜像：域 modes 集 × 供应商写侧词表求交 ----
//
// 媒体域（video/audio/image）的域 modes 集按「域×供应商合法矩阵」显式枚举，
// 不依赖词表推断——矩阵依据是媒体设计供应商盘点表的「已实施」adapter + 写侧校验
// + 凭据要求（计划 §域×供应商合法矩阵）。chat 域不枚举供应商：允许集 =
// 13 值对话 modes 锚集合 ∩ 供应商词表（v8-A 后不追加 images_json，存量共享、
// 新增隔离），minimax 等纯媒体词表求交后自然为空集（无 chat 域）。

/** 媒体域供应商矩阵行（与计划矩阵行一一对应；deepseek/anthropic 与 gemini OpenAI Chat 档案无媒体行）。 */
type MediaDomainMatrixRow = {
  video: readonly AccountSupportedEndpointMode[]
  audio: readonly AccountSupportedEndpointMode[]
  image: readonly AccountSupportedEndpointMode[]
}

const VIDEO_GROUP: readonly AccountSupportedEndpointMode[] = ['video_create', 'video_get', 'video_content', 'video_cancel']
const AUDIO_JOB_GROUP: readonly AccountSupportedEndpointMode[] = ['audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel']
const AUDIO_SPEECH: readonly AccountSupportedEndpointMode[] = ['audio_speech']
// v8-A 音频域新增 lane：短转写与实时会话（自对话域移入；各供应商行按写侧
// 词表实际支持情况取舍，qwen/gemini/minimax/volcengine 词表无则不进行值）。
const AUDIO_TRANSCRIPTION_JSON: readonly AccountSupportedEndpointMode[] = ['audio_transcription_json']
const REALTIME_SESSION: readonly AccountSupportedEndpointMode[] = ['realtime_session']
const IMAGES_JSON: readonly AccountSupportedEndpointMode[] = ['images_json']

// qwen 音频域仅长音频任务四值（CosyVoice TTS 契约未回填，audio_speech 不进允许集；
// 词表亦无短转写/实时会话值）。
const QWEN_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: AUDIO_JOB_GROUP, image: [] }
// xai：视频（grok imagine）已实施；语音/图像待契约回填，不做预留占位。
const XAI_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: [], image: [] }
// glm：视频（CogVideoX）+ 语音（cogtts 透传 audio_speech）+ STT 透传
// （audio_transcription_json，M6）；词表无 realtime_session。写侧词表不含
// images_json → 无 image 域。
const GLM_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: [...AUDIO_SPEECH, ...AUDIO_TRANSCRIPTION_JSON], image: [] }
// gemini native：Veo adapter（视频）+ TTS adapter（audio_speech）；词表无
// 短转写/实时会话。gemini 图像域另行立项。
const GEMINI_NATIVE_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: AUDIO_SPEECH, image: [] }
// openai/gpt 本体直连：sora（视频）+ tts（audio_speech）+ images；v8-A 后
// 短转写与实时会话归音频域。
const OPENAI_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: [...AUDIO_SPEECH, ...AUDIO_TRANSCRIPTION_JSON, ...REALTIME_SESSION], image: IMAGES_JSON }
// hybrid：媒体映射面已实施，域内允许集含视频四值/audio_speech/images_json
// （映射模型另算，D7）；v8-A 音频域同 openai 行补短转写与实时会话。
const HYBRID_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: [...AUDIO_SPEECH, ...AUDIO_TRANSCRIPTION_JSON, ...REALTIME_SESSION], image: IMAGES_JSON }
// minimax：hailuo（视频）+ t2a_v2（audio_speech）；无 chat/image 域，词表无新值。
const MINIMAX_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: AUDIO_SPEECH, image: [] }
// volcengine：seedance（视频）+ 豆包音频生成 TTS（audio_speech，新版语音
// 控制台 API Key 单值凭据 speech_token）+ seedream 图像（images_json，
// 2026-10-17 图像批开闸，词表新值与后端写侧同批）+ 长转写 audio_job_* 四值
// （2026-10-17 语音识别承接批，豆包录音文件识别 2.0 /v1/audio/jobs 面，
// opt-in 不进默认集）；无 chat 域新值。
const VOLCENGINE_MEDIA_ROW: MediaDomainMatrixRow = { video: VIDEO_GROUP, audio: [...AUDIO_SPEECH, ...AUDIO_JOB_GROUP], image: IMAGES_JSON }

function mediaMatrixRowForProfile(input: {
  providerCode?: string
  profile?: AccountProviderProfileLike
}): MediaDomainMatrixRow | undefined {
  const profile = input.profile
  const providerCode = profile?.providerCode ?? profile?.code ?? input.providerCode
  const profileId = profile?.providerProtocolProfileId ?? profile?.id
  if (isHybridProviderCode(providerCode)) return HYBRID_MEDIA_ROW
  if (isGeminiProviderCode(providerCode)) {
    // 媒体能力挂 native 档案；OpenAI Chat 档案无媒体域（词表含对话族）。
    return profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID || (!profileId && isGeminiProtocolKind(profile))
      ? GEMINI_NATIVE_MEDIA_ROW
      : undefined
  }
  if (isGlmProviderCode(providerCode)) return GLM_MEDIA_ROW
  if (isMinimaxProviderCode(providerCode)) return MINIMAX_MEDIA_ROW
  if (isVolcengineProviderCode(providerCode)) return VOLCENGINE_MEDIA_ROW
  if (isQwenProviderCode(providerCode)) return QWEN_MEDIA_ROW
  if (isXaiProviderCode(providerCode)) return XAI_MEDIA_ROW
  // 「openai / gpt」矩阵行：OpenAI 兼容供应商与 GPT 供应商共用本体词表。
  if (isGptVendorCode(providerCode) || isOpenAICompatibleProviderCode(providerCode)) return OPENAI_MEDIA_ROW
  return undefined
}

function isGeminiProtocolKind(profile?: AccountProviderProfileLike): boolean {
  return profile?.protocolCode === 'gemini' && profile?.protocolVersion === 'v1beta'
}

function intersectWithProviderVocabulary(
  modes: readonly AccountSupportedEndpointMode[],
  profile?: AccountProviderProfileLike
): AccountSupportedEndpointMode[] {
  const vocabulary = new Set(endpointModesForProfile(profile))
  return modes.filter((mode) => vocabulary.has(mode))
}

/** 媒体域可选账户类型 = `{api_key}` ∪ (gemini native: `google_oauth`)（清单表定案）；域内允许集为空（如 gemini 图像域）时返回空。 */
export function capabilityDomainAccountTypes(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: AccountProviderProfileLike
}): AccountType[] {
  if (input.capabilityDomain === 'chat') return []
  const row = mediaMatrixRowForProfile(input)
  if (!row) return []
  const domainModes = input.capabilityDomain === 'video'
    ? row.video
    : input.capabilityDomain === 'audio'
      ? row.audio
      : row.image
  if (!domainModes.length) return []
  const types: AccountType[] = ['api_key']
  const profileId = input.profile?.providerProtocolProfileId ?? input.profile?.id
  if (isGeminiProviderCode(input.profile?.providerCode ?? input.profile?.code ?? input.providerCode)
    && (profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID || (!profileId && isGeminiProtocolKind(input.profile)))) {
    types.push('google_oauth')
  }
  return types
}

/**
 * 允许能力集（allowed）镜像：域 modes 集 × 供应商写侧词表求交（§D6 / §档案×域清单表定案；
 * v8-A 对话域纯粹化：chat 域不再含 images_json / audio_transcription_json / realtime_session，
 * 音频域补后两项）。
 *
 * - chat 域 = 13 值对话锚集合 ∩ 供应商词表；纯媒体词表（minimax 等）求交后为
 *   空集，即该供应商无 chat 域；被移出三项不属于对话域允许集。
 * - 媒体域按域×供应商矩阵取域 modes 集，再与词表求交；gpt/xai/anthropic 系 oauth
 *   不可媒体（账户类型门），gemini native `google_oauth` 在 video/audio 域可用。
 * - 账户类型不在域允许集合内（媒体域传 `oauth`，或 google_oauth 非 gemini native）
 *   返回空集——新建表单按空集拒绝该组合。
 */
export function allowedEndpointModesFor(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: AccountProviderProfileLike
  accountType?: AccountType
}): AccountSupportedEndpointMode[] {
  const { capabilityDomain } = input
  const profile = input.profile
  if (capabilityDomain === 'chat') {
    return intersectWithProviderVocabulary(
      [...DOMAIN_ENDPOINT_MODES_CHAT],
      profile
    )
  }
  const allowedTypes = capabilityDomainAccountTypes({ capabilityDomain, providerCode: input.providerCode, profile })
  if (!input.accountType || !allowedTypes.includes(input.accountType)) return []
  const row = mediaMatrixRowForProfile({ providerCode: input.providerCode, profile })
  if (!row) return []
  const domainModes = capabilityDomain === 'video'
    ? row.video
    : capabilityDomain === 'audio'
      ? row.audio
      : row.image
  return intersectWithProviderVocabulary(domainModes, profile)
}

/**
 * 默认能力集（default）按域收窄：chat 域逐项复用现行 `defaultEndpointModesForAccount`
 * （对话域行为零变化，I3）；video 域 = 视频四值组；audio 域 = 音频组按供应商实际接入
 * （qwen 长音频四值，其余 audio_speech）；image 域 = `images_json`。媒体域默认集
 * 与「服务端统一校验」节的后端缺省解析镜像。v8-A：音频域默认集维持现状语义，
 * 不含新移入的 audio_transcription_json / realtime_session（默认集不收，显式
 * 勾选承担，契约 §媒体域表单完整性升级 v8-B 联动明文）。
 */
export function defaultEndpointModesForCapabilityDomain(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: AccountProviderProfileLike
  type: AccountType
}): AccountSupportedEndpointMode[] {
  if (input.capabilityDomain === 'chat') {
    // BUG-0323 根治镜像（与后端 DefaultEndpointModesForDomain chat 分支同一收
    // 敛点）：chat 域缺省恒 ⊆ 域允许集——现行默认与域内允许集求交非空则原样
    // 保留（openai 族/gemini/anthropic/glm 逐项不变）；现行默认不含对话值
    // （volcengine/qwen 缺省 video_get）落词表内基础对话对。
    const allowed = allowedEndpointModesFor({
      capabilityDomain: 'chat',
      providerCode: input.providerCode,
      profile: input.profile,
      accountType: input.type
    })
    const inDomain = defaultEndpointModesForAccount({
      provider: undefined,
      profile: input.profile,
      type: input.type
    }).filter((mode) => allowed.includes(mode))
    if (inDomain.length > 0) return inDomain
    return allowed.filter((mode) => mode === 'chat_json' || mode === 'chat_sse')
  }
  const allowed = allowedEndpointModesFor({
    capabilityDomain: input.capabilityDomain,
    providerCode: input.providerCode,
    profile: input.profile,
    accountType: input.type
  })
  if (input.capabilityDomain === 'image') return [...allowed]
  if (input.capabilityDomain === 'video') return [...allowed]
  // audio：qwen 长音频四值整组为默认；其余供应商默认 audio_speech。
  return allowed.includes('audio_speech')
    ? ['audio_speech']
    : [...allowed]
}

/**
 * 媒体域新建账户 `health_check_endpoint_mode` 默认落值（生命周期定案表 v7）：
 * image→images_json、audio→audio_speech、video→video_create。各分支均为纯域
 * 常量（镜像后端 DefaultHealthCheckEndpointModeForDomain）：chat/image 域具备
 * 后台探针，音频/视频域账户无后台探针、该字段仅为记录值，一律不随域内默认集
 * 首值漂移——qwen 长音频默认集首值 audio_job_create 不是合法检查形态词表值，
 * 取它会导致保存被「已启用能力交集」校验阻断（R5）。
 */
export function defaultHealthCheckEndpointModeForCapabilityDomain(input: {
  capabilityDomain: AccountCapabilityDomain
}): AccountFormModel['healthCheckEndpointMode'] {
  if (input.capabilityDomain === 'image') return 'images_json'
  if (input.capabilityDomain === 'video') return 'video_create'
  if (input.capabilityDomain === 'audio') return 'audio_speech'
  return 'chat_json'
}

/** 媒体账户生命周期定案（v7）：新建表单默认 status 预选——chat=pending_test（现行），image/audio/video=active。 */
export function defaultStatusForCapabilityDomain(capabilityDomain: AccountCapabilityDomain): AccountFormModel['status'] {
  return capabilityDomain === 'chat' ? 'pending_test' : 'active'
}

/** 域是否媒体域（video/audio/image）：媒体域走精简三段式表单与 D7 清单表语义。 */
export function capabilityDomainIsMedia(capabilityDomain: AccountCapabilityDomain | undefined): boolean {
  return capabilityDomain === 'video' || capabilityDomain === 'audio' || capabilityDomain === 'image'
}

/**
 * 媒体域支持模型选项的目录协议 token（模型目录 `supported_api_protocols` 词表，
 * 与 model_catalog_data_gen.go 种子数据同源）：video→`video`；audio→`audio_speech`
 * （TTS）∪ `audio_transcription`（长转写任务）∪ `realtime`（实时语音会话，v8 起
 * audio 域承载 realtime_session lane）；image→`images`。
 */
export function mediaDomainModelProtocolTokens(capabilityDomain: AccountCapabilityDomain): readonly string[] {
  if (capabilityDomain === 'video') return ['video']
  if (capabilityDomain === 'audio') return ['audio_speech', 'audio_transcription', 'realtime']
  if (capabilityDomain === 'image') return ['images']
  return []
}

/**
 * 媒体域新建默认供应商收敛（v8-D）：在域过滤（与媒体表单供应商下拉同一判据，
 * `capabilityDomainAccountTypes` 按档案求非空允许集）后的启用供应商里，返回
 * 首个「该域有在售目录模型」的供应商编码；候选顺序与传入清单一致（列表页
 * 供应商序）。交集为空或传入对话域时返回 undefined——对话域默认供应商行为
 * 零变化（全局规则「GPT 优先、否则首个启用」），调用方对 undefined 保持既有
 * 默认值，不做打开后异步改选。
 */
export function preferredMediaDomainProviderCode(input: {
  capabilityDomain: AccountCapabilityDomain
  providers: ReadonlyArray<Pick<ProviderDefinition, 'code' | 'enabled' | 'protocolProfiles'>>
  providerCodesWithDomainModels: ReadonlySet<string>
}): string | undefined {
  if (input.capabilityDomain === 'chat') return undefined
  for (const provider of input.providers) {
    if (!provider.enabled) continue
    if (!input.providerCodesWithDomainModels.has(provider.code)) continue
    const inDomain = provider.protocolProfiles.some((profile) => (
      capabilityDomainAccountTypes({
        capabilityDomain: input.capabilityDomain,
        providerCode: provider.code,
        profile
      }).length > 0
    ))
    if (inDomain) return provider.code
  }
  return undefined
}
