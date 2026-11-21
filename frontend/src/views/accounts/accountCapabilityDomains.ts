import type { AccountSupportedEndpointMode, AccountType, ProviderDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import { GEMINI_NATIVE_V1BETA_PROFILE_ID, isGeminiProviderCode, isGlmProviderCode, isGptVendorCode, isMinimaxProviderCode, isQwenProviderCode, isVolcengineProviderCode, isXaiProviderCode } from '@/shared/providerProtocol'
import { defaultEndpointModesForAccount, endpointModesForProfile, type AccountProviderProfileLike } from './accountProviderCapabilities'

// AI 账户能力域拆分（docs/plans/计划-20261011T141004000Z-AI账户能力域拆分.md
// §域枚举与语义 / §D6 三集合模型 / §M1 清单「前端：域常量 + 推导镜像 + 三集合常量」）。
// 本文件是 M1 纯常量层：只承载域枚举、对话 modes 锚集合、域推导镜像与三集合类型，
// 不接线任何 UI/表单消费方（保存契约与勾选 UI 由 M2 接线）。

/** 能力域枚举：`accounts.capability_domain` 列 CHECK 的六值（chat/embedding/rerank/video/audio/image）。 */
export type AccountCapabilityDomain = 'chat' | 'embedding' | 'rerank' | 'video' | 'audio' | 'image'

export const ACCOUNT_CAPABILITY_DOMAINS: readonly AccountCapabilityDomain[] = [
  'chat',
  'embedding',
  'rerank',
  'video',
  'audio',
  'image'
]

export const ACCOUNT_CAPABILITY_DOMAIN_LABELS: Record<AccountCapabilityDomain, string> = {
  chat: '对话',
  embedding: '向量',
  rerank: '重排',
  video: '视频',
  audio: '音频',
  image: '图像'
}

export function accountCapabilityDomainLabel(domain: AccountCapabilityDomain): string {
  return ACCOUNT_CAPABILITY_DOMAIN_LABELS[domain]
}

/**
 * 「上游接口能力」声明集是否为该域的配置面（2026-11-21 五域退役，AI账户上游接口
 * 能力设计 §2/§3）：仅 chat 域账户持有声明集；图像/音频/视频/向量/重排五域前端
 * 不渲染勾选区、不装载、payload 不携带，域闸门是唯一的候选域隔离防线。undefined
 * 视作 chat（缺省域口径，与列表读通道一致）。
 */
export function capabilityDomainSupportsDeclaration(capabilityDomain: AccountCapabilityDomain | undefined): boolean {
  return (capabilityDomain ?? 'chat') === 'chat'
}

/** 存量迁移推导规则版本；与迁移阶段记录 `account_capability_domain_migration` 的 ruleVersion 同源。 */
export const ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION = 2

// 「对话 modes 集合」（锚定义，前后端镜像）：命中任一值即视为具备对话能力，
// 推导规则的前置条件「不含对话 modes」被否决，账户归入对话域。
// v8-A 对话域纯粹化：audio_transcription_json 与 realtime_session 移出对话域
// （归音频域）；images_json 本就不在锚集且 v8-A 后对话域不再追加共享（存量
// 共享、新增隔离）。2026-11-20 协议补齐批纳入的 embeddings_json / rerank_json
// 已随向量/重排独立能力域拆分（2026-11-21 用户裁决）移出本集合（15→13）——
// 两协议各自升级为单值 lane 的独立域（与后端 capabilitydomain.chatModes v2 同步）。
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

// 向量/重排独立域的单值 lane（与后端 capabilitydomain v2 域 lane 表同步）：
// /v1/embeddings 候选只命中 embedding 域账户、/v1/rerank 候选只命中 rerank 域
// 账户的效果经由「chat 账户不再能声明两 mode + 新域账户声明单 mode」达成。
export const DOMAIN_ENDPOINT_MODES_EMBEDDING: readonly AccountSupportedEndpointMode[] = ['embeddings_json']
export const DOMAIN_ENDPOINT_MODES_RERANK: readonly AccountSupportedEndpointMode[] = ['rerank_json']

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
 * 域→目录 mode 白名单（v8-C 模型选择器按域过滤根治）：六域允许选出的
 * `provider_model_catalog.mode` 词表值（与 model_catalog_data_gen.go
 * 种子及服务端 `ProviderCatalogModeValues` 同源）：chat={chat,responses}
 * （向量/重排目录 mode 已随 2026-11-21 拆分移出对话域，各自独立成域）；
 * embedding={embedding}；rerank={rerank}；video={video}；audio={audio}（含
 * TTS/转写/realtime，目录统一 mode=audio）；image={image_generation}。
 * 自定义表 `custom_provider_models.mode` 是另一套六值 UI 分类词表
 * （text/embedding/rerank/image/audio/video，写入枚举固定），服务端按表翻译后
 * 过滤（chat/responses→text、image_generation→image、audio/video 同形，
 * BUG-0324）：调用方始终传本表值，不做本地换算。
 */
export const DOMAIN_MODEL_MODES: Record<AccountCapabilityDomain, readonly string[]> = {
  chat: ['chat', 'responses'],
  embedding: ['embedding'],
  rerank: ['rerank'],
  video: ['video'],
  audio: ['audio'],
  image: ['image_generation']
}

/**
 * 按 modes 推导能力域。与 maintenance capabilitydomain 包 ruleVersion=2 镜像
 * （backend-go/shared/platform/capabilitydomain/capabilitydomain.go `DeriveDomain`），
 * 改动须双侧同步。规则按序命中即停（ruleVersion=2）：
 *
 * ① 授权实例 → 继承源账户域（前端无此场景，镜像省略）；
 * ② 不含对话 modes 与任何媒体触发值，含 `embeddings_json` 且**不含 `rerank_json`**
 *    → `embedding`（向量/重排独立域 v2 新增；两新域声明互斥）；
 * ③ 同前提，含 `rerank_json` 且不含 `embeddings_json` → `rerank`；
 * ④ 不含对话 modes 且**不含任一新域 mode**，含任一 `video_create/get/content/cancel`
 *    → `video`；
 * ⑤ 同前提含 `audio_speech`、`audio_transcription_json`、`realtime_session` 或任一
 *    `audio_job_create/get/content/cancel` → `audio`（v8-A：短转写与实时会话自对话域移入）；
 * ⑥ 同前提含 `images_json` → `image`；
 * ⑦ 其余 → `chat`（未命中触发集合的 modes 全量保留，域边界以服务端校验为准）。
 *
 * 混合声明保护（后端 DeriveDomain :238-247 同构）：向量/重排与对话生成形态、
 * 媒体触发值或彼此同账户声明时，一律照 v1 落 `chat`，由迁移失败清单识别人工
 * 处理（设计 §4）——落媒体域的行不会被失败清单标记，故媒体分支必须排除
 * 含新域 mode 的输入。
 *
 * 入参接受原始字符串数组以对齐迁移读取口径；未知模式不命中任何触发集合、
 * 也不做通配符推断（对齐迁移契约「不能用通配符把未知 video_* 自动视为合法能力」），
 * 按规则⑦归入 `chat`。
 */
export function deriveCapabilityDomain(modes: ReadonlyArray<AccountSupportedEndpointMode | string>): AccountCapabilityDomain {
  const declared = new Set(modes)
  const hasChatMode = DOMAIN_ENDPOINT_MODES_CHAT.some((mode) => declared.has(mode))
  const hasMediaMode = DOMAIN_ENDPOINT_MODES_VIDEO.some((mode) => declared.has(mode))
    || DOMAIN_ENDPOINT_MODES_AUDIO.some((mode) => declared.has(mode))
    || DOMAIN_ENDPOINT_MODES_IMAGE.some((mode) => declared.has(mode))
  const hasEmbedding = declared.has('embeddings_json')
  const hasRerank = declared.has('rerank_json')
  // 规则②③互斥：只有纯单值新域声明落独立域，彼此混合落 chat。
  if (!hasChatMode && !hasMediaMode) {
    if (hasEmbedding && !hasRerank) return 'embedding'
    if (hasRerank && !hasEmbedding) return 'rerank'
  }
  // 媒体分支排除任何新域 mode：含向量/重排声明的混合声明照 v1 落 chat。
  if (!hasChatMode && !hasEmbedding && !hasRerank) {
    if (DOMAIN_ENDPOINT_MODES_VIDEO.some((mode) => declared.has(mode))) return 'video'
    if (DOMAIN_ENDPOINT_MODES_AUDIO.some((mode) => declared.has(mode))) return 'audio'
    if (DOMAIN_ENDPOINT_MODES_IMAGE.some((mode) => declared.has(mode))) return 'image'
  }
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
// 不依赖词表推断——矩阵依据是媒体设计供应商盘点表的「已实施」adapter + 写侧
// 校验 + 凭据要求（计划 §域×供应商合法矩阵）。chat 域与向量/重排域不枚举供应商：
// 允许集 = 域 lane（chat 13 值锚集合、embedding/rerank 单值 lane）∩ 供应商词表
// （v8-A 后不追加 images_json，存量共享、新增隔离；向量/重排域词表依据见
// allowedEndpointModesFor 注释），minimax 等纯媒体词表求交后自然为空集（无 chat 域）。

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
  // GPT 本体直连的媒体矩阵行。
  if (isGptVendorCode(providerCode)) return OPENAI_MEDIA_ROW
  return undefined
}

function isGeminiProtocolKind(profile?: AccountProviderProfileLike): boolean {
  return profile?.protocolCode === 'gemini' && profile?.protocolVersion === 'v1beta'
}

function intersectWithProviderVocabulary(
  modes: readonly AccountSupportedEndpointMode[],
  profile?: AccountProviderProfileLike,
  accountType?: AccountType,
  capabilityDomain?: AccountCapabilityDomain
): AccountSupportedEndpointMode[] {
  const vocabulary = new Set(endpointModesForProfile(profile, accountType, capabilityDomain))
  return modes.filter((mode) => vocabulary.has(mode))
}

/**
 * 域可选账户类型（清单表定案）：媒体域 = `{api_key}` ∪ (gemini native:
 * `google_oauth`)；向量/重排域 = `{api_key}` 按「域 lane × 供应商词表」求交非空
 * 才保留（§5 矩阵；oauth 词表收窄为 responses 对、gemini native 词表无两 mode，
 * 与单值 lane 恒不相交，自然被过滤）；域内允许集为空（如 gemini 图像域）时返回空。
 */
export function capabilityDomainAccountTypes(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: AccountProviderProfileLike
}): AccountType[] {
  if (input.capabilityDomain === 'embedding' || input.capabilityDomain === 'rerank') {
    const lane = input.capabilityDomain === 'embedding' ? DOMAIN_ENDPOINT_MODES_EMBEDDING : DOMAIN_ENDPOINT_MODES_RERANK
    const types: AccountType[] = ['api_key']
    return types.filter((type) => intersectWithProviderVocabulary(lane, input.profile, type, input.capabilityDomain).length > 0)
  }
  if (input.capabilityDomain === 'chat') return []
  const row = mediaMatrixRowForProfile(input)
  if (!row) return []
  const domainModes = input.capabilityDomain === 'video'
    ? row.video
    : input.capabilityDomain === 'audio'
      ? row.audio
      : row.image
  if (!domainModes.length) return []
  const providerCode = input.profile?.providerCode ?? input.profile?.code ?? input.providerCode
  // volcengine 的域能力由预设档案 capabilities 声明（Ark: chat/video/image；
  // OpenSpeech: audio）。endpointModesForProfile 的 volcengine 分支会为任意档案
  // 追加 images_json，单靠词表求交会把 OpenSpeech 误判为可建图像户；先按档案
  // 能力域否决，再进入词表求交。
  if (isVolcengineProviderCode(providerCode)
    && input.profile?.capabilities?.length
    && !input.profile.capabilities.includes(input.capabilityDomain)) return []
  const types: AccountType[] = ['api_key']
  const profileId = input.profile?.providerProtocolProfileId ?? input.profile?.id
  if (isGeminiProviderCode(providerCode)
    && (profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID || (!profileId && isGeminiProtocolKind(input.profile)))) {
    types.push('google_oauth')
  }
  // 档案×域过滤：矩阵行只表达供应商域能力，具体档案未必承接该域（如 volcengine
  // Ark 无 audio、OpenSpeech 只有 audio）。按档案写侧词表求交，非空才保留该类型，
  // 保证创建类型卡片与保存链 registry 组合校验一致。
  return types.filter((type) => intersectWithProviderVocabulary(domainModes, input.profile, type).length > 0)
}

