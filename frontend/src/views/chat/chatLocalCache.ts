import type { ChatConversation, ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'
import { serverDateTimeTimestamp } from '@/shared/formatters'

const DEFAULT_BUDGET = 64 * 1024 ** 2
const MAX_BUDGET = 256 * 1024 ** 2
const MAX_MESSAGE_PAGE = 200
const EVICTION_BATCH = 16
const MAX_EVICTION_SCAN = 64
const MAX_PERSISTED_STRING_BYTES = 2 * 1024 ** 2
const DB_NAME = 'juhe-ai-chat-cache'
const DB_VERSION = 2
const TOTAL_METADATA_ID = 'total'

export interface ChatCacheConversationHead {
  systemAccountId: string
  conversationId: string
  messageRevision: number
  lastAccessAt: number
  byteSize: number
  title?: string
  isPinned?: boolean
  lastModel?: string
  defaultImageModel?: ChatConversation['defaultImageModel']
  activeTurnId?: string
  userTurnCount?: number
  lastMessageAt?: string
  createdAt?: string
  updatedAt?: string
  projectionEventVersion?: number
  projectionStatus?: ChatMessage['status']
  projectionTurnId?: string
  projectionAssistantMessageId?: string
}

export interface ChatRunningTurn {
  systemAccountId: string
  conversationId: string
  turnId: string
  assistantMessageId?: string
  startedAt: string
  eventVersion?: number
  status?: ChatMessage['status']
}

export interface ChatCacheProjectionWatermark {
  eventVersion?: number
  status: ChatMessage['status']
  turnId: string
  assistantMessageId: string
}

export interface ChatCachePutContext { now: number }
export interface ChatCacheEvictionCursor { lastAccessAt: number; systemAccountId: string; conversationId: string }
export interface ChatCachePutResult { head: ChatCacheConversationHead; totalBytes: number }
export interface ChatCacheSyncCommitResult extends ChatCachePutResult { committed: boolean }
export interface ChatCacheSyncSnapshot {
  systemAccountId: string
  conversationId: string
  messageRevision: number
  messages: ChatMessage[]
  runningTurn?: ChatRunningTurn
  projection?: ChatCacheProjectionWatermark
  now: number
}

export interface ChatLocalCacheStorageAdapter {
  readConversation(systemAccountId: string, conversationId: string): Promise<{ head?: ChatCacheConversationHead; messages: ChatMessage[]; runningTurn?: ChatRunningTurn }>
  putHead(head: ChatCacheConversationHead): Promise<void>
  putMessages(systemAccountId: string, conversationId: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<ChatCachePutResult>
  commitSyncSnapshot(snapshot: ChatCacheSyncSnapshot): Promise<ChatCacheSyncCommitResult>
  deleteFromSequence(systemAccountId: string, conversationId: string, sequenceNo: number): Promise<void>
  deleteConversation(systemAccountId: string, conversationId: string): Promise<void>
  putRunningTurn(turn: ChatRunningTurn): Promise<void>
  removeRunningTurn(systemAccountId: string, conversationId: string): Promise<void>
  touch(systemAccountId: string, conversationId: string, now: number): Promise<void>
  clearAccount(systemAccountId: string): Promise<void>
  getTotalBytes(): Promise<number>
  listEvictionCandidates(limit: number, after?: ChatCacheEvictionCursor): Promise<ChatCacheConversationHead[]>
  cleanupExpired(serverTime: string, conversationLimit: number, messageLimit: number): Promise<{ conversations: number; messages: number }>
  close(): void
}

export interface ChatCacheResult<T = void> { enabled: boolean; ok: boolean; value?: T }
export interface ChatCacheWriteOptions { currentConversationId?: string; pendingConfirmationConversationIds?: ReadonlySet<string>; projection?: ChatCacheProjectionWatermark }

export function calculateChatCacheBudget(estimate?: { quota?: number }): number {
  const quota = estimate?.quota
  return typeof quota === 'number' && Number.isFinite(quota) && quota > 0 ? Math.min(MAX_BUDGET, Math.floor(quota * 0.2)) : DEFAULT_BUDGET
}

const DATA_URL = /^data:/i
const BASE64_PAYLOAD = /(?:^|[,;])base64(?:,|$)/i
const encoder = new TextEncoder()

function safeString(value: unknown): value is string {
  return typeof value === 'string' && encoder.encode(value).byteLength <= MAX_PERSISTED_STRING_BYTES && !DATA_URL.test(value) && !BASE64_PAYLOAD.test(value)
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const prototype = Object.getPrototypeOf(value)
  return prototype === Object.prototype || prototype === null
}

function validateCloneInput(value: unknown, seen = new Set<object>()): boolean {
  if (value === null || value === undefined || typeof value === 'boolean' || typeof value === 'string') return true
  if (typeof value === 'number') return Number.isFinite(value)
  if (typeof value !== 'object') return false
  if (typeof Blob !== 'undefined' && value instanceof Blob) return false
  if (typeof File !== 'undefined' && value instanceof File) return false
  if (seen.has(value)) return false
  seen.add(value)
  const valid = Array.isArray(value) ? value.every((item) => validateCloneInput(item, seen)) : isPlainRecord(value) && Object.values(value).every((item) => validateCloneInput(item, seen))
  seen.delete(value)
  return valid
}

function validatePersistentPayload(value: unknown): boolean {
  if (typeof value === 'string') return safeString(value)
  if (value === null || value === undefined || typeof value === 'boolean') return true
  if (typeof value === 'number') return Number.isFinite(value)
  if (Array.isArray(value)) return value.every(validatePersistentPayload)
  return isPlainRecord(value) && Object.values(value).every(validatePersistentPayload)
}

function integerValue(value: unknown): number | undefined { return typeof value === 'number' && Number.isSafeInteger(value) ? value : undefined }
function positiveIntegerValue(value: unknown): number | undefined { return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : undefined }
function optionalSafeString(value: unknown): string | undefined { return value === undefined ? undefined : safeString(value) ? value : undefined }

function cloneContentBlock(value: unknown): ChatMessageContentBlock | undefined {
  if (!isPlainRecord(value) || typeof value.type !== 'string') return undefined
  if (value.type === 'output_text' && typeof value.text === 'string' && integerValue(value.order) !== undefined) {
    return { type: 'output_text', ...(optionalSafeString(value.blockId) ? { blockId: value.blockId as string } : {}), order: value.order as number, text: value.text }
  }
  if (value.type === 'reasoning' && typeof value.text === 'string') {
    return { type: 'reasoning', ...(optionalSafeString(value.blockId) ? { blockId: value.blockId as string } : {}), ...(integerValue(value.order) !== undefined ? { order: value.order as number } : {}), text: value.text, ...(isProcessStatus(value.status) ? { status: value.status } : {}) }
  }
  if (value.type === 'input_text' && typeof value.text === 'string' && integerValue(value.order) !== undefined) return { type: 'input_text', text: value.text, order: value.order as number }
  if (value.type === 'input_image' && typeof value.assetId === 'string' && integerValue(value.order) !== undefined) return { type: 'input_image', assetId: value.assetId, order: value.order as number }
  if (value.type === 'tool_call' && typeof value.toolType === 'string' && isToolStatus(value.status)) {
    const id = typeof value.id === 'string' ? value.id : undefined
    const callId = typeof value.callId === 'string' ? value.callId : undefined
    if (!id && !callId) return undefined
    return {
      type: 'tool_call',
      ...(optionalSafeString(value.blockId) ? { blockId: value.blockId as string } : {}),
      ...(integerValue(value.order) !== undefined ? { order: value.order as number } : {}),
      ...(id ? { id } : {}), ...(callId ? { callId } : {}), toolType: value.toolType, status: value.status
    }
  }
  if (value.type === 'output_image' && typeof value.assetId === 'string' && integerValue(value.order) !== undefined && isProcessStatus(value.status)) {
    const width = positiveIntegerValue(value.width)
    const height = positiveIntegerValue(value.height)
    return {
      type: 'output_image', blockId: typeof value.blockId === 'string' && safeString(value.blockId) ? value.blockId : `cached-image-${value.order}`,
      order: value.order as number, assetId: value.assetId, status: value.status,
      ...(optionalSafeString(value.mimeType) ? { mimeType: value.mimeType as string } : {}),
      ...(width ? { width } : {}), ...(height ? { height } : {}),
      ...(optionalSafeString(value.revisedPrompt) ? { revisedPrompt: value.revisedPrompt as string } : {})
    }
  }
  return undefined
}

function isProcessStatus(value: unknown): value is 'started' | 'completed' | 'failed' | 'canceled' { return value === 'started' || value === 'completed' || value === 'failed' || value === 'canceled' }
function isToolStatus(value: unknown): value is 'started' | 'updated' | 'completed' | 'failed' | 'canceled' { return isProcessStatus(value) || value === 'updated' }

function canonicalServerDateTime(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const timestamp = serverDateTimeTimestamp(value)
  return timestamp === undefined ? undefined : new Date(timestamp).toISOString()
}

export function cloneVisibleChatMessage(value: unknown): ChatMessage | undefined {
  try {
    if (!isPlainRecord(value) || !validateCloneInput(value)) return undefined
    const sequenceNo = integerValue(value.sequenceNo)
    const createdAt = canonicalServerDateTime(value.createdAt)
    const expiresAt = canonicalServerDateTime(value.expiresAt)
    if (!safeString(value.id) || !safeString(value.conversationId) || !safeString(value.turnId) || sequenceNo === undefined || !['user', 'assistant'].includes(String(value.role)) || !['completed', 'streaming', 'failed', 'canceled'].includes(String(value.status)) || !safeString(value.contentText) || !safeString(value.model) || !createdAt || !expiresAt) return undefined
    const result: ChatMessage = { id: value.id, conversationId: value.conversationId, turnId: value.turnId, sequenceNo, role: value.role as ChatMessage['role'], status: value.status as ChatMessage['status'], contentText: value.contentText, model: value.model, createdAt, expiresAt }
    for (const key of ['clientMessageId', 'traceId', 'finishReason', 'errorCode', 'errorMessage', 'reasoningText'] as const) if (value[key] !== undefined) { if (!safeString(value[key])) return undefined; result[key] = value[key] }
    if (value.completedAt !== undefined) { const completedAt = canonicalServerDateTime(value.completedAt); if (!completedAt) return undefined; result.completedAt = completedAt }
    if (Array.isArray(value.contentBlocks)) result.contentBlocks = value.contentBlocks.map(cloneContentBlock).filter((item): item is ChatMessageContentBlock => Boolean(item))
    if (Array.isArray(value.toolEvents)) result.toolEvents = value.toolEvents.flatMap((event) => isPlainRecord(event) && typeof event.id === 'string' && typeof event.type === 'string' && isToolStatus(event.status) ? [{ id: event.id, type: event.type, status: event.status }] : [])
    for (const key of ['eventVersion', 'renderRevision'] as const) if (value[key] !== undefined) { const version = integerValue(value[key]); if (version === undefined || version < 0) return undefined; result[key] = version }
    if (!validatePersistentPayload(result)) return undefined
    return JSON.parse(JSON.stringify(result)) as ChatMessage
  } catch { return undefined }
}

function approximateBytes(value: unknown): number { return encoder.encode(JSON.stringify(value)).byteLength }
function isQuotaError(error: unknown): boolean { return error instanceof Error && error.name === 'QuotaExceededError' }
function diagnosticCode(operation: string, error: unknown): string {
  const name = error instanceof Error ? error.name : 'Error'
  const kind = name === 'QuotaExceededError' ? 'quota' : name === 'SecurityError' ? 'security' : name === 'AbortError' ? 'abort' : name === 'DataCloneError' ? 'clone' : 'storage'
  return `cache_${operation}_${kind}`.slice(0, 64)
}

export class ChatLocalCache {
  private enabledState = true
  private readonly adapter: ChatLocalCacheStorageAdapter
  private readonly clock: () => number
  private readonly estimate: () => Promise<{ quota?: number; usage?: number } | undefined>
  private readonly diagnostic?: (code: string) => void

  constructor(options: { adapter?: ChatLocalCacheStorageAdapter; clock?: () => number; estimate?: () => Promise<{ quota?: number; usage?: number } | undefined>; diagnostic?: (code: string) => void } = {}) {
    this.adapter = options.adapter ?? new NativeIndexedDbChatCacheAdapter()
    this.clock = options.clock ?? Date.now
    this.estimate = options.estimate ?? (async () => { try { return await navigator.storage?.estimate() } catch { return undefined } })
    this.diagnostic = options.diagnostic
  }
  get enabled(): boolean { return this.enabledState }
  async readConversation(account: string, conversation: string) { return this.call('read', () => this.adapter.readConversation(account, conversation)) }

  async putHead(account: string, conversation: ({ id: string } | { conversationId: string }) & { messageRevision: number } & Partial<ChatConversation>): Promise<ChatCacheResult> {
    const conversationId = 'conversationId' in conversation ? conversation.conversationId : conversation.id
    const head: ChatCacheConversationHead = { systemAccountId: account, conversationId, messageRevision: conversation.messageRevision, lastAccessAt: this.clock(), byteSize: 0, ...pickHeadFields(conversation) }
    if (!validatePersistentPayload(head)) return { enabled: this.enabledState, ok: false }
    return this.call('put_head', () => this.adapter.putHead(head))
  }

  async putMessages(account: string, conversation: string, values: readonly unknown[], options: ChatCacheWriteOptions = {}): Promise<ChatCacheResult> {
    if (!this.enabledState) return { enabled: false, ok: false }
    const messages = values.slice(-MAX_MESSAGE_PAGE).map(cloneVisibleChatMessage)
    if (messages.some((item) => !item || item.conversationId !== conversation)) return { enabled: true, ok: false }
    try {
      const written = await this.adapter.putMessages(account, conversation, messages as ChatMessage[], { now: this.clock() })
      if (!await this.enforceBudget(written.totalBytes, account, conversation, options)) return { enabled: true, ok: false }
      return { enabled: true, ok: true }
    } catch (error) {
      if (!isQuotaError(error)) return this.fail('put_messages', error)
      this.report('put_messages_first', error)
      try {
        const budget = calculateChatCacheBudget(await this.safeEstimate())
        const total = await this.adapter.getTotalBytes()
        await this.evict(Math.max(1, total - budget), account, conversation, options)
      } catch (evictionError) { this.report('evict', evictionError) }
      try {
        const written = await this.adapter.putMessages(account, conversation, messages as ChatMessage[], { now: this.clock() })
        if (!await this.enforceBudget(written.totalBytes, account, conversation, options)) return { enabled: true, ok: false }
        return { enabled: true, ok: true }
      } catch (retryError) { return this.fail('put_messages_retry', retryError) }
    }
  }

  async commitSyncSnapshot(
    account: string,
    head: { conversationId: string; messageRevision: number; activeTurn?: { turnId: string; assistantMessageId?: string; startedAt: string } },
    values: readonly unknown[],
    options: ChatCacheWriteOptions = {}
  ): Promise<ChatCacheResult<{ committed: boolean }>> {
    if (!this.enabledState) return { enabled: false, ok: false }
    const messages = values.slice(-MAX_MESSAGE_PAGE).map(cloneVisibleChatMessage)
    if (messages.some((item) => !item || item.conversationId !== head.conversationId)) return { enabled: true, ok: false }
    const projection = options.projection
    const runningTurn: ChatRunningTurn | undefined = head.activeTurn && !isTerminalProjection(projection) ? {
      systemAccountId: account,
      conversationId: head.conversationId,
      turnId: head.activeTurn.turnId,
      startedAt: head.activeTurn.startedAt,
      ...(head.activeTurn.assistantMessageId ? { assistantMessageId: head.activeTurn.assistantMessageId } : {}),
      ...(projection?.eventVersion !== undefined ? { eventVersion: projection.eventVersion } : {}),
      ...(projection?.status ? { status: projection.status } : {})
    } : undefined
    const snapshot: ChatCacheSyncSnapshot = {
      systemAccountId: account,
      conversationId: head.conversationId,
      messageRevision: head.messageRevision,
      messages: messages as ChatMessage[],
      runningTurn,
      projection,
      now: this.clock()
    }
    if (!validatePersistentPayload(snapshot)) return { enabled: true, ok: false }
    const write = async (): Promise<ChatCacheResult<{ committed: boolean }>> => {
      const committed = await this.adapter.commitSyncSnapshot(snapshot)
      if (committed.committed && !await this.enforceBudget(committed.totalBytes, account, head.conversationId, options)) return { enabled: true, ok: false }
      return { enabled: true, ok: true, value: { committed: committed.committed } }
    }
    try {
      return await write()
    } catch (error) {
      if (!isQuotaError(error)) return this.fail('commit_sync', error)
      this.report('commit_sync_first', error)
      try {
        const budget = calculateChatCacheBudget(await this.safeEstimate())
        await this.evict(Math.max(1, await this.adapter.getTotalBytes() - budget), account, head.conversationId, options)
        return await write()
      } catch (retryError) { return this.fail('commit_sync_retry', retryError) }
    }
  }

  async deleteFromSequence(account: string, conversation: string, sequenceNo: number) { return this.call('delete_from', () => this.adapter.deleteFromSequence(account, conversation, sequenceNo)) }
  async deleteConversation(account: string, conversation: string) { return this.call('delete_conversation', () => this.adapter.deleteConversation(account, conversation)) }
  async putRunningTurn(account: string, conversation: string, turn: Omit<ChatRunningTurn, 'systemAccountId' | 'conversationId'>): Promise<ChatCacheResult> {
    const safeTurn: ChatRunningTurn = {
      systemAccountId: account,
      conversationId: conversation,
      turnId: turn.turnId,
      startedAt: turn.startedAt,
      ...(turn.assistantMessageId ? { assistantMessageId: turn.assistantMessageId } : {}),
      ...(turn.eventVersion !== undefined ? { eventVersion: turn.eventVersion } : {}),
      ...(turn.status ? { status: turn.status } : {})
    }
    if (!validatePersistentPayload(safeTurn)) return { enabled: this.enabledState, ok: false }
    return this.call('put_running', () => this.adapter.putRunningTurn(safeTurn))
  }
  async removeRunningTurn(account: string, conversation: string) { return this.call('remove_running', () => this.adapter.removeRunningTurn(account, conversation)) }
  async touch(account: string, conversation: string) { return this.call('touch', () => this.adapter.touch(account, conversation, this.clock())) }
  async clearAccount(account: string) { return this.call('clear_account', () => this.adapter.clearAccount(account)) }
  async cleanupExpired(serverTime: string, limits: { conversationLimit?: number; messageLimit?: number } = {}) {
    const timestamp = safeString(serverTime) ? serverDateTimeTimestamp(serverTime) : undefined
    if (timestamp === undefined) return this.fail('cleanup_invalid_server_time', new Error('server time must be RFC3339 with an offset'))
    const normalizedServerTime = new Date(timestamp).toISOString()
    return this.call('cleanup', () => this.adapter.cleanupExpired(normalizedServerTime, Math.max(1, Math.min(limits.conversationLimit ?? 8, 16)), Math.max(1, Math.min(limits.messageLimit ?? 100, 200))))
  }
  close(): void { this.enabledState = false; try { this.adapter.close() } catch { /* no-op */ } }

  private async call<T>(operation: string, action: () => Promise<T>): Promise<ChatCacheResult<T>> { if (!this.enabledState) return { enabled: false, ok: false }; try { return { enabled: true, ok: true, value: await action() } } catch (error) { return this.fail(operation, error) } }
  private fail(operation: string, error: unknown): ChatCacheResult<never> { this.report(operation, error); this.enabledState = false; return { enabled: false, ok: false } }
  private report(operation: string, error: unknown): void { try { this.diagnostic?.(diagnosticCode(operation, error)) } catch { /* diagnostics are isolated */ } }
  private async safeEstimate() { try { return await this.estimate() } catch { return undefined } }
  private async enforceBudget(totalBytes: number, account: string, writtenConversation: string, options: ChatCacheWriteOptions): Promise<boolean> {
    const excess = Math.max(0, totalBytes - calculateChatCacheBudget(await this.safeEstimate()))
    if (excess === 0) return true
    const released = await this.evict(excess, account, writtenConversation, options)
    if (released >= excess) return true
    this.report('evict_insufficient', new Error('capacity'))
    return false
  }
  private async evict(targetBytes: number, account: string, writtenConversation: string, options: ChatCacheWriteOptions): Promise<number> {
    let released = 0; let scanned = 0; let cursor: ChatCacheEvictionCursor | undefined
    while (scanned < MAX_EVICTION_SCAN && released < targetBytes) {
      const candidates = await this.adapter.listEvictionCandidates(Math.min(EVICTION_BATCH, MAX_EVICTION_SCAN - scanned), cursor)
      if (candidates.length === 0) break
      scanned += candidates.length
      for (const candidate of candidates) {
        cursor = { lastAccessAt: candidate.lastAccessAt, systemAccountId: candidate.systemAccountId, conversationId: candidate.conversationId }
        if (candidate.activeTurnId || (candidate.systemAccountId === account && (candidate.conversationId === writtenConversation || candidate.conversationId === options.currentConversationId || options.pendingConfirmationConversationIds?.has(candidate.conversationId)))) continue
        if ((await this.adapter.readConversation(candidate.systemAccountId, candidate.conversationId)).runningTurn) continue
        await this.adapter.deleteConversation(candidate.systemAccountId, candidate.conversationId); released += candidate.byteSize
        if (released >= targetBytes) break
      }
      if (candidates.length < EVICTION_BATCH) break
    }
    return released
  }
}

function pickHeadFields(conversation: Partial<ChatConversation>): Partial<ChatCacheConversationHead> {
  const output: Partial<ChatCacheConversationHead> = {}
  for (const key of ['title', 'isPinned', 'lastModel', 'defaultImageModel', 'activeTurnId', 'userTurnCount', 'lastMessageAt', 'createdAt', 'updatedAt'] as const) if (conversation[key] !== undefined) Object.assign(output, { [key]: conversation[key] })
  return output
}

type HeadRecord = ChatCacheConversationHead
type MessageRecord = ChatMessage & { systemAccountId: string; byteSize: number }
interface CacheMetadataRecord { id: string; byteSize: number }

export class NativeIndexedDbChatCacheAdapter implements ChatLocalCacheStorageAdapter {
  private database?: Promise<IDBDatabase>
  private open(): Promise<IDBDatabase> {
    if (this.database) return this.database
    this.database = new Promise((resolve, reject) => {
      if (typeof indexedDB === 'undefined') { reject(new DOMException('IndexedDB unavailable', 'SecurityError')); return }
      const request = indexedDB.open(DB_NAME, DB_VERSION)
      request.onupgradeneeded = () => {
        const db = request.result
        for (const storeName of Array.from(db.objectStoreNames)) db.deleteObjectStore(storeName)
        const heads = db.createObjectStore('conversation_heads', { keyPath: ['systemAccountId', 'conversationId'] }); heads.createIndex('by_last_access', ['lastAccessAt', 'systemAccountId', 'conversationId']); heads.createIndex('by_account', 'systemAccountId')
        const messages = db.createObjectStore('messages', { keyPath: ['systemAccountId', 'conversationId', 'sequenceNo'] }); messages.createIndex('by_conversation', ['systemAccountId', 'conversationId']); messages.createIndex('by_conversation_sequence', ['systemAccountId', 'conversationId', 'sequenceNo']); messages.createIndex('by_expires_at', ['expiresAt', 'systemAccountId', 'conversationId', 'sequenceNo']); messages.createIndex('by_account', 'systemAccountId')
        const running = db.createObjectStore('running_turns', { keyPath: ['systemAccountId', 'conversationId'] }); running.createIndex('by_account', 'systemAccountId')
        db.createObjectStore('cache_metadata', { keyPath: 'id' })
      }
      request.onsuccess = () => resolve(request.result)
      request.onerror = () => reject(request.error ?? new Error('IndexedDB open failed'))
      request.onblocked = () => reject(new DOMException('IndexedDB blocked', 'AbortError'))
    })
    return this.database
  }

  async readConversation(account: string, conversation: string) { return this.run(['conversation_heads', 'messages', 'running_turns'], 'readonly', async (tx) => {
    const head = await requestValue<HeadRecord | undefined>(tx.objectStore('conversation_heads').get([account, conversation]))
    const records: MessageRecord[] = []; const range = IDBKeyRange.bound([account, conversation, 0], [account, conversation, Number.MAX_SAFE_INTEGER])
    await collectCursor(tx.objectStore('messages').index('by_conversation_sequence').openCursor(range, 'prev'), MAX_MESSAGE_PAGE, (cursor) => { records.push(cursor.value as MessageRecord) })
    const runningTurn = await requestValue<ChatRunningTurn | undefined>(tx.objectStore('running_turns').get([account, conversation]))
    return { head, messages: records.reverse().map(stripMessageRecord), runningTurn }
  }) }

  async putHead(head: HeadRecord): Promise<void> { await this.run(['conversation_heads'], 'readwrite', async (tx) => { const store = tx.objectStore('conversation_heads'); const current = await requestValue<HeadRecord | undefined>(store.get([head.systemAccountId, head.conversationId])); store.put({ ...head, byteSize: current?.byteSize ?? head.byteSize }) }) }

  async putMessages(account: string, conversation: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<ChatCachePutResult> { return this.run(['conversation_heads', 'messages', 'cache_metadata'], 'readwrite', async (tx) => {
    const heads = tx.objectStore('conversation_heads'); const messageStore = tx.objectStore('messages'); const current = await requestValue<HeadRecord | undefined>(heads.get([account, conversation])); let bytes = current?.byteSize ?? 0
    for (const message of messages) { const old = await requestValue<MessageRecord | undefined>(messageStore.get([account, conversation, message.sequenceNo])); if (old) bytes -= old.byteSize; const record: MessageRecord = { ...message, systemAccountId: account, byteSize: approximateBytes(message) }; bytes += record.byteSize; messageStore.put(record) }
    const head: HeadRecord = { systemAccountId: account, conversationId: conversation, messageRevision: current?.messageRevision ?? 0, lastAccessAt: context.now, byteSize: Math.max(0, bytes), ...current }; head.lastAccessAt = context.now; head.byteSize = Math.max(0, bytes); heads.put(head)
    const totalBytes = await adjustBytes(tx, account, head.byteSize - (current?.byteSize ?? 0)); return { head, totalBytes }
  }) }

  async commitSyncSnapshot(snapshot: ChatCacheSyncSnapshot): Promise<ChatCacheSyncCommitResult> { return this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => {
    const { systemAccountId: account, conversationId: conversation } = snapshot
    const heads = tx.objectStore('conversation_heads')
    const messageStore = tx.objectStore('messages')
    const runningStore = tx.objectStore('running_turns')
    const current = await requestValue<HeadRecord | undefined>(heads.get([account, conversation]))
    if (shouldRejectSyncSnapshot(current, snapshot)) {
      return { committed: false, head: current!, totalBytes: await currentTotalBytes(tx) }
    }
    messageStore.delete(messageRange(account, conversation))
    let bytes = 0
    for (const message of snapshot.messages) {
      const record: MessageRecord = { ...message, systemAccountId: account, byteSize: approximateBytes(message) }
      bytes += record.byteSize
      messageStore.put(record)
    }
    const head: HeadRecord = {
      ...(current ?? {}),
      systemAccountId: account,
      conversationId: conversation,
      messageRevision: snapshot.messageRevision,
      lastAccessAt: snapshot.now,
      byteSize: bytes,
      activeTurnId: snapshot.runningTurn?.turnId
    }
    applyProjectionWatermark(head, snapshot.projection)
    heads.put(head)
    if (snapshot.runningTurn) runningStore.put(snapshot.runningTurn)
    else runningStore.delete([account, conversation])
    const totalBytes = await adjustBytes(tx, account, bytes - (current?.byteSize ?? 0))
    return { committed: true, head, totalBytes }
  }) }

  async deleteFromSequence(account: string, conversation: string, sequenceNo: number): Promise<void> { await this.run(['conversation_heads', 'messages', 'cache_metadata'], 'readwrite', async (tx) => {
    const messages = tx.objectStore('messages'); const range = IDBKeyRange.bound([account, conversation, sequenceNo], [account, conversation, Number.MAX_SAFE_INTEGER]); let removed = 0
    await collectCursor(messages.openCursor(range), Number.MAX_SAFE_INTEGER, (cursor) => { removed += (cursor.value as MessageRecord).byteSize; cursor.delete() })
    const heads = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(heads.get([account, conversation])); if (head) heads.put({ ...head, byteSize: Math.max(0, head.byteSize - removed) }); await adjustBytes(tx, account, -removed)
  }) }

  async deleteConversation(account: string, conversation: string): Promise<void> { await this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => { const heads = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(heads.get([account, conversation])); tx.objectStore('messages').delete(messageRange(account, conversation)); heads.delete([account, conversation]); tx.objectStore('running_turns').delete([account, conversation]); await adjustBytes(tx, account, -(head?.byteSize ?? 0)) }) }
  async putRunningTurn(turn: ChatRunningTurn): Promise<void> { await this.run(['running_turns'], 'readwrite', async (tx) => { tx.objectStore('running_turns').put(turn) }) }
  async removeRunningTurn(account: string, conversation: string): Promise<void> { await this.run(['running_turns'], 'readwrite', async (tx) => { tx.objectStore('running_turns').delete([account, conversation]) }) }
  async touch(account: string, conversation: string, now: number): Promise<void> { await this.run(['conversation_heads'], 'readwrite', async (tx) => { const store = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(store.get([account, conversation])); if (head) store.put({ ...head, lastAccessAt: now }) }) }

  async clearAccount(account: string): Promise<void> { await this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => {
    const metadata = tx.objectStore('cache_metadata'); const accountId = accountMetadataId(account); const accountBytes = (await requestValue<CacheMetadataRecord | undefined>(metadata.get(accountId)))?.byteSize ?? 0
    tx.objectStore('conversation_heads').delete(pairRange(account)); tx.objectStore('messages').delete(accountMessageRange(account)); tx.objectStore('running_turns').delete(pairRange(account)); await adjustTotal(tx, -accountBytes); metadata.delete(accountId)
  }) }
  async getTotalBytes(): Promise<number> { return this.run(['cache_metadata'], 'readonly', async (tx) => (await requestValue<CacheMetadataRecord | undefined>(tx.objectStore('cache_metadata').get(TOTAL_METADATA_ID)))?.byteSize ?? 0) }
  async listEvictionCandidates(limit: number, after?: ChatCacheEvictionCursor): Promise<HeadRecord[]> { return this.run(['conversation_heads'], 'readonly', async (tx) => requestValue<HeadRecord[]>(tx.objectStore('conversation_heads').index('by_last_access').getAll(after ? IDBKeyRange.lowerBound([after.lastAccessAt, after.systemAccountId, after.conversationId], true) : undefined, limit))) }

  async cleanupExpired(serverTime: string, conversationLimit: number, messageLimit: number): Promise<{ conversations: number; messages: number }> { return this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => {
    const removed = new Map<string, { account: string; conversation: string; bytes: number }>(); const expires = tx.objectStore('messages').index('by_expires_at'); const upper = IDBKeyRange.upperBound([serverTime, '\uffff', '\uffff', Number.MAX_SAFE_INTEGER]); let messageCount = 0
    await collectCursor(expires.openCursor(upper), messageLimit, (cursor) => { const record = cursor.value as MessageRecord; const key = `${record.systemAccountId}\u0000${record.conversationId}`; if (!removed.has(key) && removed.size >= conversationLimit) return false; const item = removed.get(key) ?? { account: record.systemAccountId, conversation: record.conversationId, bytes: 0 }; item.bytes += record.byteSize; removed.set(key, item); cursor.delete(); messageCount += 1 })
    const heads = tx.objectStore('conversation_heads'); const messages = tx.objectStore('messages').index('by_conversation'); const running = tx.objectStore('running_turns')
    const removedByAccount = new Map<string, number>()
    for (const item of removed.values()) { removedByAccount.set(item.account, (removedByAccount.get(item.account) ?? 0) + item.bytes); const head = await requestValue<HeadRecord | undefined>(heads.get([item.account, item.conversation])); if (!head) continue; const [remaining, active] = await Promise.all([requestValue<number>(messages.count(IDBKeyRange.only([item.account, item.conversation]))), requestValue<ChatRunningTurn | undefined>(running.get([item.account, item.conversation]))]); if (remaining === 0 && !active) heads.delete([item.account, item.conversation]); else heads.put({ ...head, byteSize: Math.max(0, head.byteSize - item.bytes) }) }
    for (const [account, bytes] of removedByAccount) await adjustBytes(tx, account, -bytes)
    return { conversations: removed.size, messages: messageCount }
  }) }

  close(): void { if (this.database) void this.database.then((db) => db.close()).catch(() => undefined); this.database = undefined }
  private async run<T>(stores: string[], mode: IDBTransactionMode, work: (tx: IDBTransaction) => Promise<T>): Promise<T> { const db = await this.open(); const tx = db.transaction(stores, mode); const done = transactionDone(tx); try { const result = await work(tx); await done; return result } catch (error) { try { tx.abort() } catch { /* already inactive */ } try { await done } catch { /* preserve request error */ } throw error } }
}

