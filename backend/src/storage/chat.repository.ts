import { randomUUID } from 'node:crypto'

import type { DatabaseClient } from './database-client.js'
import { ensurePostgresChatMessagePartitions } from './postgres-chat-message-partitions.js'
import { commitChatAssetsToMessageInClient, expireChatAssetsForConversationInClient, removeChatAssetReferencesForMessage } from './chat-assets.repository.js'

export type ChatMessageRole = 'user' | 'assistant'
export type ChatMessageStatus = 'completed' | 'streaming' | 'failed' | 'canceled'
export type ChatImageModel = 'gpt-image-2'

export type CancelActiveChatTurnResult =
  | { state: 'canceled'; assistantStatus: 'canceled' }
  | { state: 'already_terminal'; assistantStatus: Exclude<ChatMessageStatus, 'streaming'> }
  | { state: 'turn_mismatch' }
  | { state: 'not_found' }

export interface ChatConversation {
  id: string
  systemAccountId: string
  apiKeyId?: string
  apiKeyNameSnapshot: string
  title: string
  isPinned: boolean
  lastModel?: string
  defaultImageModel: ChatImageModel
  activeTurnId?: string
  userTurnCount: number
  messageRevision: number
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

export interface ChatConversationSyncMessage {
  id: string
  turnId: string
  sequenceNo: number
  role: ChatMessageRole
  status: ChatMessageStatus
  completedAt?: string
  expiresAt: string
}

export interface ChatConversationSyncHead {
  conversationId: string
  messageRevision: number
  lastSequenceNo: number
  activeTurn?: {
    turnId: string
    assistantMessageId: string
    startedAt: string
  }
  tail: ChatConversationSyncMessage[]
}

export interface ChatTurnSubmissionFact {
  turnId: string
  assistantMessageId: string
  assistantStatus: ChatMessageStatus
  errorCode?: string
  completedAt?: string
  traceId?: string
}

export type ChatMessageContentBlock =
  | { type: 'output_text'; blockId?: string; order?: number; text: string }
  | { type: 'reasoning'; blockId?: string; order?: number; text: string; status?: 'started' | 'completed' | 'failed' | 'canceled' }
  | { type: 'tool_call'; blockId?: string; order?: number; id?: string; callId?: string; toolType: string; status: 'started' | 'updated' | 'completed' | 'failed' | 'canceled'; item?: Record<string, unknown> }
  | { type: 'output_image'; blockId: string; order: number; assetId: string; status: 'started' | 'completed' | 'failed' | 'canceled'; mimeType?: string; width?: number; height?: number; revisedPrompt?: string }
  | { type: 'input_text'; text: string; order: number }
  | { type: 'input_image'; assetId: string; order: number }

export interface ChatInputContentBlock {
  type: 'input_text' | 'input_image'
  text?: string
  assetId?: string
}

const maxContentBlocksBytes = 256 * 1024
const maxInputContentBlocks = 11
// 192 KiB visible answer + 192 KiB persisted blocks target + 64 KiB serialization/safety margin.
export const chatAssistantStorageReservationBytes = (192 + 192 + 64) * 1024
const postgresIntegerMin = -2_147_483_648
const postgresIntegerMax = 2_147_483_647
const sqliteChatUserPolicyLocks = new Map<string, Promise<void>>()

export class ChatConflictError extends Error {
  constructor(public readonly code: 'chat_message_in_progress' | 'chat_context_compacting' | 'chat_conversation_clearing' | 'chat_storage_quota_exceeded' | 'chat_replace_conflict' | 'chat_conversation_limit_exceeded' | 'chat_turn_limit_exceeded') {
    super({
      chat_message_in_progress: '当前会话正在生成回答',
      chat_context_compacting: '当前会话正在压缩上下文',
      chat_conversation_clearing: '当前会话正在清空',
      chat_storage_quota_exceeded: '聊天容量已达到上限，请先删除部分会话',
      chat_replace_conflict: '最近一轮已变化，请重新确认后再编辑',
      chat_conversation_limit_exceeded: '会话数量已达到上限，请先删除部分会话',
      chat_turn_limit_exceeded: '当前会话轮次已达到上限，请新建会话继续提问'
    }[code])
  }
}

export class ChatAssistantStorageLimitError extends Error {
  readonly code = 'chat_assistant_storage_limit_exceeded'

