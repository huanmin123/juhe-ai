import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import {
  parseGatewayProtocolErrorPayloadFromJsonValue,
  parseGatewayProtocolUsageFromJsonValueForRequest
} from '../../modules/gateway/protocols/registry.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { protocolValidatedNonStreamResponse } from '../../modules/gateway/response/finalization.js'
import { parseGatewayNonStreamJsonBody } from '../../modules/gateway/response/non-stream-json-body.js'
import { parseHybridAuxiliaryResponse } from '../../modules/gateway/hybrid/auxiliary-dispatch.service.js'
import { buildDiagnosticUpstreamError } from '../../modules/gateway/upstream/error-helpers.js'

const openAIRequest = request('/v1/chat/completions')
const anthropicAccount = account({
  providerCode: 'anthropic',
  providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
})

const originalParse = JSON.parse
let parseCount = 0
JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
  parseCount += 1
  return originalParse(...args)
}) as typeof JSON.parse

try {
  const successBodyText = JSON.stringify({
    id: 'chatcmpl-once',
    object: 'chat.completion',
    choices: [{ message: { role: 'assistant', content: 'ok' } }],
    usage: { prompt_tokens: 12, completion_tokens: 3 }
  })
  const successJsonBody = parseGatewayNonStreamJsonBody(
    successBodyText,
    new Headers({ 'content-type': 'application/json' })
  )
  assert.equal(successJsonBody.status, 'valid')
  assert.equal(parseCount, 1, '成功响应正文只应完整 JSON.parse 一次')
  if (successJsonBody.status !== 'valid') throw new Error('测试成功响应必须是有效 JSON')

  const usage = parseGatewayProtocolUsageFromJsonValueForRequest(
    openAIRequest,
    anthropicAccount,
    successJsonBody.value
  )
  assert.equal(usage.inputTokens, 12, '成功 usage 必须按最终客户端 OpenAI 协议提取')
  assert.equal(usage.outputTokens, 3)
  assert.equal(protocolValidatedNonStreamResponse({
    req: openAIRequest,
    account: anthropicAccount,
    responseBodyText: successBodyText,
    parsedJsonBody: successJsonBody,
    statusCode: 200
  }), true)
  assert.equal(parseCount, 1, 'usage 与协议成功校验必须复用已解析对象')

  const errorBodyText = JSON.stringify({
    type: 'error',
    error: { type: 'authentication_error', message: 'bad anthropic key' }
  })
  const errorJsonBody = parseGatewayNonStreamJsonBody(
    errorBodyText,
    new Headers({ 'content-type': 'application/json' })
  )
  assert.equal(errorJsonBody.status, 'valid')
  assert.equal(parseCount, 2, '另一个非 2xx attempt 应建立自己的唯一解析结果')
  if (errorJsonBody.status !== 'valid') throw new Error('测试错误响应必须是有效 JSON')

  const errorPayload = parseGatewayProtocolErrorPayloadFromJsonValue(anthropicAccount, errorJsonBody.value)
  assert.equal(errorPayload.type, 'authentication_error', '错误必须按真实上游 Anthropic 协议解释')
  assert.equal(errorPayload.message, 'bad anthropic key')
  assert.equal(parseCount, 2, '错误策略与错误归一化不得重新 JSON.parse 正文')

  const diagnosticError = buildDiagnosticUpstreamError({
    accountId: 'anthropic-diagnostic-account',
    accountName: 'anthropic-diagnostic-account',
    providerCode: 'anthropic',
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    protocolCode: 'anthropic',
    protocolVersion: 'v1',
    upstreamUrl: 'https://api.anthropic.com/v1/messages',
    status: 401,
    responseHeaders: { 'content-type': 'application/json' },
    responseBodyText: errorBodyText,
    parsedResponseBody: errorJsonBody
  }, 'fallback')
  assert.equal(parseCount, 2, '最终诊断错误必须复用 attempt 已解析正文')
  assert.equal(diagnosticError?.errorMessage, 'bad anthropic key')
  assert.equal((diagnosticError?.payload.error as { type?: unknown } | undefined)?.type, 'authentication_error')

  const hybridBodyText = JSON.stringify({
    choices: [{ message: { content: '{"level":2}' } }],
    usage: { prompt_tokens: 7, completion_tokens: 2 }
  })
  const hybrid = parseHybridAuxiliaryResponse(
    hybridBodyText,
    new Headers({ 'content-type': 'application/json' })
  )
  assert.equal(parseCount, 3, 'hybrid auxiliary 完整响应必须只 JSON.parse 一次')
  assert.equal(hybrid.parsedResponseBody.status, 'valid')
  assert.equal(hybrid.usage.inputTokens, 7)
  assert.equal(hybrid.usage.outputTokens, 2)

  const opaque = parseGatewayNonStreamJsonBody(
    'opaque upstream failure',
    new Headers({ 'content-type': 'text/plain' })
  )
  assert.equal(opaque.status, 'not_json')
  assert.equal(parseCount, 3, '明确非 JSON 的 generic 错误正文不得触发 JSON.parse')
} finally {
  JSON.parse = originalParse
}

const codexBridgeStateSource = readFileSync(
  new URL('../../modules/gateway/codex-responses/chat-bridge-state.ts', import.meta.url),
  'utf8'
)
const openAIAnthropicBridgeSource = readFileSync(
  new URL('../../modules/providers/drivers/_shared/openai-anthropic-bridge.ts', import.meta.url),
  'utf8'
)
assert.doesNotMatch(codexBridgeStateSource, /JSON\.parse\(JSON\.stringify\(/, 'Codex bridge 状态恢复不得用 JSON 文本往返做深拷贝')
assert.doesNotMatch(openAIAnthropicBridgeSource, /JSON\.parse\(JSON\.stringify\(/, 'OpenAI-Anthropic bridge 不得用 JSON 文本往返做深拷贝')

console.log('网关非流式 JSON attempt context 回归通过：成功与错误响应各最多完整解析一次，协议选择边界正确')

function request(path: string): Request {
  return {
    method: 'POST',
    path,
    originalUrl: path,
    headers: {},
    header() {
      return undefined
    }
  } as unknown as Request
}

function account(input: {
  providerCode: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
}): UpstreamAccount {
  return {
    id: 'non-stream-json-context-account',
    name: 'non-stream-json-context-account',
    type: 'api_key',
    credentials: {},
    ...input
  } as unknown as UpstreamAccount
}
