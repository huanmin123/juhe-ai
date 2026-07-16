import type { ChatConversation, ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'

const DEFAULT_BUDGET = 64 * 1024 ** 2
const MAX_BUDGET = 256 * 1024 ** 2
const MAX_MESSAGE_PAGE = 200
const EVICTION_BATCH = 16
const MAX_EVICTION_SCAN = 64
const MAX_PERSISTED_STRING_BYTES = 2 * 1024 ** 2
const DB_NAME = 'juhe-ai-chat-cache'
const DB_VERSION = 1
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
  activeTurnId?: string
  userTurnCount?: number
  lastMessageAt?: string
  createdAt?: string
  updatedAt?: string
}

export interface ChatRunningTurn {
  systemAccountId: string
  conversationId: string
  turnId: string
  assistantMessageId?: string
  startedAt: string
}

export interface ChatCachePutContext { now: number }
export interface ChatCacheEvictionCursor { lastAccessAt: number; systemAccountId: string; conversationId: string }
export interface ChatCachePutResult { head: ChatCacheConversationHead; totalBytes: number }

export interface ChatLocalCacheStorageAdapter {
  readConversation(systemAccountId: string, conversationId: string): Promise<{ head?: ChatCacheConversationHead; messages: ChatMessage[]; runningTurn?: ChatRunningTurn }>
  putHead(head: ChatCacheConversationHead): Promise<void>
  putMessages(systemAccountId: string, conversationId: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<ChatCachePutResult>
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
export interface ChatCacheWriteOptions { currentConversationId?: string; pendingConfirmationConversationIds?: ReadonlySet<string> }

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

function cloneContentBlock(value: unknown): ChatMessageContentBlock | undefined {
  if (!isPlainRecord(value) || typeof value.type !== 'string') return undefined
  if (value.type === 'reasoning' && typeof value.text === 'string') return { type: 'reasoning', text: value.text }
  if (value.type === 'input_text' && typeof value.text === 'string' && integerValue(value.order) !== undefined) return { type: 'input_text', text: value.text, order: value.order as number }
  if (value.type === 'input_image' && typeof value.assetId === 'string' && integerValue(value.order) !== undefined) return { type: 'input_image', assetId: value.assetId, order: value.order as number }
  if (value.type === 'tool_call' && typeof value.id === 'string' && typeof value.toolType === 'string' && ['started', 'updated', 'completed', 'failed'].includes(String(value.status))) return { type: 'tool_call', id: value.id, toolType: value.toolType, status: value.status as 'started' | 'updated' | 'completed' | 'failed' }
  return undefined
}

export function cloneVisibleChatMessage(value: unknown): ChatMessage | undefined {
  try {
    if (!isPlainRecord(value) || !validateCloneInput(value)) return undefined
    const sequenceNo = integerValue(value.sequenceNo)
    if (!safeString(value.id) || !safeString(value.conversationId) || !safeString(value.turnId) || sequenceNo === undefined || !['user', 'assistant'].includes(String(value.role)) || !['completed', 'streaming', 'failed', 'canceled'].includes(String(value.status)) || !safeString(value.contentText) || !safeString(value.model) || !safeString(value.createdAt) || !safeString(value.expiresAt)) return undefined
    const result: ChatMessage = { id: value.id, conversationId: value.conversationId, turnId: value.turnId, sequenceNo, role: value.role as ChatMessage['role'], status: value.status as ChatMessage['status'], contentText: value.contentText, model: value.model, createdAt: value.createdAt, expiresAt: value.expiresAt }
    for (const key of ['clientMessageId', 'traceId', 'finishReason', 'errorCode', 'completedAt', 'reasoningText'] as const) if (value[key] !== undefined) { if (!safeString(value[key])) return undefined; result[key] = value[key] }
    if (Array.isArray(value.contentBlocks)) result.contentBlocks = value.contentBlocks.map(cloneContentBlock).filter((item): item is ChatMessageContentBlock => Boolean(item))
    if (Array.isArray(value.toolEvents)) result.toolEvents = value.toolEvents.flatMap((event) => isPlainRecord(event) && typeof event.id === 'string' && typeof event.type === 'string' && ['started', 'updated', 'completed', 'failed'].includes(String(event.status)) ? [{ id: event.id, type: event.type, status: event.status as 'started' | 'updated' | 'completed' | 'failed' }] : [])
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
      const budget = calculateChatCacheBudget(await this.safeEstimate())
      const excess = Math.max(0, written.totalBytes - budget)
      if (excess > 0) {
        const released = await this.evict(excess, account, conversation, options)
        if (released < excess) { this.report('evict_insufficient', new Error('capacity')); return { enabled: true, ok: false } }
      }
      return { enabled: true, ok: true }
    } catch (error) {
      if (!isQuotaError(error)) return this.fail('put_messages', error)
      this.report('put_messages_first', error)
      try {
        const budget = calculateChatCacheBudget(await this.safeEstimate())
        const total = await this.adapter.getTotalBytes()
        await this.evict(Math.max(1, total - budget), account, conversation, options)
      } catch (evictionError) { this.report('evict', evictionError) }
      try { await this.adapter.putMessages(account, conversation, messages as ChatMessage[], { now: this.clock() }); return { enabled: true, ok: true } } catch (retryError) { return this.fail('put_messages_retry', retryError) }
    }
  }

  async deleteFromSequence(account: string, conversation: string, sequenceNo: number) { return this.call('delete_from', () => this.adapter.deleteFromSequence(account, conversation, sequenceNo)) }
  async deleteConversation(account: string, conversation: string) { return this.call('delete_conversation', () => this.adapter.deleteConversation(account, conversation)) }
  async putRunningTurn(account: string, conversation: string, turn: Omit<ChatRunningTurn, 'systemAccountId' | 'conversationId'>): Promise<ChatCacheResult> {
    const safeTurn: ChatRunningTurn = { systemAccountId: account, conversationId: conversation, turnId: turn.turnId, startedAt: turn.startedAt, ...(turn.assistantMessageId ? { assistantMessageId: turn.assistantMessageId } : {}) }
    if (!validatePersistentPayload(safeTurn)) return { enabled: this.enabledState, ok: false }
    return this.call('put_running', () => this.adapter.putRunningTurn(safeTurn))
  }
  async removeRunningTurn(account: string, conversation: string) { return this.call('remove_running', () => this.adapter.removeRunningTurn(account, conversation)) }
  async touch(account: string, conversation: string) { return this.call('touch', () => this.adapter.touch(account, conversation, this.clock())) }
  async clearAccount(account: string) { return this.call('clear_account', () => this.adapter.clearAccount(account)) }
  async cleanupExpired(serverTime: string, limits: { conversationLimit?: number; messageLimit?: number } = {}) { if (!safeString(serverTime) || !Number.isFinite(Date.parse(serverTime))) return { enabled: this.enabledState, ok: false }; return this.call('cleanup', () => this.adapter.cleanupExpired(serverTime, Math.max(1, Math.min(limits.conversationLimit ?? 8, 16)), Math.max(1, Math.min(limits.messageLimit ?? 100, 200)))) }
  close(): void { this.enabledState = false; try { this.adapter.close() } catch { /* no-op */ } }

  private async call<T>(operation: string, action: () => Promise<T>): Promise<ChatCacheResult<T>> { if (!this.enabledState) return { enabled: false, ok: false }; try { return { enabled: true, ok: true, value: await action() } } catch (error) { return this.fail(operation, error) } }
  private fail(operation: string, error: unknown): ChatCacheResult<never> { this.report(operation, error); this.enabledState = false; return { enabled: false, ok: false } }
  private report(operation: string, error: unknown): void { try { this.diagnostic?.(diagnosticCode(operation, error)) } catch { /* diagnostics are isolated */ } }
  private async safeEstimate() { try { return await this.estimate() } catch { return undefined } }
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
  for (const key of ['title', 'isPinned', 'lastModel', 'activeTurnId', 'userTurnCount', 'lastMessageAt', 'createdAt', 'updatedAt'] as const) if (conversation[key] !== undefined) Object.assign(output, { [key]: conversation[key] })
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
    const totalBytes = await adjustTotal(tx, head.byteSize - (current?.byteSize ?? 0)); return { head, totalBytes }
  }) }

  async deleteFromSequence(account: string, conversation: string, sequenceNo: number): Promise<void> { await this.run(['conversation_heads', 'messages', 'cache_metadata'], 'readwrite', async (tx) => {
    const messages = tx.objectStore('messages'); const range = IDBKeyRange.bound([account, conversation, sequenceNo], [account, conversation, Number.MAX_SAFE_INTEGER]); let removed = 0
    await collectCursor(messages.openCursor(range), Number.MAX_SAFE_INTEGER, (cursor) => { removed += (cursor.value as MessageRecord).byteSize; cursor.delete() })
    const heads = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(heads.get([account, conversation])); if (head) heads.put({ ...head, byteSize: Math.max(0, head.byteSize - removed) }); await adjustTotal(tx, -removed)
  }) }

  async deleteConversation(account: string, conversation: string): Promise<void> { await this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => { const heads = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(heads.get([account, conversation])); tx.objectStore('messages').delete(messageRange(account, conversation)); heads.delete([account, conversation]); tx.objectStore('running_turns').delete([account, conversation]); await adjustTotal(tx, -(head?.byteSize ?? 0)) }) }
  async putRunningTurn(turn: ChatRunningTurn): Promise<void> { await this.run(['running_turns'], 'readwrite', async (tx) => { tx.objectStore('running_turns').put(turn) }) }
  async removeRunningTurn(account: string, conversation: string): Promise<void> { await this.run(['running_turns'], 'readwrite', async (tx) => { tx.objectStore('running_turns').delete([account, conversation]) }) }
  async touch(account: string, conversation: string, now: number): Promise<void> { await this.run(['conversation_heads'], 'readwrite', async (tx) => { const store = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(store.get([account, conversation])); if (head) store.put({ ...head, lastAccessAt: now }) }) }

  async clearAccount(account: string): Promise<void> { await this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => {
    let removed = 0; await collectCursor(tx.objectStore('conversation_heads').index('by_account').openCursor(IDBKeyRange.only(account)), 64, (cursor) => { removed += (cursor.value as HeadRecord).byteSize })
    tx.objectStore('conversation_heads').delete(pairRange(account)); tx.objectStore('messages').delete(accountMessageRange(account)); tx.objectStore('running_turns').delete(pairRange(account)); await adjustTotal(tx, -removed)
  }) }
  async getTotalBytes(): Promise<number> { return this.run(['cache_metadata'], 'readonly', async (tx) => (await requestValue<CacheMetadataRecord | undefined>(tx.objectStore('cache_metadata').get(TOTAL_METADATA_ID)))?.byteSize ?? 0) }
  async listEvictionCandidates(limit: number, after?: ChatCacheEvictionCursor): Promise<HeadRecord[]> { return this.run(['conversation_heads'], 'readonly', async (tx) => requestValue<HeadRecord[]>(tx.objectStore('conversation_heads').index('by_last_access').getAll(after ? IDBKeyRange.lowerBound([after.lastAccessAt, after.systemAccountId, after.conversationId], true) : undefined, limit))) }

  async cleanupExpired(serverTime: string, conversationLimit: number, messageLimit: number): Promise<{ conversations: number; messages: number }> { return this.run(['conversation_heads', 'messages', 'running_turns', 'cache_metadata'], 'readwrite', async (tx) => {
    const removed = new Map<string, { account: string; conversation: string; bytes: number }>(); const expires = tx.objectStore('messages').index('by_expires_at'); const upper = IDBKeyRange.upperBound([serverTime, '\uffff', '\uffff', Number.MAX_SAFE_INTEGER]); let messageCount = 0
    await collectCursor(expires.openCursor(upper), messageLimit, (cursor) => { const record = cursor.value as MessageRecord; const key = `${record.systemAccountId}\u0000${record.conversationId}`; if (!removed.has(key) && removed.size >= conversationLimit) return false; const item = removed.get(key) ?? { account: record.systemAccountId, conversation: record.conversationId, bytes: 0 }; item.bytes += record.byteSize; removed.set(key, item); cursor.delete(); messageCount += 1 })
    const heads = tx.objectStore('conversation_heads'); const messages = tx.objectStore('messages').index('by_conversation'); const running = tx.objectStore('running_turns')
    let removedTotal = 0
    for (const item of removed.values()) { removedTotal += item.bytes; const head = await requestValue<HeadRecord | undefined>(heads.get([item.account, item.conversation])); if (!head) continue; const [remaining, active] = await Promise.all([requestValue<number>(messages.count(IDBKeyRange.only([item.account, item.conversation]))), requestValue<ChatRunningTurn | undefined>(running.get([item.account, item.conversation]))]); if (remaining === 0 && !active) heads.delete([item.account, item.conversation]); else heads.put({ ...head, byteSize: Math.max(0, head.byteSize - item.bytes) }) }
    await adjustTotal(tx, -removedTotal); return { conversations: removed.size, messages: messageCount }
  }) }

  close(): void { if (this.database) void this.database.then((db) => db.close()).catch(() => undefined); this.database = undefined }
  private async run<T>(stores: string[], mode: IDBTransactionMode, work: (tx: IDBTransaction) => Promise<T>): Promise<T> { const db = await this.open(); const tx = db.transaction(stores, mode); const done = transactionDone(tx); try { const result = await work(tx); await done; return result } catch (error) { try { tx.abort() } catch { /* already inactive */ } try { await done } catch { /* preserve request error */ } throw error } }
}