  constructor() {
    super('助手回答超过可安全持久化的字节上限')
    this.name = 'ChatAssistantStorageLimitError'
  }
}

export async function createChatConversation(client: DatabaseClient, input: {
  id?: string
  systemAccountId: string
  apiKeyId: string
  apiKeyNameSnapshot: string
  defaultModel?: string
  now: string
  maxConversationsPerUser: number
}): Promise<ChatConversation> {
  const id = input.id ?? chatId('conv')
  return withSqliteChatUserPolicyLock(client, input.systemAccountId, () => client.transaction(async (tx) => {
    await lockChatUserStorageQuota(tx, input.systemAccountId)
    const table = chatTable(tx, 'chat_conversations')
    const count = await tx.one<{ total?: unknown }>(`SELECT COUNT(*) AS total FROM ${table} WHERE system_account_id = ?`, [input.systemAccountId])
    if (Number(count?.total ?? 0) >= input.maxConversationsPerUser) throw new ChatConflictError('chat_conversation_limit_exceeded')
    await tx.execute(`
      INSERT INTO ${table} (
        id, system_account_id, api_key_id, api_key_name_snapshot, title, last_model, default_image_model,
        next_sequence_no, user_turn_count, last_message_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, '新对话', ?, 'gpt-image-2', 1, 0, ?, ?, ?)
    `, [id, input.systemAccountId, input.apiKeyId, input.apiKeyNameSnapshot, input.defaultModel ?? null, input.now, input.now, input.now])
    return requireConversation(tx, id, input.systemAccountId)
  }))
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

export async function getChatConversationSyncHead(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  now: string
}): Promise<ChatConversationSyncHead | undefined> {
  const rows = await client.query<ChatConversationSyncRow>(`
    WITH owned_conversation AS (
      SELECT id, message_revision, active_turn_id, active_started_at
      FROM ${chatTable(client, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?
    ), candidate_messages AS (
      SELECT message.id, message.turn_id, message.sequence_no, message.role,
             message.status, message.completed_at, message.expires_at
      FROM ${chatTable(client, 'chat_messages')} AS message
      JOIN owned_conversation AS conversation ON conversation.id = message.conversation_id
      WHERE message.system_account_id = ? AND message.expires_at > ?
      ORDER BY message.sequence_no DESC
      LIMIT 16
    ), complete_turns AS (
      SELECT turn_id, MAX(sequence_no) AS latest_sequence_no
      FROM candidate_messages
      GROUP BY turn_id
      HAVING COUNT(*) = 2
        AND SUM(CASE WHEN role = 'user' THEN 1 ELSE 0 END) = 1
        AND SUM(CASE WHEN role = 'assistant' THEN 1 ELSE 0 END) = 1
        AND MAX(sequence_no) = MIN(sequence_no) + 1
    ), tail_turn AS (
      SELECT turn_id
      FROM complete_turns
      ORDER BY latest_sequence_no DESC
      LIMIT 1
    ), tail_messages AS (
      SELECT message.id, message.turn_id, message.sequence_no, message.role,
             message.status, message.completed_at, message.expires_at
      FROM candidate_messages AS message
      JOIN tail_turn ON tail_turn.turn_id = message.turn_id
    ), active_assistant AS (
      SELECT message.id, message.turn_id
      FROM ${chatTable(client, 'chat_messages')} AS message
      JOIN owned_conversation AS conversation
        ON conversation.id = message.conversation_id
        AND conversation.active_turn_id = message.turn_id
      WHERE message.system_account_id = ? AND message.expires_at > ?
        AND message.role = 'assistant' AND message.status = 'streaming'
      LIMIT 1
    )
    SELECT conversation.id AS conversation_id,
           conversation.message_revision,
           COALESCE((
             SELECT message.sequence_no
             FROM ${chatTable(client, 'chat_messages')} AS message
             WHERE message.conversation_id = conversation.id
               AND message.system_account_id = ? AND message.expires_at > ?
             ORDER BY message.sequence_no DESC
             LIMIT 1
           ), 0) AS last_sequence_no,
           active.id AS active_assistant_message_id,
           active.turn_id AS active_turn_id,
           conversation.active_started_at,
           tail.id AS tail_id,
           tail.turn_id AS tail_turn_id,
           tail.sequence_no AS tail_sequence_no,
           tail.role AS tail_role,
           tail.status AS tail_status,
           tail.completed_at AS tail_completed_at,
           tail.expires_at AS tail_expires_at
    FROM owned_conversation AS conversation
    LEFT JOIN active_assistant AS active ON 1 = 1
    LEFT JOIN tail_messages AS tail ON 1 = 1
    ORDER BY tail.sequence_no ASC
  `, [
    input.conversationId,
    input.systemAccountId,
    input.systemAccountId,
    input.now,
    input.systemAccountId,
    input.now,
    input.systemAccountId,
    input.now
  ])
  const first = rows[0]
  if (!first) return undefined
  const activeTurnId = nullable(first.active_turn_id)
  const activeAssistantMessageId = nullable(first.active_assistant_message_id)
  const activeStartedAt = nullable(first.active_started_at)
  const tail = rows.flatMap((row): ChatConversationSyncMessage[] => {
    const id = nullable(row.tail_id)
    const turnId = nullable(row.tail_turn_id)
    const expiresAt = nullable(row.tail_expires_at)
    if (!id || !turnId || !expiresAt) return []
    return [{
      id,
      turnId,
      sequenceNo: Number(row.tail_sequence_no),
      role: String(row.tail_role) as ChatMessageRole,
      status: String(row.tail_status) as ChatMessageStatus,
      completedAt: nullable(row.tail_completed_at),
      expiresAt
    }]
  })
  return {
    conversationId: String(first.conversation_id),
    messageRevision: normalizedChatMessageRevision(first.message_revision),
    lastSequenceNo: Number(first.last_sequence_no),
    activeTurn: activeTurnId && activeAssistantMessageId && activeStartedAt
      ? { turnId: activeTurnId, assistantMessageId: activeAssistantMessageId, startedAt: activeStartedAt }
      : undefined,
    tail
  }
}

export async function findChatTurnByClientMessageId(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  clientMessageId: string
}): Promise<ChatTurnSubmissionFact | undefined> {
  const idempotencyTable = chatTable(client, 'chat_message_idempotency')
  const messagesTable = chatTable(client, 'chat_messages')
  const row = await client.one<{
    turn_id?: unknown
    assistant_message_id?: unknown
    assistant_status?: unknown
    error_code?: unknown
    completed_at?: unknown
    trace_id?: unknown
  }>(`
    SELECT submission.turn_id, assistant.id AS assistant_message_id,
           assistant.status AS assistant_status, assistant.error_code,
           assistant.completed_at, assistant.trace_id
    FROM ${idempotencyTable} AS submission
    JOIN ${messagesTable} AS assistant
      ON assistant.id = submission.assistant_message_id
      AND assistant.conversation_id = submission.conversation_id
      AND assistant.system_account_id = submission.system_account_id
      AND assistant.turn_id = submission.turn_id
      AND assistant.role = 'assistant'
    WHERE submission.conversation_id = ?
      AND submission.client_message_id = ?
      AND submission.system_account_id = ?
  `, [input.conversationId, input.clientMessageId, input.systemAccountId])
  if (!row) return undefined
  const errorCode = nullable(row.error_code)
  const completedAt = nullable(row.completed_at)
  const traceId = nullable(row.trace_id)
  return {
    turnId: String(row.turn_id),
    assistantMessageId: String(row.assistant_message_id),
    assistantStatus: String(row.assistant_status) as ChatMessageStatus,
    ...(errorCode ? { errorCode } : {}),
    ...(completedAt ? { completedAt } : {}),
    ...(traceId ? { traceId } : {})
  }
}

export async function updateChatConversation(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  title?: string
  isPinned?: boolean
  defaultImageModel?: ChatImageModel
  now: string
}): Promise<ChatConversation | undefined> {
  const assignments: string[] = []
  const params: unknown[] = []
  if (input.title !== undefined) {
    assignments.push('title = ?', 'title_source_message_id = NULL')
    params.push(input.title)
  }
  if (input.isPinned !== undefined) {
    assignments.push('is_pinned = ?')
    params.push(input.isPinned ? 1 : 0)
  }
  if (input.defaultImageModel !== undefined) {
    assignments.push('default_image_model = ?')
    params.push(normalizedChatImageModel(input.defaultImageModel))
  }
  assignments.push('updated_at = ?')
  params.push(input.now, input.conversationId, input.systemAccountId)
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET ${assignments.join(', ')}
    WHERE id = ? AND system_account_id = ?
  `, params)
  if (result.changes !== 1) return undefined
  return getChatConversation(client, input.conversationId, input.systemAccountId)
}

export async function deleteChatConversation(client: DatabaseClient, conversationId: string, systemAccountId: string): Promise<boolean> {
  return client.transaction(async (tx) => {
    await lockChatUserStorageQuota(tx, systemAccountId)
    const conversation = await tx.one<ConversationRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [conversationId, systemAccountId])
    if (!conversation) return false
    if (conversation.active_turn_id) throw new ChatConflictError('chat_message_in_progress')
    await releaseChatConversationStorageAndExpireAssets(tx, {
      conversationId,
      systemAccountId,
      now: new Date().toISOString()
    })
    const result = await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_conversations')} WHERE id = ? AND system_account_id = ?
    `, [conversationId, systemAccountId])
    return result.changes === 1
  })
}

