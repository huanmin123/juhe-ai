import { randomUUID } from 'node:crypto'

import type { DatabaseClient } from './database-client.js'
import { ensurePostgresChatMessagePartitions } from './postgres-chat-message-partitions.js'

export type ChatMessageRole = 'user' | 'assistant'
export type ChatMessageStatus = 'completed' | 'streaming' | 'failed' | 'canceled'

export interface ChatConversation {
  id: string
  systemAccountId: string
  apiKeyId?: string
  apiKeyNameSnapshot: string
  title: string
  lastModel?: string
  activeTurnId?: string
  lastMessageAt: string
  createdAt: string
  updatedAt: string
}

export interface ChatMessage {
  id: string
  conversationId: string
  turnId: string
  sequenceNo: number
  clientMessageId?: string
  role: ChatMessageRole
  status: ChatMessageStatus
  contentText: string
  model: string
  traceId?: string
  finishReason?: string
  errorCode?: string
  createdAt: string
  completedAt?: string
  expiresAt: string
}

export class ChatConflictError extends Error {
  constructor(public readonly code: 'chat_message_in_progress' | 'chat_storage_quota_exceeded') {
    super(code === 'chat_message_in_progress' ? '当前会话正在生成回答' : '最近 7 天聊天容量已达到上限，请先删除部分会话')
  }
}

export async function createChatConversation(client: DatabaseClient, input: {
  id?: string
  systemAccountId: string
  apiKeyId: string
  apiKeyNameSnapshot: string
  now: string
}): Promise<ChatConversation> {
  const id = input.id ?? chatId('conv')
  const table = chatTable(client, 'chat_conversations')
  await client.execute(`
    INSERT INTO ${table} (
      id, system_account_id, api_key_id, api_key_name_snapshot, title,
      next_sequence_no, last_message_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, '新对话', 1, ?, ?, ?)
  `, [id, input.systemAccountId, input.apiKeyId, input.apiKeyNameSnapshot, input.now, input.now, input.now])
  return requireConversation(client, id, input.systemAccountId)
}

export async function listChatConversations(client: DatabaseClient, input: {
  systemAccountId: string
  beforeLastMessageAt?: string
  beforeId?: string
  limit: number
}): Promise<ChatConversation[]> {
  const hasCursor = Boolean(input.beforeLastMessageAt && input.beforeId)
  const rows = await client.query<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')}
    WHERE system_account_id = ?
      ${hasCursor ? 'AND (last_message_at < ? OR (last_message_at = ? AND id < ?))' : ''}
    ORDER BY last_message_at DESC, id DESC
    LIMIT ?
  `, hasCursor
    ? [input.systemAccountId, input.beforeLastMessageAt, input.beforeLastMessageAt, input.beforeId, Math.max(1, Math.min(input.limit, 50))]
    : [input.systemAccountId, Math.max(1, Math.min(input.limit, 50))])
  return rows.map(mapConversation)
}

export async function getChatConversation(client: DatabaseClient, conversationId: string, systemAccountId: string): Promise<ChatConversation | undefined> {
  const row = await client.one<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')} WHERE id = ? AND system_account_id = ?
  `, [conversationId, systemAccountId])
  return row ? mapConversation(row) : undefined
}

