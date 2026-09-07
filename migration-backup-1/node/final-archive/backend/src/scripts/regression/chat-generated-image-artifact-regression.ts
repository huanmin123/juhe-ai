import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import sharp from 'sharp'

const root = mkdtempSync(join(tmpdir(), 'juhe-chat-generated-artifact-'))
process.env.JUHE_AI_CHAT_ASSETS_ROOT = join(root, 'objects')

const [schema, databaseClient, chatRepository, assetRepository, assetStorage, artifactModule, cleanupModule] = await Promise.all([
  import('../../storage/schema.js'),
  import('../../storage/database-client.js'),
  import('../../storage/chat.repository.js'),
  import('../../storage/chat-assets.repository.js'),
  import('../../storage/chat-asset-storage.js'),
  import('../../modules/chat/tools/artifact-sink.js'),
  import('../../modules/chat/chat-asset-cleanup.js')
])

const database = new DatabaseSync(':memory:')
try {
  schema.applyChatSchema(database)
  const client = databaseClient.createSqliteDatabaseClient(database)
  const now = '2026-07-20T00:00:00.000Z'
  const conversation = await chatRepository.createChatConversation(client, {
    id: 'chat_generated_artifact_conversation', systemAccountId: 'artifact_owner', apiKeyId: 'artifact_key',
    apiKeyNameSnapshot: 'Artifact Key', maxConversationsPerUser: 10, now
  })
  const turn = await chatRepository.acceptChatTurn(client, {
    conversationId: conversation.id, systemAccountId: 'artifact_owner', clientMessageId: 'artifact_client_message',
    userContent: '生成图片', model: 'gpt-5.6', now, storageQuotaBytes: 8 * 1024 * 1024,
    retentionDays: 3, maxTurnsPerConversation: 10
  })
  const sourcePath = join(root, 'source.webp')
  const source = await sharp({ create: { width: 1600, height: 900, channels: 3, background: { r: 40, g: 100, b: 180 } } }).webp({ quality: 92 }).toBuffer()
  await writeFile(sourcePath, source)
  const sink = artifactModule.createChatGeneratedImageArtifactSink({
    client, systemAccountId: 'artifact_owner', conversationId: conversation.id,
    turnId: turn.turnId, messageId: turn.assistantMessage.id, retentionDays: 3,
    nextContentOrder: () => 1, now: () => now
  })
  const committed = await sink.commitGeneratedImage({
    result: {
      path: sourcePath, bytes: source.byteLength, sha256: createHash('sha256').update(source).digest('hex'),
      mimeType: 'image/webp', width: 1600, height: 900
    },
    generation: {
      operation: 'generate', model: 'gpt-image-2', prompt: '生成验收图', sourceAssetIds: [],
      size: '1536x1024', quality: 'auto', outputFormat: 'webp'
    }
  })
  const record = await assetRepository.getChatAsset(client, {
    assetId: committed.assetId, systemAccountId: 'artifact_owner', conversationId: conversation.id, now
  })
  assert(record?.storageKey)
  assert(record?.previewStorageKey)
  assert.notEqual(record?.storageKey, record?.previewStorageKey)
  assert.equal(record?.previewMimeType, 'image/webp')
  assert((record?.previewWidth ?? 0) <= 640)
  assert((record?.previewBytes ?? Infinity) <= 512 * 1024)
  assert.equal(record?.quotaBytes, record!.processedBytes! + record!.previewBytes!)

  const originalObject = await assetStorage.openChatGeneratedAssetObject(record!.storageKey!)
  const previewObject = await assetStorage.openChatAssetObject(record!.previewStorageKey!, assetStorage.chatAssetPreviewMaxBytes)
  originalObject.stream.destroy()
  previewObject.stream.destroy()

  database.prepare("UPDATE chat_assets SET expires_at = ? WHERE id = ?").run(now, committed.assetId)
  const cleanup = await cleanupModule.cleanupExpiredChatAssets({ client, now, limit: 10 })
  assert.equal(cleanup.deletedAssets, 1)
  await assert.rejects(assetStorage.openChatGeneratedAssetObject(record!.storageKey!))
  await assert.rejects(assetStorage.openChatAssetObject(record!.previewStorageKey!))
} finally {
  database.close()
  rmSync(root, { recursive: true, force: true })
}

console.log('AI 问答生成图片双变体 Artifact Sink 回归通过')