/**
 * 允许能力集（allowed）镜像：域 modes 集 × 供应商写侧词表求交（§D6 / §档案×域清单表定案；
 * v8-A 对话域纯粹化：chat 域不再含 images_json / audio_transcription_json / realtime_session，
 * 音频域补后两项；2026-11-21 拆分后 chat 锚集合 13 值，向量/重排独立成域）。
 *
 * **2026-11-21 五域退役（AI账户上游接口能力设计 §3）**：声明集仅 chat 域持有，
 * 图像/音频/视频/向量/重排五域恒返回空集——五域不再有「允许集」概念，域闸门是
 * 唯一的候选域隔离防线；五域创建墙（供应商×类型过滤）由 `capabilityDomainAccountTypes`
 * 的域×供应商矩阵承担，不经本函数。
 *
 * - chat 域 = 13 值对话锚集合 ∩ 供应商词表；纯媒体词表（minimax 等）求交后为
 *   空集，即该供应商无 chat 域；被移出项（含 embeddings_json / rerank_json）
 *   不属于对话域允许集。
 */
export function allowedEndpointModesFor(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: AccountProviderProfileLike
  accountType?: AccountType
}): AccountSupportedEndpointMode[] {
  const { capabilityDomain } = input
  const profile = input.profile
  // 五域退役：声明集不进表单、不进校验，允许集恒为空。
  if (capabilityDomain !== 'chat') return []
  return intersectWithProviderVocabulary(
    [...DOMAIN_ENDPOINT_MODES_CHAT],
    profile,
    input.accountType,
    'chat'
  )
}