export async function deleteChatConversation(client: DatabaseClient, conversationId: string, systemAccountId: string): Promise<boolean> {
  return client.transaction(async (tx) => {
    const conversation = await tx.one<ConversationRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [conversationId, systemAccountId])
    if (!conversation) return false
    if (conversation.active_turn_id) throw new ChatConflictError('chat_message_in_progress')
    const buckets = await tx.query<{ bucket_date?: unknown; content_bytes?: unknown }>(`
      SELECT substr(created_at, 1, 10) AS bucket_date, COALESCE(SUM(content_bytes), 0) AS content_bytes
      FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ?
      GROUP BY substr(created_at, 1, 10)
    `, [conversationId, systemAccountId])
    for (const bucket of buckets) {
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_user_storage_windows')}
        SET content_bytes = CASE WHEN content_bytes > ? THEN content_bytes - ? ELSE 0 END,
            updated_at = ?
        WHERE system_account_id = ? AND bucket_date = ?
      `, [Number(bucket.content_bytes ?? 0), Number(bucket.content_bytes ?? 0), new Date().toISOString(), systemAccountId, String(bucket.bucket_date)])
    }
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_user_storage_windows')}
      WHERE system_account_id = ? AND content_bytes = 0
    `, [systemAccountId])
    const result = await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_conversations')} WHERE id = ? AND system_account_id = ?
    `, [conversationId, systemAccountId])
    return result.changes === 1
  })
}

export async function acceptChatTurn(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  clientMessageId: string
  userContent: string
  model: string
  now: string
  storageQuotaBytes: number
}): Promise<{ turnId: string; userMessage: ChatMessage; assistantMessage: ChatMessage; duplicate: boolean }> {
  return client.transaction(async (tx) => {
    await ensurePostgresChatMessagePartitions(tx, input.now)
    const conversation = await lockConversation(tx, input.conversationId, input.systemAccountId)
    const existing = await tx.one<IdempotencyRow>(`
      SELECT turn_id, user_message_id, assistant_message_id
      FROM ${chatTable(tx, 'chat_message_idempotency')}
      WHERE conversation_id = ? AND client_message_id = ? AND system_account_id = ?
    `, [input.conversationId, input.clientMessageId, input.systemAccountId])
    if (existing) {
      const pair = await loadMessagePair(tx, input.conversationId, input.systemAccountId, String(existing.turn_id))
      return { turnId: String(existing.turn_id), ...pair, duplicate: true }
    }
    if (conversation.active_turn_id) throw new ChatConflictError('chat_message_in_progress')

    const userBytes = Buffer.byteLength(input.userContent, 'utf8')
    const usedBytes = await recentStorageBytes(tx, input.systemAccountId, input.now)
    if (usedBytes + userBytes > input.storageQuotaBytes) throw new ChatConflictError('chat_storage_quota_exceeded')

    const turnId = chatId('turn')
    const userMessageId = chatId('msg')
    const assistantMessageId = chatId('msg')
    const userSequence = Number(conversation.next_sequence_no)
    const assistantSequence = userSequence + 1
    const expiresAt = addDays(input.now, 7)
    const messagesTable = chatTable(tx, 'chat_messages')
    await tx.execute(`
      INSERT INTO ${messagesTable} (
        id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
        role, status, content_text, content_bytes, model, created_at, completed_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, ?, 'user', 'completed', ?, ?, ?, ?, ?, ?)
    `, [userMessageId, input.conversationId, input.systemAccountId, turnId, userSequence, input.clientMessageId, input.userContent, userBytes, input.model, input.now, input.now, expiresAt])
    await tx.execute(`
      INSERT INTO ${messagesTable} (
        id, conversation_id, system_account_id, turn_id, sequence_no,
        role, status, content_text, content_bytes, model, created_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, 'assistant', 'streaming', '', 0, ?, ?, ?)
    `, [assistantMessageId, input.conversationId, input.systemAccountId, turnId, assistantSequence, input.model, input.now, expiresAt])
    await tx.execute(`
      INSERT INTO ${chatTable(tx, 'chat_message_idempotency')} (
        conversation_id, client_message_id, system_account_id, turn_id,
        user_message_id, assistant_message_id, created_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `, [input.conversationId, input.clientMessageId, input.systemAccountId, turnId, userMessageId, assistantMessageId, input.now, expiresAt])
    await incrementStorageWindow(tx, input.systemAccountId, input.now, userBytes)
    const title = titleFromContent(input.userContent)
    await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET title = CASE WHEN next_sequence_no = 1 THEN ? ELSE title END,
          title_source_message_id = CASE WHEN next_sequence_no = 1 THEN ? ELSE title_source_message_id END,
          next_sequence_no = ?, active_turn_id = ?, active_started_at = ?,
          last_model = ?, last_message_at = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ?
    `, [title, userMessageId, assistantSequence + 1, turnId, input.now, input.model, input.now, input.now, input.conversationId, input.systemAccountId])
    const pair = await loadMessagePair(tx, input.conversationId, input.systemAccountId, turnId)
    return { turnId, ...pair, duplicate: false }
  })
}

export async function completeChatTurn(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  turnId: string
  assistantContent: string
  finishReason: string
  traceId: string
  now: string
}): Promise<ChatMessage> {
  return finalizeChatTurn(client, { ...input, status: 'completed', errorCode: undefined })
}

