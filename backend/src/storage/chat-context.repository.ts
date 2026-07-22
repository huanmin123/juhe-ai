import { randomUUID } from 'node:crypto'

import type { DatabaseClient } from './database-client.js'

export type ChatContextState = 'ready' | 'compact_pending' | 'compacting' | 'compact_failed'
export type ChatContextEntryKind = 'verbatim' | 'durable_memory' | 'task_state' | 'tool_result' | 'image_observation' | 'provider_compaction'
export type ChatContextEntryProvenance = 'user' | 'assistant' | 'tool' | 'asset' | 'provider'
export type ChatContextEntryTrustLevel = 'untrusted' | 'assistant_derived' | 'provider_opaque'

export const chatContextMaintenanceMaxBatchSize = 500

export interface ChatContextHead {
  conversationId: string
  systemAccountId: string
  contextRevision: number
  activeCheckpointId?: string
  compactedThroughSequence: number
  contextState: ChatContextState
  activeContextTokens?: number
  effectiveContextLimitTokens?: number
  usageEstimated: boolean
  contextRetryAt?: string
  contextAttemptCount: number
  contextErrorCode?: string
  nextSequenceNo: number
}

export interface ChatContextCheckpoint {
  id: string
  conversationId: string
  systemAccountId: string
  version: number
  sourceRevision: number
  sourceFromSequence: number
  sourceThroughSequence: number
  recentTailFromSequence: number
  entryFromSequence: number
  entryThroughSequence: number
  payloadDigest: string
  estimatedInputTokens?: number
  upstreamInputTokens?: number
  requestBodyBytes: number
  modelId: string
  providerCode?: string
  providerProfileId?: string
  endpointFamily: string
  compactCompatibilityHash?: string
  promptVersion: string
  status: 'pending' | 'active' | 'superseded' | 'rejected'
  qualityStatus: 'passed' | 'failed'
  createdAt: string
  expiresAt: string
}

export interface ChatContextEntry {
  conversationId: string
  checkpointId: string
  sequence: number
  sourceMessageId?: string
  kind: ChatContextEntryKind
  content: unknown
  contentBytes: number
  provenance: ChatContextEntryProvenance
  trustLevel: ChatContextEntryTrustLevel
  tokenCount?: number
  createdAt: string
  expiresAt: string
}

export interface ChatContextSourceMessage {
  id: string
  turnId: string
  sequenceNo: number
  role: 'user' | 'assistant'
  contentText: string
  contentBlocks: unknown[]
  contentBytes: number
  model: string
  createdAt: string
  completedAt?: string
  expiresAt: string
}

export interface ChatModelContextLoadResult {
  head: ChatContextHead
  checkpoint?: ChatContextCheckpoint
  entries: ChatContextEntry[]
  suffix: ChatContextSourceMessage[]
  loadedBytes: number
  complete: boolean
  truncatedAt?: 'checkpoint_entries' | 'suffix_messages'
}

export interface ChatContextCompactionClaim {
  claimId: string
  conversationId: string
  systemAccountId: string
  sourceRevision: number
  sourceFromSequence: number
  sourceThroughSequence: number
  progressSequence: number
  attemptCount: number
  claimedAt: string
}

export interface ChatCompactionSourcePage {
  claim: ChatContextCompactionClaim
  messages: ChatContextSourceMessage[]
  nextAfterSequence: number
  earliestExpiresAt?: string
  loadedBytes: number
  hasMore: boolean
  blockedByByteLimit: boolean
}

export interface ChatContextCheckpointEntryInput {
  sourceMessageId?: string
  kind: ChatContextEntryKind
  content: unknown
  provenance: ChatContextEntryProvenance
  trustLevel: ChatContextEntryTrustLevel
  tokenCount?: number
}

export interface InstallChatContextCheckpointInput {
  checkpointId?: string
  claimId: string
  conversationId: string
  systemAccountId: string
  sourceRevision: number
  sourceThroughSequence: number
  expiresAt: string
  payloadDigest: string
  estimatedInputTokens?: number
  upstreamInputTokens?: number
  activeContextTokens?: number
  effectiveContextLimitTokens?: number
  requestBodyBytes: number
  modelId: string
  providerCode?: string
  providerProfileId?: string
  endpointFamily: string
  compactCompatibilityHash?: string
  promptVersion: string
  entries: readonly ChatContextCheckpointEntryInput[]
  now: string
}

export class ChatContextConflictError extends Error {
  constructor(message = '聊天上下文已变化，当前压缩结果不能安装') {
    super(message)
  }
}

const maxContextLoadRows = 512
const maxContextLoadBytes = 16 * 1024 * 1024
const maxCompactionSourceRows = 500
const maxCheckpointEntries = 256
const maxCheckpointEntryBytes = 256 * 1024
const maxCheckpointPayloadBytes = 2 * 1024 * 1024

