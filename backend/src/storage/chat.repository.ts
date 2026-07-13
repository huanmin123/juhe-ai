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
  isPinned: boolean
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
  contentBlocks: ChatMessageContentBlock[]
  model: string
  traceId?: string
  finishReason?: string
  errorCode?: string
  createdAt: string
  completedAt?: string
  expiresAt: string
}

export type ChatMessageContentBlock =
  | { type: 'reasoning'; text: string }
  | { type: 'tool_call'; id: string; toolType: string; status: 'started' | 'updated' | 'completed' | 'failed'; item?: Record<string, unknown> }
  | { type: 'input_marker'; inputType: 'input_text' | 'input_image'; order: number }

export interface ChatInputContentBlock {
  type: 'input_text' | 'input_image'
  text?: string
  dataUrl?: string
}

const maxContentBlocksBytes = 256 * 1024
const maxInputContentBlocks = 9
const postgresIntegerMin = -2_147_483_648
const postgresIntegerMax = 2_147_483_647

export class ChatConflictError extends Error {
  constructor(public readonly code: 'chat_message_in_progress' | 'chat_storage_quota_exceeded' | 'chat_replace_conflict') {
    super({
      chat_message_in_progress: '当前会话正在生成回答',
      chat_storage_quota_exceeded: '最近 7 天聊天容量已达到上限，请先删除部分会话',
      chat_replace_conflict: '最近一轮已变化，请重新确认后再编辑'
    }[code])
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
  beforeIsPinned?: boolean
  beforeLastMessageAt?: string
  beforeId?: string
  limit: number
}): Promise<ChatConversation[]> {
  const hasCursor = input.beforeIsPinned !== undefined && Boolean(input.beforeLastMessageAt && input.beforeId)
  const beforePinnedValue = input.beforeIsPinned ? 1 : 0
  const rows = await client.query<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')}
    WHERE system_account_id = ?
      ${hasCursor ? 'AND (is_pinned < ? OR (is_pinned = ? AND (last_message_at < ? OR (last_message_at = ? AND id < ?))))' : ''}
    ORDER BY is_pinned DESC, last_message_at DESC, id DESC
    LIMIT ?
  `, hasCursor
    ? [input.systemAccountId, beforePinnedValue, beforePinnedValue, input.beforeLastMessageAt, input.beforeLastMessageAt, input.beforeId, Math.max(1, Math.min(input.limit, 50))]
    : [input.systemAccountId, Math.max(1, Math.min(input.limit, 50))])
  return rows.map(mapConversation)
}

export async function getChatConversation(client: DatabaseClient, conversationId: string, systemAccountId: string): Promise<ChatConversation | undefined> {
  const row = await client.one<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')} WHERE id = ? AND system_account_id = ?
  `, [conversationId, systemAccountId])
  return row ? mapConversation(row) : undefined
}

export async function findChatTurnByClientMessageId(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  clientMessageId: string
}): Promise<{ turnId: string } | undefined> {
  const row = await client.one<{ turn_id?: unknown }>(`
    SELECT turn_id
    FROM ${chatTable(client, 'chat_message_idempotency')}
    WHERE conversation_id = ? AND client_message_id = ? AND system_account_id = ?
  `, [input.conversationId, input.clientMessageId, input.systemAccountId])
  return row ? { turnId: String(row.turn_id) } : undefined
}