export async function clearChatConversation(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  now: string
}): Promise<ChatConversation | undefined> {
  return client.transaction(async (tx) => {
    await lockChatUserStorageQuota(tx, input.systemAccountId)
    const conversation = await tx.one<ConversationRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [input.conversationId, input.systemAccountId])
    if (!conversation) return undefined
    if (conversation.active_turn_id) throw new ChatConflictError('chat_message_in_progress')
    if (String(conversation.context_state) === 'compacting') throw new ChatConflictError('chat_context_compacting')

    await releaseChatConversationStorageAndExpireAssets(tx, input)
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_image_generations')}
      WHERE conversation_id = ? AND system_account_id = ?
    `, [input.conversationId, input.systemAccountId])
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_asset_references')}
      WHERE conversation_id = ?
    `, [input.conversationId])
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_message_idempotency')}
      WHERE conversation_id = ? AND system_account_id = ?
    `, [input.conversationId, input.systemAccountId])
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ?
    `, [input.conversationId, input.systemAccountId])
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_context_checkpoints')}
      WHERE conversation_id = ? AND system_account_id = ?
    `, [input.conversationId, input.systemAccountId])

    const result = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET title = '新对话', title_source_message_id = NULL,
          next_sequence_no = 1, user_turn_count = 0,
          message_revision = message_revision + 1,
          active_turn_id = NULL, active_started_at = NULL,
          context_revision = context_revision + 1,
          active_checkpoint_id = NULL, compacted_through_sequence = 0,
          context_state = 'ready', active_context_tokens = NULL,
          effective_context_limit_tokens = NULL, context_usage_estimated = 1,
          context_claim_id = NULL, context_claim_revision = NULL,
          context_claim_through_sequence = NULL, context_claimed_at = NULL,
          context_retry_at = NULL, context_attempt_count = 0,
          context_error_code = NULL, context_progress_sequence = 0,
          context_progress_earliest_expires_at = NULL,
          last_message_at = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ?
        AND active_turn_id IS NULL AND context_state != 'compacting'
    `, [input.now, input.now, input.conversationId, input.systemAccountId])
    if (result.changes !== 1) throw new Error('清空会话状态发生并发冲突')
    const cleared = await tx.one<ConversationRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?
    `, [input.conversationId, input.systemAccountId])
    return cleared ? mapConversation(cleared) : undefined
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
  retentionDays: number
  maxTurnsPerConversation: number
  replaceTurnId?: string
}): Promise<{ turnId: string; userMessage: ChatMessage; assistantMessage: ChatMessage; duplicate: boolean }> {
  return client.transaction(async (tx) => {
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

    const userTurnCount = normalizedChatUserTurnCount(conversation.user_turn_count)
    if (!input.replaceTurnId && userTurnCount >= input.maxTurnsPerConversation) {
      throw new ChatConflictError('chat_turn_limit_exceeded')
    }
    await ensurePostgresChatMessagePartitions(tx, input.now)

    const userContentBlocksJson = serializeInputContentMarkers(input.contentBlocks, input.userContent)
    const userBytes = Buffer.byteLength(input.userContent, 'utf8') + Buffer.byteLength(userContentBlocksJson, 'utf8')
    let userSequence = Number(conversation.next_sequence_no)
    let assistantSequence = userSequence + 1
    let replacedUserMessageId: string | undefined
    let replacedAssistantMessageId: string | undefined
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
      replacedAssistantMessageId = String(replacement.assistantMessage.id)
      const usedBytes = await recentStorageBytes(tx, input.systemAccountId, input.now, input.retentionDays)
      if (usedBytes < replacement.totalBytes) throw new Error('聊天容量窗口数据不一致：最近窗口小于待替换轮次')
      if (usedBytes - replacement.totalBytes + userBytes + chatAssistantStorageReservationBytes > input.storageQuotaBytes) {
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
      const retainedAssetIds = [...new Set(
        input.contentBlocks
          ?.filter((block) => block.type === 'input_image' && block.assetId)
          .map((block) => block.assetId!.trim()) ?? []
      )]
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_assets')}
        SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
            cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
            updated_at = ?
        WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
          AND source_kind = 'user_upload'
          ${retainedAssetIds.length > 0 ? `AND id NOT IN (${tx.dialect.bindPlaceholders(retainedAssetIds.length)})` : ''}
      `, [
        input.now,
        input.now,
        input.now,
        input.now,
        input.systemAccountId,
        input.conversationId,
        replacedUserMessageId,
        ...retainedAssetIds
      ])
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_assets')}
        SET turn_id = NULL, message_id = NULL, committed_at = NULL,
            observation_status = 'not_requested', observation_json = NULL,
            observation_revision = observation_revision + 1,
            observation_claim_id = NULL, observation_claimed_at = NULL,
            updated_at = ?
        WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
      `, [input.now, input.systemAccountId, input.conversationId, replacedUserMessageId])
      const replacedUserInputAssetIds = (await tx.query<{ asset_id?: unknown }>(`
        SELECT asset_id FROM ${chatTable(tx, 'chat_asset_references')}
        WHERE conversation_id = ? AND message_id = ? AND reference_kind = 'user_input' AND expires_at > ?
      `, [input.conversationId, replacedUserMessageId, input.now]))
        .map((row) => String(row.asset_id ?? '').trim())
        .filter(Boolean)
      await removeChatAssetReferencesForMessage(tx, {
        systemAccountId: input.systemAccountId,
        conversationId: input.conversationId,
        messageId: replacedUserMessageId,
        now: input.now
      })
      if (replacedUserInputAssetIds.length > 0) {
        await tx.execute(`
          UPDATE ${chatTable(tx, 'chat_assets')} AS asset
          SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
              cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
              updated_at = ?
          WHERE asset.system_account_id = ? AND asset.conversation_id = ?
            AND asset.source_kind = 'assistant_generated'
            AND asset.id IN (${tx.dialect.bindPlaceholders(replacedUserInputAssetIds.length)})
            AND asset.turn_id IS NULL AND asset.message_id IS NULL
            AND NOT EXISTS (
              SELECT 1 FROM ${chatTable(tx, 'chat_asset_references')} AS reference
              WHERE reference.asset_id = asset.id
                AND reference.conversation_id = asset.conversation_id
                AND reference.expires_at > ?
            )
            ${retainedAssetIds.length > 0 ? `AND asset.id NOT IN (${tx.dialect.bindPlaceholders(retainedAssetIds.length)})` : ''}
        `, [
          input.now,
          input.now,
          input.now,
          input.now,
          input.systemAccountId,
          input.conversationId,
          ...replacedUserInputAssetIds,
          input.now,
          ...retainedAssetIds
        ])
      }
      await removeChatAssetReferencesForMessage(tx, {
        systemAccountId: input.systemAccountId,
        conversationId: input.conversationId,
        messageId: replacedAssistantMessageId,
        now: input.now
      })
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_assets')}
        SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
            cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
            updated_at = ?
        WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
          AND source_kind = 'assistant_generated'
          AND cleanup_status IN ('active', 'failed')
          ${retainedAssetIds.length > 0 ? `AND id NOT IN (${tx.dialect.bindPlaceholders(retainedAssetIds.length)})` : ''}
      `, [
        input.now,
        input.now,
        input.now,
        input.now,
        input.systemAccountId,
        input.conversationId,
        replacedAssistantMessageId,
        ...retainedAssetIds
      ])
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_assets')}
        SET turn_id = NULL, message_id = NULL, committed_at = NULL,
            observation_status = 'not_requested', observation_json = NULL,
            observation_revision = observation_revision + 1,
            observation_claim_id = NULL, observation_claimed_at = NULL,
            updated_at = ?
        WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
          AND source_kind = 'assistant_generated'
          AND cleanup_status IN ('active', 'failed')
      `, [
        input.now,
        input.systemAccountId,
        input.conversationId,
        replacedAssistantMessageId
      ])
      const deletedMessages = await tx.execute(`
        DELETE FROM ${chatTable(tx, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
      `, [input.conversationId, input.systemAccountId, input.replaceTurnId])
      if (deletedMessages.changes !== 2) throw new ChatConflictError('chat_replace_conflict')
    } else {
      if (conversation.active_turn_id) throw new ChatConflictError('chat_message_in_progress')
      const usedBytes = await recentStorageBytes(tx, input.systemAccountId, input.now, input.retentionDays)
      if (usedBytes + userBytes + chatAssistantStorageReservationBytes > input.storageQuotaBytes) throw new ChatConflictError('chat_storage_quota_exceeded')
    }

    const turnId = chatId('turn')
    const userMessageId = chatId('msg')
    const assistantMessageId = chatId('msg')
    const expiresAt = addDays(input.now, input.retentionDays)
    const messagesTable = chatTable(tx, 'chat_messages')
    await tx.execute(`
      INSERT INTO ${messagesTable} (
        id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
        role, status, content_text, content_blocks_json, content_bytes, model, created_at, completed_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, ?, 'user', 'completed', ?, ?, ?, ?, ?, ?, ?)
    `, [userMessageId, input.conversationId, input.systemAccountId, turnId, userSequence, input.clientMessageId, input.userContent, userContentBlocksJson, userBytes, input.model, input.now, input.now, expiresAt])
    await commitChatAssetsToMessageInClient(tx, {
      assetIds: input.contentBlocks?.filter((block) => block.type === 'input_image').map((block) => block.assetId ?? '') ?? [],
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      messageId: userMessageId,
      now: input.now,
      retentionDays: input.retentionDays
    })
    await tx.execute(`
      INSERT INTO ${messagesTable} (
        id, conversation_id, system_account_id, turn_id, sequence_no,
        role, status, content_text, content_bytes, storage_reserved_bytes, model, created_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, 'assistant', 'streaming', '', 0, ?, ?, ?, ?)
    `, [assistantMessageId, input.conversationId, input.systemAccountId, turnId, assistantSequence, chatAssistantStorageReservationBytes, input.model, input.now, expiresAt])
    await tx.execute(`
      INSERT INTO ${chatTable(tx, 'chat_message_idempotency')} (
        conversation_id, client_message_id, system_account_id, turn_id,
        user_message_id, assistant_message_id, created_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `, [input.conversationId, input.clientMessageId, input.systemAccountId, turnId, userMessageId, assistantMessageId, input.now, expiresAt])
    await incrementStorageWindow(tx, input.systemAccountId, input.now, userBytes, chatAssistantStorageReservationBytes)
    const title = titleFromContent(input.userContent)
    if (replacedUserMessageId) {
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET title = CASE WHEN title_source_message_id = ? THEN ? ELSE title END,
            title_source_message_id = CASE WHEN title_source_message_id = ? THEN ? ELSE title_source_message_id END,
            message_revision = message_revision + 1,
            context_revision = context_revision + 1,
            context_state = CASE WHEN context_state = 'compacting' THEN 'compact_pending' ELSE context_state END,
            context_claim_id = NULL, context_claim_revision = NULL,
            context_claim_through_sequence = NULL, context_claimed_at = NULL,
            context_retry_at = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_retry_at END,
            context_error_code = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_error_code END,
            context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
            active_turn_id = ?, active_started_at = ?, last_model = ?, last_message_at = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [replacedUserMessageId, title, replacedUserMessageId, userMessageId, turnId, input.now, input.model, input.now, input.now, input.conversationId, input.systemAccountId])
    } else {
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET title = CASE WHEN next_sequence_no = 1 AND title = '新对话' AND title_source_message_id IS NULL THEN ? ELSE title END,
            title_source_message_id = CASE WHEN next_sequence_no = 1 AND title = '新对话' AND title_source_message_id IS NULL THEN ? ELSE title_source_message_id END,
            message_revision = message_revision + 1,
            context_revision = context_revision + 1,
            context_state = CASE WHEN context_state = 'compacting' THEN 'compact_pending' ELSE context_state END,
            context_claim_id = NULL, context_claim_revision = NULL,
            context_claim_through_sequence = NULL, context_claimed_at = NULL,
            context_retry_at = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_retry_at END,
            context_error_code = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_error_code END,
            context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
            next_sequence_no = ?, user_turn_count = user_turn_count + 1, active_turn_id = ?, active_started_at = ?,
            last_model = ?, last_message_at = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [title, userMessageId, assistantSequence + 1, turnId, input.now, input.model, input.now, input.now, input.conversationId, input.systemAccountId])
    }
    const pair = await loadMessagePair(tx, input.conversationId, input.systemAccountId, turnId)
    return { turnId, ...pair, duplicate: false }
  })
}

export async function assertChatTurnReplaceable(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  replaceTurnId: string
  now: string
}): Promise<void> {
  await client.transaction(async (tx) => {
    const conversation = await lockConversation(tx, input.conversationId, input.systemAccountId)
    if (conversation.active_turn_id) throw new ChatConflictError('chat_replace_conflict')
    await requireReplaceableTurn(tx, { conversation, ...input })
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

export async function cancelActiveChatTurnIfMatches(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedTurnId: string
  now: string
}): Promise<CancelActiveChatTurnResult> {
  return client.transaction(async (tx) => {
    const lockSuffix = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const conversation = await tx.one<ConversationRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?${lockSuffix}
    `, [input.conversationId, input.systemAccountId])
    if (!conversation) return { state: 'not_found' }

    const assistant = await tx.one<ChatMessageRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ? AND role = 'assistant'
      ${lockSuffix}
    `, [input.conversationId, input.systemAccountId, input.expectedTurnId])
    const initialState = classifyConditionalStopState(conversation, assistant, input.expectedTurnId)
    if (initialState) return initialState
    const reservationBytes = requiredAssistantStorageReservation(assistant!)

    const messageResult = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_messages')}
      SET status = 'canceled', storage_reserved_bytes = 0,
          finish_reason = NULL, error_code = NULL, completed_at = ?
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [input.now, input.conversationId, input.systemAccountId, input.expectedTurnId])
    if (messageResult.changes !== 1) {
      return (await readConditionalStopState(tx, input)) ?? { state: 'turn_mismatch' }
    }
    await releaseStorageWindowReservationStrict(
      tx,
      input.systemAccountId,
      String(assistant!.created_at),
      reservationBytes,
      input.now
    )

    const conversationResult = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET active_turn_id = NULL, active_started_at = NULL,
          message_revision = message_revision + 1,
          last_message_at = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
    `, [input.now, input.now, input.conversationId, input.systemAccountId, input.expectedTurnId])
    if (conversationResult.changes !== 1) {
      const authoritative = await readConditionalStopState(tx, input)
      if (authoritative?.state !== 'already_terminal' || authoritative.assistantStatus !== 'canceled') {
        return authoritative ?? { state: 'turn_mismatch' }
      }
    }
    return { state: 'canceled', assistantStatus: 'canceled' }
  })
}

