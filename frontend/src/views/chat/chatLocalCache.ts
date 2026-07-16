import type { ChatConversation, ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'

const DEFAULT_BUDGET = 64 * 1024 ** 2
const MAX_BUDGET = 256 * 1024 ** 2
const MAX_MESSAGE_PAGE = 200
const EVICTION_BATCH = 16
const DB_NAME = 'juhe-ai-chat-cache'
const DB_VERSION = 1

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

export interface ChatCachePutContext {
  now: number
}

export interface ChatCacheEvictionCursor {
  lastAccessAt: number
  systemAccountId: string
  conversationId: string
}

export interface ChatLocalCacheStorageAdapter {
  readConversation(systemAccountId: string, conversationId: string): Promise<{ head?: ChatCacheConversationHead; messages: ChatMessage[]; runningTurn?: ChatRunningTurn }>
  putHead(head: ChatCacheConversationHead): Promise<void>
  putMessages(systemAccountId: string, conversationId: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<ChatCacheConversationHead>
  deleteFromSequence(systemAccountId: string, conversationId: string, sequenceNo: number): Promise<void>
  deleteConversation(systemAccountId: string, conversationId: string): Promise<void>
  putRunningTurn(turn: ChatRunningTurn): Promise<void>
  removeRunningTurn(systemAccountId: string, conversationId: string): Promise<void>
  touch(systemAccountId: string, conversationId: string, now: number): Promise<void>
  clearAccount(systemAccountId: string): Promise<void>
  listEvictionCandidates(limit: number, after?: ChatCacheEvictionCursor): Promise<ChatCacheConversationHead[]>
  cleanupExpired(serverTime: string, conversationLimit: number, messageLimit: number): Promise<{ conversations: number; messages: number }>
  close(): void
}

export interface ChatCacheResult<T = void> {
  enabled: boolean
  ok: boolean
  value?: T
}

export interface ChatCacheWriteOptions {
  currentConversationId?: string
  pendingConfirmationConversationIds?: ReadonlySet<string>
}

export function calculateChatCacheBudget(estimate?: { quota?: number }): number {
  const quota = estimate?.quota
  return typeof quota === 'number' && Number.isFinite(quota) && quota > 0
    ? Math.min(MAX_BUDGET, Math.floor(quota * 0.2))
    : DEFAULT_BUDGET
}

const DATA_URL = /^data:/i
const BASE64_PAYLOAD = /(?:^|[,;])base64(?:,|$)/i

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
  const valid = Array.isArray(value)
    ? value.every((item) => validateCloneInput(item, seen))
    : isPlainRecord(value) && Object.values(value).every((item) => validateCloneInput(item, seen))
  seen.delete(value)
  return valid
}

function stringValue(value: unknown): string | undefined { return typeof value === 'string' ? value : undefined }
function numberValue(value: unknown): number | undefined { return typeof value === 'number' && Number.isSafeInteger(value) ? value : undefined }

function cloneContentBlock(value: unknown): ChatMessageContentBlock | undefined {
  if (!isPlainRecord(value) || typeof value.type !== 'string') return undefined
  if (value.type === 'reasoning' && typeof value.text === 'string') return { type: 'reasoning', text: value.text }
  if (value.type === 'input_text' && typeof value.text === 'string' && numberValue(value.order) !== undefined) return { type: 'input_text', text: value.text, order: value.order as number }
  if (value.type === 'input_image' && typeof value.assetId === 'string' && numberValue(value.order) !== undefined) return { type: 'input_image', assetId: value.assetId, order: value.order as number }
  if (value.type === 'tool_call' && typeof value.id === 'string' && typeof value.toolType === 'string' && ['started', 'updated', 'completed', 'failed'].includes(String(value.status))) {
    return { type: 'tool_call', id: value.id, toolType: value.toolType, status: value.status as 'started' | 'updated' | 'completed' | 'failed' }
  }
  return undefined
}

export function cloneVisibleChatMessage(value: unknown): ChatMessage | undefined {
  try {
    if (!isPlainRecord(value) || !validateCloneInput(value)) return undefined
    const id = stringValue(value.id); const conversationId = stringValue(value.conversationId); const turnId = stringValue(value.turnId)
    const sequenceNo = numberValue(value.sequenceNo); const role = value.role; const status = value.status
    const contentText = stringValue(value.contentText); const model = stringValue(value.model); const createdAt = stringValue(value.createdAt); const expiresAt = stringValue(value.expiresAt)
    if (!id || !conversationId || !turnId || sequenceNo === undefined || !['user', 'assistant'].includes(String(role)) || !['completed', 'streaming', 'failed', 'canceled'].includes(String(status)) || contentText === undefined || DATA_URL.test(contentText) || BASE64_PAYLOAD.test(contentText) || !model || !createdAt || !expiresAt) return undefined
    const result: ChatMessage = { id, conversationId, turnId, sequenceNo, role: role as ChatMessage['role'], status: status as ChatMessage['status'], contentText, model, createdAt, expiresAt }
    for (const key of ['clientMessageId', 'traceId', 'finishReason', 'errorCode', 'completedAt', 'reasoningText'] as const) {
      const item = stringValue(value[key]); if (item !== undefined) result[key] = item
    }
    if (Array.isArray(value.contentBlocks)) result.contentBlocks = value.contentBlocks.map(cloneContentBlock).filter((item): item is ChatMessageContentBlock => Boolean(item))
    if (Array.isArray(value.toolEvents)) {
      result.toolEvents = value.toolEvents.flatMap((event) => {
        if (!isPlainRecord(event) || typeof event.id !== 'string' || typeof event.type !== 'string' || !['started', 'updated', 'completed', 'failed'].includes(String(event.status))) return []
        return [{ id: event.id, type: event.type, status: event.status as 'started' | 'updated' | 'completed' | 'failed' }]
      })
    }
    return JSON.parse(JSON.stringify(result)) as ChatMessage
  } catch {
    return undefined
  }
}

function approximateBytes(value: unknown): number {
  return new TextEncoder().encode(JSON.stringify(value)).byteLength
}

function errorCode(operation: string, error: unknown): string {
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
    this.estimate = options.estimate ?? (async () => {
      try { return await navigator.storage?.estimate() } catch { return undefined }
    })
    this.diagnostic = options.diagnostic
  }

  get enabled(): boolean { return this.enabledState }

  async readConversation(systemAccountId: string, conversationId: string): Promise<ChatCacheResult<{ head?: ChatCacheConversationHead; messages: ChatMessage[]; runningTurn?: ChatRunningTurn }>> {
    return this.call('read', () => this.adapter.readConversation(systemAccountId, conversationId))
  }

  async putHead(systemAccountId: string, conversation: ({ id: string } | { conversationId: string }) & { messageRevision: number } & Partial<ChatConversation>): Promise<ChatCacheResult> {
    const conversationId = 'conversationId' in conversation ? conversation.conversationId : conversation.id
    const existing = (await this.readConversation(systemAccountId, conversationId)).value?.head
    const head: ChatCacheConversationHead = {
      systemAccountId, conversationId, messageRevision: conversation.messageRevision,
      lastAccessAt: this.clock(), byteSize: existing?.byteSize ?? 0,
      ...pickHeadFields(conversation)
    }
    return this.call('put_head', () => this.adapter.putHead(head))
  }

  async putMessages(systemAccountId: string, conversationId: string, values: readonly unknown[], options: ChatCacheWriteOptions = {}): Promise<ChatCacheResult> {
    if (!this.enabledState) return { enabled: false, ok: false }
    const messages = values.slice(-MAX_MESSAGE_PAGE).map(cloneVisibleChatMessage)
    if (messages.some((item) => !item) || messages.some((item) => item!.conversationId !== conversationId)) return { enabled: true, ok: false }
    try {
      const head = await this.adapter.putMessages(systemAccountId, conversationId, messages as ChatMessage[], { now: this.clock() })
      const estimate = await this.safeEstimate()
      const budget = calculateChatCacheBudget(estimate)
      if ((estimate?.usage ?? 0) > budget || head.byteSize > budget) await this.evict(budget, systemAccountId, conversationId, options)
      return { enabled: this.enabledState, ok: true }
    } catch (error) {
      if (isQuotaError(error)) {
        try { await this.evict(calculateChatCacheBudget(await this.safeEstimate()), systemAccountId, conversationId, options) } catch { /* retry determines the write result */ }
        try {
          await this.adapter.putMessages(systemAccountId, conversationId, messages as ChatMessage[], { now: this.clock() })
          return { enabled: true, ok: true }
        } catch (retryError) { return this.fail('put_messages', retryError) }
      }
      return this.fail('put_messages', error)
    }
  }

  async deleteFromSequence(account: string, conversation: string, sequenceNo: number): Promise<ChatCacheResult> { return this.call('delete_from', () => this.adapter.deleteFromSequence(account, conversation, sequenceNo)) }
  async deleteConversation(account: string, conversation: string): Promise<ChatCacheResult> { return this.call('delete_conversation', () => this.adapter.deleteConversation(account, conversation)) }
  async putRunningTurn(account: string, conversation: string, turn: Omit<ChatRunningTurn, 'systemAccountId' | 'conversationId'>): Promise<ChatCacheResult> {
    const safeTurn: ChatRunningTurn = { systemAccountId: account, conversationId: conversation, turnId: turn.turnId, startedAt: turn.startedAt, ...(turn.assistantMessageId ? { assistantMessageId: turn.assistantMessageId } : {}) }
    return this.call('put_running', () => this.adapter.putRunningTurn(safeTurn))
  }
  async removeRunningTurn(account: string, conversation: string): Promise<ChatCacheResult> { return this.call('remove_running', () => this.adapter.removeRunningTurn(account, conversation)) }
  async touch(account: string, conversation: string): Promise<ChatCacheResult> { return this.call('touch', () => this.adapter.touch(account, conversation, this.clock())) }
  async clearAccount(account: string): Promise<ChatCacheResult> { return this.call('clear_account', () => this.adapter.clearAccount(account)) }
  async cleanupExpired(serverTime: string, limits: { conversationLimit?: number; messageLimit?: number } = {}): Promise<ChatCacheResult<{ conversations: number; messages: number }>> {
    if (!Number.isFinite(Date.parse(serverTime))) return { enabled: this.enabledState, ok: false }
    return this.call('cleanup', () => this.adapter.cleanupExpired(serverTime, Math.max(1, Math.min(limits.conversationLimit ?? 8, 16)), Math.max(1, Math.min(limits.messageLimit ?? 100, 200))))
  }
  close(): void { this.enabledState = false; try { this.adapter.close() } catch { /* no-op */ } }

  private async call<T>(operation: string, action: () => Promise<T>): Promise<ChatCacheResult<T>> {
    if (!this.enabledState) return { enabled: false, ok: false }
    try { return { enabled: true, ok: true, value: await action() } } catch (error) { return this.fail(operation, error) }
  }

  private fail(operation: string, error: unknown, disable = true): ChatCacheResult<never> {
    try { this.diagnostic?.(errorCode(operation, error)) } catch { /* diagnostics are isolated */ }
    if (disable) this.enabledState = false
    return { enabled: this.enabledState, ok: false }
  }

  private async safeEstimate(): Promise<{ quota?: number; usage?: number } | undefined> { try { return await this.estimate() } catch { return undefined } }

  private async evict(budget: number, account: string, writtenConversation: string, options: ChatCacheWriteOptions): Promise<void> {
    let released = 0; let scanned = 0; let cursor: ChatCacheEvictionCursor | undefined
    while (scanned < 64) {
      const candidates = await this.adapter.listEvictionCandidates(Math.min(EVICTION_BATCH, 64 - scanned), cursor)
      if (candidates.length === 0) return
      scanned += candidates.length
      for (const candidate of candidates) {
        cursor = { lastAccessAt: candidate.lastAccessAt, systemAccountId: candidate.systemAccountId, conversationId: candidate.conversationId }
        if (candidate.activeTurnId) continue
        if (candidate.systemAccountId === account && (candidate.conversationId === writtenConversation || candidate.conversationId === options.currentConversationId || options.pendingConfirmationConversationIds?.has(candidate.conversationId))) continue
        const detail = await this.adapter.readConversation(candidate.systemAccountId, candidate.conversationId)
        if (detail.runningTurn) continue
        await this.adapter.deleteConversation(candidate.systemAccountId, candidate.conversationId)
        released += candidate.byteSize
        if (released >= budget) return
      }
      if (candidates.length < EVICTION_BATCH) return
    }
  }
}