export async function updateChatConversation(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  title?: string
  isPinned?: boolean
  now: string
}): Promise<ChatConversation | undefined> {
  const current = await getChatConversation(client, input.conversationId, input.systemAccountId)
  if (!current) return undefined
  const title = input.title ?? current.title
  const isPinned = input.isPinned ?? current.isPinned
  await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET title = ?, title_source_message_id = CASE WHEN ? THEN NULL ELSE title_source_message_id END,
        is_pinned = ?, updated_at = ?
    WHERE id = ? AND system_account_id = ?
  `, [title, input.title !== undefined, isPinned ? 1 : 0, input.now, input.conversationId, input.systemAccountId])
  return getChatConversation(client, input.conversationId, input.systemAccountId)
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
  contentBlocks?: readonly ChatInputContentBlock[]
  model: string
  now: string
  storageQuotaBytes: number
  replaceTurnId?: string
}): Promise<{ turnId: string; userMessage: ChatMessage; assistantMessage: ChatMessage; duplicate: boolean }> {
  return client.transaction(async (tx) => {
    await ensurePostgresChatMessagePartitions(tx, input.now)
    await lockChatUserStorageQuota(tx, input.systemAccountId)
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

    const userContentBlocksJson = serializeInputContentMarkers(input.contentBlocks)
    const userBytes = Buffer.byteLength(input.userContent, 'utf8') + Buffer.byteLength(userContentBlocksJson, 'utf8')
    let userSequence = Number(conversation.next_sequence_no)
    let assistantSequence = userSequence + 1
    let replacedUserMessageId: string | undefined
    if (input.replaceTurnId) {
      if (conversation.active_turn_id) throw new ChatConflictError('chat_replace_conflict')
      const replacement = await requireReplaceableTurn(tx, {
        conversation,
        conversationId: input.conversationId,
        systemAccountId: input.systemAccountId,
        replaceTurnId: input.replaceTurnId,
        now: input.now
      })
      userSequence = Number(replacement.userMessage.sequence_no)
      assistantSequence = Number(replacement.assistantMessage.sequence_no)
      replacedUserMessageId = String(replacement.userMessage.id)
      const usedBytes = await recentStorageBytes(tx, input.systemAccountId, input.now)
      if (usedBytes < replacement.totalBytes) throw new Error('聊天容量窗口数据不一致：最近窗口小于待替换轮次')
      if (usedBytes - replacement.totalBytes + userBytes > input.storageQuotaBytes) {
        throw new ChatConflictError('chat_storage_quota_exceeded')
      }
      for (const [bucketDate, bytes] of replacement.bytesByBucket) {
        await decrementStorageWindowStrict(tx, input.systemAccountId, bucketDate, bytes, input.now)
      }
      const deletedIdempotency = await tx.execute(`
        DELETE FROM ${chatTable(tx, 'chat_message_idempotency')}
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
      `, [input.conversationId, input.systemAccountId, input.replaceTurnId])
      if (deletedIdempotency.changes !== 1) throw new ChatConflictError('chat_replace_conflict')
      const deletedMessages = await tx.execute(`
        DELETE FROM ${chatTable(tx, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
      `, [input.conversationId, input.systemAccountId, input.replaceTurnId])
      if (deletedMessages.changes !== 2) throw new ChatConflictError('chat_replace_conflict')
    } else {
      if (conversation.active_turn_id) throw new ChatConflictError('chat_message_in_progress')
      const usedBytes = await recentStorageBytes(tx, input.systemAccountId, input.now)
      if (usedBytes + userBytes > input.storageQuotaBytes) throw new ChatConflictError('chat_storage_quota_exceeded')
    }

    const turnId = chatId('turn')
    const userMessageId = chatId('msg')
    const assistantMessageId = chatId('msg')
    const expiresAt = addDays(input.now, 7)
    const messagesTable = chatTable(tx, 'chat_messages')
    await tx.execute(`
      INSERT INTO ${messagesTable} (
        id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
        role, status, content_text, content_blocks_json, content_bytes, model, created_at, completed_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, ?, 'user', 'completed', ?, ?, ?, ?, ?, ?, ?)
    `, [userMessageId, input.conversationId, input.systemAccountId, turnId, userSequence, input.clientMessageId, input.userContent, userContentBlocksJson, userBytes, input.model, input.now, input.now, expiresAt])
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
    if (replacedUserMessageId) {
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET title = CASE WHEN title_source_message_id = ? THEN ? ELSE title END,
            title_source_message_id = CASE WHEN title_source_message_id = ? THEN ? ELSE title_source_message_id END,
            active_turn_id = ?, active_started_at = ?, last_model = ?, last_message_at = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [replacedUserMessageId, title, replacedUserMessageId, userMessageId, turnId, input.now, input.model, input.now, input.now, input.conversationId, input.systemAccountId])
    } else {
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET title = CASE WHEN next_sequence_no = 1 THEN ? ELSE title END,
            title_source_message_id = CASE WHEN next_sequence_no = 1 THEN ? ELSE title_source_message_id END,
            next_sequence_no = ?, active_turn_id = ?, active_started_at = ?,
            last_model = ?, last_message_at = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [title, userMessageId, assistantSequence + 1, turnId, input.now, input.model, input.now, input.now, input.conversationId, input.systemAccountId])
    }
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
  contentBlocks?: ChatMessageContentBlock[]
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
  contentBlocks?: ChatMessageContentBlock[]
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
  contentBlocks?: ChatMessageContentBlock[]
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
  contentBlocks?: ChatMessageContentBlock[]
  now: string
}): Promise<ChatMessage> {
  return client.transaction(async (tx) => {
    const contentBlocksJson = serializeContentBlocks(input.contentBlocks ?? [])
    const bytes = Buffer.byteLength(input.assistantContent, 'utf8') + Buffer.byteLength(contentBlocksJson, 'utf8')
    const current = await tx.one<ChatMessageRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [input.conversationId, input.systemAccountId, input.turnId])
    if (!current) throw new Error('活动回答不存在')
    const result = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_messages')}
      SET status = ?, content_text = ?, content_blocks_json = ?, content_bytes = ?, trace_id = ?,
          finish_reason = ?, error_code = ?, completed_at = ?
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [input.status, input.assistantContent, contentBlocksJson, bytes, input.traceId ?? null, input.finishReason ?? null, input.errorCode ?? null, input.now, input.conversationId, input.systemAccountId, input.turnId])
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
  const hasCursor = Number.isSafeInteger(input.beforeSequenceNo)
    && Number(input.beforeSequenceNo) >= postgresIntegerMin
    && Number(input.beforeSequenceNo) <= postgresIntegerMax
  const rows = await client.query<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ?
      ${hasCursor ? 'AND sequence_no < ?' : ''}
    ORDER BY sequence_no DESC
    LIMIT ?
  `, hasCursor
    ? [input.conversationId, input.systemAccountId, input.now, input.beforeSequenceNo, Math.max(1, Math.min(input.limit, 100))]
    : [input.conversationId, input.systemAccountId, input.now, Math.max(1, Math.min(input.limit, 100))])
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

export interface ChatRetentionCleanupResult {
  droppedPartitions: number
  deletedMessages: number
  deletedConversations: number
  recoveredTurns: number
  hasMore: boolean
}

export async function cleanupChatRetention(client: DatabaseClient, input: {
  now: string
  interruptedBefore: string
  limit: number
}): Promise<ChatRetentionCleanupResult> {
  const limit = Math.max(2, Math.min(Math.trunc(input.limit), 1000))
  const droppedPartitions = await dropExpiredPostgresChatPartitions(client, input.now)
  return client.transaction(async (tx) => {
    const staleTurns = await tx.query<{ id?: unknown; system_account_id?: unknown; active_turn_id?: unknown }>(`
      SELECT id, system_account_id, active_turn_id
      FROM ${chatTable(tx, 'chat_conversations')}
      WHERE active_turn_id IS NOT NULL AND active_started_at <= ?
      ORDER BY active_started_at ASC, id ASC LIMIT ?
    `, [input.interruptedBefore, Math.max(1, Math.floor(limit / 2))])
    let recoveredTurns = 0
    for (const stale of staleTurns) {
      const now = input.now
      const updated = await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_messages')}
        SET status = 'failed', error_code = 'stream_interrupted', completed_at = ?
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
          AND role = 'assistant' AND status = 'streaming'
      `, [now, String(stale.id), String(stale.system_account_id), String(stale.active_turn_id)])
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET active_turn_id = NULL, active_started_at = NULL, updated_at = ?
        WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
      `, [now, String(stale.id), String(stale.system_account_id), String(stale.active_turn_id)])
      recoveredTurns += updated.changes
    }

    const expiredTurns = await tx.query<{ conversation_id?: unknown; system_account_id?: unknown; turn_id?: unknown }>(`
      SELECT conversation_id, system_account_id, turn_id
      FROM ${chatTable(tx, 'chat_messages')}
      GROUP BY conversation_id, system_account_id, turn_id
      HAVING MAX(expires_at) <= ?
      ORDER BY MIN(expires_at) ASC, turn_id ASC LIMIT ?
    `, [input.now, Math.max(1, Math.floor(limit / 2))])
    let deletedMessages = 0
    const affectedConversations = new Set<string>()
    for (const expired of expiredTurns) {
      const conversationId = String(expired.conversation_id)
      const systemAccountId = String(expired.system_account_id)
      const turnId = String(expired.turn_id)
      const buckets = await tx.query<{ bucket_date?: unknown; content_bytes?: unknown }>(`
        SELECT substr(created_at, 1, 10) AS bucket_date, COALESCE(SUM(content_bytes), 0) AS content_bytes
        FROM ${chatTable(tx, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        GROUP BY substr(created_at, 1, 10)
      `, [conversationId, systemAccountId, turnId])
      for (const bucket of buckets) {
        const bytes = Number(bucket.content_bytes ?? 0)
        await tx.execute(`
          UPDATE ${chatTable(tx, 'chat_user_storage_windows')}
          SET content_bytes = CASE WHEN content_bytes > ? THEN content_bytes - ? ELSE 0 END, updated_at = ?
          WHERE system_account_id = ? AND bucket_date = ?
        `, [bytes, bytes, input.now, systemAccountId, String(bucket.bucket_date)])
      }
      await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_message_idempotency')} WHERE conversation_id = ? AND turn_id = ?`, [conversationId, turnId])
      const deleted = await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_messages')} WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?`, [conversationId, systemAccountId, turnId])
      deletedMessages += deleted.changes
      affectedConversations.add(conversationId)
    }
    await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_message_idempotency')} WHERE expires_at <= ?`, [input.now])
    await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_user_storage_windows')} WHERE content_bytes = 0 OR bucket_date < ?`, [storageWindowCutoffDate(input.now)])
    let deletedConversations = 0
    for (const conversationId of affectedConversations) {
      const deleted = await tx.execute(`
        DELETE FROM ${chatTable(tx, 'chat_conversations')}
        WHERE id = ? AND active_turn_id IS NULL
          AND NOT EXISTS (SELECT 1 FROM ${chatTable(tx, 'chat_messages')} WHERE conversation_id = ? LIMIT 1)
      `, [conversationId, conversationId])
      deletedConversations += deleted.changes
    }
    const staleTitles = await tx.query<{ id?: unknown; system_account_id?: unknown }>(`
      SELECT id, system_account_id FROM ${chatTable(tx, 'chat_conversations')} conversation
      WHERE title_source_message_id IS NOT NULL
        AND NOT EXISTS (
          SELECT 1 FROM ${chatTable(tx, 'chat_messages')} message
          WHERE message.conversation_id = conversation.id AND message.id = conversation.title_source_message_id
        )
      ORDER BY updated_at ASC, id ASC LIMIT ?
    `, [Math.max(1, Math.floor(limit / 2))])
    for (const conversation of staleTitles) {
      const conversationId = String(conversation.id)
      const ownerId = String(conversation.system_account_id)
      const firstUser = await tx.one<{ id?: unknown; content_text?: unknown }>(`
        SELECT id, content_text FROM ${chatTable(tx, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND role = 'user' AND expires_at > ?
        ORDER BY sequence_no ASC LIMIT 1
      `, [conversationId, ownerId, input.now])
      if (!firstUser) continue
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET title = ?, title_source_message_id = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [titleFromContent(String(firstUser.content_text ?? '')), String(firstUser.id), input.now, conversationId, ownerId])
    }
    const emptyBefore = new Date(new Date(input.now).getTime() - 24 * 60 * 60 * 1000).toISOString()
    const empty = await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_conversations')}
      WHERE active_turn_id IS NULL AND created_at <= ?
        AND NOT EXISTS (SELECT 1 FROM ${chatTable(tx, 'chat_messages')} WHERE conversation_id = ${chatTable(tx, 'chat_conversations')}.id LIMIT 1)
    `, [emptyBefore])
    deletedConversations += empty.changes
    return { droppedPartitions, deletedMessages, deletedConversations, recoveredTurns, hasMore: expiredTurns.length * 2 >= limit || staleTurns.length * 2 >= limit }
  })
}

async function dropExpiredPostgresChatPartitions(client: DatabaseClient, now: string): Promise<number> {
  if (client.driver !== 'postgres') return 0
  const cutoff = new Date(new Date(now).getTime() - 7 * 24 * 60 * 60 * 1000)
  const rows = await client.query<{ partition_name?: unknown }>(`
    SELECT child.relname AS partition_name
    FROM pg_inherits
    JOIN pg_class parent ON parent.oid = inhparent
    JOIN pg_namespace namespace ON namespace.oid = parent.relnamespace
    JOIN pg_class child ON child.oid = inhrelid
    WHERE namespace.nspname = 'juhe_chat' AND parent.relname = 'chat_messages'
  `)
  let dropped = 0
  for (const row of rows) {
    const name = String(row.partition_name ?? '')
    const match = /^chat_messages_(\d{4})(\d{2})(\d{2})$/.exec(name)
    if (!match) continue
    const partitionEnd = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]) + 1))
    if (partitionEnd > cutoff) continue
    await client.execute(`DROP TABLE IF EXISTS juhe_chat."${name}"`)
    dropped += 1
  }
  return dropped
}

function storageWindowCutoffDate(now: string): string {
  const date = new Date(now)
  date.setUTCDate(date.getUTCDate() - 7)
  return date.toISOString().slice(0, 10)
}

async function requireReplaceableTurn(client: DatabaseClient, input: {
  conversation: ConversationRow
  conversationId: string
  systemAccountId: string
  replaceTurnId: string
  now: string
}): Promise<{
  userMessage: ChatMessageRow
  assistantMessage: ChatMessageRow
  bytesByBucket: Map<string, number>
  totalBytes: number
}> {
  const rows = await client.query<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
    ORDER BY sequence_no ASC
  `, [input.conversationId, input.systemAccountId, input.replaceTurnId])
  if (rows.length !== 2) throw new ChatConflictError('chat_replace_conflict')
  const [userMessage, assistantMessage] = rows
  const userSequence = Number(userMessage.sequence_no)
  const assistantSequence = Number(assistantMessage.sequence_no)
  if (
    String(userMessage.role) !== 'user'
    || String(assistantMessage.role) !== 'assistant'
    || String(userMessage.status) !== 'completed'
    || String(assistantMessage.status) !== 'completed'
    || !Number.isSafeInteger(userSequence)
    || !Number.isSafeInteger(assistantSequence)
    || assistantSequence !== userSequence + 1
    || String(userMessage.expires_at) <= input.now
    || String(assistantMessage.expires_at) <= input.now
  ) throw new ChatConflictError('chat_replace_conflict')

  const maxSequenceRow = await client.one<{ max_sequence_no?: unknown }>(`
    SELECT MAX(sequence_no) AS max_sequence_no
    FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ?
  `, [input.conversationId, input.systemAccountId])
  if (Number(maxSequenceRow?.max_sequence_no) !== assistantSequence) throw new ChatConflictError('chat_replace_conflict')

  const inputMarkers = parseStoredInputMarkers(userMessage.content_blocks_json)
  if (!inputMarkers || inputMarkers.some((marker) => marker.inputType === 'input_image')) {
    throw new ChatConflictError('chat_replace_conflict')
  }

  const idempotencyRows = await client.query<IdempotencyRow & { client_message_id?: unknown; system_account_id?: unknown }>(`
    SELECT * FROM ${chatTable(client, 'chat_message_idempotency')}
    WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
  `, [input.conversationId, input.systemAccountId, input.replaceTurnId])
  const idempotency = idempotencyRows[0]
  if (
    idempotencyRows.length !== 1
    || String(idempotency.user_message_id) !== String(userMessage.id)
    || String(idempotency.assistant_message_id) !== String(assistantMessage.id)
    || String(idempotency.client_message_id) !== String(userMessage.client_message_id)
    || String(idempotency.system_account_id) !== input.systemAccountId
    || String(input.conversation.title_source_message_id ?? '') === String(assistantMessage.id)
  ) throw new ChatConflictError('chat_replace_conflict')

  const bytesByBucket = new Map<string, number>()
  let totalBytes = 0
  for (const row of rows) {
    const bytes = Number(row.content_bytes)
    const bucketDate = String(row.created_at ?? '').slice(0, 10)
    if (!Number.isSafeInteger(bytes) || bytes < 0 || !/^\d{4}-\d{2}-\d{2}$/.test(bucketDate)) {
      throw new ChatConflictError('chat_replace_conflict')
    }
    bytesByBucket.set(bucketDate, (bytesByBucket.get(bucketDate) ?? 0) + bytes)
    totalBytes += bytes
  }
  return { userMessage, assistantMessage, bytesByBucket, totalBytes }
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

async function lockChatUserStorageQuota(client: DatabaseClient, systemAccountId: string): Promise<void> {
  if (client.driver !== 'postgres') return
  await client.execute('SELECT pg_advisory_xact_lock(hashtextextended(?, 0))', [`juhe-ai:chat-storage:${systemAccountId}`])
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

async function decrementStorageWindowStrict(client: DatabaseClient, systemAccountId: string, bucketDate: string, bytes: number, now: string): Promise<void> {
  const table = chatTable(client, 'chat_user_storage_windows')
  const result = await client.execute(`
    UPDATE ${table}
    SET content_bytes = content_bytes - ?, updated_at = ?
    WHERE system_account_id = ? AND bucket_date = ? AND content_bytes >= ?
  `, [bytes, now, systemAccountId, bucketDate, bytes])
  if (result.changes !== 1) throw new Error(`聊天容量窗口数据不一致：${bucketDate} 日桶缺失或不足`)
  await client.execute(`
    DELETE FROM ${table}
    WHERE system_account_id = ? AND bucket_date = ? AND content_bytes = 0
  `, [systemAccountId, bucketDate])
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

function serializeInputContentMarkers(blocks: readonly ChatInputContentBlock[] | undefined): string {
  const normalized = blocks && blocks.length > 0 ? blocks : [{ type: 'input_text' as const }]
  if (normalized.length > maxInputContentBlocks) throw new Error(`用户输入块不能超过 ${maxInputContentBlocks} 个`)
  const markers: ChatMessageContentBlock[] = normalized.map((block, order) => {
    if (block?.type !== 'input_text' && block?.type !== 'input_image') throw new Error('用户输入块类型无效')
    return { type: 'input_marker', inputType: block.type, order }
  })
  return serializeContentBlocks(markers)
}

function mapConversation(row: ConversationRow): ChatConversation {
  return {
    id: String(row.id), systemAccountId: String(row.system_account_id),
    apiKeyId: nullable(row.api_key_id), apiKeyNameSnapshot: String(row.api_key_name_snapshot),
    title: String(row.title), isPinned: Number(row.is_pinned ?? 0) === 1, lastModel: nullable(row.last_model), activeTurnId: nullable(row.active_turn_id),
    lastMessageAt: String(row.last_message_at), createdAt: String(row.created_at), updatedAt: String(row.updated_at)
  }
}

function mapMessage(row: ChatMessageRow): ChatMessage {
  return {
    id: String(row.id), conversationId: String(row.conversation_id), turnId: String(row.turn_id),
    sequenceNo: Number(row.sequence_no), clientMessageId: nullable(row.client_message_id),
    role: String(row.role) as ChatMessageRole, status: String(row.status) as ChatMessageStatus,
    contentText: String(row.content_text ?? ''), contentBlocks: parseContentBlocks(row.content_blocks_json), model: String(row.model), traceId: nullable(row.trace_id),
    finishReason: nullable(row.finish_reason), errorCode: nullable(row.error_code), createdAt: String(row.created_at),
    completedAt: nullable(row.completed_at), expiresAt: String(row.expires_at)
  }
}

function serializeContentBlocks(blocks: ChatMessageContentBlock[]): string {
  const value = JSON.stringify(blocks)
  if (Buffer.byteLength(value, 'utf8') > maxContentBlocksBytes) throw new Error('消息结构化内容超过 256 KiB 上限')
  return value
}

function parseContentBlocks(value: unknown): ChatMessageContentBlock[] {
  if (typeof value !== 'string' || !value) return []
  if (Buffer.byteLength(value, 'utf8') > maxContentBlocksBytes) return []
  try {
    const parsed = JSON.parse(value) as unknown
    return Array.isArray(parsed) ? parsed.filter(isChatMessageContentBlock) : []
  } catch { return [] }
}

function isChatMessageContentBlock(value: unknown): value is ChatMessageContentBlock {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const block = value as Record<string, unknown>
  if (block.type === 'reasoning') return typeof block.text === 'string'
  if (block.type === 'input_marker') {
    return (block.inputType === 'input_text' || block.inputType === 'input_image')
      && Number.isSafeInteger(block.order) && Number(block.order) >= 0 && Number(block.order) < maxInputContentBlocks
  }
  return block.type === 'tool_call' && typeof block.id === 'string' && typeof block.toolType === 'string'
    && ['started', 'updated', 'completed', 'failed'].includes(String(block.status))
}

function parseStoredInputMarkers(value: unknown): Array<Extract<ChatMessageContentBlock, { type: 'input_marker' }>> | undefined {
  if (typeof value !== 'string' || !value || Buffer.byteLength(value, 'utf8') > maxContentBlocksBytes) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed) || parsed.length < 1 || parsed.length > maxInputContentBlocks) return undefined
    const markers: Array<Extract<ChatMessageContentBlock, { type: 'input_marker' }>> = []
    for (let order = 0; order < parsed.length; order += 1) {
      const valueAtOrder = parsed[order]
      if (!valueAtOrder || typeof valueAtOrder !== 'object' || Array.isArray(valueAtOrder)) return undefined
      const marker = valueAtOrder as Record<string, unknown>
      const keys = Object.keys(marker).sort()
      if (keys.length !== 3 || keys[0] !== 'inputType' || keys[1] !== 'order' || keys[2] !== 'type') return undefined
      if (marker.type !== 'input_marker' || (marker.inputType !== 'input_text' && marker.inputType !== 'input_image') || marker.order !== order) return undefined
      markers.push({ type: 'input_marker', inputType: marker.inputType, order })
    }
    return markers
  } catch {
    return undefined
  }
}

function nullable(value: unknown): string | undefined {
  return value === null || value === undefined || value === '' ? undefined : String(value)
}

type ConversationRow = Record<string, unknown> & { id?: unknown; system_account_id?: unknown; active_turn_id?: unknown; next_sequence_no?: unknown }
type ChatMessageRow = Record<string, unknown>
type IdempotencyRow = Record<string, unknown> & { turn_id?: unknown; user_message_id?: unknown; assistant_message_id?: unknown }
