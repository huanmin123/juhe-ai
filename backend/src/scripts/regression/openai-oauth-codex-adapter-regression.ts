import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import type { Request } from 'express'

import {
  buildOpenAIOAuthCodexRequestParts,
  isolateOpenAIOAuthCodexSessionId,
  OpenAIOAuthCodexAdapterError
} from '../../modules/gateway/adapters/gpt-codex/oauth-adapter.js'
import {
  buildUpstreamRequestBody,
  buildUpstreamHeaders,
  isEffectiveOpenAIStreamRequest
} from '../../modules/gateway/upstream/request.js'
import {
  gatewayJsonBodyInlineParseMaxBytes,
  gatewayJsonBodyLargeWarningBytes,
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../../modules/gateway/request/body.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  parseGatewayJsonBodyInWorker,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'
import {
  applyOpenAIClientCompatibilityHeaders,
  buildOpenAIClientCompatibilityBody
} from '../../modules/gateway/protocols/openai-v1/api-key-client-compatibility.js'

type TestRequest = GatewayRawBodyRequest

const account = {
  id: 'acct_owner_oauth',
  apiKey: 'oauth-access-token',
  type: 'oauth',
  credentials: {
    account_id: 'chatgpt-account'
  }
}

const identity = {
  systemAccountId: 'sys_user_a',
  apiKeyId: 'key_a',
  groupId: 'group_a'
}

async function main(): Promise<void> {
  await testResponsesBodyNormalization()
  await testCompactBodyNormalization()
  await testOAuthAccountRequestOverrides()
  await testOAuthCompactRequestOverrides()
  await testOAuthFlexRequestOverrideRejection()
  await testHeaderAllowlistAndDefaults()
  await testOldCodexHeadersAreRaisedToCompatibilityFloor()
  await testInvalidAttestationRejection()
  await testSessionIsolation()
  await testInvalidBodyRejection()
  await testLargeBodyWorkerNormalization()
  await testLargeApiKeyLiteBodyWorkerNormalization()
  await testMediumBodyDeferredMiddlewareToOAuthWorker()
  await testLargeBodyDeferredMiddlewareToOAuthWorker()
  await testGatewayJsonWorkerConcurrentParsing()
  await testRequiredBodyFieldRejection()
  testOAuthEffectiveStreamSemantics()
  testApiKeyPassthroughUnchanged()
  console.log('OpenAI OAuth Codex adapter regression passed')
}

async function testOAuthAccountRequestOverrides(): Promise<void> {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.6-sol',
    input: [],
    service_tier: 'auto',
    reasoning_effort: 'low',
    reasoning: {
      effort: 'medium',
      summary: 'detailed'
    }
  })
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, {
    ...account,
    credentials: {
      ...account.credentials,
      service_tier_override: 'priority',
      reasoning_effort_override: 'high'
    }
  }, identity, undefined, {
    requestOverrideModelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['low', 'medium', 'high']
    }
  })
  const body = parseBody(parts.body)

  assert.equal(body.service_tier, 'priority')
  assert.deepEqual(body.reasoning, {
    effort: 'high',
    summary: 'detailed',
    context: 'all_turns'
  })
  assert.equal(body.reasoning_effort, undefined)
  assert.equal(body.parallel_tool_calls, false)
}

async function testOAuthCompactRequestOverrides(): Promise<void> {
  const req = createRequest('/v1/responses/compact', {
    model: 'gpt-5.6-terra',
    input: [],
    service_tier: 'auto',
    reasoning: {
      effort: 'low',
      summary: 'auto'
    }
  })
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, {
    ...account,
    credentials: {
      ...account.credentials,
      service_tier_override: 'priority',
      reasoning_effort_override: 'max'
    }
  }, identity, undefined, {
    requestOverrideModelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['max']
    }
  })
  const body = parseBody(parts.body)

  assert.equal(body.service_tier, 'priority')
  assert.deepEqual(body.reasoning, {
    effort: 'low',
    summary: 'auto',
    context: 'all_turns'
  }, 'OAuth Lite compact 必须保留客户端 reasoning 并声明全部轮次 context')
  assert.equal(body.parallel_tool_calls, false, 'OAuth Lite compact 必须关闭并行工具调用')
}

