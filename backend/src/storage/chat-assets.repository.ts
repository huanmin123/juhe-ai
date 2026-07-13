import { randomUUID } from 'node:crypto'

import type { DatabaseClient } from './database-client.js'
import { chatAssetOriginalMaxBytes, chatAssetProcessedMaxBytes } from './chat-asset-storage.js'

export type ChatAssetOriginalMimeType = 'image/jpeg' | 'image/png' | 'image/webp' | 'image/gif'
export type ChatAssetProcessedMimeType = 'image/jpeg' | 'image/png' | 'image/webp'
export type ChatAssetProcessingStatus = 'pending' | 'ready' | 'failed'
export type ChatAssetObservationStatus = 'not_requested' | 'pending' | 'ready' | 'failed'
export type ChatAssetCleanupStatus = 'active' | 'claimed' | 'failed'

export interface ChatAssetRecord {
  id: string
  systemAccountId: string
  conversationId: string
  originalFilename: string
  originalMimeType: ChatAssetOriginalMimeType
  originalWidth?: number
  originalHeight?: number
  originalBytes: number
  originalSha256: string
  processedMimeType?: ChatAssetProcessedMimeType
  processedWidth?: number
  processedHeight?: number
  processedBytes?: number
  processedSha256?: string
  storageKey?: string
  processingStatus: ChatAssetProcessingStatus
  processingErrorCode?: string
  observationStatus: ChatAssetObservationStatus
  observation?: Record<string, unknown>
  turnId?: string
  messageId?: string
  committedAt?: string
  cleanupStatus: ChatAssetCleanupStatus
  cleanupAttemptCount: number
  cleanupClaimedAt?: string
  cleanupRetryAt?: string
  cleanupErrorCode?: string
  createdAt: string
  updatedAt: string
  expiresAt: string
}

export interface ChatAssetApiMetadata {
  id: string
  fileName: string
  mimeType: ChatAssetProcessedMimeType
  width: number
  height: number
  byteSize: number
}

export interface ChatAssetCreateInput {
  id?: string
  systemAccountId: string
  conversationId: string
  originalFilename: string
  originalMimeType: ChatAssetOriginalMimeType
  originalWidth?: number
  originalHeight?: number
  originalBytes: number
  originalSha256: string
  now: string
}

export interface ChatAssetProcessingResultInput {
  assetId: string
  systemAccountId: string
  conversationId: string
  processedMimeType: ChatAssetProcessedMimeType
  processedWidth: number
  processedHeight: number
  processedBytes: number
  processedSha256: string
  storageKey: string
  now: string
}

export interface ChatAssetLookupInput {
  assetId: string
  systemAccountId: string
  conversationId: string
  now?: string
}

export interface ChatAssetCleanupClaim {
  claimId: string
  assets: ChatAssetRecord[]
  hasMore: boolean
}

interface ChatAssetRow {
  id: unknown
  system_account_id: unknown
  conversation_id: unknown
  original_filename: unknown
  original_mime_type: unknown
  original_width: unknown
  original_height: unknown
  original_bytes: unknown
  original_sha256: unknown
  processed_mime_type: unknown
  processed_width: unknown
  processed_height: unknown
  processed_bytes: unknown
  processed_sha256: unknown
  storage_key: unknown
  processing_status: unknown
  processing_error_code: unknown
  observation_status: unknown
  observation_json: unknown
  turn_id: unknown
  message_id: unknown
  committed_at: unknown
  cleanup_status: unknown
  cleanup_attempt_count: unknown
  cleanup_claimed_at: unknown
  cleanup_retry_at: unknown
  cleanup_error_code: unknown
  created_at: unknown
  updated_at: unknown
  expires_at: unknown
}

const chatAssetRetentionDays = 7
const chatAssetObservationMaxBytes = 128 * 1024
const maxChatAssetsPerMessage = 4

