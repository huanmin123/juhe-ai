import assert from 'node:assert/strict'
import type { Request } from 'express'

import { resolveGatewayModelsResponseProtocol } from '../../modules/gateway/request/models-response-protocol.js'

type HeaderMap = Record<string, string>

function request(originalUrl: string, headers: HeaderMap = {}): Request {
  const normalizedHeaders = Object.fromEntries(
    Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value])
  )
  return {
    method: 'GET',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: normalizedHeaders,
    header(name: string) {
      return normalizedHeaders[name.toLowerCase()]
    }
  } as unknown as Request
}

assert.equal(resolveGatewayModelsResponseProtocol(request('/models')), 'openai_v1', '普通 /models 必须默认返回 OpenAI-compatible 目录')
assert.equal(resolveGatewayModelsResponseProtocol(request('/v1/models')), 'openai_v1', '/v1/models 必须返回 OpenAI-compatible 目录')
assert.equal(resolveGatewayModelsResponseProtocol(request('/v1beta/models')), 'gemini_v1beta', '/v1beta/models 必须返回 Gemini 原生目录')
assert.equal(
  resolveGatewayModelsResponseProtocol(request('/models', { 'x-juhe-client-profile': 'generic-gemini' })),
  'gemini_v1beta',
  '显式 Gemini 客户端画像必须返回 Gemini 原生目录'
)
assert.equal(
  resolveGatewayModelsResponseProtocol(request('/models', { 'x-goog-api-key': 'test-key' })),
  'gemini_v1beta',
  'X-Goog-API-Key 必须作为明确 Gemini 信号'
)
assert.equal(resolveGatewayModelsResponseProtocol(request('/models?key=test-key')), 'gemini_v1beta', 'Gemini key query 必须返回 Gemini 原生目录')
assert.equal(resolveGatewayModelsResponseProtocol(request('/models?key=')), 'openai_v1', '空 key query 不能伪装成明确 Gemini 客户端')
assert.equal(
  resolveGatewayModelsResponseProtocol(request('/models', { 'anthropic-version': '2023-06-01' })),
  'anthropic_v1',
  '明确 Anthropic 信号必须返回 Anthropic 原生目录'
)
assert.equal(resolveGatewayModelsResponseProtocol(request('/not-models')), undefined, '非模型目录请求不得进入固定响应')

console.log('gateway-models-response-protocol-regression passed')
