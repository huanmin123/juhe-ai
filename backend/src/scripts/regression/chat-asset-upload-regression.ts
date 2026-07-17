import assert from 'node:assert/strict'
import { readdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { PassThrough } from 'node:stream'
import { DatabaseSync } from 'node:sqlite'
import type { Request } from 'express'
import sharp from 'sharp'

import { ChatAssetUploadError, ChatAssetUploadSlotReservations, uploadChatAsset } from '../../modules/chat/chat-asset-upload.js'
import { createChatAsset } from '../../storage/chat-assets.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { createChatConversation } from '../../storage/chat.repository.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const conversationId = 'upload_abort_conversation'
const systemAccountId = 'upload_abort_owner'

const reservationState = new ChatAssetUploadSlotReservations()
const reservationA = reservationState.reserve(systemAccountId, conversationId, 3)
const reservationB = reservationState.reserve(systemAccountId, conversationId, 3)
assert.ok(reservationA && reservationB, '已有 2 张资产时 A/B 应能各预占一个剩余槽位')
reservationA.transferToDatabase()
const reservationC = reservationState.reserve(systemAccountId, conversationId, 2)
assert.ok(reservationC, 'A 已落库并转移 reservation 后，B 尚未落库时 C 仍应能占用合法第 5 个总槽位')
assert.equal(
  reservationState.reserve(systemAccountId, conversationId, 2),
  undefined,
  'DB 已有 3 张且 B/C 共预占 2 个槽位时不能再突破总上限 5'
)
reservationA.release()
reservationB.release()
reservationC.release()
const reservationAfterCleanup = reservationState.reserve(systemAccountId, conversationId, 3)
assert.ok(reservationAfterCleanup, '数据库转移与失败清理都必须幂等释放 reservation')
reservationAfterCleanup.release()

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
  withTimeout(upload, 2_000, 'upload_abort_timeout'),
  (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断',
  '客户端中断 multipart 后必须及时拒绝，不能永久等待 Busboy finish'
)
await new Promise((resolve) => setTimeout(resolve, 20))
const leaked = (await readdir(tmpdir())).filter((name) => name.startsWith('juhe-ai-chat-upload-') && !before.has(name))
assert.deepEqual(leaked, [], '中断上传必须执行 finally 并清理临时目录')
const assetCount = database.prepare('SELECT COUNT(*) AS total FROM chat_assets').get() as { total?: unknown } | undefined
assert.equal(Number(assetCount?.total ?? -1), 0)

const validPng = await sharp({ create: { width: 2, height: 2, channels: 4, background: { r: 255, g: 0, b: 0, alpha: 1 } } }).png().toBuffer()
const multipleBoundary = '----juhe-ai-upload-multiple-boundary'
const multipleRequest = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
multipleRequest.headers = { 'content-type': `multipart/form-data; boundary=${multipleBoundary}` }
multipleRequest.aborted = false
const multipleUpload = uploadChatAsset({
  req: multipleRequest as Request,
  client,
  systemAccountId,
  conversationId,
  now: '2026-07-14T02:02:00.000Z',
  retentionDays: 7
})
for (const filename of ['first.png', 'second.png']) {
  multipleRequest.write(`--${multipleBoundary}\r\nContent-Disposition: form-data; name="file"; filename="${filename}"\r\nContent-Type: image/png\r\n\r\n`)
  multipleRequest.write(validPng)
  multipleRequest.write('\r\n')
}
multipleRequest.end(`--${multipleBoundary}--\r\n`)
await assert.rejects(
  multipleUpload,
  (error) => error instanceof ChatAssetUploadError && error.message === '每次只能上传一张图片',
  'multipart 伪造多个 file 字段必须明确拒绝'
)
const multipleAssetCount = database.prepare('SELECT COUNT(*) AS total FROM chat_assets').get() as { total?: unknown } | undefined
assert.equal(Number(multipleAssetCount?.total ?? -1), 0, 'multipart 多文件超限不得创建或占用任何资产记录')

const oversizedBoundary = '----juhe-ai-upload-oversized-boundary'
const oversizedRequest = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
oversizedRequest.headers = { 'content-type': `multipart/form-data; boundary=${oversizedBoundary}` }
oversizedRequest.aborted = false
const oversizedUpload = uploadChatAsset({
  req: oversizedRequest as Request,
  client,
  systemAccountId,
  conversationId,
  now: '2026-07-14T02:02:30.000Z',
  retentionDays: 7
})
oversizedRequest.write(`--${oversizedBoundary}\r\nContent-Disposition: form-data; name="file"; filename="oversized.jpg"\r\nContent-Type: image/jpeg\r\n\r\n`)
oversizedRequest.write(Buffer.alloc(1024 * 1024 + 1, 0x41))
oversizedRequest.end(`\r\n--${oversizedBoundary}--\r\n`)
await assert.rejects(
  oversizedUpload,
  (error) => error instanceof ChatAssetUploadError && error.code === 'chat_asset_too_large',
  '绕过前端时后端必须在 multipart 读取阶段拒绝超过 1 MiB 的单图'
)
assert.equal(Number((database.prepare('SELECT COUNT(*) AS total FROM chat_assets').get() as { total?: unknown })?.total ?? -1), 0)

for (let index = 0; index < 4; index += 1) {
  await createChatAsset(client, {
    id: `chat_asset_${String(index).padStart(32, '0')}`,
    systemAccountId,
    conversationId,
    originalFilename: `${index}.png`,
    originalMimeType: 'image/png',
    originalWidth: 2,
    originalHeight: 2,
    originalBytes: validPng.byteLength,
    originalSha256: String(index).padStart(64, 'a'),
    quotaBytes: validPng.byteLength,
    now: '2026-07-14T02:03:00.000Z',
    retentionDays: 7
  })
}

const reservedBoundary = '----juhe-ai-upload-reserved-boundary'
const reservedRequest = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
reservedRequest.headers = { 'content-type': `multipart/form-data; boundary=${reservedBoundary}` }
reservedRequest.aborted = false
reservedRequest.on('error', () => undefined)
const reservedUpload = uploadChatAsset({
  req: reservedRequest as Request,
  client,
  systemAccountId,
  conversationId,
  now: '2026-07-14T02:03:30.000Z',
  retentionDays: 7
})
await waitForRequestBodyReader(reservedRequest)

const concurrentBoundary = '----juhe-ai-upload-concurrent-boundary'
const concurrentRequest = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
concurrentRequest.headers = { 'content-type': `multipart/form-data; boundary=${concurrentBoundary}` }
concurrentRequest.aborted = false
concurrentRequest.on('error', () => undefined)
const concurrentUpload = uploadChatAsset({
  req: concurrentRequest as Request,
  client,
  systemAccountId,
  conversationId,
  now: '2026-07-14T02:03:31.000Z',
  retentionDays: 7
})
try {
  await assert.rejects(
    withTimeout(concurrentUpload, 250, 'concurrent_asset_slot_timeout'),
    (error) => error instanceof ChatAssetUploadError && error.code === 'chat_asset_count_exceeded',
    '已有 4 张草稿且首个上传持有槽位时，并发上传必须在读取完整请求体前快速拒绝'
  )
  assert.notEqual(concurrentRequest.readableFlowing, true, '槽位不足的并发请求不能开始消费 multipart 请求体')
} finally {
  concurrentRequest.aborted = true
  concurrentRequest.destroy()
  reservedRequest.aborted = true
  reservedRequest.destroy()
  await Promise.allSettled([concurrentUpload, reservedUpload])
}
await assert.rejects(
  reservedUpload,
  (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断',
  '终止持有槽位的上传必须正常走中断清理'
)

const releasedBoundary = '----juhe-ai-upload-released-boundary'
const releasedRequest = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
releasedRequest.headers = { 'content-type': `multipart/form-data; boundary=${releasedBoundary}` }
releasedRequest.aborted = false
releasedRequest.on('error', () => undefined)
const releasedUpload = uploadChatAsset({
  req: releasedRequest as Request,
  client,
  systemAccountId,
  conversationId,
  now: '2026-07-14T02:03:32.000Z',
  retentionDays: 7
})
const releasedState = await Promise.race([
  waitForRequestBodyReader(releasedRequest).then(() => 'reading' as const),
  releasedUpload.then(() => 'resolved' as const, (error) => {
    if (error instanceof ChatAssetUploadError && error.code === 'chat_asset_count_exceeded') return 'slot_blocked' as const
    throw error
  })
])
assert.equal(releasedState, 'reading', '首个上传终止后必须释放预占槽位，使下一次上传能开始读取请求体')
releasedRequest.aborted = true
releasedRequest.destroy()
await assert.rejects(
  releasedUpload,
  (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断'
)

await createChatAsset(client, {
  id: `chat_asset_${String(4).padStart(32, '0')}`,
  systemAccountId,
  conversationId,
  originalFilename: '4.png',
  originalMimeType: 'image/png',
  originalWidth: 2,
  originalHeight: 2,
  originalBytes: validPng.byteLength,
  originalSha256: String(4).padStart(64, 'a'),
  quotaBytes: validPng.byteLength,
  now: '2026-07-14T02:03:50.000Z',
  retentionDays: 7
})
const blockedRequest = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
blockedRequest.headers = { 'content-type': `multipart/form-data; boundary=${multipleBoundary}` }
blockedRequest.aborted = false
await assert.rejects(
  withTimeout(uploadChatAsset({
    req: blockedRequest as Request,
    client,
    systemAccountId,
    conversationId,
    now: '2026-07-14T02:04:00.000Z',
    retentionDays: 7
  }), 250, 'asset_count_preflight_timeout'),
  (error) => error instanceof ChatAssetUploadError && error.code === 'chat_asset_count_exceeded',
  '已有 5 张草稿时必须在读取第 6 个请求体前拒绝'
)
blockedRequest.destroy()

const wiredConversationId = 'upload_reservation_wiring_conversation'
await createChatConversation(client, {
  id: wiredConversationId,
  systemAccountId,
  apiKeyId: 'upload_abort_key',
  apiKeyNameSnapshot: '上传预占接线测试',
  maxConversationsPerUser: 1000,
  now: '2026-07-14T03:00:00.000Z'
})
for (let index = 0; index < 2; index += 1) {
  await createChatAsset(client, {
    id: `chat_asset_${String(index + 10).padStart(32, '0')}`,
    systemAccountId,
    conversationId: wiredConversationId,
    originalFilename: `wired-${index}.png`,
    originalMimeType: 'image/png',
    originalWidth: 2,
    originalHeight: 2,
    originalBytes: validPng.byteLength,
    originalSha256: String(index + 10).padStart(64, 'a'),
    quotaBytes: validPng.byteLength,
    now: '2026-07-14T03:00:01.000Z',
    retentionDays: 7
  })
}

const wiredTempBefore = new Set((await readdir(tmpdir())).filter((name) => name.startsWith('juhe-ai-chat-upload-')))
const wiredB = pendingMultipartRequest('wired-b')
const wiredBUpload = uploadChatAsset({
  req: wiredB.request as Request,
  client,
  systemAccountId,
  conversationId: wiredConversationId,
  now: '2026-07-14T03:00:02.000Z',
  retentionDays: 7
})
await waitForRequestBodyReader(wiredB.request)

let enterWiredAPause!: () => void
const wiredAPaused = new Promise<void>((resolve) => { enterWiredAPause = resolve })
let rejectWiredAPause!: (error: Error) => void
const releaseWiredAPause = new Promise<void>((_resolve, reject) => { rejectWiredAPause = reject })
const wiredA = pendingMultipartRequest('wired-a')
const wiredAUpload = uploadChatAsset({
  req: wiredA.request as Request,
  client,
  systemAccountId,
  conversationId: wiredConversationId,
  now: '2026-07-14T03:00:03.000Z',
  retentionDays: 7,
  lifecycle: {
    afterAssetTransferredToDatabase: async () => {
      enterWiredAPause()
      await releaseWiredAPause
    }
  }
})
writeMultipartImage(wiredA, validPng, 'wired-a.png')
await withTimeout(wiredAPaused, 2_000, 'wired_a_create_timeout')

const wiredC = pendingMultipartRequest('wired-c')
const wiredCUpload = uploadChatAsset({
  req: wiredC.request as Request,
  client,
  systemAccountId,
  conversationId: wiredConversationId,
  now: '2026-07-14T03:00:04.000Z',
  retentionDays: 7
})
try {
  const wiredCState = await Promise.race([
    waitForRequestBodyReader(wiredC.request).then(() => 'reading' as const),
    wiredCUpload.then(() => 'resolved' as const, (error) => {
      if (error instanceof ChatAssetUploadError && error.code === 'chat_asset_count_exceeded') return 'slot_blocked' as const
      throw error
    })
  ])
  assert.equal(wiredCState, 'reading', 'A 已真实落库并暂停在对象写入前时，C 必须通过 uploadChatAsset 接线取得合法第 5 个槽位')

  const wiredD = pendingMultipartRequest('wired-d')
  try {
    await assert.rejects(
      withTimeout(uploadChatAsset({
        req: wiredD.request as Request,
        client,
        systemAccountId,
        conversationId: wiredConversationId,
        now: '2026-07-14T03:00:05.000Z',
        retentionDays: 7
      }), 250, 'wired_d_preflight_timeout'),
      (error) => error instanceof ChatAssetUploadError && error.code === 'chat_asset_count_exceeded',
      '已有 3 张 DB 资产且 B/C 持有两个 reservation 时，第 6 个真实上传必须拒绝'
    )
    assert.notEqual(wiredD.request.readableFlowing, true)
  } finally {
    wiredD.request.destroy()
  }
} finally {
  wiredB.request.aborted = true
  wiredB.request.destroy()
  wiredC.request.aborted = true
  wiredC.request.destroy()
  rejectWiredAPause(new Error('wired_a_pause_released_for_cleanup'))
  await Promise.allSettled([wiredAUpload, wiredBUpload, wiredCUpload])
}
await assert.rejects(wiredAUpload, /wired_a_pause_released_for_cleanup/)
await assert.rejects(wiredBUpload, (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断')
await assert.rejects(wiredCUpload, (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断')
assert.equal(Number((database.prepare('SELECT COUNT(*) AS total FROM chat_assets WHERE conversation_id = ?').get(wiredConversationId) as { total?: unknown })?.total ?? -1), 2, 'A 受控失败以及 B/C 中断后不得遗留 pending 资产')

const wiredProbe = pendingMultipartRequest('wired-probe')
const wiredProbeUpload = uploadChatAsset({
  req: wiredProbe.request as Request,
  client,
  systemAccountId,
  conversationId: wiredConversationId,
  now: '2026-07-14T03:00:06.000Z',
  retentionDays: 7
})
await waitForRequestBodyReader(wiredProbe.request)
wiredProbe.request.aborted = true
wiredProbe.request.destroy()
await assert.rejects(wiredProbeUpload, (error) => error instanceof ChatAssetUploadError && error.message === '图片上传连接已中断')
await new Promise((resolve) => setTimeout(resolve, 20))
const wiredTempLeaked = (await readdir(tmpdir())).filter((name) => name.startsWith('juhe-ai-chat-upload-') && !wiredTempBefore.has(name))
assert.deepEqual(wiredTempLeaked, [], '真实并发上传完成清理后不得泄漏 reservation 对应的临时目录')

database.close()
console.log('AI 问答 multipart 中断上传清理回归通过')

async function waitForRequestBodyReader(request: PassThrough, timeoutMs = 250): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (request.readableFlowing !== true) {
    if (Date.now() >= deadline) throw new Error('request_body_reader_timeout')
    await new Promise((resolve) => setTimeout(resolve, 1))
  }
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, timeoutCode: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(timeoutCode)), timeoutMs)
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

function pendingMultipartRequest(label: string): {
  request: PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
  boundary: string
} {
  const boundary = `----juhe-ai-upload-${label}-boundary`
  const request = new PassThrough() as PassThrough & Partial<Request> & { headers: Record<string, string>; aborted: boolean }
  request.headers = { 'content-type': `multipart/form-data; boundary=${boundary}` }
  request.aborted = false
  request.on('error', () => undefined)
  return { request, boundary }
}

function writeMultipartImage(
  target: ReturnType<typeof pendingMultipartRequest>,
  contents: Buffer,
  filename: string
): void {
  target.request.write(`--${target.boundary}\r\nContent-Disposition: form-data; name="file"; filename="${filename}"\r\nContent-Type: image/png\r\n\r\n`)
  target.request.write(contents)
  target.request.end(`\r\n--${target.boundary}--\r\n`)
}