export async function loadChatModelContext(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  now: string
  maxRows: number
  maxBytes: number
}): Promise<ChatModelContextLoadResult | undefined> {
  const maxRows = boundedInteger(input.maxRows, 'maxRows', 1, maxContextLoadRows)
  const maxBytes = boundedInteger(input.maxBytes, 'maxBytes', 1, maxContextLoadBytes)
  const headRow = await client.one<ContextHeadRow>(`
    SELECT id, system_account_id, context_revision, active_checkpoint_id,
           compacted_through_sequence, context_state, active_context_tokens,
           effective_context_limit_tokens, context_usage_estimated,
           context_retry_at, context_attempt_count, context_error_code, next_sequence_no
    FROM ${chatTable(client, 'chat_conversations')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `, [input.conversationId, input.systemAccountId])
  if (!headRow) return undefined

  const storedHead = contextHeadFromRow(headRow)
  const checkpoint = storedHead.activeCheckpointId
    ? await loadActiveCheckpoint(client, storedHead.activeCheckpointId, input.conversationId, input.systemAccountId, input.now)
    : undefined
  const head: ChatContextHead = checkpoint
    ? storedHead
    : { ...storedHead, activeCheckpointId: undefined, compactedThroughSequence: 0 }

  let loadedBytes = 0
  const entries: ChatContextEntry[] = []
  if (checkpoint) {
    const entryRows = await client.query<ContextEntryRow>(`
      SELECT conversation_id, checkpoint_id, sequence, source_message_id, kind,
             content_json, content_bytes, provenance, trust_level, token_count,
             created_at, expires_at
      FROM ${chatTable(client, 'chat_context_entries')}
      WHERE conversation_id = ? AND checkpoint_id = ?
      ORDER BY sequence ASC
      LIMIT ?
    `, [input.conversationId, checkpoint.id, maxRows + 1])
    for (const row of entryRows.slice(0, maxRows)) {
      const entry = contextEntryFromRow(row)
      if (loadedBytes + entry.contentBytes > maxBytes) {
        return { head, checkpoint, entries, suffix: [], loadedBytes, complete: false, truncatedAt: 'checkpoint_entries' }
      }
      entries.push(entry)
      loadedBytes += entry.contentBytes
    }
    if (entryRows.length > maxRows) {
      return { head, checkpoint, entries, suffix: [], loadedBytes, complete: false, truncatedAt: 'checkpoint_entries' }
    }
    const expectedEntryCount = checkpoint.entryThroughSequence - checkpoint.entryFromSequence + 1
    if (entries.length !== expectedEntryCount) {
      return { head, checkpoint, entries, suffix: [], loadedBytes, complete: false, truncatedAt: 'checkpoint_entries' }
    }
  }

  const remainingRows = maxRows - entries.length
  const suffixRowBudget = remainingRows - (remainingRows % 2)
  const messagesTable = chatTable(client, 'chat_messages')
  const suffixRows = await client.query<ContextMessageRow>(`
    SELECT source.id, source.turn_id, source.sequence_no, source.role, source.content_text, source.content_blocks_json,
           source.content_bytes, source.model, source.created_at, source.completed_at, source.expires_at
    FROM ${messagesTable} AS source
    WHERE source.conversation_id = ? AND source.system_account_id = ?
      AND source.status = 'completed' AND source.expires_at > ? AND source.sequence_no > ?
      AND EXISTS (
        SELECT 1 FROM ${messagesTable} AS pair
        WHERE pair.conversation_id = source.conversation_id
          AND pair.system_account_id = source.system_account_id
          AND pair.turn_id = source.turn_id
          AND pair.status = 'completed' AND pair.expires_at > ?
          AND (
            (source.role = 'user' AND pair.role = 'assistant' AND pair.sequence_no = source.sequence_no + 1)
            OR (source.role = 'assistant' AND pair.role = 'user' AND pair.sequence_no = source.sequence_no - 1)
          )
      )
    ORDER BY source.sequence_no ASC
    LIMIT ?
  `, [input.conversationId, input.systemAccountId, input.now, head.compactedThroughSequence, input.now, suffixRowBudget + 2])
  const suffix: ChatContextSourceMessage[] = []
  for (let index = 0; index < Math.min(suffixRows.length, suffixRowBudget); index += 2) {
    const pair = contextMessagePair(suffixRows, index)
    const pairBytes = pair[0].contentBytes + pair[1].contentBytes
    if (loadedBytes + pairBytes > maxBytes) {
      return { head, checkpoint, entries, suffix, loadedBytes, complete: false, truncatedAt: 'suffix_messages' }
    }
    suffix.push(...pair)
    loadedBytes += pairBytes
  }
  const complete = suffixRows.length <= suffixRowBudget
  return {
    head,
    checkpoint,
    entries,
    suffix,
    loadedBytes,
    complete,
    truncatedAt: complete ? undefined : 'suffix_messages'
  }
}

export async function requestChatContextCompaction(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedRevision: number
  sourceThroughSequence: number
  now: string
}): Promise<boolean> {
  const expectedRevision = nonNegativeSafeInteger(input.expectedRevision, 'expectedRevision')
  const sourceThroughSequence = positiveSafeInteger(input.sourceThroughSequence, 'sourceThroughSequence')
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET context_state = 'compact_pending', context_retry_at = NULL,
        context_error_code = NULL, context_progress_sequence = 0,
        context_progress_earliest_expires_at = NULL, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND context_revision = ?
      AND active_turn_id IS NULL
      AND (
        context_state = 'ready'
        OR (context_state = 'compact_failed' AND context_retry_at IS NOT NULL AND context_retry_at <= ?)
      )
      AND ? > compacted_through_sequence AND ? <= next_sequence_no - 3
  `, [input.now, input.conversationId, input.systemAccountId, expectedRevision, input.now, sourceThroughSequence, sourceThroughSequence])
  return result.changes === 1
}

export async function recordChatContextUsage(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedContextRevision: number
  activeContextTokens: number
  effectiveContextLimitTokens?: number
  usageEstimated: boolean
  now: string
}): Promise<boolean> {
  const activeContextTokens = nonNegativeSafeInteger(input.activeContextTokens, 'activeContextTokens')
  const expectedContextRevision = nonNegativeSafeInteger(input.expectedContextRevision, 'expectedContextRevision')
  const effectiveContextLimitTokens = optionalPositiveSafeInteger(input.effectiveContextLimitTokens, 'effectiveContextLimitTokens')
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET active_context_tokens = ?, effective_context_limit_tokens = ?,
        context_usage_estimated = ?, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND context_revision = ?
  `, [
    activeContextTokens,
    effectiveContextLimitTokens ?? null,
    input.usageEstimated ? 1 : 0,
    input.now,
    input.conversationId,
    input.systemAccountId,
    expectedContextRevision
  ])
  return result.changes === 1
}

