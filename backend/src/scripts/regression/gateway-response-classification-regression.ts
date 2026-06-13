import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  isOpenAIBinaryResponseContentType,
  isOpenAIJsonResponseContentType,
  isOpenAIStreamContentType,
  shouldHandleOpenAIUpstreamResponseAsStream
} from '../../modules/gateway/response/responses.js'

assert.equal(isOpenAIStreamContentType('text/event-stream; charset=utf-8'), true, 'SSE content-type 应识别为流式响应')
assert.equal(isOpenAIStreamContentType('application/octet-stream'), false, 'octet-stream 不应直接识别为 OpenAI SSE')
assert.equal(isOpenAIJsonResponseContentType('application/json; charset=utf-8'), true, 'JSON content-type 应识别为非流式 JSON')
assert.equal(isOpenAIJsonResponseContentType('application/problem+json'), true, '+json content-type 应识别为非流式 JSON')
assert.equal(isOpenAIBinaryResponseContentType('application/octet-stream'), true, 'octet-stream 应识别为二进制响应')
assert.equal(isOpenAIBinaryResponseContentType('image/png'), true, '图片响应应识别为二进制响应')
assert.equal(isOpenAIBinaryResponseContentType('audio/mpeg'), true, '音频响应应识别为二进制响应')
assert.equal(isOpenAIBinaryResponseContentType('video/mp4'), true, '视频响应应识别为二进制响应')
assert.equal(isOpenAIBinaryResponseContentType('application/pdf'), true, 'PDF 响应应识别为二进制响应')

assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: 'text/event-stream; charset=utf-8', streamRequest: false }), true, '上游明确 SSE 时应走流式管道')
assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: 'application/json', streamRequest: true }), false, 'stream 请求收到 JSON 响应时不应走 SSE 管道')
assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: 'application/octet-stream', streamRequest: true }), false, 'stream 请求收到 octet-stream 时不应走 SSE 管道')
assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: 'image/png', streamRequest: true }), false, 'stream 请求收到图片时不应走 SSE 管道')
assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: '', streamRequest: true }), true, 'stream 请求缺少 content-type 时仍保留流式兜底')
assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: 'text/plain; charset=utf-8', streamRequest: true }), true, 'stream 请求收到未知文本响应时仍保留流式兜底')
assert.equal(shouldHandleOpenAIUpstreamResponseAsStream({ contentType: 'application/octet-stream', streamRequest: false }), false, '非 stream 请求收到 octet-stream 时应走非流式 pipe')

const routeSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
assert(routeSource.includes('shouldHandleOpenAIUpstreamResponseAsStream'), '网关路由应使用统一响应分类函数')
assert(!routeSource.includes('isEffectiveOpenAIStreamRequest(req, account) && !isJsonResponseContentType(contentType)'), '网关路由不应继续用 stream 请求 + 非 JSON 兜底误判二进制响应')

console.log('网关响应分类回归通过：octet-stream 和常见二进制响应不会被误判为 OpenAI SSE，缺失 content-type 的 stream 请求仍保留流式兜底')