export async function failInterruptedChatTurnIfMatches(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedTurnId: string
  now: string
}): Promise<CancelActiveChatTurnResult> {
  return client.transaction(async (tx) => {
    const lockSuffix = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const conversation = await tx.one<ConversationRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?${lockSuffix}
    `, [input.conversationId, input.systemAccountId])
    if (!conversation) return { state: 'not_found' }
    const assistant = await tx.one<ChatMessageRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ? AND role = 'assistant'
      ${lockSuffix}
    `, [input.conversationId, input.systemAccountId, input.expectedTurnId])
    const initialState = classifyConditionalStopState(conversation, assistant, input.expectedTurnId)
    if (initialState) return initialState
    const reservationBytes = requiredAssistantStorageReservation(assistant!)
    const messageResult = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_messages')}
      SET status = 'failed', storage_reserved_bytes = 0,
          finish_reason = NULL, error_code = 'stream_interrupted', completed_at = ?
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [input.now, input.conversationId, input.systemAccountId, input.expectedTurnId])
    if (messageResult.changes !== 1) return (await readConditionalStopState(tx, input)) ?? { state: 'turn_mismatch' }
    await releaseStorageWindowReservationStrict(tx, input.systemAccountId, String(assistant!.created_at), reservationBytes, input.now)
    const conversationResult = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET active_turn_id = NULL, active_started_at = NULL,
          message_revision = message_revision + 1, last_message_at = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
    `, [input.now, input.now, input.conversationId, input.systemAccountId, input.expectedTurnId])
    if (conversationResult.changes !== 1) throw new Error('活动轮次中断收口失败')
    return { state: 'already_terminal', assistantStatus: 'failed' }
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
  let requestedContentBlocksJson = '[]'
  let requestedBytes = chatAssistantStorageReservationBytes + 1
  let serializationExceeded = false
  try {
    requestedContentBlocksJson = serializeContentBlocks(input.contentBlocks ?? [])
    requestedBytes = Buffer.byteLength(input.assistantContent, 'utf8') + Buffer.byteLength(requestedContentBlocksJson, 'utf8')
  } catch {
    serializationExceeded = true
  }
  const outcome = await client.transaction(async (tx) => {
    const conversation = await lockConversation(tx, input.conversationId, input.systemAccountId)
    if (String(conversation.active_turn_id ?? '') !== input.turnId) throw new Error('活动回答不存在')
    const lockSuffix = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const current = await tx.one<ChatMessageRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_messages')}
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'${lockSuffix}
    `, [input.conversationId, input.systemAccountId, input.turnId])
    if (!current) throw new Error('活动回答不存在')
    const reservationBytes = requiredAssistantStorageReservation(current)
    const storageLimitExceeded = serializationExceeded || requestedBytes > reservationBytes
    const contentText = storageLimitExceeded ? '' : input.assistantContent
    const contentBlocksJson = storageLimitExceeded ? '[]' : requestedContentBlocksJson
    const bytes = storageLimitExceeded ? Buffer.byteLength('[]', 'utf8') : requestedBytes
    const status = storageLimitExceeded ? 'failed' : input.status
    const finishReason = storageLimitExceeded ? undefined : input.finishReason
    const errorCode = storageLimitExceeded ? 'chat_assistant_storage_limit_exceeded' : input.errorCode
    const result = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_messages')}
      SET status = ?, content_text = ?, content_blocks_json = ?, content_bytes = ?, trace_id = ?,
          storage_reserved_bytes = 0, finish_reason = ?, error_code = ?, completed_at = ?
      WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        AND role = 'assistant' AND status = 'streaming'
    `, [status, contentText, contentBlocksJson, bytes, input.traceId ?? null, finishReason ?? null, errorCode ?? null, input.now, input.conversationId, input.systemAccountId, input.turnId])
    if (result.changes !== 1) throw new Error('活动回答不存在')
    await settleStorageWindowReservationStrict(tx, input.systemAccountId, String(current.created_at), reservationBytes, bytes, input.now)
    const conversationResult = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET active_turn_id = NULL, active_started_at = NULL,
          message_revision = message_revision + 1,
          last_message_at = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
    `, [input.now, input.now, input.conversationId, input.systemAccountId, input.turnId])
    if (conversationResult.changes !== 1) throw new Error('活动轮次状态更新失败')
    const pair = await loadMessagePair(tx, input.conversationId, input.systemAccountId, input.turnId)
    return { assistantMessage: pair.assistantMessage, storageLimitExceeded }
  })
  if (outcome.storageLimitExceeded) throw new ChatAssistantStorageLimitError()
  return outcome.assistantMessage
}