export async function failChatTurn(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  turnId: string
  assistantContent: string
  errorCode: string
  traceId?: string
  now: string
}): Promise<ChatMessage> {
  return finalizeChatTurn(client, {
    ...input,
    status: 'failed',
    finishReason: undefined
  })
}

export async function cancelChatTurn(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  turnId: string
  assistantContent: string
  traceId?: string
  now: string
}): Promise<ChatMessage> {
  return finalizeChatTurn(client, {
    ...input,
    status: 'canceled',
    errorCode: undefined,
    finishReason: undefined
  })
}

async function finalizeChatTurn(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  turnId: string
  assistantContent: string
  status: 'completed' | 'failed' | 'canceled'
  finishReason?: string
  errorCode?: string
  traceId?: string
  now: string
}): Promise<ChatMessage> {
  return client.transaction(async (tx) => {
    const bytes = Buffer.byteLength(input.assistantContent, 'utf8')
    const current = await tx.one<ChatMessageRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [input.conversationId, input.systemAccountId, input.turnId])
    if (!current) throw new Error('活动回答不存在')
    const result = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_messages')}
      SET status = ?, content_text = ?, content_bytes = ?, trace_id = ?,
          finish_reason = ?, error_code = ?, completed_at = ?
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [input.status, input.assistantContent, bytes, input.traceId ?? null, input.finishReason ?? null, input.errorCode ?? null, input.now, input.conversationId, input.systemAccountId, input.turnId])
    if (result.changes !== 1) throw new Error('活动回答不存在')
    await incrementStorageWindow(tx, input.systemAccountId, String(current.created_at), bytes)
    await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET active_turn_id = NULL, active_started_at = NULL, last_message_at = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
    `, [input.now, input.now, input.conversationId, input.systemAccountId, input.turnId])
    const pair = await loadMessagePair(tx, input.conversationId, input.systemAccountId, input.turnId)
    return pair.assistantMessage
  })
}

export async function listChatMessages(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  beforeSequenceNo?: number
  limit: number
  now: string
}): Promise<ChatMessage[]> {
  await requireConversation(client, input.conversationId, input.systemAccountId)
  const before = input.beforeSequenceNo ?? Number.MAX_SAFE_INTEGER
  const rows = await client.query<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ? AND sequence_no < ?
    ORDER BY sequence_no DESC
    LIMIT ?
  `, [input.conversationId, input.systemAccountId, input.now, before, Math.max(1, Math.min(input.limit, 100))])
  return rows.reverse().map(mapMessage)
}

export async function listChatContextMessages(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  limitTurns: number
  now: string
}): Promise<Array<{ role: ChatMessageRole; content: string }>> {
  await requireConversation(client, input.conversationId, input.systemAccountId)
  const rows = await client.query<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ? AND status = 'completed'
      AND turn_id IN (
        SELECT turn_id FROM ${chatTable(client, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ? AND status = 'completed'
        GROUP BY turn_id HAVING COUNT(*) = 2
        ORDER BY MAX(sequence_no) DESC LIMIT ?
      )
    ORDER BY sequence_no ASC
  `, [input.conversationId, input.systemAccountId, input.now, input.conversationId, input.systemAccountId, input.now, Math.max(1, Math.min(input.limitTurns, 64))])
  return rows.map((row) => ({ role: String(row.role) as ChatMessageRole, content: String(row.content_text ?? '') }))
}

async function lockConversation(client: DatabaseClient, conversationId: string, systemAccountId: string): Promise<ConversationRow> {
  const suffix = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  const row = await client.one<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')}
    WHERE id = ? AND system_account_id = ?${suffix}
  `, [conversationId, systemAccountId])
  if (!row) throw new Error('会话不存在')
  return row
}

async function requireConversation(client: DatabaseClient, conversationId: string, systemAccountId: string): Promise<ChatConversation> {
  const row = await client.one<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')} WHERE id = ? AND system_account_id = ?
  `, [conversationId, systemAccountId])
  if (!row) throw new Error('会话不存在')
  return mapConversation(row)
}

async function loadMessagePair(client: DatabaseClient, conversationId: string, systemAccountId: string, turnId: string): Promise<{ userMessage: ChatMessage; assistantMessage: ChatMessage }> {
  const rows = await client.query<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
    ORDER BY sequence_no ASC
  `, [conversationId, systemAccountId, turnId])
  if (rows.length !== 2) throw new Error('聊天轮次数据不完整')
  return { userMessage: mapMessage(rows[0]), assistantMessage: mapMessage(rows[1]) }
}