export async function getChatContextHead(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
}): Promise<ChatContextHead | undefined> {
  const row = await client.one<ContextHeadRow>(`
    SELECT id, system_account_id, context_revision, active_checkpoint_id,
           compacted_through_sequence, context_state, active_context_tokens,
           effective_context_limit_tokens, context_usage_estimated,
           context_retry_at, context_attempt_count, context_error_code, next_sequence_no
    FROM ${chatTable(client, 'chat_conversations')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `, [input.conversationId, input.systemAccountId])
  return row ? contextHeadFromRow(row) : undefined
}

export async function claimChatContextCompaction(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedRevision: number
  sourceThroughSequence: number
  now: string
  staleClaimBefore: string
}): Promise<ChatContextCompactionClaim | undefined> {
  const expectedRevision = nonNegativeSafeInteger(input.expectedRevision, 'expectedRevision')
  const sourceThroughSequence = positiveSafeInteger(input.sourceThroughSequence, 'sourceThroughSequence')
  const claimId = `chat_context_claim_${randomUUID().replace(/-/g, '')}`
  return client.transaction(async (tx) => {
    const current = await tx.one<ContextHeadRow>(`
      SELECT id, system_account_id, context_revision, active_checkpoint_id,
             compacted_through_sequence
      FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ? AND context_revision = ?
      ${tx.driver === 'postgres' ? 'FOR UPDATE' : ''}
    `, [input.conversationId, input.systemAccountId, expectedRevision])
    if (!current) return undefined

    const storedCheckpointId = optionalString(current.active_checkpoint_id)
    const activeCheckpoint = storedCheckpointId
      ? await loadActiveCheckpoint(tx, storedCheckpointId, input.conversationId, input.systemAccountId, input.now)
      : undefined
    const invalidatedCheckpoint = Boolean(storedCheckpointId && !activeCheckpoint)
    const effectiveCompactedThroughSequence = activeCheckpoint
      ? Number(current.compacted_through_sequence)
      : 0
    const claimRevision = expectedRevision + (invalidatedCheckpoint ? 1 : 0)
    const result = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET context_revision = ?, active_checkpoint_id = ?,
          compacted_through_sequence = ?,
          active_context_tokens = CASE WHEN ? = 1 THEN NULL ELSE active_context_tokens END,
          context_usage_estimated = CASE WHEN ? = 1 THEN 1 ELSE context_usage_estimated END,
          context_state = 'compacting', context_claim_id = ?,
          context_claim_revision = ?, context_claim_through_sequence = ?,
          context_claimed_at = ?, context_retry_at = NULL,
          context_error_code = NULL, context_progress_sequence = ?,
          context_progress_earliest_expires_at = ?,
          updated_at = ?
      WHERE id = ? AND system_account_id = ? AND context_revision = ?
        AND active_turn_id IS NULL
        AND compacted_through_sequence = ?
        AND ? > ? AND ? <= next_sequence_no - 3
        AND (
          context_state IN ('ready', 'compact_pending')
          OR (context_state = 'compact_failed' AND context_retry_at IS NOT NULL AND context_retry_at <= ?)
          OR (context_state = 'compacting' AND context_claimed_at <= ?)
        )
    `, [
      claimRevision,
      activeCheckpoint?.id ?? null,
      effectiveCompactedThroughSequence,
      invalidatedCheckpoint ? 1 : 0,
      invalidatedCheckpoint ? 1 : 0,
      claimId,
      claimRevision,
      sourceThroughSequence,
      input.now,
      effectiveCompactedThroughSequence,
      activeCheckpoint?.expiresAt ?? null,
      input.now,
      input.conversationId,
      input.systemAccountId,
      expectedRevision,
      Number(current.compacted_through_sequence),
      sourceThroughSequence,
      effectiveCompactedThroughSequence,
      sourceThroughSequence,
      input.now,
      input.staleClaimBefore
    ])
    if (result.changes !== 1) return undefined
    if (invalidatedCheckpoint) {
      await tx.execute(`
        UPDATE ${chatTable(tx, 'chat_context_checkpoints')}
        SET status = 'superseded'
        WHERE id = ? AND conversation_id = ? AND system_account_id = ?
          AND status = 'active' AND expires_at <= ?
      `, [storedCheckpointId!, input.conversationId, input.systemAccountId, input.now])
    }
    return requireCompactionClaim(tx, input.conversationId, input.systemAccountId, claimId)
  })
}

export async function loadChatCompactionSourcePage(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  claimId: string
  afterSequence: number
  now: string
  limit: number
  maxBytes: number
}): Promise<ChatCompactionSourcePage | undefined> {
  const afterSequence = nonNegativeSafeInteger(input.afterSequence, 'afterSequence')
  const limit = boundedInteger(input.limit, 'limit', 2, maxCompactionSourceRows)
  const rowBudget = limit - (limit % 2)
  const maxBytes = boundedInteger(input.maxBytes, 'maxBytes', 1, maxContextLoadBytes)
  const claim = await findCompactionClaim(client, input.conversationId, input.systemAccountId, input.claimId)
  if (!claim) return undefined
  if (afterSequence !== claim.progressSequence) {
    throw new Error('压缩来源游标超出当前认领范围')
  }
  const messagesTable = chatTable(client, 'chat_messages')
  const rows = await client.query<ContextMessageRow>(`
    SELECT source.id, source.turn_id, source.sequence_no, source.role, source.content_text, source.content_blocks_json,
           source.content_bytes, source.model, source.created_at, source.completed_at, source.expires_at
    FROM ${messagesTable} AS source
    WHERE source.conversation_id = ? AND source.system_account_id = ?
      AND source.status = 'completed' AND source.expires_at > ?
      AND source.sequence_no > ? AND source.sequence_no <= ?
      AND EXISTS (
        SELECT 1 FROM ${messagesTable} AS pair
        WHERE pair.conversation_id = source.conversation_id
          AND pair.system_account_id = source.system_account_id
          AND pair.turn_id = source.turn_id
          AND pair.status = 'completed' AND pair.expires_at > ?
          AND (
            (source.role = 'user' AND pair.role = 'assistant' AND pair.sequence_no = source.sequence_no + 1)
            OR (source.role = 'assistant' AND pair.role = 'user' AND pair.sequence_no = source.sequence_no - 1)
          )
      )
    ORDER BY source.sequence_no ASC
    LIMIT ?
  `, [input.conversationId, input.systemAccountId, input.now, afterSequence, claim.sourceThroughSequence, input.now, rowBudget + 2])
  const messages: ChatContextSourceMessage[] = []
  let loadedBytes = 0
  let earliestExpiresAt: string | undefined
  let blockedByByteLimit = false
  for (let index = 0; index < Math.min(rows.length, rowBudget); index += 2) {
    const pair = contextMessagePair(rows, index)
    const pairBytes = pair[0].contentBytes + pair[1].contentBytes
    if (loadedBytes + pairBytes > maxBytes && messages.length > 0) {
      blockedByByteLimit = true
      break
    }
    if (pairBytes > maxContextLoadBytes) throw new Error('单个完整聊天轮次超过压缩来源绝对大小限制')
    messages.push(...pair)
    loadedBytes += pairBytes
    earliestExpiresAt = earlierTimestamp(earliestExpiresAt, pair[0].expiresAt)
    earliestExpiresAt = earlierTimestamp(earliestExpiresAt, pair[1].expiresAt)
  }
  const lastMessage = messages[messages.length - 1]
  const rowOverflow = rows.length > rowBudget
  const hasMore = blockedByByteLimit || rowOverflow
  return {
    claim,
    messages,
    nextAfterSequence: hasMore
      ? (lastMessage?.sequenceNo ?? afterSequence)
      : claim.sourceThroughSequence,
    earliestExpiresAt,
    loadedBytes,
    hasMore,
    blockedByByteLimit
  }
}

export async function recordChatContextCompactionProgress(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  claimId: string
  throughSequence: number
  earliestExpiresAt: string
  now: string
}): Promise<boolean> {
  const throughSequence = nonNegativeSafeInteger(input.throughSequence, 'throughSequence')
  const earliestExpiresAt = normalizedTimestamp(input.earliestExpiresAt, 'earliestExpiresAt')
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET context_progress_sequence = ?,
        context_progress_earliest_expires_at = CASE
          WHEN context_progress_earliest_expires_at IS NULL OR context_progress_earliest_expires_at > ? THEN ?
          ELSE context_progress_earliest_expires_at
        END,
        context_claimed_at = ?, updated_at = ?
    WHERE id = ? AND system_account_id = ?
      AND context_state = 'compacting' AND context_claim_id = ?
      AND context_progress_sequence < ? AND context_claim_through_sequence >= ?
  `, [throughSequence, earliestExpiresAt, earliestExpiresAt, input.now, input.now, input.conversationId, input.systemAccountId, input.claimId, throughSequence, throughSequence])
  return result.changes === 1
}

export async function releaseChatContextCompactionClaim(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  claimId: string
  now: string
}): Promise<boolean> {
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET context_state = 'compact_pending', context_claim_id = NULL,
        context_claim_revision = NULL, context_claim_through_sequence = NULL,
        context_claimed_at = NULL, context_retry_at = NULL,
        context_error_code = NULL, context_progress_sequence = 0,
        context_progress_earliest_expires_at = NULL, updated_at = ?
    WHERE id = ? AND system_account_id = ?
      AND context_state = 'compacting' AND context_claim_id = ?
  `, [input.now, input.conversationId, input.systemAccountId, input.claimId])
  return result.changes === 1
}

export async function failChatContextCompaction(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  claimId: string
  errorCode: string
  retryAt?: string
  now: string
}): Promise<boolean> {
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET context_state = 'compact_failed', context_claim_id = NULL,
        context_claim_revision = NULL, context_claim_through_sequence = NULL,
        context_claimed_at = NULL, context_retry_at = ?, context_error_code = ?,
        context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
        context_attempt_count = context_attempt_count + 1,
        updated_at = ?
    WHERE id = ? AND system_account_id = ?
      AND context_state = 'compacting' AND context_claim_id = ?
  `, [
    input.retryAt ?? null,
    normalizedText(input.errorCode, 'errorCode', 128),
    input.now,
    input.conversationId,
    input.systemAccountId,
    input.claimId
  ])
  return result.changes === 1
}

