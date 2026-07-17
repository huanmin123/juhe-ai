import type { JSONContent } from '@tiptap/core'
import type { ChatMessageStatus } from '@/types/domain/chat'

const legacyStorageKey = 'juhe-ai:chat:pending-submission:v1'
const storageKeyPrefix = 'juhe-ai:chat:pending-submission:v2:'
const storageVersion = 2

export interface ChatPendingSubmission {
  request: {
    systemAccountId: string
    conversationId: string
    clientMessageId: string
    replaceTurnId?: string
    snapshot: JSONContent
  }
  streamStarted: boolean
  startedTurnId?: string
  acceptedAssistantStatus?: ChatMessageStatus
  silent: boolean
  errorMessage: string
}

interface StoredChatPendingSubmission extends ChatPendingSubmission {
  version: typeof storageVersion
}

export function chatPendingSubmissionStorageKey(systemAccountId: string): string {
  const normalized = strictIdentifier(systemAccountId)
  if (!normalized) throw new TypeError('systemAccountId 无效')
  return `${storageKeyPrefix}${encodeURIComponent(normalized)}`
}

export function writeChatPendingSubmission(storage: Storage, value: ChatPendingSubmission): boolean {
  removeLegacyEntry(storage)
  const systemAccountId = strictIdentifier(value?.request?.systemAccountId)
  if (!systemAccountId) return false
  const stored = sanitizeStoredSubmission({ ...value, version: storageVersion }, systemAccountId)
  if (!stored) return false
  try {
    storage.setItem(chatPendingSubmissionStorageKey(systemAccountId), JSON.stringify(stored))
    return true
  } catch {
    return false
  }
}

export function readChatPendingSubmission(storage: Storage, systemAccountId: string): ChatPendingSubmission | undefined {
  removeLegacyEntry(storage)
  const normalized = strictIdentifier(systemAccountId)
  if (!normalized) return undefined
  const key = chatPendingSubmissionStorageKey(normalized)
  try {
    const raw = storage.getItem(key)
    if (!raw) return undefined
    const stored = sanitizeStoredSubmission(JSON.parse(raw), normalized)
    if (!stored) {
      storage.removeItem(key)
      return undefined
    }
    return cloneSubmission(stored)
  } catch {
    try { storage.removeItem(key) } catch {}
    return undefined
  }
}

export function clearChatPendingSubmission(storage: Storage, systemAccountId: string): void {
  removeLegacyEntry(storage)
  const normalized = strictIdentifier(systemAccountId)
  if (!normalized) return
  try { storage.removeItem(chatPendingSubmissionStorageKey(normalized)) } catch {}
}

function sanitizeStoredSubmission(value: unknown, expectedSystemAccountId: string): StoredChatPendingSubmission | undefined {
  if (!isRecord(value) || value.version !== storageVersion || !isRecord(value.request)) return undefined
  const systemAccountId = strictIdentifier(value.request.systemAccountId)
  const conversationId = strictIdentifier(value.request.conversationId)
  const clientMessageId = strictIdentifier(value.request.clientMessageId)
  const replaceTurnId = optionalIdentifier(value.request.replaceTurnId)
  const startedTurnId = optionalIdentifier(value.startedTurnId)
  const acceptedAssistantStatus = optionalMessageStatus(value.acceptedAssistantStatus)
  const snapshot = cloneAndValidateSnapshot(value.request.snapshot)
  if (systemAccountId !== expectedSystemAccountId || !conversationId || !clientMessageId || !snapshot) return undefined
  if (value.request.replaceTurnId !== undefined && !replaceTurnId) return undefined
  if (value.startedTurnId !== undefined && !startedTurnId) return undefined
  if (value.acceptedAssistantStatus !== undefined && !acceptedAssistantStatus) return undefined
  if (typeof value.streamStarted !== 'boolean' || typeof value.silent !== 'boolean' || typeof value.errorMessage !== 'string') return undefined
  return {
    version: storageVersion,
    request: {
      systemAccountId,
      conversationId,
      clientMessageId,
      replaceTurnId,
      snapshot
    },
    streamStarted: value.streamStarted,
    startedTurnId,
    acceptedAssistantStatus,
    silent: value.silent,
    errorMessage: value.errorMessage
  }
}

function cloneSubmission(value: StoredChatPendingSubmission): ChatPendingSubmission {
  return {
    request: {
      systemAccountId: value.request.systemAccountId,
      conversationId: value.request.conversationId,
      clientMessageId: value.request.clientMessageId,
      replaceTurnId: value.request.replaceTurnId,
      snapshot: cloneAndValidateSnapshot(value.request.snapshot)!
    },
    streamStarted: value.streamStarted,
    startedTurnId: value.startedTurnId,
    acceptedAssistantStatus: value.acceptedAssistantStatus,
    silent: value.silent,
    errorMessage: value.errorMessage
  }
}

function cloneAndValidateSnapshot(value: unknown): JSONContent | undefined {
  try {
    const clone = JSON.parse(JSON.stringify(value)) as unknown
    return isJsonContent(clone) && clone.type === 'doc' ? clone : undefined
  } catch {
    return undefined
  }
}

function isJsonContent(value: unknown): value is JSONContent {
  if (!isRecord(value) || typeof value.type !== 'string' || !value.type) return false
  if (value.text !== undefined && typeof value.text !== 'string') return false
  if (value.attrs !== undefined && !isJsonObject(value.attrs)) return false
  if (value.content !== undefined && (!Array.isArray(value.content) || !value.content.every(isJsonContent))) return false
  if (value.marks !== undefined && (!Array.isArray(value.marks) || !value.marks.every(isJsonMark))) return false
  return true
}

function isJsonMark(value: unknown): boolean {
  return isRecord(value)
    && typeof value.type === 'string'
    && Boolean(value.type)
    && (value.attrs === undefined || isJsonObject(value.attrs))
}

function isJsonObject(value: unknown): boolean {
  return isRecord(value) && Object.values(value).every(isJsonValue)
}

function isJsonValue(value: unknown): boolean {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return true
  if (typeof value === 'number') return Number.isFinite(value)
  if (Array.isArray(value)) return value.every(isJsonValue)
  return isJsonObject(value)
}

function strictIdentifier(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 && value.length <= 256 && value === value.trim()
    ? value
    : undefined
}

function optionalIdentifier(value: unknown): string | undefined {
  return value === undefined ? undefined : strictIdentifier(value)
}

function optionalMessageStatus(value: unknown): ChatMessageStatus | undefined {
  return value === undefined || value === 'streaming' || value === 'completed' || value === 'failed' || value === 'canceled'
    ? value
    : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function removeLegacyEntry(storage: Storage): void {
  try { storage.removeItem(legacyStorageKey) } catch {}
}
