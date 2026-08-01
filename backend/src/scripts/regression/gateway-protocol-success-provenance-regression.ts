import assert from 'node:assert/strict'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { extractGatewayMultipartAudioResponseFormat } from '../../modules/gateway/request/multipart-image-metadata.js'

Object.assign(runtimeConfig.gateway, {
  automaticProbeMaxConcurrency: 1,
  usageFinalizationMaxItems: 128,
  usageFinalizationMaxConcurrency: 1
})

const {
  nonStreamJsonProtocolValidationAllowed,
  protocolValidatedNonStreamResponse
} = await import('../../modules/gateway/response/finalization.js')

function request(path: string, method = 'POST', body?: Record<string, unknown>): Request {
  return {
    method,
    path,
    originalUrl: path,
    headers: {},
    body,
    header: () => undefined
  } as unknown as Request
}

const openAIAccount = {
  id: 'protocol-success-openai',
  name: 'protocol-success-openai',
  providerCode: 'gpt',
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: 'openai',
  protocolVersion: 'v1'
} as UpstreamAccount
const anthropicAccount = {
  ...openAIAccount,
  providerCode: 'anthropic',
  providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: 'anthropic'
} as UpstreamAccount
const geminiAccount = {
  ...openAIAccount,
  providerCode: 'gemini',
  providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
} as UpstreamAccount

const successMatrix: Array<{ label: string; req: Request; account: UpstreamAccount; body: unknown }> = [
  { label: 'chat', req: request('/v1/chat/completions'), account: openAIAccount, body: { choices: [{ message: { content: 'ok' } }] } },
  { label: 'responses', req: request('/v1/responses'), account: openAIAccount, body: { id: 'resp-ok', object: 'response', status: 'completed', output: [] } },
  { label: 'models', req: request('/v1/models', 'GET'), account: openAIAccount, body: { data: [] } },
  { label: 'embeddings', req: request('/v1/embeddings'), account: openAIAccount, body: { data: [{ embedding: [0.1] }] } },
  { label: 'images', req: request('/v1/images/generations'), account: openAIAccount, body: { data: [{ b64_json: 'aW1hZQ==' }] } },
  { label: 'moderations', req: request('/v1/moderations'), account: openAIAccount, body: { results: [] } },
  { label: 'audio transcription', req: request('/v1/audio/transcriptions'), account: openAIAccount, body: { text: 'ok' } },
  { label: 'batch', req: request('/v1/batches'), account: openAIAccount, body: { id: 'batch-ok' } },
  { label: 'failed batch resource', req: request('/v1/batches/batch-failed', 'GET'), account: openAIAccount, body: { id: 'batch-failed', status: 'failed', error: { message: 'input invalid' } } },
  { label: 'file list', req: request('/v1/files', 'GET'), account: openAIAccount, body: { data: [] } },
  { label: 'messages', req: request('/v1/messages'), account: anthropicAccount, body: { type: 'message', content: [] } },
  { label: 'anthropic count tokens', req: request('/v1/messages/count_tokens'), account: anthropicAccount, body: { input_tokens: 12 } },
  { label: 'gemini generate', req: request('/v1beta/models/gemini-2.5-pro:generateContent'), account: geminiAccount, body: { candidates: [] } },
  { label: 'gemini count tokens', req: request('/v1beta/models/gemini-2.5-pro:countTokens'), account: geminiAccount, body: { totalTokens: 12 } },
  { label: 'gemini embed', req: request('/v1beta/models/text-embedding-004:embedContent'), account: geminiAccount, body: { embedding: { values: [] } } },
  { label: 'gemini interactions', req: request('/v1beta/interactions'), account: geminiAccount, body: { id: 'interaction-ok' } },
  { label: 'gemini models', req: request('/v1beta/models', 'GET'), account: geminiAccount, body: { models: [] } }
]

for (const scenario of successMatrix) {
  assert.equal(protocolValidatedNonStreamResponse({
    req: scenario.req,
    account: scenario.account,
    responseBodyText: JSON.stringify(scenario.body),
    statusCode: 200
  }), true, `${scenario.label} 的最小有效 JSON 必须形成验证成功`)
  assert.equal(protocolValidatedNonStreamResponse({
    req: scenario.req,
    account: scenario.account,
    responseBodyText: JSON.stringify({ error: { message: 'opaque upstream failure' } }),
    statusCode: 200
  }), false, `${scenario.label} 的 2xx error envelope 不得伪装为成功`)
  assert.equal(protocolValidatedNonStreamResponse({
    req: scenario.req,
    account: scenario.account,
    responseBodyText: JSON.stringify({}),
    statusCode: 200
  }), false, `${scenario.label} 的空 JSON 对象不得伪装为成功`)
}

assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: request('/v1/embeddings'),
  account: openAIAccount,
  upstreamResponse: { ok: true, headers: new Headers({ 'content-type': 'text/plain' }) }
}), true, '已知 JSON 端点即使上游伪装 content-type 也必须先验证')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: request('/v1/embeddings'),
  account: openAIAccount,
  upstreamResponse: { ok: true, headers: new Headers({ 'content-type': 'image/png' }) }
}), true, '已知 JSON 端点不得因伪造二进制 content-type 跳过验证')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: request('/v1/files/file-1/content', 'GET'),
  account: openAIAccount,
  upstreamResponse: { ok: true, headers: new Headers({ 'content-type': 'application/octet-stream' }) }
}), false, '二进制下载不得被错误纳入 JSON 协议验证')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: request('/v1/audio/speech'),
  account: openAIAccount,
  upstreamResponse: { ok: true, headers: new Headers({ 'content-type': 'audio/mpeg' }) }
}), false, '二进制语音输出不得被错误纳入 JSON 协议验证')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: request('/v1/audio/transcriptions', 'POST', { response_format: 'text' }),
  account: openAIAccount,
  upstreamResponse: { ok: true }
}), false, '请求 text 转写格式时不得强制 JSON 协议校验')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: request('/v1/audio/transcriptions', 'POST', { response_format: 'verbose_json' }),
  account: openAIAccount,
  upstreamResponse: { ok: true }
}), true, '请求 verbose_json 转写格式时必须校验 JSON 协议')

const multipartResponseFormat = await extractGatewayMultipartAudioResponseFormat({
  contentType: 'multipart/form-data; boundary=protocol-regression-boundary',
  path: '/v1/audio/transcriptions',
  rawBody: Buffer.from([
    '--protocol-regression-boundary',
    'Content-Disposition: form-data; name="response_format"',
    '',
    'vtt',
    '--protocol-regression-boundary--',
    ''
  ].join('\r\n'))
})
assert.equal(multipartResponseFormat, 'vtt', 'multipart 转写请求必须提取 response_format，避免把文本结果误判为 JSON')

console.log('gateway protocol success provenance regression passed')