async function testOAuthFlexRequestOverrideRejection(): Promise<void> {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.6-sol',
    input: []
  })
  await assert.rejects(async () => {
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, {
      ...account,
      credentials: {
        ...account.credentials,
        service_tier_override: 'flex'
      }
    }, identity)
  }, (error: unknown) => (
    error instanceof OpenAIOAuthCodexAdapterError
    && error.message === '当前 OAuth 上游适配器不支持服务等级 flex'
  ))
}

async function testResponsesBodyNormalization(): Promise<void> {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'hello',
    instructions: 'be concise',
    store: true,
    stream: false,
    metadata: { session_id: 'metadata-session' },
    max_output_tokens: 200,
    temperature: 0.7,
    top_p: 0.9,
    user: 'local-user',
    prompt_cache_retention: '24h',
    safety_identifier: 'unsafe-to-forward',
    stream_options: { include_usage: true },
    tools: [{ type: 'web_search_preview' }],
    tool_choice: { type: 'web_search_preview_2025_03_11' },
    service_tier: 'auto'
  }, {
    session_id: 'client-session',
    conversation_id: 'client-conversation',
    'content-type': 'application/json; charset=utf-8',
    cookie: 'secret=value',
    'x-forwarded-for': '127.0.0.1',
    'user-agent': 'OpenAI/Node'
  })
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  const body = parseBody(parts.body)

  assert.equal(body.store, false)
  assert.equal(body.stream, true)
  assert.equal(body.instructions, 'be concise')
  assert.deepEqual(body.input, [
    {
      type: 'message',
      role: 'user',
      content: [{ type: 'input_text', text: 'hello' }]
    }
  ])
  assert.equal((body.tools as Array<Record<string, unknown>>)[0].type, 'web_search')
  assert.equal((body.tool_choice as Record<string, unknown>).type, 'web_search')
  assert.equal(body.metadata, undefined)
  assert.equal(body.max_output_tokens, undefined)
  assert.equal(body.temperature, undefined)
  assert.equal(body.top_p, undefined)
  assert.equal(body.user, undefined)
  assert.equal(body.prompt_cache_retention, undefined)
  assert.equal(body.safety_identifier, undefined)
  assert.equal(body.stream_options, undefined)
  assert.equal(body.service_tier, 'auto', 'OAuth 请求必须保留客户端 service_tier，不能只允许 priority')
  assert.equal(body.session_id, undefined)
  assert.equal(body.conversation_id, undefined)
  assert.equal(typeof body.prompt_cache_key, 'string')

  assert.equal(parts.headers.get('content-type'), 'application/json')
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('cookie'), null)
  assert.equal(parts.headers.get('x-forwarded-for'), null)
  assert.equal(parts.headers.get('user-agent'), 'codex_cli_rs/0.144.4')
  assert.equal(parts.headers.get('version'), null)
  assert.equal(parts.headers.get('openai-beta'), null)
  assert.equal(typeof parts.headers.get('session-id'), 'string')
  assert.equal(typeof parts.headers.get('thread-id'), 'string')
  assert.equal(parts.headers.get('session_id'), null)
  assert.equal(parts.headers.get('conversation_id'), null)
}

