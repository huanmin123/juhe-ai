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

assert.deepEqual(normalizeChatImageSize(undefined, { allowLarge: false }), {
  width: 1024, height: 1024, size: '1024x1024', sizeAdjusted: false
})
assert.deepEqual(normalizeChatImageSize('1536x1024', { allowLarge: false }), {
  width: 1536, height: 1024, size: '1536x1024', sizeAdjusted: false
})
assert.deepEqual(normalizeChatImageSize('2048x1024', { allowLarge: false }), {
  width: 1536, height: 768, size: '1536x768', sizeAdjusted: true
})
assert.deepEqual(normalizeChatImageSize('1000x1000', { allowLarge: false }), {
  width: 992, height: 992, size: '992x992', sizeAdjusted: true
})
assert.deepEqual(normalizeChatImageSize('4096x2048', { allowLarge: true }), {
  width: 4096, height: 2048, size: '4096x2048', sizeAdjusted: false
})
assert.deepEqual(normalizeChatImageSize('100x5000', { allowLarge: true }), {
  width: 1376, height: 4096, size: '1376x4096', sizeAdjusted: true
})
assert.deepEqual(normalizeChatImageSize('not-a-size', { allowLarge: false }), {
  width: 1024, height: 1024, size: '1024x1024', sizeAdjusted: true
})
assert.equal(chatImageInputPolicy.mimeType, 'image/webp')
assert.equal(chatImageInputPolicy.maxEdge, 1024)
assert.equal(chatImagePreviewPolicy.mimeType, 'image/webp')
assert.equal(chatImagePreviewPolicy.maxEdge, 640)
assert(chatImagePreviewPolicy.maxBytes < chatImageInputPolicy.maxBytes)
assert.equal(isChatImageGenerationAccount({ type: 'api_key' }), true, 'API Key 账户可承载 Images 生图请求')
assert.equal(isChatImageGenerationAccount({ type: 'oauth' }), false, 'OAuth 账户不得被注入站内 gpt-image-2 工具')

console.log('AI 问答图片策略回归通过')