export async function createChatAsset(client: DatabaseClient, input: ChatAssetCreateInput): Promise<ChatAssetRecord> {
  const id = input.id === undefined ? newChatAssetId() : normalizedAssetId(input.id)
  const originalFilename = normalizedFilename(input.originalFilename)
  const originalMimeType = normalizedOriginalMimeType(input.originalMimeType)
  const originalBytes = normalizedPositiveInteger(input.originalBytes, 'originalBytes', chatAssetOriginalMaxBytes)
  const originalSha256 = normalizedSha256(input.originalSha256)
  const originalWidth = normalizedOptionalDimension(input.originalWidth, 'originalWidth')
  const originalHeight = normalizedOptionalDimension(input.originalHeight, 'originalHeight')
  if ((originalWidth === undefined) !== (originalHeight === undefined)) throw new Error('聊天资产原始宽高必须同时提供')
  const expiresAt = addDays(input.now, chatAssetRetentionDays)
  const result = await client.execute(`
    INSERT INTO ${chatTable(client, 'chat_assets')} (
      id, system_account_id, conversation_id, original_filename, original_mime_type,
      original_width, original_height, original_bytes, original_sha256,
      processing_status, observation_status, cleanup_status, cleanup_attempt_count,
      created_at, updated_at, expires_at
    )
    SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'not_requested', 'active', 0, ?, ?, ?
    FROM ${chatTable(client, 'chat_conversations')}
    WHERE id = ? AND system_account_id = ?
  `, [
    id,
    input.systemAccountId,
    input.conversationId,
    originalFilename,
    originalMimeType,
    originalWidth ?? null,
    originalHeight ?? null,
    originalBytes,
    originalSha256,
    input.now,
    input.now,
    expiresAt,
    input.conversationId,
    input.systemAccountId
  ])
  if (result.changes !== 1) throw new Error('聊天会话不存在或不属于当前用户')
  return requireChatAsset(client, { assetId: id, systemAccountId: input.systemAccountId, conversationId: input.conversationId })
}

export async function completeChatAssetProcessing(client: DatabaseClient, input: ChatAssetProcessingResultInput): Promise<ChatAssetRecord> {
  const processedMimeType = normalizedProcessedMimeType(input.processedMimeType)
  const processedWidth = normalizedDimension(input.processedWidth, 'processedWidth')
  const processedHeight = normalizedDimension(input.processedHeight, 'processedHeight')
  const processedBytes = normalizedPositiveInteger(input.processedBytes, 'processedBytes', chatAssetProcessedMaxBytes)
  const processedSha256 = normalizedSha256(input.processedSha256)
  const storageKey = normalizedStorageKey(input.storageKey)
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET processed_mime_type = ?, processed_width = ?, processed_height = ?, processed_bytes = ?,
        processed_sha256 = ?, storage_key = ?, processing_status = 'ready',
        processing_error_code = NULL, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
      AND processing_status = 'pending' AND cleanup_status = 'active' AND expires_at > ?
  `, [
    processedMimeType,
    processedWidth,
    processedHeight,
    processedBytes,
    processedSha256,
    storageKey,
    input.now,
    input.assetId,
    input.systemAccountId,
    input.conversationId,
    input.now
  ])
  if (result.changes !== 1) throw new Error('聊天资产不存在、已过期或处理状态已变化')
  return requireChatAsset(client, input)
}

export async function failChatAssetProcessing(client: DatabaseClient, input: ChatAssetLookupInput & {
  errorCode: string
  now: string
}): Promise<ChatAssetRecord | undefined> {
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET processing_status = 'failed', processing_error_code = ?, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
      AND processing_status = 'pending' AND cleanup_status = 'active'
  `, [normalizedErrorCode(input.errorCode), input.now, input.assetId, input.systemAccountId, input.conversationId])
  if (result.changes !== 1) return undefined
  return requireChatAsset(client, input)
}

export async function getChatAsset(client: DatabaseClient, input: ChatAssetLookupInput): Promise<ChatAssetRecord | undefined> {
  const params: unknown[] = [input.assetId, input.systemAccountId, input.conversationId]
  const activeClause = input.now ? 'AND expires_at > ?' : ''
  if (input.now) params.push(input.now)
  const row = await client.one<ChatAssetRow>(`
    SELECT * FROM ${chatTable(client, 'chat_assets')}
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
      AND cleanup_status = 'active' ${activeClause}
    LIMIT 1
  `, params)
  return row ? chatAssetFromRow(row) : undefined
}