export async function failPendingChatContextCompaction(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  expectedRevision: number
  errorCode: string
  retryAt?: string
  now: string
}): Promise<boolean> {
  const expectedRevision = nonNegativeSafeInteger(input.expectedRevision, 'expectedRevision')
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_conversations')}
    SET context_state = 'compact_failed', context_claim_id = NULL,
        context_claim_revision = NULL, context_claim_through_sequence = NULL,
        context_claimed_at = NULL, context_retry_at = ?, context_error_code = ?,
        context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
        context_attempt_count = context_attempt_count + 1,
        updated_at = ?
    WHERE id = ? AND system_account_id = ? AND context_revision = ?
      AND context_state = 'compact_pending'
  `, [
    input.retryAt ?? null,
    normalizedText(input.errorCode, 'errorCode', 128),
    input.now,
    input.conversationId,
    input.systemAccountId,
    expectedRevision
  ])
  return result.changes === 1
}

export async function installChatContextCheckpoint(client: DatabaseClient, input: InstallChatContextCheckpointInput): Promise<ChatContextCheckpoint> {
  const sourceRevision = nonNegativeSafeInteger(input.sourceRevision, 'sourceRevision')
  const sourceThroughSequence = positiveSafeInteger(input.sourceThroughSequence, 'sourceThroughSequence')
  const expiresAt = normalizedTimestamp(input.expiresAt, 'expiresAt')
  const now = normalizedTimestamp(input.now, 'now')
  if (Date.parse(expiresAt) <= Date.parse(now)) throw new Error('不能安装已过期的 checkpoint')
  const checkpointId = input.checkpointId ? normalizedText(input.checkpointId, 'checkpointId', 128) : `chat_checkpoint_${randomUUID().replace(/-/g, '')}`
  const payloadDigest = normalizedDigest(input.payloadDigest)
  const estimatedInputTokens = optionalNonNegativeSafeInteger(input.estimatedInputTokens, 'estimatedInputTokens')
  const upstreamInputTokens = optionalNonNegativeSafeInteger(input.upstreamInputTokens, 'upstreamInputTokens')
  const activeContextTokens = optionalNonNegativeSafeInteger(input.activeContextTokens, 'activeContextTokens')
    ?? upstreamInputTokens
    ?? estimatedInputTokens
  const effectiveContextLimitTokens = optionalPositiveSafeInteger(input.effectiveContextLimitTokens, 'effectiveContextLimitTokens')
  const requestBodyBytes = nonNegativeSafeInteger(input.requestBodyBytes, 'requestBodyBytes')
  const entries = serializeCheckpointEntries(input.entries, checkpointId, input.conversationId, now, expiresAt)
  const version = sourceRevision + 1

  return client.transaction(async (tx) => {
    const current = await tx.one<ContextHeadRow>(`
      SELECT id, system_account_id, context_revision, active_checkpoint_id,
             compacted_through_sequence, context_state, active_context_tokens, active_turn_id,
             effective_context_limit_tokens, next_sequence_no,
             context_usage_estimated,
             context_claim_id, context_claim_revision, context_claim_through_sequence,
             context_progress_sequence, context_progress_earliest_expires_at
      FROM ${chatTable(tx, 'chat_conversations')}
      WHERE id = ? AND system_account_id = ?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [input.conversationId, input.systemAccountId])
    if (
      !current
      || Number(current.context_revision) !== sourceRevision
      || String(current.context_state) !== 'compacting'
      || String(current.context_claim_id ?? '') !== input.claimId
      || Number(current.context_claim_revision) !== sourceRevision
      || Number(current.context_claim_through_sequence) !== sourceThroughSequence
      || Number(current.context_progress_sequence) !== sourceThroughSequence
      || !current.context_progress_earliest_expires_at
      || current.active_turn_id
      || sourceThroughSequence > Number(current.next_sequence_no) - 3
    ) {
      throw new ChatContextConflictError()
    }
    if (Date.parse(expiresAt) > Date.parse(String(current.context_progress_earliest_expires_at))) {
      throw new Error('checkpoint 过期时间不能晚于来源消息最早过期时间')
    }
    let checkpointSourceFromSequence = Number(current.compacted_through_sequence) + 1
    if (current.active_checkpoint_id) {
      const activeCheckpoint = await loadCheckpointById(tx, String(current.active_checkpoint_id))
      if (!activeCheckpoint || activeCheckpoint.status !== 'active' || activeCheckpoint.conversationId !== input.conversationId) {
        throw new ChatContextConflictError('活动 checkpoint 已变化，当前压缩结果不能安装')
      }
      checkpointSourceFromSequence = activeCheckpoint.sourceFromSequence
    }

    await tx.execute(`
      INSERT INTO ${chatTable(tx, 'chat_context_checkpoints')} (
        id, conversation_id, system_account_id, version, source_revision,
        source_from_sequence, source_through_sequence, recent_tail_from_sequence,
        entry_from_sequence, entry_through_sequence, payload_digest,
        estimated_input_tokens, upstream_input_tokens, request_body_bytes,
        model_id, provider_code, provider_profile_id, endpoint_family,
        compact_compatibility_hash, prompt_version, status, quality_status,
        created_at, expires_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'passed', ?, ?)
    `, [
      checkpointId,
      input.conversationId,
      input.systemAccountId,
      version,
      sourceRevision,
      checkpointSourceFromSequence,
      sourceThroughSequence,
      sourceThroughSequence + 1,
      entries.length,
      payloadDigest,
      estimatedInputTokens ?? null,
      upstreamInputTokens ?? null,
      requestBodyBytes,
      normalizedText(input.modelId, 'modelId', 256),
      optionalText(input.providerCode, 'providerCode', 64) ?? null,
      optionalText(input.providerProfileId, 'providerProfileId', 128) ?? null,
      normalizedText(input.endpointFamily, 'endpointFamily', 64),
      optionalDigest(input.compactCompatibilityHash, 'compactCompatibilityHash') ?? null,
      normalizedText(input.promptVersion, 'promptVersion', 128),
      now,
      expiresAt
    ])
    for (const entry of entries) {
      await tx.execute(`
        INSERT INTO ${chatTable(tx, 'chat_context_entries')} (
          conversation_id, checkpoint_id, sequence, source_message_id, kind,
          content_json, content_bytes, provenance, trust_level, token_count,
          created_at, expires_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, [
        entry.conversationId,
        entry.checkpointId,
        entry.sequence,
        entry.sourceMessageId ?? null,
        entry.kind,
        JSON.stringify(entry.content),
        entry.contentBytes,
        entry.provenance,
        entry.trustLevel,
        entry.tokenCount ?? null,
        entry.createdAt,
        entry.expiresAt
      ])
    }

    await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_context_checkpoints')}
      SET status = 'superseded'
      WHERE conversation_id = ? AND status = 'active'
    `, [input.conversationId])
    await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_context_checkpoints')}
      SET status = 'active'
      WHERE id = ? AND conversation_id = ? AND status = 'pending'
    `, [checkpointId, input.conversationId])
    const installed = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_conversations')}
      SET context_revision = context_revision + 1, active_checkpoint_id = ?,
          compacted_through_sequence = ?, context_state = 'ready',
          active_context_tokens = ?, effective_context_limit_tokens = ?,
          context_usage_estimated = ?,
           context_claim_id = NULL, context_claim_revision = NULL,
          context_claim_through_sequence = NULL, context_claimed_at = NULL,
           context_retry_at = NULL, context_error_code = NULL,
           context_attempt_count = 0,
          context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
          updated_at = ?
      WHERE id = ? AND system_account_id = ? AND context_revision = ?
        AND active_turn_id IS NULL
        AND context_state = 'compacting' AND context_claim_id = ?
        AND context_claim_revision = ? AND context_claim_through_sequence = ?
    `, [
      checkpointId,
      sourceThroughSequence,
      activeContextTokens ?? null,
      effectiveContextLimitTokens ?? null,
      input.upstreamInputTokens === undefined ? 1 : 0,
      now,
      input.conversationId,
      input.systemAccountId,
      sourceRevision,
      input.claimId,
      sourceRevision,
      sourceThroughSequence
    ])
    if (installed.changes !== 1) throw new ChatContextConflictError()
    const checkpoint = await loadCheckpointById(tx, checkpointId)
    if (!checkpoint) throw new Error('checkpoint 安装后读取失败')
    return checkpoint
  })
}

