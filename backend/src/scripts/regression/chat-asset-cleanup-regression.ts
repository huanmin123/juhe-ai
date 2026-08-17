import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
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
  await assert.rejects(
    cleanupExpiredChatAssets({ client, now: '2026-07-04T00:00:00.000', limit: 1 }),
    /聊天资产清理 now必须是带 Z 或数值 offset 的 RFC3339 时间/,
    '聊天资产清理不得按本机时区解释裸 now'
  )
  const ownerId = 'asset_cleanup_owner'
  const conversationId = 'asset_cleanup_conversation'
  const createdAt = '2026-07-01T00:00:00.000Z'
  const expiresAt = '2026-07-04T00:00:00.000Z'
  await chatRepository.createChatConversation(client, {
    id: conversationId,
    systemAccountId: ownerId,
    apiKeyId: 'asset_cleanup_key',
    apiKeyNameSnapshot: '资产清理测试', maxConversationsPerUser: 1000,
    now: createdAt
  })

  const successful = await createReadyAsset('chat_asset_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'aa/bb/success.png', Buffer.from('successful-object'))
  assert.equal(await exists(successful.storageKey!), true)
  const successCleanup = await cleanupExpiredChatAssets({ client, now: expiresAt, limit: 10 })
  assert.deepEqual([successCleanup.claimedAssets, successCleanup.deletedAssets, successCleanup.failedAssets], [1, 1, 0])
  assert.equal(await exists(successful.storageKey!), false, '成功清理必须同时删除对象文件')
  assert.equal(await assetRepository.getChatAsset(client, { assetId: successful.id, systemAccountId: ownerId, conversationId }), undefined)
  assert.equal(database.prepare('SELECT 1 FROM chat_user_asset_usage WHERE system_account_id = ?').get(ownerId), undefined, '资产删除必须同步扣减预聚合用量')

  const failed = await createReadyAsset('chat_asset_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'cc/dd/failure.png', Buffer.from('failure-object'))
  rmSync(assetStorage.chatAssetObjectPath(failed.storageKey!), { force: true })
  mkdirSync(assetStorage.chatAssetObjectPath(failed.storageKey!), { recursive: true })
  const failedCleanup = await cleanupExpiredChatAssets({ client, now: '2026-07-04T09:00:00.000+09:00', limit: 10 })
  assert.deepEqual([failedCleanup.claimedAssets, failedCleanup.deletedAssets, failedCleanup.failedAssets], [1, 0, 1])
  const failedRow = database.prepare('SELECT cleanup_status, cleanup_attempt_count, cleanup_retry_at FROM chat_assets WHERE id = ?').get(failed.id) as Record<string, unknown>
  assert.equal(failedRow.cleanup_status, 'failed')
  assert.equal(Number(failedRow.cleanup_attempt_count), 1)
  assert.equal(failedRow.cleanup_retry_at, '2026-07-04T00:01:00.000Z', '清理 retryAt 必须 canonical 为 UTC')
  rmSync(assetStorage.chatAssetObjectPath(failed.storageKey!), { recursive: true, force: true })
  const retryCleanup = await cleanupExpiredChatAssets({ client, now: '2026-07-08T00:01:01.000Z', limit: 10 })
  assert.deepEqual([retryCleanup.deletedAssets, retryCleanup.failedAssets], [1, 0], '退避到期后必须重试并收口 DB 行')

  const stale = await createReadyAsset('chat_asset_cccccccccccccccccccccccccccccccc', 'ee/ff/stale.png', Buffer.from('stale-object'))
  const staleClaim = await assetRepository.claimExpiredChatAssetsForCleanup(client, { now: expiresAt, limit: 10 })
  assert.deepEqual(staleClaim.assets.map((asset) => asset.id), [stale.id])
  const staleCleanup = await cleanupExpiredChatAssets({ client, now: '2026-07-08T00:16:01.000Z', limit: 10 })
  assert.equal(staleCleanup.deletedAssets, 1, '超过 15 分钟的清理认领必须被重新认领并完成')

  const quotaOwnerId = 'asset_quota_owner'
  const quotaConversationId = 'asset_quota_conversation'
  await chatRepository.createChatConversation(client, { id: quotaConversationId, systemAccountId: quotaOwnerId, apiKeyId: 'quota_key', apiKeyNameSnapshot: '资产配额测试', maxConversationsPerUser: 1000, now: createdAt })
  database.prepare('INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at) VALUES (?, ?, ?, ?)')
    .run(quotaOwnerId, assetRepository.chatAssetUserMaxBytes - 8, 1, createdAt)
  await assert.rejects(assetRepository.createChatAsset(client, {
    id: 'chat_asset_dddddddddddddddddddddddddddddddd',
    systemAccountId: quotaOwnerId,
    conversationId: quotaConversationId,
    sourceKind: 'user_upload',
    originalFilename: 'quota.png',
    originalMimeType: 'image/png',
    originalBytes: 16,
    originalSha256: 'd'.repeat(64),
    quotaBytes: 16, retentionDays: 7,
    now: createdAt
  }), (error) => error instanceof assetRepository.ChatAssetQuotaExceededError, '未提交资产也必须受用户预聚合配额限制')

  const committed = await createReadyAsset('chat_asset_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'gg/hh/committed.png', Buffer.from('committed-object'))
  const committedTurn = await chatRepository.acceptChatTurn(client, {
    conversationId, systemAccountId: ownerId, clientMessageId: 'asset_retention_commit', userContent: '[图片]',
    contentBlocks: [{ type: 'input_image', assetId: committed.id }], model: 'mock-model',
    now: '2026-07-01T00:10:00.000Z', storageQuotaBytes: 1024 * 1024 ,
    retentionDays: 3, maxTurnsPerConversation: 50
  })
  assert.equal((await assetRepository.getChatAsset(client, { assetId: committed.id, systemAccountId: ownerId, conversationId }))?.expiresAt, '2026-07-04T00:10:00.000Z', '资产绑定消息时必须按统一配置刷新保留期')
  await chatRepository.cancelChatTurn(client, {
    conversationId, systemAccountId: ownerId, turnId: committedTurn.turnId,
    assistantContent: '', traceId: 'trace_asset_retention', now: '2026-07-01T00:11:00.000Z'
  })
  const clearNow = '2026-07-01T00:12:00.000Z'
  assert.ok(await chatRepository.clearChatConversation(client, { conversationId, systemAccountId: ownerId, now: clearNow }))
  assert.equal((await assetRepository.getChatAsset(client, { assetId: committed.id, systemAccountId: ownerId, conversationId }))?.expiresAt, clearNow, '清空会话必须让已绑定资产立即进入过期队列')
  assert.equal(await exists(committed.storageKey!), true, '清空请求不得直接删除对象文件')
  const clearedAssetCleanup = await cleanupExpiredChatAssets({ client, now: clearNow, limit: 10 })
  assert.deepEqual([clearedAssetCleanup.claimedAssets, clearedAssetCleanup.deletedAssets, clearedAssetCleanup.failedAssets], [1, 1, 0])
  assert.equal(await exists(committed.storageKey!), false, '后台清理应收口清空会话的过期资产')

  await assert.rejects(
    () => cleanupExpiredChatAssets({ client, now: '2026-07-08T00:16:01.000', limit: 10 }),
    /聊天资产清理 now必须是带 Z 或数值 offset 的 RFC3339 时间/,
    '聊天资产清理 supplied bare now 必须显式失败'
  )

  const cleanupSource = readFileSync(new URL('../../modules/chat/chat-asset-cleanup.ts', import.meta.url), 'utf8')
  const chatRoutesSource = readFileSync(new URL('../../modules/chat/chat.routes.ts', import.meta.url), 'utf8')
  const deleteRouteStart = chatRoutesSource.indexOf("chatRouter.delete('/conversations/:conversationId/assets/:assetId'")
  const deleteRouteEnd = chatRoutesSource.indexOf("\nchatRouter.get('/conversations/:conversationId/models'", deleteRouteStart)
  assert.ok(deleteRouteStart >= 0 && deleteRouteEnd > deleteRouteStart, '必须能定位聊天资产删除路由')
  const deleteRouteSource = chatRoutesSource.slice(deleteRouteStart, deleteRouteEnd)
  assert.doesNotMatch(cleanupSource, /Date\.parse\(/, '聊天资产后台清理不得按进程时区解析 now')
  assert.match(cleanupSource, /requiredRfc3339Instant\(input\.now, '聊天资产清理 now'\)/, '聊天资产后台清理 now 必须严格 canonical')
  assert.doesNotMatch(deleteRouteSource, /retryAt: new Date\(Date\.parse\(now\)/, '聊天资产删除路由不得重新宽松解析内部 now')
  assert.match(deleteRouteSource, /const nowMs = Date\.now\(\)[\s\S]*retryAt: new Date\(nowMs \+ 60_000\)\.toISOString\(\)/, '聊天资产删除路由必须从同一 epoch 生成 retryAt')

  async function createReadyAsset(id: string, storageKey: string, bytes: Buffer) {
    const sha256 = createHash('sha256').update(bytes).digest('hex')
    await assetRepository.createChatAsset(client, {
      id,
      systemAccountId: ownerId,
      conversationId,
      sourceKind: 'user_upload',
      originalFilename: `${id}.png`,
      originalMimeType: 'image/png',
      originalBytes: bytes.length,
      originalSha256: sha256,
      quotaBytes: bytes.length,
      now: createdAt,
      retentionDays: 3
    })
    await assetStorage.writeChatAssetObject({ storageKey, source: Readable.from(bytes), expectedBytes: bytes.length, expectedSha256: sha256 })
    return assetRepository.completeChatAssetProcessing(client, {
      assetId: id,
      systemAccountId: ownerId,
      conversationId,
      processedMimeType: 'image/jpeg',
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
