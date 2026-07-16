import assert from 'node:assert/strict'
import { readdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { PassThrough } from 'node:stream'
import { DatabaseSync } from 'node:sqlite'
import type { Request } from 'express'

import { uploadChatAsset, ChatAssetUploadError } from '../../modules/chat/chat-asset-upload.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { createChatConversation } from '../../storage/chat.repository.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const conversationId = 'upload_abort_conversation'
const systemAccountId = 'upload_abort_owner'
await createChatConversation(client, {
  id: conversationId,
  systemAccountId,
  apiKeyId: 'upload_abort_key',
  apiKeyNameSnapshot: '上传中断测试', maxConversationsPerUser: 1000,
  now: '2026-07-14T02:00:00.000Z'
})

const before = new Set((await readdir(tmpdir())).filter((name) => name.startsWith('juhe-ai-chat-upload-')))
const boundary = '----juhe-ai-upload-abort-boundary'
const requestStream = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
requestStream.headers = { 'content-type': `multipart/form-data; boundary=${boundary}` }
requestStream.aborted = false
requestStream.on('error', () => undefined)
const upload = uploadChatAsset({
  req: requestStream as Request,
  client,
  systemAccountId,
  conversationId,
  now: '2026-07-14T02:01:00.000Z',
  retentionDays: 7
})
requestStream.write(`--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="partial.png"\r\nContent-Type: image/png\r\n\r\n`)
requestStream.write(Buffer.from([0x89, 0x50, 0x4e, 0x47]))
requestStream.aborted = true
requestStream.destroy(Object.assign(new Error('socket aborted'), { code: 'ECONNRESET' }))

await assert.rejects(
  Promise.race([
    upload,
    new Promise((_resolve, reject) => setTimeout(() => reject(new Error('upload_abort_timeout')), 2_000))
  ]),
  (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断',
  '客户端中断 multipart 后必须及时拒绝，不能永久等待 Busboy finish'
)
await new Promise((resolve) => setTimeout(resolve, 20))
const leaked = (await readdir(tmpdir())).filter((name) => name.startsWith('juhe-ai-chat-upload-') && !before.has(name))
assert.deepEqual(leaked, [], '中断上传必须执行 finally 并清理临时目录')
const assetCount = database.prepare('SELECT COUNT(*) AS total FROM chat_assets').get() as { total?: unknown } | undefined
assert.equal(Number(assetCount?.total ?? -1), 0)

database.close()
console.log('AI 问答 multipart 中断上传清理回归通过')