/**
 * 默认能力集（default）按域收窄：chat 域逐项复用现行 `defaultEndpointModesForAccount`
 * （对话域行为零变化，I3）。**2026-11-21 五域退役（AI账户上游接口能力设计 §3）**：
 * 五域创建不播种默认集，恒返回空集——表单与 payload 均不写声明集。
 */
export function defaultEndpointModesForCapabilityDomain(input: {
  capabilityDomain: AccountCapabilityDomain
  providerCode?: string
  profile?: AccountProviderProfileLike
  type: AccountType
}): AccountSupportedEndpointMode[] {
  // 五域退役：创建不播种默认集。
  if (input.capabilityDomain !== 'chat') return []
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

/**
 * 五域与 chat 新建/展示口径的 `health_check_endpoint_mode` 域常量默认（生命周期
 * 定案表 v7 + 2026-11-21 拆分 + 同日五域退役）：chat→chat_sse（流式优先）、
 * image→images_json、audio→audio_speech、video→video_create、
 * embedding→embeddings_json、rerank→rerank_json。与后端
 * `DefaultHealthCheckEndpointModeForDomain` 同值；五域声明集退役后该字段是连接
 * 测试记录值，按域常量落值/展示，不再按声明集收敛。
 */
export function defaultHealthCheckEndpointModeForCapabilityDomain(input: {
  capabilityDomain: AccountCapabilityDomain
}): AccountFormModel['healthCheckEndpointMode'] {
  if (input.capabilityDomain === 'image') return 'images_json'
  if (input.capabilityDomain === 'video') return 'video_create'
  if (input.capabilityDomain === 'audio') return 'audio_speech'
  if (input.capabilityDomain === 'embedding') return 'embeddings_json'
  if (input.capabilityDomain === 'rerank') return 'rerank_json'
  // 对话域流式优先（AI账户检查模型与人工测试设计 2026-10-19 定案，与后端
  // accountscore.DefaultHealthCheckEndpointModeForDomain 同值）。
  return 'chat_sse'
}

/** 媒体账户新建表单默认 status：开启后台探测后所有域统一 pending_test，等待首次检查。 */
export function defaultStatusForCapabilityDomain(_capabilityDomain: AccountCapabilityDomain): AccountFormModel['status'] {
  return 'pending_test'
}

/**
 * 域是否媒体域（video/audio/image）：媒体域供应商/类型按域矩阵过滤。向量/重排域
 * （2026-11-21 拆分）是文本域，与 chat 同走「不过滤供应商清单」路径，不属媒体域。
 */
export function capabilityDomainIsMedia(capabilityDomain: AccountCapabilityDomain | undefined): boolean {
  return capabilityDomain === 'video' || capabilityDomain === 'audio' || capabilityDomain === 'image'
}

// ---- 账户测试方式（2026-11-21 定案，与后端 capabilitydomain.TestKind* 镜像） ----

/** model = 真实对话生成形态，需要模型 + 请求形态；connection = 只验证凭证与上游可达性。 */
export type AccountTestKind = 'model' | 'connection'

/** 真实对话生成形态（chatModes 去掉 count_tokens/embed_content/message_token_counting/embeddings_json/rerank_json）。 */
export const MODEL_TEST_ENDPOINT_MODES: readonly AccountSupportedEndpointMode[] = [
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse',
  'interactions_json',
  'interactions_sse'
]

const MODEL_TEST_ENDPOINT_MODE_SET = new Set<string>(MODEL_TEST_ENDPOINT_MODES)

export function testKindForEndpointModes(
  modes: ReadonlyArray<AccountSupportedEndpointMode | string>
): AccountTestKind {
  return modes.some((mode) => MODEL_TEST_ENDPOINT_MODE_SET.has(mode)) ? 'model' : 'connection'
}

/** 账户测试方式：媒体域与向量/重排域恒连接测试（T214 边界延续，无人工测试请求模板）；chat 域按声明形态判定。 */
export function testKindForAccount(
  capabilityDomain: AccountCapabilityDomain | undefined,
  modes: ReadonlyArray<AccountSupportedEndpointMode | string>
): AccountTestKind {
  if (capabilityDomain === 'video' || capabilityDomain === 'audio' || capabilityDomain === 'image'
    || capabilityDomain === 'embedding' || capabilityDomain === 'rerank') {
    return 'connection'
  }
  return testKindForEndpointModes(modes)
}

/** 账户是否走连接测试（媒体域、向量/重排域，或只声明输入型形态的 chat 账户）。 */
export function accountUsesConnectionTest(
  capabilityDomain: AccountCapabilityDomain | undefined,
  modes: ReadonlyArray<AccountSupportedEndpointMode | string>
): boolean {
  return testKindForAccount(capabilityDomain, modes) === 'connection'
}

/**
 * 非对话域支持模型选项的目录协议 token（模型目录 `supported_api_protocols` 词表，
 * 与 model_catalog_data_gen.go 种子数据同源）：video→`video`；audio→`audio_speech`
 * （TTS）∪ `audio_transcription`（长转写任务）∪ `realtime`（实时语音会话，v8 起
 * audio 域承载 realtime_session lane）；image→`images`；embedding→`embeddings`、
 * rerank→`rerank`（2026-11-21 拆分；两域的模型下拉实际走 chat 路径的协议层过滤
 * ——providerProfileModelProtocols 按域参数化后的档案可声明 mode 反推协议，本表
 * 仅作 token 映射镜像与媒体路径二次保险）。
 */
export function mediaDomainModelProtocolTokens(capabilityDomain: AccountCapabilityDomain): readonly string[] {
  if (capabilityDomain === 'video') return ['video']
  if (capabilityDomain === 'audio') return ['audio_speech', 'audio_transcription', 'realtime']
  if (capabilityDomain === 'image') return ['images']
  if (capabilityDomain === 'embedding') return ['embeddings']
  if (capabilityDomain === 'rerank') return ['rerank']
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
  // 对话域与向量/重排文本域不做媒体收敛（向量/重排供应商清单同 chat 路径，§8.2）。
  if (!capabilityDomainIsMedia(input.capabilityDomain)) return undefined
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
