import type { DatabaseClient } from './database-client.js'
import type { ChatImageModel } from './chat.repository.js'

export type ChatImageGenerationOperation = 'generate' | 'edit'

export interface ChatImageGenerationRecord {
  assetId: string
  conversationId: string
  systemAccountId: string
  operation: ChatImageGenerationOperation
  model: ChatImageModel
  prompt: string
  sourceAssetIds: string[]
  rootAssetId: string
  size: string
  quality: string
  outputFormat: string
  createdAt: string
  expiresAt: string
}

export interface ChatImageGenerationCommitInput {
  assetId: string
  conversationId: string
  systemAccountId: string
  operation: ChatImageGenerationOperation
  model: ChatImageModel
  prompt: string
  sourceAssetIds: readonly string[]
  size: string
  quality: string
  outputFormat: string
  createdAt: string
  expiresAt: string
}

interface ChatImageGenerationRow {
  asset_id: unknown
  conversation_id: unknown
  system_account_id: unknown
  operation: unknown
  model: unknown
  prompt: unknown
  source_asset_ids_json: unknown
  root_asset_id: unknown
  size: unknown
  quality: unknown
  output_format: unknown
  created_at: unknown
  expires_at: unknown
}

const maxImageReferences = 5
const maxPromptBytes = 65_536

export async function commitChatImageGenerationInClient(client: DatabaseClient, input: ChatImageGenerationCommitInput): Promise<ChatImageGenerationRecord> {
  const assetId = normalizedAssetId(input.assetId)
  const sourceAssetIds = normalizedSourceAssetIds(input.sourceAssetIds)
  const operation = normalizedOperation(input.operation)
  if (operation === 'generate' && sourceAssetIds.length !== 0) throw new Error('生成图片不能包含来源图片')
  if (operation === 'edit' && sourceAssetIds.length === 0) throw new Error('编辑图片必须至少引用一张来源图片')
  const prompt = normalizedText(input.prompt, '图像提示词', maxPromptBytes)
  const model = normalizedModel(input.model)
  const size = normalizedText(input.size, '图像尺寸', 32)
  const quality = normalizedText(input.quality, '图像质量', 32)
  const outputFormat = normalizedText(input.outputFormat, '图像格式', 16)

  let rootAssetId = assetId
  if (operation === 'edit') {
    const sourceRows = await client.query<{ id?: unknown; root_asset_id?: unknown }>(`
      SELECT asset.id, generation.root_asset_id
      FROM ${chatTable(client, 'chat_assets')} AS asset
      LEFT JOIN ${chatTable(client, 'chat_image_generations')} AS generation ON generation.asset_id = asset.id
      WHERE asset.id IN (${client.dialect.bindPlaceholders(sourceAssetIds.length)})
        AND asset.system_account_id = ? AND asset.conversation_id = ?
        AND asset.processing_status = 'ready' AND asset.cleanup_status = 'active' AND asset.expires_at > ?
      ${client.driver === 'postgres' ? 'FOR UPDATE OF asset' : ''}
    `, [...sourceAssetIds, input.systemAccountId, input.conversationId, input.createdAt])
    const sourceById = new Map(sourceRows.map((row) => [String(row.id ?? ''), row]))
    if (sourceById.size !== sourceAssetIds.length || sourceAssetIds.some((id) => !sourceById.has(id))) {
      throw new Error('引用图片不存在、已过期或不属于当前会话')
    }
    const sourceRootAssetIds = sourceAssetIds.map((sourceAssetId) => {
      const source = sourceById.get(sourceAssetId)!
      return normalizedAssetId(source.root_asset_id == null ? sourceAssetId : String(source.root_asset_id))
    })
    rootAssetId = sourceRootAssetIds[0]!
    const retainedAssetIds = [...new Set([...sourceAssetIds, ...sourceRootAssetIds])]
    const retained = await client.execute(`
      UPDATE ${chatTable(client, 'chat_assets')}
      SET expires_at = CASE WHEN expires_at < ? THEN ? ELSE expires_at END,
          updated_at = CASE WHEN expires_at < ? THEN ? ELSE updated_at END
      WHERE id IN (${client.dialect.bindPlaceholders(retainedAssetIds.length)})
        AND system_account_id = ? AND conversation_id = ?
        AND processing_status = 'ready' AND cleanup_status = 'active'
    `, [input.expiresAt, input.expiresAt, input.expiresAt, input.createdAt, ...retainedAssetIds, input.systemAccountId, input.conversationId])
    if (retained.changes !== retainedAssetIds.length) throw new Error('引用图片保留期限更新失败')
    await renewChatImageGenerationExpiryInClient(client, {
      assetIds: retainedAssetIds,
      conversationId: input.conversationId,
      systemAccountId: input.systemAccountId,
      expiresAt: input.expiresAt
    })
  }

  const row = await client.one<ChatImageGenerationRow>(`
    INSERT INTO ${chatTable(client, 'chat_image_generations')} (
      asset_id, conversation_id, system_account_id, operation, model, prompt,
      source_asset_ids_json, root_asset_id, size, quality, output_format, created_at, expires_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    RETURNING *
  `, [
    assetId,
    input.conversationId,
    input.systemAccountId,
    operation,
    model,
    prompt,
    JSON.stringify(sourceAssetIds),
    rootAssetId,
    size,
    quality,
    outputFormat,
    input.createdAt,
    input.expiresAt
  ])
  if (!row) throw new Error('图像谱系写入失败')
  return mapChatImageGeneration(row)
}