async function testCompactBodyNormalization(): Promise<void> {
  const req = createRequest('/v1/responses/compact', {
    model: 'gpt-5.3-codex',
    input: [{ role: 'system', content: 'developer context' }],
    previous_response_id: 'resp_123',
    instructions: 'compact this',
    store: false,
    stream: true,
    prompt_cache_key: 'client-cache',
    tools: [{ type: 'web_search_preview' }],
    metadata: { session_id: 'metadata-session' }
  })
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  const body = parseBody(parts.body)

  assert.equal(body.store, undefined)
  assert.equal(body.stream, undefined)
  assert.equal(body.prompt_cache_key, undefined)
  assert.equal(body.tools, undefined)
  assert.equal(body.metadata, undefined)
  assert.equal(body.previous_response_id, 'resp_123')
  assert.equal((body.input as Array<Record<string, unknown>>)[0].role, 'developer')
  assert.equal(parts.headers.get('accept'), 'application/json')
}

async function testHeaderAllowlistAndDefaults(): Promise<void> {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: [],
    prompt_cache_key: 'cache-a'
  }, {
    accept: 'application/json',
    'accept-language': 'zh-CN,zh;q=0.9',
    authorization: 'Bearer local-key',
    'content-type': 'application/json; charset=utf-8',
    originator: 'codex_vscode',
    'user-agent': 'codex_vscode/1.2.3',
    version: '1.2.3',
    'openai-beta': 'responses=experimental',
    'x-client-request-id': 'client-request',
    'x-codex-turn-state': 'turn-state',
    'x-codex-turn-metadata': 'turn-metadata',
    'x-oai-attestation': 'device-proof',
    cookie: 'secret=value',
    'x-real-ip': '127.0.0.1'
  })
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)

  assert.equal(parts.headers.get('authorization'), 'Bearer oauth-access-token')
  assert.equal(parts.headers.get('chatgpt-account-id'), 'chatgpt-account')
  assert.equal(parts.headers.get('content-type'), 'application/json')
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('accept-language'), 'zh-CN,zh;q=0.9')
  assert.equal(parts.headers.get('originator'), 'codex_vscode')
  assert.equal(parts.headers.get('user-agent'), 'codex_vscode/0.144.4')
  assert.equal(parts.headers.get('version'), null)
  assert.equal(parts.headers.get('openai-beta'), null)
  assert.equal(parts.headers.get('x-client-request-id'), 'client-request')
  assert.equal(parts.headers.get('x-codex-turn-state'), 'turn-state')
  assert.equal(parts.headers.get('x-codex-turn-metadata'), 'turn-metadata')
  assert.equal(parts.headers.get('x-oai-attestation'), 'device-proof')
  assert.equal(parts.headers.get('cookie'), null)
  assert.equal(parts.headers.get('x-real-ip'), null)
}

async function testOldCodexHeadersAreRaisedToCompatibilityFloor(): Promise<void> {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.6-sol',
    input: []
  }, {
    originator: 'codex_cli_rs',
    'user-agent': 'codex_cli_rs/0.125.0',
    version: '0.125.0'
  })
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)

  assert.equal(parts.headers.get('originator'), 'codex_cli_rs')
  assert.equal(parts.headers.get('user-agent'), 'codex_cli_rs/0.144.4')
  assert.equal(parts.headers.get('version'), null)
  assert.equal(parts.headers.get('openai-beta'), null)
  assert.equal(parts.headers.get('x-openai-internal-codex-responses-lite'), 'true')
}

async function testInvalidAttestationRejection(): Promise<void> {
  for (const value of ['device-proof\r\ninjected: true', 'x'.repeat(32 * 1024 + 1)]) {
    const req = createRequest('/v1/responses', {
      model: 'gpt-5.3-codex',
      input: []
    }, {
      'x-oai-attestation': value
    })
    await assert.rejects(
      buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity),
      (error: unknown) => error instanceof OpenAIOAuthCodexAdapterError
        && error.code === 'invalid_openai_oauth_codex_attestation'
    )
  }
}

