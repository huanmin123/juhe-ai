import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Readable } from 'node:stream'

import sharp from 'sharp'

const transport = readFileSync('src/modules/chat/chat-image-generation-transport.ts', 'utf8')
const routes = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')
const runner = readFileSync('src/modules/chat/chat-generation-runner.ts', 'utf8')
const repository = readFileSync('src/storage/chat.repository.ts', 'utf8')
const timeline = readFileSync('src/modules/chat/chat-assistant-timeline.ts', 'utf8')

assert.match(transport, /export (async )?function (generate|request)ChatImage/)
assert.match(transport, /\/v1\/images\/generations/)
assert.match(transport, /\/v1\/images\/edits/, 'gpt-image-2 二次编辑必须走 Images edits multipart 接口')
assert.match(transport, /n:\s*1|n = 1/)
assert.doesNotMatch(transport, /response_format/, 'GPT Image 不支持 DALL-E response_format 参数')
assert.match(transport, /decodeBase64FieldToTempFile/)
assert.match(transport, /chatAssetGeneratedMaxBytes/)
assert.match(transport, /references/, '编辑输入必须通过已校验的资产引用传入 transport')
assert.match(routes, /createGenerateImageTool/)
assert.match(routes, /ChatInternalToolOrchestrator/)
assert.match(routes, /createChatGeneratedImageArtifactSink/)
assert.doesNotMatch(routes, /body\.model === 'gpt-image-2'/, 'AI 问答不得再把图像模型作为文本模型特殊分支')
assert.doesNotMatch(routes, /shouldOfferChatImageGenerationTool/, '是否生图应由文本模型 function call 决定，路由不得继续使用意图正则')
assert.match(routes, /new ChatGenerationRunner/)
assert.match(routes, /message\.started/)
assert.match(routes, /runner\.snapshotContentBlocks/)
assert.doesNotMatch(routes, /commitChatGeneratedAsset/, '资产提交必须通过独立 Artifact Sink，不得继续堆在路由')
assert.match(timeline, /output_image/)
assert.match(runner, /output_image/)
assert.match(repository, /output_image|insertChatAssetReference/)

const { buildChatImageGenerationRequest, generateChatImage } = await import('../../modules/chat/chat-image-generation-transport.js')
assert.deepEqual(buildChatImageGenerationRequest({ model: 'gpt-image-2', prompt: '生成验收图' }), {
  path: '/v1/images/generations',
  body: { model: 'gpt-image-2', prompt: '生成验收图', n: 1, size: 'auto', quality: 'auto', output_format: 'webp' }
}, 'GPT Image 请求缺省尺寸必须使用公开的 auto 默认值与 WebP，依赖默认 Base64 返回且不能发送不受支持的 response_format')
assert.equal(buildChatImageGenerationRequest({ model: 'gpt-image-2', prompt: '生成 4K 图', size: '3840x2160' }).body.size, '3840x2160', '合法尺寸必须原样传递')
assert.throws(
  () => buildChatImageGenerationRequest({ model: 'gpt-image-2', prompt: '任意提示词都不能放宽非法尺寸', size: '4096x2048' }),
  /最长边/,
  '非法尺寸必须在网络调用前失败，不能按提示词关键词放行'
)

const providerPng = await sharp({
  create: { width: 2, height: 1, channels: 4, background: { r: 220, g: 40, b: 40, alpha: 1 } }
}).png().toBuffer()
const providerFormatTempDir = await mkdtemp(join(tmpdir(), 'juhe-chat-image-provider-format-'))
try {
  const providerFormatResult = await generateChatImage({
    gatewayBaseUrl: 'http://127.0.0.1:1',
    apiKey: 'test-only',
    model: 'gpt-image-2',
    prompt: '上游未遵循 WebP 请求而返回 PNG',
    tempDir: providerFormatTempDir,
    fetchImpl: async () => Response.json({ data: [{ b64_json: providerPng.toString('base64') }] }, { status: 200 })
  })
  assert.equal(providerFormatResult.mimeType, 'image/png', '必须接受上游实际返回的受支持 MIME，不能把请求格式当成硬校验')
  await rm(providerFormatResult.path, { force: true, maxRetries: 10, retryDelay: 100 })
} finally {
  await rm(providerFormatTempDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
}

let responseCanceled = false
const tempDir = await mkdtemp(join(tmpdir(), 'juhe-chat-image-cancel-'))
try {
  await assert.rejects(generateChatImage({
    gatewayBaseUrl: 'http://127.0.0.1:1',
    apiKey: 'test-only',
    model: 'gpt-image-2',
    prompt: '测试提前失败取消 reader',
    tempDir,
    fetchImpl: async () => new Response(new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('{"data":[{"b64_json":"!!!!"}]}'))
      },
      cancel() { responseCanceled = true }
    }), { status: 200, headers: { 'content-type': 'application/json' } })
  }), /Base64 字符非法/)
  assert.equal(responseCanceled, true, 'Base64 解码提前拒绝后必须取消仍未结束的上游响应体')
} finally {
  rm(tempDir, { recursive: true, force: true })
}

let editRequestUrl = ''
let editRequestBody: FormData | undefined
await assert.rejects(generateChatImage({
  gatewayBaseUrl: 'http://127.0.0.1:9999',
  apiKey: 'test-only',
  model: 'gpt-image-2',
  prompt: '把背景改成夜晚',
  size: '1024x1024',
  quality: 'high',
  outputFormat: 'webp',
  references: [
    { assetId: `chat_asset_${'1'.repeat(32)}`, stream: Readable.from(Buffer.from('first')), bytes: 5, mimeType: 'image/webp', filename: 'first.webp' },
    { assetId: `chat_asset_${'2'.repeat(32)}`, stream: Readable.from(Buffer.from('second')), bytes: 6, mimeType: 'image/png', filename: 'second.png' }
  ],
  fetchImpl: async (url, init) => {
    editRequestUrl = String(url)
    editRequestBody = init?.body as FormData
    return Response.json({ error: { message: 'fixture edit rejected after capture' } }, { status: 400 })
  }
}), /fixture edit rejected after capture/)
assert.equal(editRequestUrl, 'http://127.0.0.1:9999/v1/images/edits')
assert.ok(editRequestBody instanceof FormData, '编辑请求必须使用 multipart FormData')
assert.equal(editRequestBody?.get('model'), 'gpt-image-2')
assert.equal(editRequestBody?.get('prompt'), '把背景改成夜晚')
assert.equal(editRequestBody?.get('size'), '1024x1024')
assert.equal(editRequestBody?.get('quality'), 'high')
assert.equal(editRequestBody?.get('output_format'), 'webp')
assert.equal(editRequestBody?.getAll('image[]').length, 2, '每张来源图片必须使用重复 image[] 字段')

console.log('AI 问答 Images 生图 transport 回归通过')
