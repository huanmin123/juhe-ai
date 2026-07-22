import assert from 'node:assert/strict'

import { extractGatewayMultipartImageModel } from '../../modules/gateway/request/multipart-image-metadata.js'

const form = new FormData()
form.set('model', 'gpt-image-2')
form.set('prompt', '把背景改成夜晚')
form.append('image[]', new Blob([new Uint8Array([1, 2, 3])], { type: 'image/webp' }), 'source.webp')
const request = new Request('http://127.0.0.1/v1/images/edits', { method: 'POST', body: form })
const rawBody = Buffer.from(await request.arrayBuffer())
const contentType = request.headers.get('content-type') ?? ''

assert.equal(await extractGatewayMultipartImageModel({ rawBody, contentType, path: '/v1/images/edits' }), 'gpt-image-2')
assert.equal(await extractGatewayMultipartImageModel({ rawBody, contentType, path: '/v1/chat/completions' }), undefined, '非图片端点不得解析 multipart 图片元数据')
assert.equal(await extractGatewayMultipartImageModel({ rawBody: Buffer.from('invalid'), contentType: 'application/json', path: '/v1/images/edits' }), undefined)

const oversizedModelForm = new FormData()
oversizedModelForm.set('model', 'x'.repeat(300))
oversizedModelForm.append('image[]', new Blob([new Uint8Array([1])], { type: 'image/png' }), 'source.png')
const oversizedRequest = new Request('http://127.0.0.1/v1/images/edits', { method: 'POST', body: oversizedModelForm })
assert.equal(await extractGatewayMultipartImageModel({
  rawBody: Buffer.from(await oversizedRequest.arrayBuffer()),
  contentType: oversizedRequest.headers.get('content-type') ?? '',
  path: '/v1/images/edits'
}), undefined, '模型字段超过边界时不得进入路由元数据')

console.log('网关 multipart 图片模型元数据回归通过')