export async function recoverStaleChatContextCompactions(client: DatabaseClient, input: {
  now: string
  staleClaimBefore: string
  limit: number
}): Promise<number> {
  const limit = boundedInteger(input.limit, 'limit', 1, chatContextMaintenanceMaxBatchSize)
  const staleClaimBefore = normalizedTimestamp(input.staleClaimBefore, 'staleClaimBefore')
  const rows = await client.query<{ id?: unknown; system_account_id?: unknown }>(`
    SELECT id, system_account_id
    FROM ${chatTable(client, 'chat_conversations')}
    WHERE context_state = 'compacting' AND context_claimed_at <= ?
    ORDER BY context_claimed_at ASC, id ASC
    LIMIT ?${client.driver === 'postgres' ? ' FOR UPDATE SKIP LOCKED' : ''}
  `, [staleClaimBefore, limit])
  let recovered = 0
  for (const row of rows) {
    const result = await client.execute(`
      UPDATE ${chatTable(client, 'chat_conversations')}
      SET context_state = 'compact_failed', context_claim_id = NULL,
          context_claim_revision = NULL, context_claim_through_sequence = NULL,
          context_claimed_at = NULL, context_retry_at = ?,
          context_error_code = 'chat_context_compaction_stale',
          context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
          updated_at = ?
      WHERE id = ? AND system_account_id = ?
        AND context_state = 'compacting' AND context_claimed_at <= ?
    `, [input.now, input.now, String(row.id ?? ''), String(row.system_account_id ?? ''), staleClaimBefore])
    recovered += result.changes
  }
  return recovered
}