async function testSessionIsolation(): Promise<void> {
  const reqA = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: [],
    prompt_cache_key: 'same-cache'
  })
  const reqB = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: [],
    prompt_cache_key: 'same-cache'
  })
  const partsA = await buildOpenAIOAuthCodexRequestParts(reqA, reqA.headers, account, identity)
  const partsB = await buildOpenAIOAuthCodexRequestParts(reqB, reqB.headers, account, { ...identity, apiKeyId: 'key_b' })
  const partsSameKeyOtherGroup = await buildOpenAIOAuthCodexRequestParts(reqA, reqA.headers, account, { ...identity, groupId: 'group_b' })
  const switchedAccount = { ...account, id: 'acct_switched_oauth' }
  const partsSwitchedAccount = await buildOpenAIOAuthCodexRequestParts(reqA, reqA.headers, switchedAccount, identity)
  const bodyA = parseBody(partsA.body)
  const bodyB = parseBody(partsB.body)
  const sameKeyOtherGroupBody = parseBody(partsSameKeyOtherGroup.body)
  const switchedAccountBody = parseBody(partsSwitchedAccount.body)

  assert.notEqual(bodyA.prompt_cache_key, 'same-cache')
  assert.notEqual(bodyB.prompt_cache_key, 'same-cache')
  assert.notEqual(bodyA.prompt_cache_key, bodyB.prompt_cache_key)
  assert.equal(bodyA.prompt_cache_key, sameKeyOtherGroupBody.prompt_cache_key)
  assert.equal(bodyA.prompt_cache_key, switchedAccountBody.prompt_cache_key)
  assert.notEqual(partsA.headers.get('session-id'), partsB.headers.get('session-id'))
  assert.equal(partsA.headers.get('session_id'), null)
  assert.equal(
    bodyA.prompt_cache_key,
    isolateOpenAIOAuthCodexSessionId('same-cache', account, identity)
  )
}

async function testInvalidBodyRejection(): Promise<void> {
  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', ['not-object'])
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', {
      input: [],
      instructions: { text: 'invalid' }
    })
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', undefined, { 'content-type': 'application/json' }, '{not-json')
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.rejects(async () => {
    const rawBodyText = JSON.stringify({
      model: 'gpt-5.3-codex',
      input: 'x'.repeat(gatewayJsonBodyLargeWarningBytes),
      instructions: { text: 'invalid' }
    })
    const req = createRequest('/v1/responses', undefined, { 'content-type': 'application/json' }, rawBodyText)
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)
}

async function testLargeBodyWorkerNormalization(): Promise<void> {
  const requestBody = {
    model: 'gpt-5.6-sol',
    input: 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 1024),
    store: true,
    stream: false,
    metadata: { session_id: 'large-body-session' },
    tools: [{ type: 'web_search_preview_2025_03_11' }],
    tool_choice: {
      type: 'allowed_tools',
      tools: [{ type: 'web_search_preview' }]
    },
    service_tier: 'priority'
  }
  const rawBodyText = JSON.stringify(requestBody)
  assert.ok(Buffer.byteLength(rawBodyText) > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(Buffer.byteLength(rawBodyText) < gatewayJsonBodyLargeWarningBytes)

  const req = createRequest(
    '/v1/responses',
    undefined,
    { 'content-type': 'application/json' },
    rawBodyText
  )
  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  const body = parseBody(parts.body)
  const input = body.input as Array<{ content?: Array<{ text?: string }> }>

  assert.equal(body.store, false)
  assert.equal(body.stream, true)
  assert.equal(body.instructions, '')
  assert.equal(body.metadata, undefined)
  assert.equal((body.tools as Array<Record<string, unknown>>)[0].type, 'web_search')
  assert.equal(((body.tool_choice as { tools?: Array<Record<string, unknown>> }).tools ?? [])[0]?.type, 'web_search')
  assert.equal(body.service_tier, 'priority')
  assert.deepEqual(body.reasoning, { context: 'all_turns' })
  assert.equal(body.parallel_tool_calls, false)
  assert.equal(input[0]?.content?.[0]?.text, requestBody.input)
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('x-openai-internal-codex-responses-lite'), 'true')
}

