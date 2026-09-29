/**
 * 会话偏好存储（工具体系设计 §10.6，纯前端、无 schema 变更）：localStorage
 * 维护「上次使用的会话配置」（账户、模型、搜索绑定、生图绑定、默认图像模型），
 * 账户/模型/绑定变更成功即更新；新建会话进入后读取并按候选校验后应用。
 * 偏好不跨设备同步，清除浏览器数据即重置。
 */
export const CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY = 'juhe-ai:chat:conversation-preferences:v1'

export interface ChatConversationSearchBindingPreference {
  accountId: string
  modelId: string
}

export interface ChatConversationImageBindingPreference {
  accountId: string
}

export interface ChatConversationPreferences {
  accountId: string | null
  model: string | null
  searchBinding: ChatConversationSearchBindingPreference | null
  imageBinding: ChatConversationImageBindingPreference | null
  defaultImageModel: string | null
}

/** upsert 补丁：仅出现的字段被写入，未出现（undefined）保持原值。 */
export type ChatConversationPreferencesPatch = Partial<ChatConversationPreferences>

function resolveDefaultChatPreferenceStorage(): Storage | undefined {
  try {
    return typeof window === 'undefined' ? undefined : window.localStorage
  } catch {
    return undefined
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function readNullableString(source: Record<string, unknown>, key: string): string | null | undefined {
  if (!(key in source)) return undefined
  const value = source[key]
  if (value === null) return null
  return typeof value === 'string' ? value : null
}

function readSearchBinding(value: unknown): ChatConversationSearchBindingPreference | null | undefined {
  if (value === undefined) return undefined
  if (value === null) return null
  if (!isRecord(value)) return null
  const accountId = value.accountId
  const modelId = value.modelId
  return typeof accountId === 'string' && typeof modelId === 'string' ? { accountId, modelId } : null
}

function readImageBinding(value: unknown): ChatConversationImageBindingPreference | null | undefined {
  if (value === undefined) return undefined
  if (value === null) return null
  if (!isRecord(value)) return null
  const accountId = value.accountId
  return typeof accountId === 'string' ? { accountId } : null
}

/** 合并纯函数：patch 未出现的字段沿用 base（base 为 null 时视为全空偏好）。 */
export function mergeConversationPreferences(
  base: ChatConversationPreferences | null,
  patch: ChatConversationPreferencesPatch
): ChatConversationPreferences {
  const source = base ?? {
    accountId: null,
    model: null,
    searchBinding: null,
    imageBinding: null,
    defaultImageModel: null
  }
  return {
    accountId: patch.accountId !== undefined ? patch.accountId : source.accountId,
    model: patch.model !== undefined ? patch.model : source.model,
    searchBinding: patch.searchBinding !== undefined ? patch.searchBinding : source.searchBinding,
    imageBinding: patch.imageBinding !== undefined ? patch.imageBinding : source.imageBinding,
    defaultImageModel: patch.defaultImageModel !== undefined ? patch.defaultImageModel : source.defaultImageModel
  }
}

/** 解析纯函数：空值/坏 JSON/形状不符返回 null，不抛（调用方按「无偏好」处理）。 */
export function parseConversationPreferences(raw: string | null): ChatConversationPreferences | null {
  if (!raw) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (!isRecord(parsed)) return null
  return {
    accountId: readNullableString(parsed, 'accountId') ?? null,
    model: readNullableString(parsed, 'model') ?? null,
    searchBinding: readSearchBinding(parsed.searchBinding) ?? null,
    imageBinding: readImageBinding(parsed.imageBinding) ?? null,
    defaultImageModel: readNullableString(parsed, 'defaultImageModel') ?? null
  }
}

/** 读取偏好：存储不可用或解析失败返回 null，不抛。 */
export function loadConversationPreferences(storage: Storage | undefined = resolveDefaultChatPreferenceStorage()): ChatConversationPreferences | null {
  if (!storage) return null
  try {
    return parseConversationPreferences(storage.getItem(CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY))
  } catch {
    return null
  }
}

/** upsert 写入：与既有偏好合并后落盘；存储不可用/写入失败返回 false，不抛。 */
export function saveConversationPreferences(
  patch: ChatConversationPreferencesPatch,
  storage: Storage | undefined = resolveDefaultChatPreferenceStorage()
): boolean {
  if (!storage) return false
  try {
    const next = mergeConversationPreferences(loadConversationPreferences(storage), patch)
    storage.setItem(CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY, JSON.stringify(next))
    return true
  } catch {
    return false
  }
}