export async function cleanupExpiredChatContextCheckpoints(client: DatabaseClient, input: {
  now: string
  limit: number
}): Promise<{ deletedCheckpoints: number; hasMore: boolean }> {
  const limit = boundedInteger(input.limit, 'limit', 1, chatContextMaintenanceMaxBatchSize)
  return client.transaction(async (tx) => {
    const rows = await tx.query<{ id?: unknown; conversation_id?: unknown; status?: unknown }>(`
      SELECT id, conversation_id, status
      FROM ${chatTable(tx, 'chat_context_checkpoints')}
      WHERE expires_at <= ?
      ORDER BY expires_at ASC, id ASC
      LIMIT ?${tx.driver === 'postgres' ? ' FOR UPDATE SKIP LOCKED' : ''}
    `, [input.now, limit])
    const deletableIds: string[] = []
    for (const row of rows) {
      const checkpointId = String(row.id ?? '')
      if (!checkpointId) continue
      if (String(row.status) === 'active') {
        const detached = await tx.execute(`
          UPDATE ${chatTable(tx, 'chat_conversations')}
          SET context_revision = context_revision + 1, active_checkpoint_id = NULL,
              compacted_through_sequence = 0, context_state = 'ready',
              active_context_tokens = NULL, effective_context_limit_tokens = NULL,
              context_usage_estimated = 1,
              context_retry_at = NULL, context_error_code = NULL,
              context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
              updated_at = ?
          WHERE id = ? AND active_checkpoint_id = ? AND context_state != 'compacting'
        `, [input.now, String(row.conversation_id), checkpointId])
        if (detached.changes !== 1) continue
      }
      deletableIds.push(checkpointId)
    }
    if (deletableIds.length === 0) return { deletedCheckpoints: 0, hasMore: rows.length === limit }
    const deleted = await tx.execute(`
      DELETE FROM ${chatTable(tx, 'chat_context_checkpoints')}
      WHERE id IN (${tx.dialect.bindPlaceholders(deletableIds.length)})
    `, deletableIds)
    return { deletedCheckpoints: deleted.changes, hasMore: rows.length === limit }
  })
}

async function loadActiveCheckpoint(client: DatabaseClient, checkpointId: string, conversationId: string, systemAccountId: string, now: string): Promise<ChatContextCheckpoint | undefined> {
  const row = await client.one<ContextCheckpointRow>(`
    SELECT * FROM ${chatTable(client, 'chat_context_checkpoints')}
    WHERE id = ? AND conversation_id = ? AND system_account_id = ?
      AND status = 'active' AND expires_at > ?
    LIMIT 1
  `, [checkpointId, conversationId, systemAccountId, now])
  return row ? contextCheckpointFromRow(row) : undefined
}

async function loadCheckpointById(client: DatabaseClient, checkpointId: string): Promise<ChatContextCheckpoint | undefined> {
  const row = await client.one<ContextCheckpointRow>(`
    SELECT * FROM ${chatTable(client, 'chat_context_checkpoints')} WHERE id = ? LIMIT 1
  `, [checkpointId])
  return row ? contextCheckpointFromRow(row) : undefined
}

async function requireCompactionClaim(client: DatabaseClient, conversationId: string, systemAccountId: string, claimId: string): Promise<ChatContextCompactionClaim> {
  const claim = await findCompactionClaim(client, conversationId, systemAccountId, claimId)
  if (!claim) throw new ChatContextConflictError('聊天上下文压缩认领已失效')
  return claim
}