async function readConditionalStopState(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedTurnId: string
}): Promise<CancelActiveChatTurnResult | undefined> {
  const lockSuffix = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  const conversation = await client.one<ConversationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_conversations')}
    WHERE id = ? AND system_account_id = ?${lockSuffix}
  `, [input.conversationId, input.systemAccountId])
  if (!conversation) return { state: 'not_found' }
  const assistant = await client.one<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ? AND role = 'assistant'
    ${lockSuffix}
  `, [input.conversationId, input.systemAccountId, input.expectedTurnId])
  return classifyConditionalStopState(conversation, assistant, input.expectedTurnId)
}

function classifyConditionalStopState(
  conversation: ConversationRow,
  assistant: ChatMessageRow | undefined,
  expectedTurnId: string
): CancelActiveChatTurnResult | undefined {
  if (!assistant) {
    return nullable(conversation.active_turn_id) && String(conversation.active_turn_id) !== expectedTurnId
      ? { state: 'turn_mismatch' }
      : { state: 'not_found' }
  }
  const assistantStatus = String(assistant.status) as ChatMessageStatus
  if (assistantStatus !== 'streaming') {
    return {
      state: 'already_terminal',
      assistantStatus: assistantStatus as Exclude<ChatMessageStatus, 'streaming'>
    }
  }
  if (String(conversation.active_turn_id ?? '') !== expectedTurnId) return { state: 'turn_mismatch' }
  return undefined
}

export async function listChatMessages(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  beforeSequenceNo?: number
  afterSequenceNo?: number
  fromSequenceNo?: number
  limit: number
  now: string
}): Promise<ChatMessage[]> {
  const cursorCount = [input.beforeSequenceNo, input.afterSequenceNo, input.fromSequenceNo]
    .filter((value) => value !== undefined).length
  if (cursorCount > 1) throw new Error('消息游标只能指定一个')
  await requireConversation(client, input.conversationId, input.systemAccountId)
  const cursor = input.beforeSequenceNo ?? input.afterSequenceNo ?? input.fromSequenceNo
  const hasCursor = Number.isSafeInteger(cursor)
    && Number(cursor) >= postgresIntegerMin
    && Number(cursor) <= postgresIntegerMax
  const cursorCondition = !hasCursor
    ? ''
    : input.beforeSequenceNo !== undefined
      ? 'AND sequence_no < ?'
      : input.afterSequenceNo !== undefined
        ? 'AND sequence_no > ?'
        : 'AND sequence_no >= ?'
  const ascending = input.afterSequenceNo !== undefined || input.fromSequenceNo !== undefined
  const params = hasCursor
    ? [input.conversationId, input.systemAccountId, input.now, cursor, Math.max(1, Math.min(input.limit, 100))]
    : [input.conversationId, input.systemAccountId, input.now, Math.max(1, Math.min(input.limit, 100))]
  const rows = await client.query<ChatMessageRow>(`
    SELECT * FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ?
      ${cursorCondition}
    ORDER BY sequence_no ${ascending ? 'ASC' : 'DESC'}
    LIMIT ?
  `, params)
  return (ascending ? rows : rows.reverse()).map(mapMessage)
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
  recoveredCompactions: number
  claimedAssets: number
  deletedAssets: number
  failedAssets: number
  hasMoreAssets: boolean
  deletedCheckpoints: number
  hasMoreCheckpoints: boolean
  hasMore: boolean
}