export async function listReadyChatAssetsByIds(client: DatabaseClient, input: {
  assetIds: readonly string[]
  systemAccountId: string
  conversationId: string
  now: string
}): Promise<ChatAssetRecord[]> {
  const assetIds = normalizedAssetIds(input.assetIds)
  if (assetIds.length === 0) return []
  const rows = await client.query<ChatAssetRow>(`
    SELECT * FROM ${chatTable(client, 'chat_assets')}
    WHERE id IN (${client.dialect.bindPlaceholders(assetIds.length)})
      AND system_account_id = ? AND conversation_id = ?
      AND processing_status = 'ready' AND cleanup_status = 'active' AND expires_at > ?
  `, [...assetIds, input.systemAccountId, input.conversationId, input.now])
  const recordsById = new Map(rows.map((row) => {
    const record = chatAssetFromRow(row)
    return [record.id, record]
  }))
  return assetIds.map((assetId) => recordsById.get(assetId)).filter((asset): asset is ChatAssetRecord => Boolean(asset))
}

export async function commitChatAssetsToMessage(client: DatabaseClient, input: {
  assetIds: readonly string[]
  systemAccountId: string
  conversationId: string
  messageId: string
  now: string
}): Promise<ChatAssetRecord[]> {
  const assetIds = normalizedAssetIds(input.assetIds)
  if (assetIds.length === 0) return []
  return client.transaction((tx) => commitChatAssetsToMessageInClient(tx, { ...input, assetIds }))
}

export async function commitChatAssetsToMessageInClient(client: DatabaseClient, input: {
  assetIds: readonly string[]
  systemAccountId: string
  conversationId: string
  messageId: string
  now: string
}): Promise<ChatAssetRecord[]> {
  const assetIds = normalizedAssetIds(input.assetIds)
  if (assetIds.length === 0) return []
  const message = await client.one<{ turn_id?: unknown }>(`
    SELECT turn_id FROM ${chatTable(client, 'chat_messages')}
    WHERE id = ? AND conversation_id = ? AND system_account_id = ?
      AND role = 'user' AND status = 'completed'
    LIMIT 1 ${client.driver === 'postgres' ? 'FOR UPDATE' : ''}
  `, [input.messageId, input.conversationId, input.systemAccountId])
  const turnId = optionalString(message?.turn_id)
  if (!turnId) throw new Error('聊天资产只能绑定到已写入的用户消息')
  const rows = await client.query<ChatAssetRow>(`
    SELECT * FROM ${chatTable(client, 'chat_assets')}
    WHERE id IN (${client.dialect.bindPlaceholders(assetIds.length)})
      AND system_account_id = ? AND conversation_id = ?
      ${client.driver === 'postgres' ? 'FOR UPDATE' : ''}
  `, [...assetIds, input.systemAccountId, input.conversationId])
  const records = rows.map(chatAssetFromRow)
  const recordsById = new Map(records.map((asset) => [asset.id, asset]))
  for (const assetId of assetIds) {
    const asset = recordsById.get(assetId)
    if (!asset || asset.processingStatus !== 'ready' || asset.cleanupStatus !== 'active' || asset.expiresAt <= input.now) {
      throw new Error('聊天资产不存在、未处理完成或已过期')
    }
    if ((asset.turnId && asset.turnId !== turnId) || (asset.messageId && asset.messageId !== input.messageId)) {
      throw new Error('聊天资产已绑定其他消息')
    }
  }
  const expiresAt = addDays(input.now, chatAssetRetentionDays)
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET turn_id = ?, message_id = ?, committed_at = COALESCE(committed_at, ?), expires_at = ?, updated_at = ?
    WHERE id IN (${client.dialect.bindPlaceholders(assetIds.length)})
      AND system_account_id = ? AND conversation_id = ? AND cleanup_status = 'active'
      AND (turn_id IS NULL OR turn_id = ?) AND (message_id IS NULL OR message_id = ?)
  `, [turnId, input.messageId, input.now, expiresAt, input.now, ...assetIds, input.systemAccountId, input.conversationId, turnId, input.messageId])
  if (result.changes !== assetIds.length) throw new Error('聊天资产绑定消息时发生并发冲突')
  return listReadyChatAssetsByIds(client, { ...input, assetIds })
}

export async function setChatAssetObservation(client: DatabaseClient, input: ChatAssetLookupInput & {
  status: Exclude<ChatAssetObservationStatus, 'not_requested'>
  observation?: Record<string, unknown>
  now: string
}): Promise<ChatAssetRecord | undefined> {
  const observationJson = input.observation === undefined ? null : boundedJson(input.observation, chatAssetObservationMaxBytes)
  if (input.status === 'ready' && !observationJson) throw new Error('图片说明完成时必须提供 observation')
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET observation_status = ?, observation_json = ?, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
      AND processing_status = 'ready' AND cleanup_status = 'active' AND expires_at > ?
  `, [input.status, observationJson, input.now, input.assetId, input.systemAccountId, input.conversationId, input.now])
  if (result.changes !== 1) return undefined
  return requireChatAsset(client, input)
}

