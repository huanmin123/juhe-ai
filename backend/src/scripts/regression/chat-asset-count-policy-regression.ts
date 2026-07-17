import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createChatAsset } from '../../storage/chat-assets.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { createChatConversation } from '../../storage/chat.repository.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const systemAccountId = 'asset_count_owner'
const conversationId = 'asset_count_conversation'
const now = '2026-07-17T01:00:00.000Z'

await createChatConversation(client, {
  id: conversationId,
  systemAccountId,
  apiKeyId: 'asset_count_key',
  apiKeyNameSnapshot: '图片数量门禁',
  maxConversationsPerUser: 50,
  now
})

for (let index = 0; index < 5; index += 1) {
  await createChatAsset(client, {
    id: `chat_asset_${String(index).padStart(32, '0')}`,
    systemAccountId,
    conversationId,
    originalFilename: `${index}.png`,
    originalMimeType: 'image/png',
    originalWidth: 10,
    originalHeight: 10,
    originalBytes: 100,
    originalSha256: String(index).padStart(64, 'a'),
    quotaBytes: 80,
    now,
    retentionDays: 3
  })
}

await assert.rejects(
  createChatAsset(client, {
    id: `chat_asset_${'f'.repeat(32)}`,
    systemAccountId,
    conversationId,
    originalFilename: 'sixth.png',
    originalMimeType: 'image/png',
    originalWidth: 10,
    originalHeight: 10,
    originalBytes: 100,
    originalSha256: 'f'.repeat(64),
    quotaBytes: 80,
    now,
    retentionDays: 3
  }),
  (error: unknown) => error instanceof Error && error.name === 'ChatAssetCountExceededError',
  '同一会话已有 5 张未绑定草稿图时，第 6 张必须在创建记录前被拒绝'
)

const total = database.prepare('SELECT COUNT(*) AS total FROM chat_assets WHERE conversation_id = ?').get(conversationId) as { total: number }
assert.equal(total.total, 5, '图片数量超限不能留下额外 pending/failed 资产或占用配额')
database.close()

console.log('AI 问答单消息最多 5 张图片的后端资产创建门禁回归通过')
