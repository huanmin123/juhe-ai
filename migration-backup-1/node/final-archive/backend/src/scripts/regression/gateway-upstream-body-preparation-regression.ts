import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  replaceGatewayJsonBody
} from '../../modules/gateway/request/body.js'
import { gatewaySerializedJsonObject } from '../../modules/gateway/request/serialized-json-body.js'
import {
  prepareAnthropicMessagesBodyForAttempt,
  preparedUpstreamBodyMetadata
} from '../../modules/gateway/upstream/body-preparation.js'

const anthropicHeaders = new Headers({
  'anthropic-version': '2023-06-01',
  'x-api-key': 'test-key'
})
const messagesUrl = 'https://api.anthropic.com/v1/messages'

const parsedBody = {
  model: 'claude-test',
  stream: false,
  service_tier: 'priority',
  output_config: { effort: 'high' },
  messages: [{
    role: 'user',
    content: [
      { type: 'text', text: 'hello ' },
      { type: 'text', text: 'world' }
    ]
  }],
  padding: 'large-body'.repeat(128 * 1024)
}
const rawBody = Buffer.from(JSON.stringify(parsedBody), 'utf8')
const req = {
  method: 'POST',
  path: '/v1/messages',
  originalUrl: '/v1/messages',
  headers: { 'content-type': 'application/json' },
  rawBody,
  body: undefined,
  gatewayRequestBody: {
    rawBodyBytes: rawBody.length,
    contentType: 'application/json',
    isJson: true,
    jsonParseStatus: 'scanned_json',
    jsonParseWarningBytes: 2 * 1024 * 1024,
    model: 'claude-test',
    stream: false,
    serviceTier: 'priority',
    reasoningEffort: 'high'
  }
} as unknown as Request

const nativePrepared = prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, rawBody)
assert.equal(nativePrepared, rawBody, '未改写的 Anthropic native Body 必须保持原 Buffer 引用')
assert.equal(
  (nativePrepared as Buffer).toString('utf8'),
  rawBody.toString('utf8'),
  '未启用 bridge/model mapping/account override 时必须逐字节透传'
)

const modifiedBody = Buffer.from(rawBody)
const firstPrepared = prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, modifiedBody)
const retryPrepared = prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, modifiedBody)
assert(Buffer.isBuffer(firstPrepared), 'Anthropic Buffer 请求规范化后仍应返回 Buffer')
assert.equal(retryPrepared, firstPrepared, '同一请求和同一 Body 的 transport retry 必须复用规范化结果')
const firstPreparedJson = JSON.parse(firstPrepared.toString('utf8')) as Record<string, unknown>
assert.equal(firstPreparedJson.stream, undefined, 'Anthropic Messages 上游不应显式发送 stream=false')
assert.equal(
  ((firstPreparedJson.messages as Array<{ content?: unknown }>)[0]?.content),
  'hello world',
  '纯文本 content blocks 应保持现有的字符串规范化语义'
)
assert.equal(parsedBody.stream, false, '上游规范化不得修改请求级共享解析对象')
assert(Array.isArray(parsedBody.messages[0]?.content), '上游规范化不得原地折叠共享 messages content')

const mixedModifiedBody = Buffer.from(JSON.stringify({
  model: 'claude-test',
  stream: true,
  messages: [{
    role: 'user',
    content: [
      { type: 'text', text: 'keep blocks' },
      { type: 'tool_result', tool_use_id: 'tool-1', content: 'ok' }
    ]
  }]
}), 'utf8')
const mixedPrepared = prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, mixedModifiedBody)
const mixedPreparedJson = JSON.parse((mixedPrepared as Buffer).toString('utf8')) as Record<string, unknown>
assert.equal(mixedPrepared, mixedModifiedBody, '不需要规范化的改写 Body 应保持原 Buffer')
assert.equal(mixedPreparedJson.stream, true, 'stream=true 必须保留')
assert(Array.isArray((mixedPreparedJson.messages as Array<{ content?: unknown }>)[0]?.content), 'tool/mixed content blocks 不得折叠')

const invalidRawBody = Buffer.from('{"model":', 'utf8')
const invalidReq = {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  rawBody: invalidRawBody
} as unknown as Request
assert.equal(
  prepareAnthropicMessagesBodyForAttempt(invalidReq, anthropicHeaders, messagesUrl, invalidRawBody),
  invalidRawBody,
  '原生 invalid JSON Body 也必须保持原字节，由上游维持原有错误语义'
)

const rawMetadata = preparedUpstreamBodyMetadata(req, rawBody)
assert.equal(rawMetadata?.serviceTier, 'priority', '原始 passthrough Body 应直接复用请求阶段 metadata')
assert.equal(rawMetadata?.reasoningEffort, 'high', '原始 passthrough Body 不应重新扫描 reasoning metadata')