export async function claimChatAssetObservation(client: DatabaseClient, input: ChatAssetLookupInput & { now: string }): Promise<ChatAssetRecord | undefined> {
  const staleBefore = new Date(Date.parse(input.now) - 15 * 60_000).toISOString()
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET observation_status = 'pending', observation_json = NULL, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
      AND processing_status = 'ready'
      AND (observation_status IN ('not_requested', 'failed') OR (observation_status = 'pending' AND updated_at <= ?))
      AND cleanup_status = 'active' AND expires_at > ?
  `, [input.now, input.assetId, input.systemAccountId, input.conversationId, staleBefore, input.now])
  if (result.changes !== 1) return undefined
  return requireChatAsset(client, input)
}

export async function claimUncommittedChatAssetForDeletion(client: DatabaseClient, input: ChatAssetLookupInput & {
  now: string
}): Promise<{ claimId: string; asset: ChatAssetRecord } | undefined> {
  const claimId = cleanupClaimId()
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET cleanup_status = 'claimed', cleanup_claim_id = ?, cleanup_claimed_at = ?,
        cleanup_attempt_count = cleanup_attempt_count + 1, cleanup_retry_at = NULL,
        cleanup_error_code = NULL, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
      AND message_id IS NULL AND cleanup_status IN ('active', 'failed')
  `, [claimId, input.now, input.now, input.assetId, input.systemAccountId, input.conversationId])
  if (result.changes !== 1) return undefined
  const asset = await getClaimedChatAsset(client, input.assetId, claimId)
  return asset ? { claimId, asset } : undefined
}

export async function claimExpiredChatAssetsForCleanup(client: DatabaseClient, input: {
  now: string
  limit: number
}): Promise<ChatAssetCleanupClaim> {
  const limit = Math.max(1, Math.min(Math.trunc(input.limit), 500))
  const claimId = cleanupClaimId()
  const staleClaimBefore = new Date(Date.parse(input.now) - 15 * 60 * 1000).toISOString()
  return client.transaction(async (tx) => {
    const rows = await tx.query<{ id?: unknown }>(`
      SELECT id FROM ${chatTable(tx, 'chat_assets')}
      WHERE expires_at <= ?
        AND (
          cleanup_status = 'active'
          OR (cleanup_status = 'failed' AND cleanup_retry_at <= ?)
          OR (cleanup_status = 'claimed' AND cleanup_claimed_at <= ?)
        )
      ORDER BY expires_at ASC, id ASC
      LIMIT ? ${tx.driver === 'postgres' ? 'FOR UPDATE SKIP LOCKED' : ''}
    `, [input.now, input.now, staleClaimBefore, limit])
    const assetIds = rows.map((row) => String(row.id ?? '')).filter(Boolean)
    if (assetIds.length === 0) return { claimId, assets: [], hasMore: false }
    const result = await tx.execute(`
      UPDATE ${chatTable(tx, 'chat_assets')}
      SET cleanup_status = 'claimed', cleanup_claim_id = ?, cleanup_claimed_at = ?,
          cleanup_attempt_count = cleanup_attempt_count + 1, cleanup_retry_at = NULL,
          cleanup_error_code = NULL, updated_at = ?
      WHERE id IN (${tx.dialect.bindPlaceholders(assetIds.length)})
    `, [claimId, input.now, input.now, ...assetIds])
    if (result.changes !== assetIds.length) throw new Error('聊天资产清理认领发生并发冲突')
    const claimedRows = await tx.query<ChatAssetRow>(`
      SELECT * FROM ${chatTable(tx, 'chat_assets')}
      WHERE cleanup_claim_id = ?
      ORDER BY expires_at ASC, id ASC
    `, [claimId])
    return { claimId, assets: claimedRows.map(chatAssetFromRow), hasMore: assetIds.length === limit }
  })
}

