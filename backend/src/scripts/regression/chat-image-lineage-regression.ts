import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { resolveChatImageModel, listChatImageModels } from '../../modules/chat/chat-image-model-registry.js'
import { validateChatImageEditReferenceLimits } from '../../modules/chat/chat-image-edit-references.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { createChatConversation } from '../../storage/chat.repository.js'
import { commitChatAssetsToMessage, commitChatGeneratedAsset, getChatAsset } from '../../storage/chat-assets.repository.js'
import { getChatImageGeneration, listRecentChatImageGenerations } from '../../storage/chat-image-generations.repository.js'

assert.deepEqual(listChatImageModels(), [{ id: 'gpt-image-2', label: 'GPT Image 2', canGenerate: true, canEdit: true, maxReferenceImages: 5 }])
assert.equal(resolveChatImageModel(undefined, 'gpt-image-2').id, 'gpt-image-2')
assert.throws(() => resolveChatImageModel('unknown-image-model', 'gpt-image-2'), /不支持的图像模型/)
assert.throws(() => validateChatImageEditReferenceLimits(new Array(6).fill({ bytes: 1 })), /最多引用 5 张/)
assert.throws(() => validateChatImageEditReferenceLimits([
  { bytes: 16 * 1024 * 1024 },
  { bytes: 16 * 1024 * 1024 },
  { bytes: 16 * 1024 * 1024 },
  { bytes: 1 }
]), /48 MiB/)

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const now = '2026-07-20T00:00:00.000Z'
const sourceAssetId = `chat_asset_${'1'.repeat(32)}`
const editedAssetId = `chat_asset_${'2'.repeat(32)}`
const crossEditAssetId = `chat_asset_${'3'.repeat(32)}`
const secondaryRootAssetId = `chat_asset_${'4'.repeat(32)}`
const secondaryEditedAssetId = `chat_asset_${'5'.repeat(32)}`
const multiSourceEditedAssetId = `chat_asset_${'6'.repeat(32)}`
const sourceConversation = await createChatConversation(client, {
  id: 'lineage-conversation',
  systemAccountId: 'lineage-owner',
  apiKeyId: 'lineage-key',
  apiKeyNameSnapshot: 'Lineage Key',
  now,
  maxConversationsPerUser: 50
})

