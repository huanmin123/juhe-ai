import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Readable } from 'node:stream'
import { DatabaseSync } from 'node:sqlite'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-chat-asset-cleanup-'))
process.env.JUHE_AI_CHAT_ASSETS_ROOT = root

const [{ createSqliteDatabaseClient }, { applyChatSchema }, chatRepository, assetRepository, assetStorage, { cleanupExpiredChatAssets }] = await Promise.all([
  import('../../storage/database-client.js'),
  import('../../storage/schema.js'),
  import('../../storage/chat.repository.js'),
  import('../../storage/chat-assets.repository.js'),
  import('../../storage/chat-asset-storage.js'),
  import('../../modules/chat/chat-asset-cleanup.js')
])

const database = new DatabaseSync(':memory:')
try {
  applyChatSchema(database)
  const client = createSqliteDatabaseClient(database)
  const ownerId = 'asset_cleanup_owner'
  const conversationId = 'asset_cleanup_conversation'
  const createdAt = '2026-07-01T00:00:00.000Z'
  const expiresAt = '2026-07-08T00:00:00.000Z'
  await chatRepository.createChatConversation(client, {
    id: conversationId,
    systemAccountId: ownerId,
    apiKeyId: 'asset_cleanup_key',
    apiKeyNameSnapshot: '资产清理测试',
    now: createdAt
  })

  const successful = await createReadyAsset('chat_asset_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'aa/bb/success.png', Buffer.from('successful-object'))
  assert.equal(await exists(successful.storageKey!), true)
  const successCleanup = await cleanupExpiredChatAssets({ client, now: expiresAt, limit: 10 })
  assert.deepEqual([successCleanup.claimedAssets, successCleanup.deletedAssets, successCleanup.failedAssets], [1, 1, 0])
  assert.equal(await exists(successful.storageKey!), false, '成功清理必须同时删除对象文件')
  assert.equal(await assetRepository.getChatAsset(client, { assetId: successful.id, systemAccountId: ownerId, conversationId }), undefined)

  const failed = await createReadyAsset('chat_asset_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'cc/dd/failure.png', Buffer.from('failure-object'))
  rmSync(assetStorage.chatAssetObjectPath(failed.storageKey!), { force: true })
  mkdirSync(assetStorage.chatAssetObjectPath(failed.storageKey!), { recursive: true })
  const failedCleanup = await cleanupExpiredChatAssets({ client, now: expiresAt, limit: 10 })
  assert.deepEqual([failedCleanup.claimedAssets, failedCleanup.deletedAssets, failedCleanup.failedAssets], [1, 0, 1])
  const failedRow = database.prepare('SELECT cleanup_status, cleanup_attempt_count, cleanup_retry_at FROM chat_assets WHERE id = ?').get(failed.id) as Record<string, unknown>
  assert.equal(failedRow.cleanup_status, 'failed')
  assert.equal(Number(failedRow.cleanup_attempt_count), 1)
  rmSync(assetStorage.chatAssetObjectPath(failed.storageKey!), { recursive: true, force: true })
  const retryCleanup = await cleanupExpiredChatAssets({ client, now: '2026-07-08T00:01:01.000Z', limit: 10 })
  assert.deepEqual([retryCleanup.deletedAssets, retryCleanup.failedAssets], [1, 0], '退避到期后必须重试并收口 DB 行')

  const stale = await createReadyAsset('chat_asset_cccccccccccccccccccccccccccccccc', 'ee/ff/stale.png', Buffer.from('stale-object'))
  const staleClaim = await assetRepository.claimExpiredChatAssetsForCleanup(client, { now: expiresAt, limit: 10 })
  assert.deepEqual(staleClaim.assets.map((asset) => asset.id), [stale.id])
  const staleCleanup = await cleanupExpiredChatAssets({ client, now: '2026-07-08T00:16:01.000Z', limit: 10 })
  assert.equal(staleCleanup.deletedAssets, 1, '超过 15 分钟的清理认领必须被重新认领并完成')

  async function createReadyAsset(id: string, storageKey: string, bytes: Buffer) {
    const sha256 = createHash('sha256').update(bytes).digest('hex')
    await assetRepository.createChatAsset(client, {
      id,
      systemAccountId: ownerId,
      conversationId,
      originalFilename: `${id}.png`,
      originalMimeType: 'image/png',
      originalBytes: bytes.length,
      originalSha256: sha256,
      now: createdAt
    })
    await assetStorage.writeChatAssetObject({ storageKey, source: Readable.from(bytes), expectedBytes: bytes.length, expectedSha256: sha256 })
    return assetRepository.completeChatAssetProcessing(client, {
      assetId: id,
      systemAccountId: ownerId,
      conversationId,
      processedMimeType: 'image/png',
      processedWidth: 16,
      processedHeight: 16,
      processedBytes: bytes.length,
      processedSha256: sha256,
      storageKey,
      now: createdAt
    })
  }

  async function exists(storageKey: string): Promise<boolean> {
    try { const opened = await assetStorage.openChatAssetObject(storageKey); opened.stream.destroy(); return true } catch { return false }
  }
} finally {
  database.close()
  rmSync(root, { recursive: true, force: true })
}

console.log('AI 问答图片资产清理、退避重试与 stale claim 回归通过')