async function recentStorageBytes(client: DatabaseClient, systemAccountId: string, now: string): Promise<number> {
  const start = new Date(now)
  start.setUTCDate(start.getUTCDate() - 7)
  const row = await client.one<{ total?: number | string }>(`
    SELECT COALESCE(SUM(content_bytes), 0) AS total
    FROM ${chatTable(client, 'chat_user_storage_windows')}
    WHERE system_account_id = ? AND bucket_date >= ?
  `, [systemAccountId, start.toISOString().slice(0, 10)])
  return Number(row?.total ?? 0)
}

async function incrementStorageWindow(client: DatabaseClient, systemAccountId: string, now: string, bytes: number): Promise<void> {
  const table = chatTable(client, 'chat_user_storage_windows')
  const bucketDate = now.slice(0, 10)
  if (client.driver === 'postgres') {
    await client.execute(`
      INSERT INTO ${table} (system_account_id, bucket_date, content_bytes, updated_at)
      VALUES (?, ?, ?, ?)
      ON CONFLICT (system_account_id, bucket_date)
      DO UPDATE SET content_bytes = ${table}.content_bytes + EXCLUDED.content_bytes, updated_at = EXCLUDED.updated_at
    `, [systemAccountId, bucketDate, bytes, now])
    return
  }
  await client.execute(`
    INSERT INTO ${table} (system_account_id, bucket_date, content_bytes, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(system_account_id, bucket_date)
    DO UPDATE SET content_bytes = content_bytes + excluded.content_bytes, updated_at = excluded.updated_at
  `, [systemAccountId, bucketDate, bytes, now])
}

function chatTable(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_chat', name)
}

function chatId(prefix: string): string {
  return `chat_${prefix}_${randomUUID().replace(/-/g, '')}`
}

function addDays(value: string, days: number): string {
  const date = new Date(value)
  date.setUTCDate(date.getUTCDate() + days)
  return date.toISOString()
}

function titleFromContent(content: string): string {
  const firstLine = content.replace(/[\u0000-\u001f\u007f]/g, ' ').split(/\r?\n/, 1)[0].replace(/\s+/g, ' ').trim()
  return firstLine.slice(0, 60) || '新对话'
}

function mapConversation(row: ConversationRow): ChatConversation {
  return {
    id: String(row.id), systemAccountId: String(row.system_account_id),
    apiKeyId: nullable(row.api_key_id), apiKeyNameSnapshot: String(row.api_key_name_snapshot),
    title: String(row.title), lastModel: nullable(row.last_model), activeTurnId: nullable(row.active_turn_id),
    lastMessageAt: String(row.last_message_at), createdAt: String(row.created_at), updatedAt: String(row.updated_at)
  }
}

function mapMessage(row: ChatMessageRow): ChatMessage {
  return {
    id: String(row.id), conversationId: String(row.conversation_id), turnId: String(row.turn_id),
    sequenceNo: Number(row.sequence_no), clientMessageId: nullable(row.client_message_id),
    role: String(row.role) as ChatMessageRole, status: String(row.status) as ChatMessageStatus,
    contentText: String(row.content_text ?? ''), model: String(row.model), traceId: nullable(row.trace_id),
    finishReason: nullable(row.finish_reason), errorCode: nullable(row.error_code), createdAt: String(row.created_at),
    completedAt: nullable(row.completed_at), expiresAt: String(row.expires_at)
  }
}

function nullable(value: unknown): string | undefined {
  return value === null || value === undefined || value === '' ? undefined : String(value)
}

type ConversationRow = Record<string, unknown> & { id?: unknown; system_account_id?: unknown; active_turn_id?: unknown; next_sequence_no?: unknown }
type ChatMessageRow = Record<string, unknown>
type IdempotencyRow = Record<string, unknown> & { turn_id?: unknown; user_message_id?: unknown; assistant_message_id?: unknown }