export async function expireChatAssetsForConversationInClient(client: DatabaseClient, input: {
  systemAccountId: string
  conversationId: string
  now: string
}): Promise<number> {
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
        cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
        updated_at = ?
    WHERE system_account_id = ? AND conversation_id = ?
      AND cleanup_status IN ('active', 'failed')
  `, [input.now, input.now, input.now, input.now, input.systemAccountId, input.conversationId])
  return result.changes
}

export async function completeChatAssetDeletion(client: DatabaseClient, input: {
  assetId: string
  claimId: string
}): Promise<boolean> {
  const result = await client.execute(`
    DELETE FROM ${chatTable(client, 'chat_assets')}
    WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
  `, [input.assetId, input.claimId])
  return result.changes === 1
}

export async function releaseChatAssetDeletionClaim(client: DatabaseClient, input: {
  assetId: string
  claimId: string
  errorCode: string
  retryAt: string
  now: string
}): Promise<boolean> {
  const result = await client.execute(`
    UPDATE ${chatTable(client, 'chat_assets')}
    SET cleanup_status = 'failed', cleanup_claim_id = NULL, cleanup_claimed_at = NULL,
        cleanup_retry_at = ?, cleanup_error_code = ?, updated_at = ?
    WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
  `, [input.retryAt, normalizedErrorCode(input.errorCode), input.now, input.assetId, input.claimId])
  return result.changes === 1
}

export function chatAssetApiMetadata(asset: ChatAssetRecord): ChatAssetApiMetadata {
  if (
    asset.processingStatus !== 'ready'
    || !asset.processedMimeType
    || asset.processedWidth === undefined
    || asset.processedHeight === undefined
    || asset.processedBytes === undefined
  ) {
    throw new Error('只有处理完成的聊天资产才能转换为上传响应')
  }
  return {
    id: asset.id,
    fileName: asset.originalFilename,
    mimeType: asset.processedMimeType,
    width: asset.processedWidth,
    height: asset.processedHeight,
    byteSize: asset.processedBytes
  }
}

async function requireChatAsset(client: DatabaseClient, input: {
  assetId: string
  systemAccountId: string
  conversationId: string
}): Promise<ChatAssetRecord> {
  const row = await client.one<ChatAssetRow>(`
    SELECT * FROM ${chatTable(client, 'chat_assets')}
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
    LIMIT 1
  `, [input.assetId, input.systemAccountId, input.conversationId])
  if (!row) throw new Error('聊天资产不存在或不属于当前用户')
  return chatAssetFromRow(row)
}

async function getClaimedChatAsset(client: DatabaseClient, assetId: string, claimId: string): Promise<ChatAssetRecord | undefined> {
  const row = await client.one<ChatAssetRow>(`
    SELECT * FROM ${chatTable(client, 'chat_assets')}
    WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
    LIMIT 1
  `, [assetId, claimId])
  return row ? chatAssetFromRow(row) : undefined
}

function chatAssetFromRow(row: ChatAssetRow): ChatAssetRecord {
  return {
    id: String(row.id),
    systemAccountId: String(row.system_account_id),
    conversationId: String(row.conversation_id),
    originalFilename: String(row.original_filename),
    originalMimeType: normalizedOriginalMimeType(String(row.original_mime_type)),
    originalWidth: optionalNumber(row.original_width),
    originalHeight: optionalNumber(row.original_height),
    originalBytes: Number(row.original_bytes),
    originalSha256: String(row.original_sha256),
    processedMimeType: row.processed_mime_type == null ? undefined : normalizedProcessedMimeType(String(row.processed_mime_type)),
    processedWidth: optionalNumber(row.processed_width),
    processedHeight: optionalNumber(row.processed_height),
    processedBytes: optionalNumber(row.processed_bytes),
    processedSha256: optionalString(row.processed_sha256),
    storageKey: optionalString(row.storage_key),
    processingStatus: normalizedProcessingStatus(row.processing_status),
    processingErrorCode: optionalString(row.processing_error_code),
    observationStatus: normalizedObservationStatus(row.observation_status),
    observation: optionalJsonObject(row.observation_json),
    turnId: optionalString(row.turn_id),
    messageId: optionalString(row.message_id),
    committedAt: optionalString(row.committed_at),
    cleanupStatus: normalizedCleanupStatus(row.cleanup_status),
    cleanupAttemptCount: Number(row.cleanup_attempt_count),
    cleanupClaimedAt: optionalString(row.cleanup_claimed_at),
    cleanupRetryAt: optionalString(row.cleanup_retry_at),
    cleanupErrorCode: optionalString(row.cleanup_error_code),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    expiresAt: String(row.expires_at)
  }
}

function chatTable(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_chat', name)
}

export function newChatAssetId(): string {
  return `chat_asset_${randomUUID().replace(/-/g, '')}`
}

function cleanupClaimId(): string {
  return `chat_asset_cleanup_${randomUUID().replace(/-/g, '')}`
}

function normalizedAssetIds(values: readonly string[]): string[] {
  const unique = [...new Set(values.map(normalizedAssetId))]
  if (unique.length > maxChatAssetsPerMessage) throw new Error(`每条消息最多关联 ${maxChatAssetsPerMessage} 张图片`)
  return unique
}

function normalizedAssetId(value: string): string {
  const normalized = value.trim()
  if (!/^chat_asset_[a-f0-9]{32}$/.test(normalized)) throw new Error('聊天资产 ID 无效')
  return normalized
}

function normalizedFilename(value: string): string {
  const filename = value.split(/[\\/]/).pop()?.trim().replace(/[\u0000-\u001f\u007f]/g, '')
  if (!filename) throw new Error('聊天资产原始文件名不能为空')
  return filename.slice(0, 255)
}

function normalizedOriginalMimeType(value: string): ChatAssetOriginalMimeType {
  const normalized = value.trim().toLowerCase()
  if (normalized === 'image/jpeg' || normalized === 'image/png' || normalized === 'image/webp' || normalized === 'image/gif') return normalized
  throw new Error(`不支持的聊天图片 MIME：${value}`)
}

function normalizedProcessedMimeType(value: string): ChatAssetProcessedMimeType {
  const normalized = value.trim().toLowerCase()
  if (normalized === 'image/jpeg' || normalized === 'image/png' || normalized === 'image/webp') return normalized
  throw new Error(`不支持的处理后聊天图片 MIME：${value}`)
}

function normalizedProcessingStatus(value: unknown): ChatAssetProcessingStatus {
  if (value === 'pending' || value === 'ready' || value === 'failed') return value
  throw new Error(`聊天资产处理状态无效：${String(value)}`)
}

function normalizedObservationStatus(value: unknown): ChatAssetObservationStatus {
  if (value === 'not_requested' || value === 'pending' || value === 'ready' || value === 'failed') return value
  throw new Error(`聊天资产说明状态无效：${String(value)}`)
}

function normalizedCleanupStatus(value: unknown): ChatAssetCleanupStatus {
  if (value === 'active' || value === 'claimed' || value === 'failed') return value
  throw new Error(`聊天资产清理状态无效：${String(value)}`)
}

function normalizedDimension(value: number, field: string): number {
  return normalizedPositiveInteger(value, field, 100_000)
}

function normalizedOptionalDimension(value: number | undefined, field: string): number | undefined {
  return value === undefined ? undefined : normalizedDimension(value, field)
}

function normalizedPositiveInteger(value: number, field: string, max: number): number {
  if (!Number.isSafeInteger(value) || value <= 0 || value > max) throw new Error(`${field} 必须是 1..${max} 的整数`)
  return value
}

function normalizedSha256(value: string): string {
  const normalized = value.trim().toLowerCase()
  if (!/^[a-f0-9]{64}$/.test(normalized)) throw new Error('聊天资产 SHA-256 无效')
  return normalized
}

function normalizedStorageKey(value: string): string {
  const normalized = value.trim().replace(/\\/g, '/')
  if (!normalized || normalized.startsWith('/') || normalized.includes('\0') || normalized.split('/').includes('..')) {
    throw new Error('聊天资产存储键无效')
  }
  return normalized.slice(0, 512)
}

function normalizedErrorCode(value: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error('错误码不能为空')
  return normalized.slice(0, 120)
}

function boundedJson(value: Record<string, unknown>, maxBytes: number): string {
  const json = JSON.stringify(value)
  if (Buffer.byteLength(json, 'utf8') > maxBytes) throw new Error(`聊天资产说明超过 ${maxBytes} 字节`)
  return json
}

function optionalJsonObject(value: unknown): Record<string, unknown> | undefined {
  if (value == null || value === '') return undefined
  const parsed = JSON.parse(String(value)) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('聊天资产说明 JSON 不是对象')
  return parsed as Record<string, unknown>
}

function optionalString(value: unknown): string | undefined {
  return value == null || value === '' ? undefined : String(value)
}

function optionalNumber(value: unknown): number | undefined {
  return value == null ? undefined : Number(value)
}

function addDays(value: string, days: number): string {
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) throw new Error('聊天资产时间无效')
  return new Date(timestamp + days * 24 * 60 * 60 * 1000).toISOString()
}