async function findCompactionClaim(client: DatabaseClient, conversationId: string, systemAccountId: string, claimId: string): Promise<ChatContextCompactionClaim | undefined> {
  const row = await client.one<ContextClaimRow>(`
    SELECT id, system_account_id, context_claim_id, context_claim_revision,
           compacted_through_sequence, context_claim_through_sequence,
           context_progress_sequence, context_attempt_count, context_claimed_at
    FROM ${chatTable(client, 'chat_conversations')}
    WHERE id = ? AND system_account_id = ?
      AND context_state = 'compacting' AND context_claim_id = ?
    LIMIT 1
  `, [conversationId, systemAccountId, claimId])
  if (!row) return undefined
  return {
    claimId: String(row.context_claim_id),
    conversationId: String(row.id),
    systemAccountId: String(row.system_account_id),
    sourceRevision: Number(row.context_claim_revision),
    sourceFromSequence: Number(row.compacted_through_sequence) + 1,
    sourceThroughSequence: Number(row.context_claim_through_sequence),
    progressSequence: Number(row.context_progress_sequence),
    attemptCount: Number(row.context_attempt_count) + 1,
    claimedAt: String(row.context_claimed_at)
  }
}

function contextMessagePair(rows: readonly ContextMessageRow[], startIndex: number): [ChatContextSourceMessage, ChatContextSourceMessage] {
  const user = rows[startIndex] ? contextMessageFromRow(rows[startIndex]!) : undefined
  const assistant = rows[startIndex + 1] ? contextMessageFromRow(rows[startIndex + 1]!) : undefined
  if (!user || !assistant || user.role !== 'user' || assistant.role !== 'assistant'
    || user.turnId !== assistant.turnId || assistant.sequenceNo !== user.sequenceNo + 1) {
    throw new Error('聊天上下文完整轮次顺序不一致')
  }
  return [user, assistant]
}

function serializeCheckpointEntries(inputs: readonly ChatContextCheckpointEntryInput[], checkpointId: string, conversationId: string, createdAt: string, expiresAt: string): ChatContextEntry[] {
  if (inputs.length < 1 || inputs.length > maxCheckpointEntries) throw new Error(`checkpoint entry 数量必须在 1..${maxCheckpointEntries} 之间`)
  let payloadBytes = 0
  return inputs.map((input, index) => {
    const contentJson = JSON.stringify(input.content)
    if (contentJson === undefined) throw new Error(`checkpoint entry ${index + 1} 内容不能序列化`)
    const contentBytes = Buffer.byteLength(contentJson, 'utf8')
    if (contentBytes < 2 || contentBytes > maxCheckpointEntryBytes) throw new Error(`checkpoint entry ${index + 1} 超过单条大小限制`)
    payloadBytes += contentBytes
    if (payloadBytes > maxCheckpointPayloadBytes) throw new Error('checkpoint entries 超过总大小限制')
    return {
      conversationId,
      checkpointId,
      sequence: index + 1,
      sourceMessageId: optionalText(input.sourceMessageId, 'sourceMessageId', 128),
      kind: normalizedEntryKind(input.kind),
      content: JSON.parse(contentJson) as unknown,
      contentBytes,
      provenance: normalizedProvenance(input.provenance),
      trustLevel: normalizedTrustLevel(input.trustLevel),
      tokenCount: optionalNonNegativeSafeInteger(input.tokenCount, 'tokenCount'),
      createdAt,
      expiresAt
    }
  })
}

function contextHeadFromRow(row: ContextHeadRow): ChatContextHead {
  return {
    conversationId: String(row.id),
    systemAccountId: String(row.system_account_id),
    contextRevision: Number(row.context_revision),
    activeCheckpointId: optionalString(row.active_checkpoint_id),
    compactedThroughSequence: Number(row.compacted_through_sequence),
    contextState: normalizedContextState(row.context_state),
    activeContextTokens: optionalNumber(row.active_context_tokens),
    effectiveContextLimitTokens: optionalNumber(row.effective_context_limit_tokens),
    usageEstimated: Number(row.context_usage_estimated ?? 1) !== 0,
    contextRetryAt: optionalString(row.context_retry_at),
    contextAttemptCount: Number(row.context_attempt_count ?? 0),
    contextErrorCode: optionalString(row.context_error_code),
    nextSequenceNo: Number(row.next_sequence_no)
  }
}

function contextCheckpointFromRow(row: ContextCheckpointRow): ChatContextCheckpoint {
  return {
    id: String(row.id),
    conversationId: String(row.conversation_id),
    systemAccountId: String(row.system_account_id),
    version: Number(row.version),
    sourceRevision: Number(row.source_revision),
    sourceFromSequence: Number(row.source_from_sequence),
    sourceThroughSequence: Number(row.source_through_sequence),
    recentTailFromSequence: Number(row.recent_tail_from_sequence),
    entryFromSequence: Number(row.entry_from_sequence),
    entryThroughSequence: Number(row.entry_through_sequence),
    payloadDigest: String(row.payload_digest),
    estimatedInputTokens: optionalNumber(row.estimated_input_tokens),
    upstreamInputTokens: optionalNumber(row.upstream_input_tokens),
    requestBodyBytes: Number(row.request_body_bytes),
    modelId: String(row.model_id),
    providerCode: optionalString(row.provider_code),
    providerProfileId: optionalString(row.provider_profile_id),
    endpointFamily: String(row.endpoint_family),
    compactCompatibilityHash: optionalString(row.compact_compatibility_hash),
    promptVersion: String(row.prompt_version),
    status: String(row.status) as ChatContextCheckpoint['status'],
    qualityStatus: String(row.quality_status) as ChatContextCheckpoint['qualityStatus'],
    createdAt: String(row.created_at),
    expiresAt: String(row.expires_at)
  }
}