insertAssistantMessage(database, sourceConversation.id, 'source-turn', 'source-message', 1, now)
const source = await commitChatGeneratedAsset(client, generatedCommitInput({
  id: sourceAssetId,
  conversationId: sourceConversation.id,
  turnId: 'source-turn',
  messageId: 'source-message',
  contentOrder: 0,
  now,
  generation: {
    operation: 'generate',
    model: 'gpt-image-2',
    prompt: '生成一张基础图',
    sourceAssetIds: [],
    size: '1024x1024',
    quality: 'auto',
    outputFormat: 'webp'
  }
}))
assert.equal((await getChatImageGeneration(client, {
  assetId: source.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.rootAssetId, source.id)

const editNow = '2026-07-25T00:00:00.000Z'
insertAssistantMessage(database, sourceConversation.id, 'edit-turn', 'edit-message', 2, editNow)
const edited = await commitChatGeneratedAsset(client, generatedCommitInput({
  id: editedAssetId,
  conversationId: sourceConversation.id,
  turnId: 'edit-turn',
  messageId: 'edit-message',
  contentOrder: 0,
  now: editNow,
  generation: {
    operation: 'edit',
    model: 'gpt-image-2',
    prompt: '把背景改成夜晚',
    sourceAssetIds: [source.id],
    size: '1024x1024',
    quality: 'auto',
    outputFormat: 'webp'
  }
}))
const editedGeneration = await getChatImageGeneration(client, {
  assetId: edited.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
})
assert.equal(editedGeneration?.operation, 'edit')
assert.deepEqual(editedGeneration?.sourceAssetIds, [source.id])
assert.equal(editedGeneration?.rootAssetId, source.id)
assert.deepEqual((await listRecentChatImageGenerations(client, {
  conversationId: sourceConversation.id,
  systemAccountId: 'lineage-owner',
  now: editNow,
  limit: 12
})).map((item) => item.assetId), [edited.id, source.id])
assert.equal((await getChatAsset(client, {
  assetId: source.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.expiresAt, '2026-08-24T00:00:00.000Z', '编辑提交必须把父图有效期延长到子图有效期')
assert.equal((await getChatImageGeneration(client, {
  assetId: source.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.expiresAt, '2026-08-24T00:00:00.000Z', '编辑提交必须同步延长父图谱系记录有效期')
assert.equal((await listRecentChatImageGenerations(client, {
  conversationId: sourceConversation.id,
  systemAccountId: 'lineage-owner',
  now: '2026-08-20T00:00:00.000Z',
  limit: 12
})).some((item) => item.assetId === source.id), true, '父图资产未过期时，谱系记录也必须继续出现在上下文索引中')

const reuseNow = '2026-08-20T00:00:00.000Z'
insertUserMessage(database, sourceConversation.id, 'reuse-turn', 'reuse-message', 3, reuseNow)
await commitChatAssetsToMessage(client, {
  assetIds: [edited.id],
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id,
  messageId: 'reuse-message',
  now: reuseNow,
  retentionDays: 30
})
const reuseExpiresAt = '2026-09-19T00:00:00.000Z'
assert.equal((await getChatAsset(client, {
  assetId: source.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.expiresAt, reuseExpiresAt, '复用编辑后的子图时必须同步续期根图资产，避免根图清理级联删除子图谱系')
assert.equal((await getChatImageGeneration(client, {
  assetId: source.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.expiresAt, reuseExpiresAt, '复用编辑后的子图时必须同步续期根图谱系记录')

insertAssistantMessage(database, sourceConversation.id, 'secondary-root-turn', 'secondary-root-message', 4, now)
const secondaryRoot = await commitChatGeneratedAsset(client, generatedCommitInput({
  id: secondaryRootAssetId,
  conversationId: sourceConversation.id,
  turnId: 'secondary-root-turn',
  messageId: 'secondary-root-message',
  contentOrder: 0,
  now,
  generation: {
    operation: 'generate',
    model: 'gpt-image-2',
    prompt: '生成第二张基础图',
    sourceAssetIds: [],
    size: '1024x1024',
    quality: 'auto',
    outputFormat: 'webp'
  }
}))
insertAssistantMessage(database, sourceConversation.id, 'secondary-edit-turn', 'secondary-edit-message', 5, editNow)
const secondaryEdited = await commitChatGeneratedAsset(client, generatedCommitInput({
  id: secondaryEditedAssetId,
  conversationId: sourceConversation.id,
  turnId: 'secondary-edit-turn',
  messageId: 'secondary-edit-message',
  contentOrder: 0,
  now: editNow,
  generation: {
    operation: 'edit',
    model: 'gpt-image-2',
    prompt: '编辑第二张基础图',
    sourceAssetIds: [secondaryRoot.id],
    size: '1024x1024',
    quality: 'auto',
    outputFormat: 'webp'
  }
}))
insertAssistantMessage(database, sourceConversation.id, 'multi-edit-turn', 'multi-edit-message', 6, reuseNow)
await commitChatGeneratedAsset(client, generatedCommitInput({
  id: multiSourceEditedAssetId,
  conversationId: sourceConversation.id,
  turnId: 'multi-edit-turn',
  messageId: 'multi-edit-message',
  contentOrder: 0,
  now: reuseNow,
  generation: {
    operation: 'edit',
    model: 'gpt-image-2',
    prompt: '同时参考两条独立谱系继续编辑',
    sourceAssetIds: [edited.id, secondaryEdited.id],
    size: '1024x1024',
    quality: 'auto',
    outputFormat: 'webp'
  }
}))
assert.equal((await getChatAsset(client, {
  assetId: secondaryRoot.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.expiresAt, reuseExpiresAt, '多图编辑必须续期每个来源各自的根资产，而不只是第一来源的根图')
assert.equal((await getChatImageGeneration(client, {
  assetId: secondaryRoot.id,
  systemAccountId: 'lineage-owner',
  conversationId: sourceConversation.id
}))?.expiresAt, reuseExpiresAt, '多图编辑必须续期每个来源根图的谱系记录')

const otherConversation = await createChatConversation(client, {
  id: 'lineage-other-conversation',
  systemAccountId: 'lineage-owner',
  apiKeyId: 'lineage-key',
  apiKeyNameSnapshot: 'Lineage Key',
  now,
  maxConversationsPerUser: 50
})
insertAssistantMessage(database, otherConversation.id, 'cross-turn', 'cross-message', 1, editNow)
await assert.rejects(commitChatGeneratedAsset(client, generatedCommitInput({
  id: crossEditAssetId,
  conversationId: otherConversation.id,
  turnId: 'cross-turn',
  messageId: 'cross-message',
  contentOrder: 0,
  now: editNow,
  generation: {
    operation: 'edit',
    model: 'gpt-image-2',
    prompt: '跨会话编辑',
    sourceAssetIds: [source.id],
    size: '1024x1024',
    quality: 'auto',
    outputFormat: 'webp'
  }
})), /引用图片不存在、已过期或不属于当前会话/)
assert.equal(await getChatAsset(client, {
  assetId: crossEditAssetId,
  systemAccountId: 'lineage-owner',
  conversationId: otherConversation.id
}), undefined, '谱系校验失败必须回滚生成资产')

function insertAssistantMessage(database: DatabaseSync, conversationId: string, turnId: string, messageId: string, sequenceNo: number, createdAt: string): void {
  database.prepare(`
    INSERT INTO chat_messages (
      id, conversation_id, system_account_id, turn_id, sequence_no, role, status,
      content_text, content_blocks_json, content_bytes, storage_reserved_bytes,
      model, created_at, completed_at, expires_at
    ) VALUES (?, ?, 'lineage-owner', ?, ?, 'assistant', 'completed', '', '[]', 0, 0, 'gpt-5.5', ?, ?, '2026-09-30T00:00:00.000Z')
  `).run(messageId, conversationId, turnId, sequenceNo, createdAt, createdAt)
}

function insertUserMessage(database: DatabaseSync, conversationId: string, turnId: string, messageId: string, sequenceNo: number, createdAt: string): void {
  database.prepare(`
    INSERT INTO chat_messages (
      id, conversation_id, system_account_id, turn_id, client_message_id, sequence_no, role, status,
      content_text, content_blocks_json, content_bytes, storage_reserved_bytes,
      model, created_at, completed_at, expires_at
    ) VALUES (?, ?, 'lineage-owner', ?, ?, ?, 'user', 'completed', '继续编辑', '[]', 12, 0, 'gpt-5.5', ?, ?, '2026-09-30T00:00:00.000Z')
  `).run(messageId, conversationId, turnId, `client-${messageId}`, sequenceNo, createdAt, createdAt)
}

function generatedCommitInput(input: {
  id: string
  conversationId: string
  turnId: string
  messageId: string
  contentOrder: number
  now: string
  generation: {
    operation: 'generate' | 'edit'
    model: 'gpt-image-2'
    prompt: string
    sourceAssetIds: string[]
    size: string
    quality: string
    outputFormat: string
  }
}) {
  return {
    ...input,
    systemAccountId: 'lineage-owner',
    mimeType: 'image/webp' as const,
    width: 1024,
    height: 1024,
    bytes: 128,
    sha256: input.id === sourceAssetId ? 'a'.repeat(64) : input.id === editedAssetId ? 'b'.repeat(64) : 'c'.repeat(64),
    storageKey: `objects/${input.id}-original.webp`,
    previewMimeType: 'image/webp' as const,
    previewWidth: 640,
    previewHeight: 640,
    previewBytes: 64,
    previewSha256: input.id === sourceAssetId ? 'd'.repeat(64) : input.id === editedAssetId ? 'e'.repeat(64) : 'f'.repeat(64),
    previewStorageKey: `objects/${input.id}-preview.webp`,
    retentionDays: 30
  }
}

console.log('AI 问答图像模型注册表与谱系回归通过')