function pickHeadFields(conversation: Partial<ChatConversation>): Partial<ChatCacheConversationHead> {
  const output: Partial<ChatCacheConversationHead> = {}
  for (const key of ['title', 'isPinned', 'lastModel', 'activeTurnId', 'userTurnCount', 'lastMessageAt', 'createdAt', 'updatedAt'] as const) if (conversation[key] !== undefined) Object.assign(output, { [key]: conversation[key] })
  return output
}

function isQuotaError(error: unknown): boolean { return error instanceof Error && error.name === 'QuotaExceededError' }

type HeadRecord = ChatCacheConversationHead
type MessageRecord = ChatMessage & { systemAccountId: string; byteSize: number }

export class NativeIndexedDbChatCacheAdapter implements ChatLocalCacheStorageAdapter {
  private database?: Promise<IDBDatabase>

  private open(): Promise<IDBDatabase> {
    if (this.database) return this.database
    this.database = new Promise((resolve, reject) => {
      if (typeof indexedDB === 'undefined') { reject(new DOMException('IndexedDB unavailable', 'SecurityError')); return }
      const request = indexedDB.open(DB_NAME, DB_VERSION)
      request.onupgradeneeded = () => {
        const db = request.result
        const heads = db.createObjectStore('conversation_heads', { keyPath: ['systemAccountId', 'conversationId'] })
        heads.createIndex('by_last_access', ['lastAccessAt', 'systemAccountId', 'conversationId'])
        heads.createIndex('by_account', 'systemAccountId')
        const messages = db.createObjectStore('messages', { keyPath: ['systemAccountId', 'conversationId', 'sequenceNo'] })
        messages.createIndex('by_conversation', ['systemAccountId', 'conversationId'])
        messages.createIndex('by_expires_at', 'expiresAt')
        const running = db.createObjectStore('running_turns', { keyPath: ['systemAccountId', 'conversationId'] })
        running.createIndex('by_account', 'systemAccountId')
      }
      request.onsuccess = () => resolve(request.result)
      request.onerror = () => reject(request.error ?? new Error('IndexedDB open failed'))
      request.onblocked = () => reject(new DOMException('IndexedDB blocked', 'AbortError'))
    })
    return this.database
  }