export async function cleanupChatRetention(client: DatabaseClient, input: {
  now: string
  interruptedBefore: string
  limit: number
  retentionDays: number
  isActiveTurn?: (ownerId: string, conversationId: string, turnId: string) => boolean
}): Promise<ChatRetentionCleanupResult> {
  const limit = Math.max(2, Math.min(Math.trunc(input.limit), 1000))
  const retentionDays = input.retentionDays
  return client.transaction(async (tx) => {
    const affectedConversations = new Map<string, AffectedChatConversation>()
    const partitionCleanup = await dropExpiredPostgresChatPartitions(tx, input.now, retentionDays, affectedConversations)
    const advancedConversationKeys = partitionCleanup.advancedConversationKeys
    const droppedPartitions = partitionCleanup.droppedPartitions
    const staleTurns = await tx.query<{ id?: unknown; system_account_id?: unknown; active_turn_id?: unknown }>(`
      SELECT id, system_account_id, active_turn_id
      FROM ${chatTable(tx, 'chat_conversations')}
      WHERE active_turn_id IS NOT NULL AND active_started_at <= ?
      ORDER BY active_started_at ASC, id ASC LIMIT ?
    `, [input.interruptedBefore, Math.max(1, Math.floor(limit / 2))])
    let recoveredTurns = 0
    for (const stale of staleTurns) {
      if (input.isActiveTurn?.(String(stale.system_account_id), String(stale.id), String(stale.active_turn_id))) continue
      const now = input.now
      const staleAssistant = await tx.one<ChatMessageRow>(`
        SELECT * FROM ${chatTable(tx, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
          AND role = 'assistant' AND status = 'streaming'
      `, [String(stale.id), String(stale.system_account_id), String(stale.active_turn_id)])
      const updated = await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_messages')}
        SET status = 'failed', storage_reserved_bytes = 0,
            error_code = 'stream_interrupted', completed_at = ?
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
          AND role = 'assistant' AND status = 'streaming'
      `, [now, String(stale.id), String(stale.system_account_id), String(stale.active_turn_id)])
      if (updated.changes === 1 && staleAssistant) {
        await releaseStorageWindowReservationStrict(
          tx,
          String(stale.system_account_id),
          String(staleAssistant.created_at),
          requiredAssistantStorageReservation(staleAssistant),
          now
        )
      }
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET active_turn_id = NULL, active_started_at = NULL,
            message_revision = message_revision + ?, updated_at = ?
        WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
      `, [updated.changes, now, String(stale.id), String(stale.system_account_id), String(stale.active_turn_id)])
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
    for (const expired of expiredTurns) {
      const conversationId = String(expired.conversation_id)
      const systemAccountId = String(expired.system_account_id)
      const turnId = String(expired.turn_id)
      const buckets = await tx.query<{ bucket_date?: unknown; content_bytes?: unknown; reserved_bytes?: unknown }>(`
        SELECT substr(created_at, 1, 10) AS bucket_date,
               COALESCE(SUM(content_bytes), 0) AS content_bytes,
               COALESCE(SUM(storage_reserved_bytes), 0) AS reserved_bytes
        FROM ${chatTable(tx, 'chat_messages')}
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
        GROUP BY substr(created_at, 1, 10)
      `, [conversationId, systemAccountId, turnId])
      for (const bucket of buckets) {
        const bytes = Number(bucket.content_bytes ?? 0)
        const reservedBytes = Number(bucket.reserved_bytes ?? 0)
        await tx.execute(`
          UPDATE ${chatTable(tx, 'chat_user_storage_windows')}
          SET content_bytes = CASE WHEN content_bytes > ? THEN content_bytes - ? ELSE 0 END,
              reserved_bytes = CASE WHEN reserved_bytes > ? THEN reserved_bytes - ? ELSE 0 END,
              updated_at = ?
          WHERE system_account_id = ? AND bucket_date = ?
        `, [bytes, bytes, reservedBytes, reservedBytes, input.now, systemAccountId, String(bucket.bucket_date)])
      }
      await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_message_idempotency')} WHERE conversation_id = ? AND turn_id = ?`, [conversationId, turnId])
      const deleted = await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_messages')} WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?`, [conversationId, systemAccountId, turnId])
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_conversations')}
        SET active_turn_id = NULL, active_started_at = NULL, updated_at = ?
        WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
      `, [input.now, conversationId, systemAccountId, turnId])
      deletedMessages += deleted.changes
      if (deleted.changes > 0) {
        affectedConversations.set(chatConversationKey(conversationId, systemAccountId), { conversationId, systemAccountId })
      }
    }
    await advanceChatConversationRevisions(tx, affectedConversations, advancedConversationKeys, input.now)
    await tx.execute(`DELETE FROM ${chatTable(tx, 'chat_message_idempotency')} WHERE expires_at <= ?`, [input.now])
    await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_user_storage_windows')} AS storage_window
      WHERE (content_bytes = 0 AND reserved_bytes = 0)
        OR (
          bucket_date < ?
          AND NOT EXISTS (
            SELECT 1 FROM ${chatTable(tx, 'chat_messages')} AS message
            WHERE message.system_account_id = storage_window.system_account_id
              AND substr(message.created_at, 1, 10) = storage_window.bucket_date
            LIMIT 1
          )
        )
    `, [storageWindowCutoffDate(input.now, retentionDays)])
    let deletedConversations = 0
    for (const { conversationId, systemAccountId } of affectedConversations.values()) {
      const deleted = await tx.execute(`
        DELETE FROM ${chatTable(tx, 'chat_conversations')}
        WHERE id = ? AND system_account_id = ? AND active_turn_id IS NULL
          AND NOT EXISTS (SELECT 1 FROM ${chatTable(tx, 'chat_messages')} WHERE conversation_id = ? LIMIT 1)
      `, [conversationId, systemAccountId, conversationId])
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
    return {
      droppedPartitions,
      deletedMessages,
      deletedConversations,
      recoveredTurns,
      recoveredCompactions: 0,
      claimedAssets: 0,
      deletedAssets: 0,
      failedAssets: 0,
      hasMoreAssets: false,
      deletedCheckpoints: 0,
      hasMoreCheckpoints: false,
      hasMore: expiredTurns.length * 2 >= limit || staleTurns.length * 2 >= limit
    }
  })
}

interface AffectedChatConversation {
  conversationId: string
  systemAccountId: string
}

async function dropExpiredPostgresChatPartitions(
  tx: DatabaseClient,
  now: string,
  retentionDays: number,
  affectedConversations: Map<string, AffectedChatConversation>
): Promise<{ droppedPartitions: number; advancedConversationKeys: Set<string> }> {
  if (tx.driver !== 'postgres') return { droppedPartitions: 0, advancedConversationKeys: new Set() }
  const cutoff = new Date(new Date(now).getTime() - retentionDays * 24 * 60 * 60 * 1000)
  const rows = await tx.query<{ partition_name?: unknown }>(`
    SELECT child.relname AS partition_name
    FROM pg_inherits
    JOIN pg_class parent ON parent.oid = inhparent
    JOIN pg_namespace namespace ON namespace.oid = parent.relnamespace
    JOIN pg_class child ON child.oid = inhrelid
    WHERE namespace.nspname = 'juhe_chat' AND parent.relname = 'chat_messages'
  `)
  const expiredPartitionNames: string[] = []
  for (const row of rows) {
    const name = String(row.partition_name ?? '')
    const match = /^chat_messages_(\d{4})(\d{2})(\d{2})$/.exec(name)
    if (!match) continue
    const partitionEnd = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]) + 1))
    if (partitionEnd > cutoff) continue
    expiredPartitionNames.push(name)
  }
  for (const name of expiredPartitionNames) {
    const rows = await tx.query<{ conversation_id?: unknown; system_account_id?: unknown }>(`
      SELECT DISTINCT conversation_id, system_account_id
      FROM juhe_chat."${name}"
    `)
    for (const row of rows) {
      const conversationId = String(row.conversation_id ?? '')
      const systemAccountId = String(row.system_account_id ?? '')
      affectedConversations.set(chatConversationKey(conversationId, systemAccountId), { conversationId, systemAccountId })
    }
  }
  const advancedConversationKeys = new Set<string>()
  await advanceChatConversationRevisions(tx, affectedConversations, advancedConversationKeys, now)
  for (const name of expiredPartitionNames) {
    await tx.execute(`DROP TABLE IF EXISTS juhe_chat."${name}"`)
  }
  return { droppedPartitions: expiredPartitionNames.length, advancedConversationKeys }
}