function contextEntryFromRow(row: ContextEntryRow): ChatContextEntry {
  const contentJson = String(row.content_json)
  return {
    conversationId: String(row.conversation_id),
    checkpointId: String(row.checkpoint_id),
    sequence: Number(row.sequence),
    sourceMessageId: optionalString(row.source_message_id),
    kind: normalizedEntryKind(row.kind),
    content: JSON.parse(contentJson) as unknown,
    contentBytes: Math.max(Number(row.content_bytes), Buffer.byteLength(contentJson, 'utf8')),
    provenance: normalizedProvenance(row.provenance),
    trustLevel: normalizedTrustLevel(row.trust_level),
    tokenCount: optionalNumber(row.token_count),
    createdAt: String(row.created_at),
    expiresAt: String(row.expires_at)
  }
}

function contextMessageFromRow(row: ContextMessageRow): ChatContextSourceMessage {
  const contentText = String(row.content_text ?? '')
  const contentBlocksJson = String(row.content_blocks_json ?? '[]')
  return {
    id: String(row.id),
    turnId: String(row.turn_id),
    sequenceNo: Number(row.sequence_no),
    role: String(row.role) as 'user' | 'assistant',
    contentText,
    contentBlocks: parseJsonArray(contentBlocksJson),
    contentBytes: Math.max(Number(row.content_bytes ?? 0), Buffer.byteLength(contentText, 'utf8') + Buffer.byteLength(contentBlocksJson, 'utf8')),
    model: String(row.model),
    createdAt: String(row.created_at),
    completedAt: optionalString(row.completed_at),
    expiresAt: String(row.expires_at)
  }
}

function parseJsonArray(value: string): unknown[] {
  try {
    const parsed = JSON.parse(value) as unknown
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

function normalizedContextState(value: unknown): ChatContextState {
  const normalized = String(value)
  if (normalized === 'ready' || normalized === 'compact_pending' || normalized === 'compacting' || normalized === 'compact_failed') return normalized
  throw new Error(`未知聊天上下文状态：${normalized}`)
}

function normalizedEntryKind(value: unknown): ChatContextEntryKind {
  const normalized = String(value)
  if (normalized === 'verbatim' || normalized === 'durable_memory' || normalized === 'task_state' || normalized === 'tool_result' || normalized === 'image_observation' || normalized === 'provider_compaction') return normalized
  throw new Error(`未知 checkpoint entry 类型：${normalized}`)
}

function normalizedProvenance(value: unknown): ChatContextEntryProvenance {
  const normalized = String(value)
  if (normalized === 'user' || normalized === 'assistant' || normalized === 'tool' || normalized === 'asset' || normalized === 'provider') return normalized
  throw new Error(`未知 checkpoint provenance：${normalized}`)
}

function normalizedTrustLevel(value: unknown): ChatContextEntryTrustLevel {
  const normalized = String(value)
  if (normalized === 'untrusted' || normalized === 'assistant_derived' || normalized === 'provider_opaque') return normalized
  throw new Error(`未知 checkpoint trust level：${normalized}`)
}

function chatTable(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_chat', name)
}

function boundedInteger(value: number, name: string, min: number, max: number): number {
  if (!Number.isSafeInteger(value) || value < min || value > max) throw new Error(`${name} 必须是 ${min}..${max} 的整数`)
  return value
}

function nonNegativeSafeInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error(`${name} 必须是非负安全整数`)
  return value
}

function positiveSafeInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value < 1) throw new Error(`${name} 必须是正安全整数`)
  return value
}

function optionalNonNegativeSafeInteger(value: number | undefined, name: string): number | undefined {
  return value === undefined ? undefined : nonNegativeSafeInteger(value, name)
}

function optionalPositiveSafeInteger(value: number | undefined, name: string): number | undefined {
  return value === undefined ? undefined : positiveSafeInteger(value, name)
}

function normalizedText(value: string, name: string, maxLength: number): string {
  const normalized = value.trim()
  if (!normalized || normalized.length > maxLength || /[\u0000-\u001f\u007f]/.test(normalized)) throw new Error(`${name} 无效`)
  return normalized
}

function optionalText(value: string | undefined, name: string, maxLength: number): string | undefined {
  return value === undefined ? undefined : normalizedText(value, name, maxLength)
}

function normalizedDigest(value: string): string {
  const normalized = value.trim().toLowerCase()
  if (!/^[a-f0-9]{64}$/.test(normalized)) throw new Error('payloadDigest 必须是 SHA-256 十六进制摘要')
  return normalized
}

function optionalDigest(value: string | undefined, name: string): string | undefined {
  if (value === undefined) return undefined
  const normalized = value.trim().toLowerCase()
  if (!/^[a-f0-9]{64}$/.test(normalized)) throw new Error(`${name} 必须是 SHA-256 十六进制摘要`)
  return normalized
}

function normalizedTimestamp(value: string, name: string): string {
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) throw new Error(`${name} 必须是有效时间`)
  return new Date(timestamp).toISOString()
}

function earlierTimestamp(current: string | undefined, candidate: string): string {
  return !current || Date.parse(candidate) < Date.parse(current) ? candidate : current
}

function optionalString(value: unknown): string | undefined {
  return value === null || value === undefined ? undefined : String(value)
}

function optionalNumber(value: unknown): number | undefined {
  return value === null || value === undefined ? undefined : Number(value)
}

type ContextHeadRow = Record<string, unknown> & {
  id?: unknown
  system_account_id?: unknown
  context_revision?: unknown
  active_checkpoint_id?: unknown
  compacted_through_sequence?: unknown
  context_state?: unknown
  active_context_tokens?: unknown
  effective_context_limit_tokens?: unknown
  context_usage_estimated?: unknown
  context_retry_at?: unknown
  context_attempt_count?: unknown
  context_error_code?: unknown
  next_sequence_no?: unknown
  context_claim_id?: unknown
  context_claim_revision?: unknown
  context_claim_through_sequence?: unknown
  context_progress_sequence?: unknown
  context_progress_earliest_expires_at?: unknown
}
type ContextClaimRow = Record<string, unknown>
type ContextCheckpointRow = Record<string, unknown>
type ContextEntryRow = Record<string, unknown>
type ContextMessageRow = Record<string, unknown>