  async readConversation(account: string, conversation: string) {
    const db = await this.open(); const tx = db.transaction(['conversation_heads', 'messages', 'running_turns'], 'readonly'); const done = transactionDone(tx)
    const head = await requestValue<HeadRecord | undefined>(tx.objectStore('conversation_heads').get([account, conversation]))
    const records = await requestValue<MessageRecord[]>(tx.objectStore('messages').index('by_conversation').getAll(IDBKeyRange.only([account, conversation]), MAX_MESSAGE_PAGE))
    const runningTurn = await requestValue<ChatRunningTurn | undefined>(tx.objectStore('running_turns').get([account, conversation])); await done
    return { head, messages: records.map(stripMessageRecord).sort((a, b) => a.sequenceNo - b.sequenceNo), runningTurn }
  }

  async putHead(head: HeadRecord): Promise<void> { const db = await this.open(); const tx = db.transaction('conversation_heads', 'readwrite'); const done = transactionDone(tx); tx.objectStore('conversation_heads').put(head); await done }

  async putMessages(account: string, conversation: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<HeadRecord> {
    const db = await this.open(); const tx = db.transaction(['conversation_heads', 'messages'], 'readwrite'); const done = transactionDone(tx); const headStore = tx.objectStore('conversation_heads'); const messageStore = tx.objectStore('messages')
    const current = await requestValue<HeadRecord | undefined>(headStore.get([account, conversation])); let bytes = current?.byteSize ?? 0
    for (const message of messages) {
      const old = await requestValue<MessageRecord | undefined>(messageStore.get([account, conversation, message.sequenceNo])); if (old) bytes -= old.byteSize
      const record: MessageRecord = { ...message, systemAccountId: account, byteSize: approximateBytes(message) }; bytes += record.byteSize; messageStore.put(record)
    }
    const head: HeadRecord = { systemAccountId: account, conversationId: conversation, messageRevision: current?.messageRevision ?? 0, lastAccessAt: context.now, byteSize: Math.max(0, bytes), ...current }
    head.lastAccessAt = context.now; head.byteSize = Math.max(0, bytes); headStore.put(head); await done; return head
  }

  async deleteFromSequence(account: string, conversation: string, sequenceNo: number): Promise<void> {
    const db = await this.open(); const tx = db.transaction(['conversation_heads', 'messages'], 'readwrite'); const done = transactionDone(tx); const store = tx.objectStore('messages'); const range = IDBKeyRange.bound([account, conversation, sequenceNo], [account, conversation, Number.MAX_SAFE_INTEGER]); let removed = 0
    await walkCursor(store.openCursor(range), Number.MAX_SAFE_INTEGER, (cursor) => { removed += (cursor.value as MessageRecord).byteSize; cursor.delete() })
    const heads = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(heads.get([account, conversation])); if (head) heads.put({ ...head, byteSize: Math.max(0, head.byteSize - removed) }); await done
  }
  async deleteConversation(account: string, conversation: string): Promise<void> { const db = await this.open(); const tx = db.transaction(['conversation_heads', 'messages', 'running_turns'], 'readwrite'); const done = transactionDone(tx); deleteConversationInTransaction(tx, account, conversation); await done }
  async putRunningTurn(turn: ChatRunningTurn): Promise<void> { const db = await this.open(); const tx = db.transaction('running_turns', 'readwrite'); const done = transactionDone(tx); tx.objectStore('running_turns').put(turn); await done }
  async removeRunningTurn(account: string, conversation: string): Promise<void> { const db = await this.open(); const tx = db.transaction('running_turns', 'readwrite'); const done = transactionDone(tx); tx.objectStore('running_turns').delete([account, conversation]); await done }
  async touch(account: string, conversation: string, now: number): Promise<void> { const db = await this.open(); const tx = db.transaction('conversation_heads', 'readwrite'); const done = transactionDone(tx); const store = tx.objectStore('conversation_heads'); const head = await requestValue<HeadRecord | undefined>(store.get([account, conversation])); if (head) store.put({ ...head, lastAccessAt: now }); await done }

  async clearAccount(account: string): Promise<void> {
    const db = await this.open()
    while (true) {
      const heads = await this.accountHeads(db, account, 16)
      if (heads.length === 0) return
      for (const head of heads) await this.deleteConversation(account, head.conversationId)
    }
  }
  async listEvictionCandidates(limit: number, after?: ChatCacheEvictionCursor): Promise<HeadRecord[]> { const db = await this.open(); const tx = db.transaction('conversation_heads', 'readonly'); const done = transactionDone(tx); const range = after ? IDBKeyRange.lowerBound([after.lastAccessAt, after.systemAccountId, after.conversationId], true) : undefined; const values = await requestValue<HeadRecord[]>(tx.objectStore('conversation_heads').index('by_last_access').getAll(range, limit)); await done; return values }

  async cleanupExpired(serverTime: string, conversationLimit: number, messageLimit: number): Promise<{ conversations: number; messages: number }> {
    const db = await this.open(); const tx = db.transaction('conversation_heads', 'readonly'); const done = transactionDone(tx); const heads = await requestValue<HeadRecord[]>(tx.objectStore('conversation_heads').index('by_last_access').getAll(undefined, conversationLimit)); await done
    let messages = 0
    for (const head of heads) {
      if (messages >= messageLimit) break
      const write = db.transaction(['conversation_heads', 'messages', 'running_turns'], 'readwrite'); const writeDone = transactionDone(write); const store = write.objectStore('messages'); let removedBytes = 0
      await walkCursor(store.index('by_conversation').openCursor(IDBKeyRange.only([head.systemAccountId, head.conversationId])), messageLimit - messages, (cursor) => {
        const record = cursor.value as MessageRecord
        if (record.expiresAt <= serverTime) { removedBytes += record.byteSize; messages += 1; cursor.delete() }
      })
      const remaining = await requestValue<number>(store.index('by_conversation').count(IDBKeyRange.only([head.systemAccountId, head.conversationId])))
      const running = await requestValue<ChatRunningTurn | undefined>(write.objectStore('running_turns').get([head.systemAccountId, head.conversationId])); const headsStore = write.objectStore('conversation_heads')
      if (remaining === 0 && !running) headsStore.delete([head.systemAccountId, head.conversationId]); else headsStore.put({ ...head, byteSize: Math.max(0, head.byteSize - removedBytes) }); await writeDone
    }
    return { conversations: heads.length, messages }
  }

  close(): void { if (this.database) void this.database.then((db) => db.close()).catch(() => undefined); this.database = undefined }
  private async accountHeads(db: IDBDatabase, account: string, limit: number): Promise<HeadRecord[]> { const tx = db.transaction('conversation_heads', 'readonly'); const done = transactionDone(tx); const values = await requestValue<HeadRecord[]>(tx.objectStore('conversation_heads').index('by_account').getAll(IDBKeyRange.only(account), limit)); await done; return values }
}

function stripMessageRecord(record: MessageRecord): ChatMessage { const { systemAccountId: _account, byteSize: _bytes, ...message } = record; return message }
function requestValue<T>(request: IDBRequest<T>): Promise<T> { return new Promise((resolve, reject) => { request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error ?? new Error('IndexedDB request failed')) }) }
function transactionDone(transaction: IDBTransaction): Promise<void> { return new Promise((resolve, reject) => { transaction.oncomplete = () => resolve(); transaction.onerror = () => reject(transaction.error ?? new DOMException('IndexedDB transaction failed', 'AbortError')); transaction.onabort = () => reject(transaction.error ?? new DOMException('IndexedDB transaction aborted', 'AbortError')) }) }
function walkCursor(request: IDBRequest<IDBCursorWithValue | null>, limit: number, visit: (cursor: IDBCursorWithValue) => void): Promise<void> { return new Promise((resolve, reject) => { let count = 0; request.onerror = () => reject(request.error ?? new Error('IndexedDB cursor failed')); request.onsuccess = () => { const cursor = request.result; if (!cursor || count >= limit) { resolve(); return } count += 1; visit(cursor); cursor.continue() } }) }
function deleteConversationInTransaction(tx: IDBTransaction, account: string, conversation: string): void { tx.objectStore('messages').delete(IDBKeyRange.bound([account, conversation, 0], [account, conversation, Number.MAX_SAFE_INTEGER])); tx.objectStore('conversation_heads').delete([account, conversation]); tx.objectStore('running_turns').delete([account, conversation]) }

let defaultCache: ChatLocalCache | undefined
export function getDefaultChatLocalCache(): ChatLocalCache { return defaultCache ??= new ChatLocalCache() }