function pairRange(account: string): IDBKeyRange { return IDBKeyRange.bound([account, ''], [account, '\uffff']) }
function messageRange(account: string, conversation: string): IDBKeyRange { return IDBKeyRange.bound([account, conversation, 0], [account, conversation, Number.MAX_SAFE_INTEGER]) }
function accountMessageRange(account: string): IDBKeyRange { return IDBKeyRange.bound([account, '', 0], [account, '\uffff', Number.MAX_SAFE_INTEGER]) }
function stripMessageRecord(record: MessageRecord): ChatMessage { const { systemAccountId: _account, byteSize: _bytes, ...message } = record; return message }
async function adjustTotal(tx: IDBTransaction, delta: number): Promise<number> { const store = tx.objectStore('cache_metadata'); const current = await requestValue<CacheMetadataRecord | undefined>(store.get(TOTAL_METADATA_ID)); const byteSize = Math.max(0, (current?.byteSize ?? 0) + delta); store.put({ id: TOTAL_METADATA_ID, byteSize }); return byteSize }
function requestValue<T>(request: IDBRequest<T> & { transaction?: IDBTransaction | null }): Promise<T> { return new Promise((resolve, reject) => { request.onsuccess = () => resolve(request.result); request.onerror = () => { const error = request.error ?? new Error('IndexedDB request failed'); try { request.transaction?.abort() } catch { /* transaction already aborting */ } reject(error) } }) }
function transactionDone(transaction: IDBTransaction): Promise<void> { return new Promise((resolve, reject) => { transaction.oncomplete = () => resolve(); transaction.onabort = () => reject(transaction.error ?? new DOMException('IndexedDB transaction aborted', 'AbortError')) }) }
function collectCursor(request: IDBRequest<IDBCursorWithValue | null> & { transaction?: IDBTransaction | null }, limit: number, visit: (cursor: IDBCursorWithValue) => void | false): Promise<void> { return new Promise((resolve, reject) => { let count = 0; request.onerror = () => { const error = request.error ?? new Error('IndexedDB cursor failed'); try { request.transaction?.abort() } catch { /* no-op */ } reject(error) }; request.onsuccess = () => { const cursor = request.result; if (!cursor || count >= limit) { resolve(); return } count += 1; if (visit(cursor) === false) { resolve(); return } cursor.continue() } }) }

let defaultCache: ChatLocalCache | undefined
export function getDefaultChatLocalCache(): ChatLocalCache { return defaultCache ??= new ChatLocalCache() }