async function testLargeApiKeyLiteBodyWorkerNormalization(): Promise<void> {
  const rawBodyText = JSON.stringify({
    model: 'gpt-5.6-sol',
    input: 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 1024),
    reasoning: { effort: 'high' },
    parallel_tool_calls: true
  })
  const req = createRequest('/v1/responses', undefined, { 'content-type': 'application/json' }, rawBodyText)
  const bodyBuffer = await buildOpenAIClientCompatibilityBody(req, undefined, {
    modelOverride: 'gpt-5.6-sol',
    requestClientCompatibility: 'codex_responses'
  })
  assert.ok(bodyBuffer)
  const body = JSON.parse(bodyBuffer.toString('utf8')) as Record<string, unknown>
  const headers = new Headers()
  applyOpenAIClientCompatibilityHeaders(req, headers, {
    modelOverride: 'gpt-5.6-sol',
    requestClientCompatibility: 'codex_responses'
  })

  assert.deepEqual(body.reasoning, { effort: 'high', context: 'all_turns' })
  assert.equal(body.parallel_tool_calls, false)
  assert.equal(headers.get('x-openai-internal-codex-responses-lite'), 'true')
}

async function testMediumBodyDeferredMiddlewareToOAuthWorker(): Promise<void> {
  const requestBody = {
    model: 'gpt-5.3-codex',
    input: 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 32 * 1024),
    stream: false,
    metadata: { session_id: 'middleware-medium-body-session' },
    tools: [{ type: 'web_search_preview_2025_03_11' }]
  }
  const rawBodyText = JSON.stringify(requestBody)
  const rawBody = Buffer.from(rawBodyText)
  assert.ok(rawBody.byteLength > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(rawBody.byteLength < gatewayJsonBodyLargeWarningBytes)

  const req = createRequest(
    '/v1/responses',
    rawBody,
    { 'content-type': 'application/json' },
    rawBodyText
  )
  let nextCalled = false

  await captureGatewayRawBody(req, new EventEmitter() as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.equal(getGatewayRequestBodyState(req)?.jsonParseStatus, 'deferred_large_json')
  assert.equal(getGatewayRequestBodyState(req)?.model, 'gpt-5.3-codex')

  const originalJsonParse = JSON.parse
  try {
    JSON.parse = (() => {
      throw new Error('主进程不应同步解析 256KB 以上 OAuth Codex JSON 请求体')
    }) as typeof JSON.parse
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  } finally {
    JSON.parse = originalJsonParse
  }

  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  const body = parseBody(parts.body)
  const input = body.input as Array<{ content?: Array<{ text?: string }> }>

  assert.equal(body.stream, true)
  assert.equal(body.store, false)
  assert.equal(body.metadata, undefined)
  assert.equal(input[0]?.content?.[0]?.text, requestBody.input)
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
}

async function testLargeBodyDeferredMiddlewareToOAuthWorker(): Promise<void> {
  const requestBody = {
    model: 'gpt-5.3-codex',
    input: 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 1024),
    stream: false,
    metadata: { session_id: 'middleware-large-body-session' },
    tools: [{ type: 'web_search_preview_2025_03_11' }]
  }
  const rawBodyText = JSON.stringify(requestBody)
  const rawBody = Buffer.from(rawBodyText)
  assert.ok(rawBody.byteLength > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(rawBody.byteLength < gatewayJsonBodyLargeWarningBytes)

  const req = createRequest(
    '/v1/responses',
    rawBody,
    { 'content-type': 'application/json' },
    rawBodyText
  )
  let nextCalled = false

  await captureGatewayRawBody(req, new EventEmitter() as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.equal(getGatewayRequestBodyState(req)?.jsonParseStatus, 'deferred_large_json')
  assert.equal(getGatewayRequestBodyState(req)?.model, 'gpt-5.3-codex')
  assert.equal(getGatewayRequestBodyState(req)?.stream, false)
  assert.equal(getGatewayRequestBodyState(req)?.imageGeneration, false)

  const parts = await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  const body = parseBody(parts.body)

  assert.equal(body.stream, true)
  assert.equal(body.store, false)
  assert.equal(body.metadata, undefined)
  assert.equal((body.tools as Array<Record<string, unknown>>)[0].type, 'web_search')
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
}

async function testRequiredBodyFieldRejection(): Promise<void> {
  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', {})
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', {
      model: '   ',
      input: []
    })
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', {
      model: 'gpt-5.3-codex'
    })
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.rejects(async () => {
    const req = createRequest('/v1/responses', {
      model: 'gpt-5.3-codex',
      input: { text: 'invalid' }
    })
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  }, OpenAIOAuthCodexAdapterError)

  await assert.doesNotReject(async () => {
    const req = createRequest('/v1/responses/compact', {
      model: 'gpt-5.3-codex'
    })
    await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity)
  })
}