const transformedBody = Buffer.from(JSON.stringify({
  model: 'claude-transformed',
  service_tier: 'flex',
  output_config: { effort: 'medium' },
  messages: []
}), 'utf8')
const transformedMetadata = preparedUpstreamBodyMetadata(req, transformedBody)
const transformedMetadataAgain = preparedUpstreamBodyMetadata(req, transformedBody)
assert.equal(transformedMetadataAgain, transformedMetadata, '相同 transformed Body 的多账号准备必须复用 metadata 扫描结果')
assert.equal(transformedMetadata?.serviceTier, 'flex')
assert.equal(transformedMetadata?.reasoningEffort, 'medium')

const continueBody = {
  model: 'claude-test',
  stream: false,
  messages: [{ role: 'user', content: [{ type: 'text', text: 'continue' }] }]
}
const continuedPrepared = prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, continueBody)
assert(Buffer.isBuffer(continuedPrepared), '结构化 continue Body 应只序列化一次并返回 Buffer')
const continuedJson = JSON.parse(continuedPrepared.toString('utf8')) as Record<string, unknown>
assert.equal(continuedJson.stream, undefined)
assert.equal((continuedJson.messages as Array<{ content?: unknown }>)[0]?.content, 'continue')

const countTokensBody = prepareAnthropicMessagesBodyForAttempt(
  req,
  anthropicHeaders,
  'https://api.anthropic.com/v1/messages/count_tokens',
  rawBody
)
assert.equal(countTokensBody, rawBody, '非 /messages Anthropic 端点必须保持原始 Body')

const preReplacementState = getGatewayRequestBodyState(req)
assert.equal(preReplacementState?.jsonParseStatus, 'scanned_json')
if (preReplacementState) {
  preReplacementState.jsonParseStatus = 'invalid_json'
}
const replacementBody = {
  model: 'claude-replaced',
  stream: false,
  output_config: { effort: 'medium' },
  messages: [{ role: 'user', content: [{ type: 'text', text: 'replacement' }] }]
}
replaceGatewayJsonBody(req, replacementBody)
const replacedRawBody = (req as Request & { rawBody?: Buffer }).rawBody
assert(replacedRawBody)
assert.equal(getGatewayRequestBodyState(req)?.jsonParseStatus, 'parsed', 'Body 替换后必须清除旧 invalid 状态')
assert.equal(getGatewayRequestBodyState(req)?.reasoningEffort, 'medium', 'Body 替换后必须从 output_config.effort 重建 metadata')
assert.equal(
  gatewaySerializedJsonObject(replacedRawBody),
  replacementBody,
  '替换后的 canonical Buffer 必须绑定到已知解析对象'
)
assert.equal(
  preparedUpstreamBodyMetadata(req, replacedRawBody)?.reasoningEffort,
  'medium',
  '替换后的上游 metadata 必须保留 output_config.effort'
)
assert.equal(
  prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, replacedRawBody),
  replacedRawBody,
  '替换后的请求级 canonical rawBody 仍应按 native passthrough 处理'
)
const originalJsonParse = JSON.parse
let boundBodyJsonParseCount = 0
JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
  boundBodyJsonParseCount += 1
  return originalJsonParse(...args)
}) as typeof JSON.parse
try {
  const boundPrepared = prepareAnthropicMessagesBodyForAttempt(
    { headers: { 'content-type': 'application/json' } } as unknown as Request,
    anthropicHeaders,
    messagesUrl,
    replacedRawBody
  )
  assert(Buffer.isBuffer(boundPrepared))
  assert.equal(boundBodyJsonParseCount, 0, '绑定后的 canonical Buffer 进入上游准备时不得再次 JSON.parse')
} finally {
  JSON.parse = originalJsonParse
}
const replacedPrepared = prepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders, messagesUrl, Buffer.from(replacedRawBody))
assert.notEqual(replacedPrepared, firstPrepared, 'Body 替换后不得复用旧规范化结果')
assert.equal(
  ((JSON.parse((replacedPrepared as Buffer).toString('utf8')) as Record<string, unknown>).messages as Array<{ content?: unknown }>)[0]?.content,
  'replacement'
)

const attemptsSource = readFileSync(
  new URL('../../modules/gateway/dispatch/upstream-attempts.ts', import.meta.url),
  'utf8'
)
assert.doesNotMatch(
  attemptsSource,
  /Buffer\.from\(JSON\.stringify\(nextBody\)/,
  'continue 路径不得先 stringify 再交给规范化入口 reparse'
)
assert.match(
  attemptsSource,
  /prepareAnthropicMessagesBodyForAttempt\(req, headers, upstreamUrl, nextBody\)/,
  'continue 路径必须直接传递结构化 Body'
)

console.log('网关上游 Body preparation 回归通过：Anthropic retry/continue 与 metadata 扫描均复用请求级结果')
