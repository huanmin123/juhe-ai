import type { AccountSupportedEndpointMode, AccountType } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'

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
  'message_token_counting',
  'realtime_session',
  'audio_transcription_json'
]

// 规则②③④的媒体触发集合（整组声明语义：视频四值、长音频四值按组出现）。
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
  'audio_job_cancel'
]
export const DOMAIN_ENDPOINT_MODES_IMAGE: readonly AccountSupportedEndpointMode[] = ['images_json']

// 对话域协议面可选项（域模型设计「对话」行：协议面可选 images_json /
// audio_transcription_json / realtime_session）。后两项已含于对话 modes 锚集合，
// images_json 是对话域内唯一锚外的协议面可选能力。M2「上游接口能力」域内单项
// 勾选按对话族（档案过滤）+ 本集合渲染；M1 仅落常量。
export const CHAT_DOMAIN_PROTOCOL_FACE_OPTIONAL_MODES: readonly AccountSupportedEndpointMode[] = [
  'images_json',
  'audio_transcription_json',
  'realtime_session'
]

/**
 * 按 modes 推导能力域。与 maintenance capabilitydomain 包 ruleVersion=1 镜像，
 * 改动须双侧同步。规则按序命中即停（ruleVersion=1）：
 *
 * ① 授权实例 → 继承源账户域（前端无此场景，镜像省略）；
 * ② 不含对话 modes 且含任一 `video_create/get/content/cancel` → `video`；
 * ③ 不含对话 modes 且含 `audio_speech` 或任一 `audio_job_create/get/content/cancel` → `audio`；
 * ④ 不含对话 modes 且含 `images_json` → `image`；
 * ⑤ 其余 → `chat`（modes 全量保留，域外值由三集合保存契约承载）。
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

// 三集合模型（§D6）。M1 只落类型与 Chat 域协议面可选项常量；
// allowed 的「域 × 供应商档案 × 账户类型」完整矩阵推导与 default 的
// defaultEndpointModesForAccount 按域收窄均由 M2 接线。

/** 允许能力集（allowed）：新建及普通编辑可新增声明的上限；不包含仅为兼容保留的遗留值。 */
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

/** 实际声明集（declared）：账户当前生效 modes，可含域外遗留值（存量兼容）；遗留集合以服务端数据库现值为准。 */
export type AccountCapabilityDeclaredSet = {
  capabilityDomain: AccountCapabilityDomain
  modes: AccountFormModel['supportedEndpointModes']
}