async function advanceChatConversationRevisions(
  tx: DatabaseClient,
  affectedConversations: Map<string, AffectedChatConversation>,
  advancedConversationKeys: Set<string>,
  now: string
): Promise<void> {
  for (const [key, conversation] of affectedConversations) {
    if (advancedConversationKeys.has(key)) continue
    await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET message_revision = message_revision + 1, updated_at = ?
      WHERE id = ? AND system_account_id = ?
    `, [now, conversation.conversationId, conversation.systemAccountId])
    advancedConversationKeys.add(key)
  }
}

function chatConversationKey(conversationId: string, systemAccountId: string): string {
  return `${systemAccountId}\u0000${conversationId}`
}

function storageWindowCutoffDate(now: string, retentionDays: number): string {
  const date = new Date(now)
  date.setUTCDate(date.getUTCDate() - retentionDays)
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
    || !['completed', 'failed', 'canceled'].includes(String(assistantMessage.status))
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
  if (!inputMarkers) {
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

async function releaseChatConversationStorageAndExpireAssets(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  now: string
}): Promise<void> {
  const buckets = await client.query<{ bucket_date?: unknown; content_bytes?: unknown; reserved_bytes?: unknown }>(`
    SELECT substr(created_at, 1, 10) AS bucket_date,
           COALESCE(SUM(content_bytes), 0) AS content_bytes,
           COALESCE(SUM(storage_reserved_bytes), 0) AS reserved_bytes
    FROM ${chatTable(client, 'chat_messages')}
    WHERE conversation_id = ? AND system_account_id = ?
    GROUP BY substr(created_at, 1, 10)
  `, [input.conversationId, input.systemAccountId])
  for (const bucket of buckets) {
    const contentBytes = Number(bucket.content_bytes ?? 0)
    const reservedBytes = Number(bucket.reserved_bytes ?? 0)
    await client.execute(`
      UPDATE ${chatTable(client, 'chat_user_storage_windows')}
      SET content_bytes = CASE WHEN content_bytes > ? THEN content_bytes - ? ELSE 0 END,
          reserved_bytes = CASE WHEN reserved_bytes > ? THEN reserved_bytes - ? ELSE 0 END,
          updated_at = ?
      WHERE system_account_id = ? AND bucket_date = ?
    `, [contentBytes, contentBytes, reservedBytes, reservedBytes, input.now, input.systemAccountId, String(bucket.bucket_date)])
  }
  await client.execute(`
    DELETE FROM ${chatTable(client, 'chat_user_storage_windows')}
    WHERE system_account_id = ? AND content_bytes = 0 AND reserved_bytes = 0
  `, [input.systemAccountId])
  await expireChatAssetsForConversationInClient(client, input)
}

async function recentStorageBytes(client: DatabaseClient, systemAccountId: string, now: string, retentionDays: number): Promise<number> {
  const start = new Date(now)
  start.setUTCDate(start.getUTCDate() - retentionDays)
  const row = await client.one<{ total?: number | string }>(`
    SELECT COALESCE(SUM(content_bytes + reserved_bytes), 0) AS total
    FROM ${chatTable(client, 'chat_user_storage_windows')}
    WHERE system_account_id = ? AND bucket_date >= ?
  `, [systemAccountId, start.toISOString().slice(0, 10)])
  return Number(row?.total ?? 0)
}

async function lockChatUserStorageQuota(client: DatabaseClient, systemAccountId: string): Promise<void> {
  if (client.driver !== 'postgres') return
  await client.execute('SELECT pg_advisory_xact_lock(hashtextextended(?, 0))', [`juhe-ai:chat-storage:${systemAccountId}`])
}

async function withSqliteChatUserPolicyLock<T>(client: DatabaseClient, systemAccountId: string, operation: () => Promise<T>): Promise<T> {
  if (client.driver !== 'sqlite') return operation()
  const previous = sqliteChatUserPolicyLocks.get(systemAccountId) ?? Promise.resolve()
  let release!: () => void
  const current = new Promise<void>((resolve) => { release = resolve })
  const queued = previous.then(() => current)
  sqliteChatUserPolicyLocks.set(systemAccountId, queued)
  await previous
  try {
    return await operation()
  } finally {
    release()
    if (sqliteChatUserPolicyLocks.get(systemAccountId) === queued) sqliteChatUserPolicyLocks.delete(systemAccountId)
  }
}

async function incrementStorageWindow(client: DatabaseClient, systemAccountId: string, now: string, contentBytes: number, reservedBytes: number): Promise<void> {
  const table = chatTable(client, 'chat_user_storage_windows')
  const bucketDate = now.slice(0, 10)
  if (client.driver === 'postgres') {
    await client.execute(`
      INSERT INTO ${table} (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
      VALUES (?, ?, ?, ?, ?)
      ON CONFLICT (system_account_id, bucket_date)
      DO UPDATE SET content_bytes = ${table}.content_bytes + EXCLUDED.content_bytes,
                    reserved_bytes = ${table}.reserved_bytes + EXCLUDED.reserved_bytes,
                    updated_at = EXCLUDED.updated_at
    `, [systemAccountId, bucketDate, contentBytes, reservedBytes, now])
    return
  }
  await client.execute(`
    INSERT INTO ${table} (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, bucket_date)
    DO UPDATE SET content_bytes = content_bytes + excluded.content_bytes,
                  reserved_bytes = reserved_bytes + excluded.reserved_bytes,
                  updated_at = excluded.updated_at
  `, [systemAccountId, bucketDate, contentBytes, reservedBytes, now])
}

async function settleStorageWindowReservationStrict(
  client: DatabaseClient,
  systemAccountId: string,
  createdAt: string,
  reservationBytes: number,
  contentBytes: number,
  now: string
): Promise<void> {
  if (!Number.isSafeInteger(contentBytes) || contentBytes < 0 || contentBytes > reservationBytes) {
    throw new Error('助手消息实际字节超过预留')
  }
  const bucketDate = createdAt.slice(0, 10)
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_user_storage_windows')}
    SET content_bytes = content_bytes + ?, reserved_bytes = reserved_bytes - ?, updated_at = ?
    WHERE system_account_id = ? AND bucket_date = ? AND reserved_bytes >= ?
  `, [contentBytes, reservationBytes, now, systemAccountId, bucketDate, reservationBytes])
  if (result.changes !== 1) throw new Error(`聊天容量预留数据不一致：${bucketDate} 日桶缺失或不足`)
}

async function releaseStorageWindowReservationStrict(
  client: DatabaseClient,
  systemAccountId: string,
  createdAt: string,
  reservationBytes: number,
  now: string
): Promise<void> {
  const bucketDate = createdAt.slice(0, 10)
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_user_storage_windows')}
    SET reserved_bytes = reserved_bytes - ?, updated_at = ?
    WHERE system_account_id = ? AND bucket_date = ? AND reserved_bytes >= ?
  `, [reservationBytes, now, systemAccountId, bucketDate, reservationBytes])
  if (result.changes !== 1) throw new Error(`聊天容量预留数据不一致：${bucketDate} 日桶缺失或不足`)
  await client.execute(`
    DELETE FROM ${chatTable(client, 'chat_user_storage_windows')}
    WHERE system_account_id = ? AND bucket_date = ? AND content_bytes = 0 AND reserved_bytes = 0
  `, [systemAccountId, bucketDate])
}

function requiredAssistantStorageReservation(row: ChatMessageRow): number {
  const reservationBytes = Number(row.storage_reserved_bytes)
  if (reservationBytes !== chatAssistantStorageReservationBytes) throw new Error('助手消息存储预留数据不一致')
  return reservationBytes
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
    WHERE system_account_id = ? AND bucket_date = ? AND content_bytes = 0 AND reserved_bytes = 0
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

function normalizedChatUserTurnCount(value: unknown): number {
  if (value === null || value === undefined) throw new Error('聊天会话轮次计数无效')
  if (typeof value !== 'number' && typeof value !== 'string') throw new Error('聊天会话轮次计数无效')
  if (typeof value === 'string' && !value.trim()) throw new Error('聊天会话轮次计数无效')
  const count = Number(value)
  if (!Number.isSafeInteger(count) || count < 0) throw new Error('聊天会话轮次计数无效')
  return count
}

function normalizedChatMessageRevision(value: unknown): number {
  if (value === null || value === undefined) throw new Error('聊天会话消息 revision 无效')
  if (typeof value !== 'number' && typeof value !== 'string') throw new Error('聊天会话消息 revision 无效')
  if (typeof value === 'string' && !value.trim()) throw new Error('聊天会话消息 revision 无效')
  const revision = Number(value)
  if (!Number.isSafeInteger(revision) || revision < 0) throw new Error('聊天会话消息 revision 无效')
  return revision
}

function titleFromContent(content: string): string {
  const firstLine = content.replace(/[\u0000-\u001f\u007f]/g, ' ').split(/\r?\n/, 1)[0].replace(/\s+/g, ' ').trim()
  return firstLine.slice(0, 60) || '新对话'
}

function serializeInputContentMarkers(blocks: readonly ChatInputContentBlock[] | undefined, userContent: string): string {
  const normalized = blocks && blocks.length > 0 ? blocks : [{ type: 'input_text' as const, text: userContent }]
  if (normalized.length > maxInputContentBlocks) throw new Error(`用户输入块不能超过 ${maxInputContentBlocks} 个`)
  const markers: ChatMessageContentBlock[] = normalized.map((block, order) => {
    if (block?.type !== 'input_text' && block?.type !== 'input_image') throw new Error('用户输入块类型无效')
    if (block.type === 'input_image') {
      const assetId = block.assetId?.trim()
      if (!assetId) throw new Error('图片资产 ID 不能为空')
      return { type: 'input_image', order, assetId }
    }
    return { type: 'input_text', text: block.text ?? '', order }
  })
  return serializeContentBlocks(markers)
}

function mapConversation(row: ConversationRow): ChatConversation {
  return {
    id: String(row.id), systemAccountId: String(row.system_account_id),
    apiKeyId: nullable(row.api_key_id), apiKeyNameSnapshot: String(row.api_key_name_snapshot),
    title: String(row.title), isPinned: Number(row.is_pinned ?? 0) === 1, lastModel: nullable(row.last_model),
    defaultImageModel: normalizedChatImageModel(row.default_image_model), activeTurnId: nullable(row.active_turn_id),
    userTurnCount: normalizedChatUserTurnCount(row.user_turn_count),
    messageRevision: normalizedChatMessageRevision(row.message_revision),
    lastMessageAt: String(row.last_message_at), createdAt: String(row.created_at), updatedAt: String(row.updated_at)
  }
}

function normalizedChatImageModel(value: unknown): ChatImageModel {
  if (value === 'gpt-image-2') return value
  throw new Error('聊天会话默认图像模型无效')
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
  if (block.type === 'output_text') return typeof block.text === 'string'
  if (block.type === 'reasoning') return typeof block.text === 'string'
    && (block.status === undefined || ['started', 'completed', 'failed', 'canceled'].includes(String(block.status)))
  if (block.type === 'input_text' || block.type === 'input_image') {
    if (!Number.isSafeInteger(block.order) || Number(block.order) < 0 || Number(block.order) >= maxInputContentBlocks) return false
    return block.type === 'input_text'
      ? typeof block.text === 'string'
      : typeof block.assetId === 'string' && Boolean(block.assetId.trim())
  }
  if (block.type === 'output_image') return typeof block.blockId === 'string' && Boolean(block.blockId)
    && Number.isSafeInteger(block.order) && Number(block.order) >= 0
    && typeof block.assetId === 'string' && Boolean(block.assetId)
    && ['started', 'completed', 'failed', 'canceled'].includes(String(block.status))
  return block.type === 'tool_call' && (typeof block.id === 'string' || typeof block.callId === 'string') && typeof block.toolType === 'string'
    && ['started', 'updated', 'completed', 'failed', 'canceled'].includes(String(block.status))
}

function parseStoredInputMarkers(value: unknown): Array<Extract<ChatMessageContentBlock, { type: 'input_text' | 'input_image' }>> | undefined {
  if (typeof value !== 'string' || !value || Buffer.byteLength(value, 'utf8') > maxContentBlocksBytes) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed) || parsed.length < 1 || parsed.length > maxInputContentBlocks) return undefined
    const markers: Array<Extract<ChatMessageContentBlock, { type: 'input_text' | 'input_image' }>> = []
    for (let order = 0; order < parsed.length; order += 1) {
      const valueAtOrder = parsed[order]
      if (!valueAtOrder || typeof valueAtOrder !== 'object' || Array.isArray(valueAtOrder)) return undefined
      const marker = valueAtOrder as Record<string, unknown>
      if (marker.order !== order) return undefined
      if (marker.type === 'input_text') {
        const keys = Object.keys(marker).sort()
        if (keys.length !== 3 || keys[0] !== 'order' || keys[1] !== 'text' || keys[2] !== 'type' || typeof marker.text !== 'string') return undefined
        markers.push({ type: 'input_text', text: marker.text, order })
        continue
      }
      const keys = Object.keys(marker).sort()
      if (keys.length !== 3 || keys[0] !== 'assetId' || keys[1] !== 'order' || keys[2] !== 'type') return undefined
      if (marker.type !== 'input_image' || typeof marker.assetId !== 'string' || !marker.assetId.trim()) return undefined
      markers.push({ type: 'input_image', order, assetId: marker.assetId.trim() })
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
type ChatConversationSyncRow = Record<string, unknown>
type IdempotencyRow = Record<string, unknown> & { turn_id?: unknown; user_message_id?: unknown; assistant_message_id?: unknown }