function pairRange(account: string): IDBKeyRange { return IDBKeyRange.bound([account, ''], [account, '\uffff']) }
function messageRange(account: string, conversation: string): IDBKeyRange { return IDBKeyRange.bound([account, conversation, 0], [account, conversation, Number.MAX_SAFE_INTEGER]) }
function accountMessageRange(account: string): IDBKeyRange { return IDBKeyRange.bound([account, '', 0], [account, '\uffff', Number.MAX_SAFE_INTEGER]) }
function stripMessageRecord(record: MessageRecord): ChatMessage { const { systemAccountId: _account, byteSize: _bytes, ...message } = record; return message }
function isTerminalProjection(projection?: ChatCacheProjectionWatermark): boolean { return projection?.status === 'completed' || projection?.status === 'failed' || projection?.status === 'canceled' }
function projectionStatusPriority(status?: ChatMessage['status']): number { return status === 'completed' || status === 'failed' || status === 'canceled' ? 2 : status === 'streaming' ? 1 : 0 }
function shouldRejectSyncSnapshot(current: HeadRecord | undefined, snapshot: ChatCacheSyncSnapshot): boolean {
  if (!current) return false
  if (current.messageRevision !== snapshot.messageRevision) return current.messageRevision > snapshot.messageRevision
  const currentHasProjection = current.projectionStatus !== undefined || current.projectionEventVersion !== undefined
  if (!currentHasProjection) return false
  const incoming = snapshot.projection
  if (!incoming) return true
  if (current.projectionTurnId && (current.projectionTurnId !== incoming.turnId || current.projectionAssistantMessageId !== incoming.assistantMessageId)) return true
  const statusDelta = projectionStatusPriority(incoming.status) - projectionStatusPriority(current.projectionStatus)
  if (statusDelta !== 0) return statusDelta < 0
  if (current.projectionEventVersion !== undefined && incoming.eventVersion === undefined) return true
  return current.projectionEventVersion !== undefined && incoming.eventVersion !== undefined && incoming.eventVersion < current.projectionEventVersion
}
function applyProjectionWatermark(head: HeadRecord, projection?: ChatCacheProjectionWatermark): void {
  delete head.projectionEventVersion
  delete head.projectionStatus
  delete head.projectionTurnId
  delete head.projectionAssistantMessageId
  if (!projection) return
  if (projection.eventVersion !== undefined) head.projectionEventVersion = projection.eventVersion
  head.projectionStatus = projection.status
  head.projectionTurnId = projection.turnId
  head.projectionAssistantMessageId = projection.assistantMessageId
}
async function adjustTotal(tx: IDBTransaction, delta: number): Promise<number> { const store = tx.objectStore('cache_metadata'); const current = await requestValue<CacheMetadataRecord | undefined>(store.get(TOTAL_METADATA_ID)); const byteSize = Math.max(0, (current?.byteSize ?? 0) + delta); store.put({ id: TOTAL_METADATA_ID, byteSize }); return byteSize }
async function currentTotalBytes(tx: IDBTransaction): Promise<number> { return (await requestValue<CacheMetadataRecord | undefined>(tx.objectStore('cache_metadata').get(TOTAL_METADATA_ID)))?.byteSize ?? 0 }
function accountMetadataId(account: string): string { return `account:${account}` }
async function adjustBytes(tx: IDBTransaction, account: string, delta: number): Promise<number> { const store = tx.objectStore('cache_metadata'); const accountId = accountMetadataId(account); const current = await requestValue<CacheMetadataRecord | undefined>(store.get(accountId)); store.put({ id: accountId, byteSize: Math.max(0, (current?.byteSize ?? 0) + delta) }); return adjustTotal(tx, delta) }
function requestValue<T>(request: IDBRequest<T> & { transaction?: IDBTransaction | null }): Promise<T> { return new Promise((resolve, reject) => { request.onsuccess = () => resolve(request.result); request.onerror = () => { const error = request.error ?? new Error('IndexedDB request failed'); try { request.transaction?.abort() } catch { /* transaction already aborting */ } reject(error) } }) }
function transactionDone(transaction: IDBTransaction): Promise<void> { return new Promise((resolve, reject) => { transaction.oncomplete = () => resolve(); transaction.onabort = () => reject(transaction.error ?? new DOMException('IndexedDB transaction aborted', 'AbortError')) }) }
function collectCursor(request: IDBRequest<IDBCursorWithValue | null> & { transaction?: IDBTransaction | null }, limit: number, visit: (cursor: IDBCursorWithValue) => void | false): Promise<void> { return new Promise((resolve, reject) => { let count = 0; request.onerror = () => { const error = request.error ?? new Error('IndexedDB cursor failed'); try { request.transaction?.abort() } catch { /* no-op */ } reject(error) }; request.onsuccess = () => { const cursor = request.result; if (!cursor || count >= limit) { resolve(); return } count += 1; if (visit(cursor) === false) { resolve(); return } cursor.continue() } }) }

let defaultCache: ChatLocalCache | undefined
export function getDefaultChatLocalCache(): ChatLocalCache { return defaultCache ??= new ChatLocalCache() }