export async function getChatImageGeneration(client: DatabaseClient, input: {
  assetId: string
  conversationId: string
  systemAccountId: string
}): Promise<ChatImageGenerationRecord | undefined> {
  const row = await client.one<ChatImageGenerationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_image_generations')}
    WHERE asset_id = ? AND conversation_id = ? AND system_account_id = ?
    LIMIT 1
  `, [normalizedAssetId(input.assetId), input.conversationId, input.systemAccountId])
  return row ? mapChatImageGeneration(row) : undefined
}

export async function renewChatImageGenerationExpiryInClient(client: DatabaseClient, input: {
  assetIds: readonly string[]
  conversationId: string
  systemAccountId: string
  expiresAt: string
}): Promise<void> {
  const assetIds = [...new Set(input.assetIds.map(normalizedAssetId))]
  if (assetIds.length === 0) return
  await client.execute(`
    UPDATE ${chatTable(client, 'chat_image_generations')}
    SET expires_at = CASE WHEN expires_at < ? THEN ? ELSE expires_at END
    WHERE asset_id IN (${client.dialect.bindPlaceholders(assetIds.length)})
      AND conversation_id = ? AND system_account_id = ?
  `, [input.expiresAt, input.expiresAt, ...assetIds, input.conversationId, input.systemAccountId])
}

export async function listChatImageGenerationRootAssetIdsInClient(client: DatabaseClient, input: {
  assetIds: readonly string[]
  conversationId: string
  systemAccountId: string
}): Promise<string[]> {
  const assetIds = [...new Set(input.assetIds.map(normalizedAssetId))]
  if (assetIds.length === 0) return []
  const rows = await client.query<{ root_asset_id?: unknown }>(`
    SELECT root_asset_id FROM ${chatTable(client, 'chat_image_generations')}
    WHERE asset_id IN (${client.dialect.bindPlaceholders(assetIds.length)})
      AND conversation_id = ? AND system_account_id = ?
  `, [...assetIds, input.conversationId, input.systemAccountId])
  return [...new Set(rows.map((row) => normalizedAssetId(String(row.root_asset_id))))]
}

export async function listRecentChatImageGenerations(client: DatabaseClient, input: {
  conversationId: string
  systemAccountId: string
  now: string
  limit: number
}): Promise<ChatImageGenerationRecord[]> {
  const limit = Math.max(1, Math.min(Math.trunc(input.limit), 12))
  const rows = await client.query<ChatImageGenerationRow>(`
    SELECT * FROM ${chatTable(client, 'chat_image_generations')}
    WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ?
    ORDER BY created_at DESC, asset_id DESC
    LIMIT ?
  `, [input.conversationId, input.systemAccountId, input.now, limit])
  return rows.map(mapChatImageGeneration)
}

function mapChatImageGeneration(row: ChatImageGenerationRow): ChatImageGenerationRecord {
  return {
    assetId: normalizedAssetId(String(row.asset_id)),
    conversationId: String(row.conversation_id),
    systemAccountId: String(row.system_account_id),
    operation: normalizedOperation(row.operation),
    model: normalizedModel(row.model),
    prompt: normalizedText(row.prompt, '图像提示词', maxPromptBytes),
    sourceAssetIds: parseSourceAssetIds(row.source_asset_ids_json),
    rootAssetId: normalizedAssetId(String(row.root_asset_id)),
    size: normalizedText(row.size, '图像尺寸', 32),
    quality: normalizedText(row.quality, '图像质量', 32),
    outputFormat: normalizedText(row.output_format, '图像格式', 16),
    createdAt: String(row.created_at),
    expiresAt: String(row.expires_at)
  }
}

function parseSourceAssetIds(value: unknown): string[] {
  if (typeof value !== 'string') throw new Error('图像谱系来源资产无效')
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed)) throw new Error('not-array')
    return normalizedSourceAssetIds(parsed.map(String))
  } catch {
    throw new Error('图像谱系来源资产无效')
  }
}

function normalizedSourceAssetIds(values: readonly string[]): string[] {
  const normalized = values.map(normalizedAssetId)
  if (new Set(normalized).size !== normalized.length) throw new Error('引用图片不能重复')
  if (normalized.length > maxImageReferences) throw new Error(`编辑图片最多引用 ${maxImageReferences} 张来源图片`)
  return normalized
}

function normalizedAssetId(value: string): string {
  const normalized = value.trim()
  if (!/^chat_asset_[a-f0-9]{32}$/.test(normalized)) throw new Error('聊天资产 ID 无效')
  return normalized
}

function normalizedOperation(value: unknown): ChatImageGenerationOperation {
  if (value === 'generate' || value === 'edit') return value
  throw new Error('图像谱系操作无效')
}

function normalizedModel(value: unknown): ChatImageModel {
  if (value === 'gpt-image-2') return value
  throw new Error('图像模型无效')
}

function normalizedText(value: unknown, label: string, maxBytes: number): string {
  const normalized = typeof value === 'string' ? value.trim() : ''
  if (!normalized) throw new Error(`${label}不能为空`)
  if (Buffer.byteLength(normalized, 'utf8') > maxBytes) throw new Error(`${label}超过字节上限`)
  return normalized
}

function chatTable(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_chat', name)
}