async function testGatewayJsonWorkerConcurrentParsing(): Promise<void> {
  const inputs = Array.from({ length: 24 }, (_, index) => Buffer.from(JSON.stringify({ index, ok: true }), 'utf8'))
  const results = await Promise.all(inputs.map((input) => parseGatewayJsonBodyInWorker(input))) as Array<{ index: number; ok: boolean }>
  assert.deepEqual(results.map((result) => result.index), inputs.map((_, index) => index))
  assert.equal(results.every((result) => result.ok === true), true)
}

function testOAuthEffectiveStreamSemantics(): void {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: [],
    stream: false
  })
  assert.equal(isEffectiveOpenAIStreamRequest(req, account), true)

  const compactReq = createRequest('/v1/responses/compact', {
    model: 'gpt-5.3-codex',
    stream: true
  })
  assert.equal(isEffectiveOpenAIStreamRequest(compactReq, account), false)

  const apiKeyReq = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: [],
    stream: false
  })
  assert.equal(isEffectiveOpenAIStreamRequest(apiKeyReq, { type: 'api_key' }), false)
}

function testApiKeyPassthroughUnchanged(): void {
  const req = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    store: true,
    temperature: 0.7
  }, {
    'content-type': 'application/json; charset=utf-8',
    'x-custom-header': 'kept-for-api-key',
    authorization: 'Bearer sk-client',
    'openai-api-key': 'sk-client-openai-api-key',
    'x-api-key': 'sk-client-x-api-key',
    'x-goog-api-key': 'sk-client-google-api-key',
    'api-key': 'sk-client-api-key',
    cookie: 'secret=value'
  })
  const apiKeyAccount = {
    apiKey: 'sk-upstream',
    type: 'api_key',
    credentials: {}
  }
  const body = buildUpstreamRequestBody(req)
  const headers = buildUpstreamHeaders(req.headers, apiKeyAccount)

  assert.ok(Buffer.isBuffer(body))
  assert.equal(headers.get('authorization'), 'Bearer sk-upstream')
  assert.equal(headers.get('content-type'), 'application/json; charset=utf-8')
  assert.equal(headers.get('x-custom-header'), 'kept-for-api-key')
  assert.equal(headers.get('openai-api-key'), null)
  assert.equal(headers.get('x-api-key'), null)
  assert.equal(headers.get('x-goog-api-key'), null)
  assert.equal(headers.get('api-key'), null)
  assert.equal(headers.get('cookie'), null)
}

function createRequest(
  originalUrl: string,
  body: unknown,
  headers: Record<string, string | string[] | undefined> = {},
  rawBodyText?: string
): TestRequest {
  const serialized = rawBodyText ?? JSON.stringify(body ?? {})
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {
      'content-type': 'application/json',
      ...headers
    },
    body,
    rawBody: Buffer.from(serialized)
  } as TestRequest
}

function parseBody(body: string | undefined): Record<string, unknown> {
  assert.equal(typeof body, 'string')
  return JSON.parse(body as string) as Record<string, unknown>
}

try {
  await main()
} finally {
  await stopGatewayJsonParseWorker()
}
