import assert from 'node:assert/strict'

import {
  chatImageInputPolicy,
  chatImagePreviewPolicy,
  isChatImageGenerationAccount,
  normalizeChatImageOutputFormat,
  normalizeChatImageQuality,
  normalizeChatImageSize
} from '../../modules/chat/chat-image-policy.js'

assert.equal(normalizeChatImageOutputFormat(undefined), 'webp')
assert.equal(normalizeChatImageOutputFormat('JPG'), 'jpeg')
assert.equal(normalizeChatImageOutputFormat('png'), 'png')
assert.throws(() => normalizeChatImageOutputFormat('gif'), /输出格式/)
assert.equal(normalizeChatImageQuality(undefined), 'auto')
assert.equal(normalizeChatImageQuality('high'), 'high')

assert.deepEqual(normalizeChatImageSize(undefined), { size: 'auto' })
assert.deepEqual(normalizeChatImageSize('AUTO'), { size: 'auto' })
assert.deepEqual(normalizeChatImageSize('1536x1024'), { width: 1536, height: 1024, size: '1536x1024' })
assert.deepEqual(normalizeChatImageSize('2048x2048'), { width: 2048, height: 2048, size: '2048x2048' })
assert.deepEqual(normalizeChatImageSize('3840x2160'), { width: 3840, height: 2160, size: '3840x2160' })
assert.deepEqual(normalizeChatImageSize('816x816'), { width: 816, height: 816, size: '816x816' })
assert.throws(() => normalizeChatImageSize('1000x1000'), /16px/)
assert.throws(() => normalizeChatImageSize('800x800'), /总像素/)
assert.throws(() => normalizeChatImageSize('4096x2048'), /最长边/)
assert.throws(() => normalizeChatImageSize('1600x512'), /比例/)
assert.throws(() => normalizeChatImageSize('not-a-size'), /WIDTHxHEIGHT/)
assert.equal(chatImageInputPolicy.mimeType, 'image/webp')
assert.equal(chatImageInputPolicy.maxEdge, 1024)
assert.equal(chatImageInputPolicy.maxBytes, 3 * 1024 * 1024, '输入图策略必须允许单张最多 3 MiB')
assert.equal(chatImagePreviewPolicy.mimeType, 'image/webp')
assert.equal(chatImagePreviewPolicy.maxEdge, 640)
assert(chatImagePreviewPolicy.maxBytes < chatImageInputPolicy.maxBytes)
assert.equal(isChatImageGenerationAccount({ type: 'api_key' }), true, 'API Key 账户可承载 Images 生图请求')
assert.equal(isChatImageGenerationAccount({ type: 'oauth' }), false, 'OAuth 账户不得被注入站内 gpt-image-2 工具')

console.log('AI 问答图片策略回归通过')
