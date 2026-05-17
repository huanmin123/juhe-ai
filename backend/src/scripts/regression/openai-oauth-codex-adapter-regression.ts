import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  buildOpenAIOAuthCodexRequestParts,
  isolateOpenAIOAuthCodexSessionId,
  OpenAIOAuthCodexAdapterError
} from '../../modules/gateway/openai-oauth-codex-adapter.js'
import {
  buildUpstreamRequestBody,
  buildUpstreamHeaders,
  isEffectiveOpenAIStreamRequest
} from '../../modules/gateway/openai-gateway-upstream.js'
import { gatewayJsonBodyLargeWarningBytes } from '../../modules/gateway/openai-gateway-request-body.js'
import { stopGatewayJsonParseWorker } from '../../modules/gateway/openai-gateway-json-parser.js'

type TestRequest = Request & { rawBody?: Buffer }

const account = {
  id: 'acct_owner_oauth',
  apiKey: 'oauth-access-token',
  passthroughEnabled: true,
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
  await testHeaderAllowlistAndDefaults()
  await testSessionIsolation()
  await testInvalidBodyRejection()
  await testLargeBodyCompatibility()
  await testRequiredBodyFieldRejection()
  testOAuthEffectiveStreamSemantics()
  testApiKeyPassthroughUnchanged()
  console.log('OpenAI OAuth Codex adapter regression passed')
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
  assert.equal(body.service_tier, undefined)
  assert.equal(body.session_id, undefined)
  assert.equal(body.conversation_id, undefined)
  assert.equal(typeof body.prompt_cache_key, 'string')

  assert.equal(parts.headers.get('content-type'), 'application/json')
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('cookie'), null)
  assert.equal(parts.headers.get('x-forwarded-for'), null)
  assert.equal(parts.headers.get('user-agent'), 'codex_cli_rs/0.125.0')
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
  assert.equal(parts.headers.get('user-agent'), 'codex_vscode/1.2.3')
  assert.equal(parts.headers.get('version'), '1.2.3')
  assert.equal(parts.headers.get('openai-beta'), 'responses=experimental')
  assert.equal(parts.headers.get('x-client-request-id'), 'client-request')
  assert.equal(parts.headers.get('x-codex-turn-state'), 'turn-state')
  assert.equal(parts.headers.get('x-codex-turn-metadata'), 'turn-metadata')
  assert.equal(parts.headers.get('cookie'), null)
  assert.equal(parts.headers.get('x-real-ip'), null)
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
  const bodyA = parseBody(partsA.body)
  const bodyB = parseBody(partsB.body)

  assert.notEqual(bodyA.prompt_cache_key, 'same-cache')
  assert.notEqual(bodyB.prompt_cache_key, 'same-cache')
  assert.notEqual(bodyA.prompt_cache_key, bodyB.prompt_cache_key)
  assert.notEqual(partsA.headers.get('session_id'), partsB.headers.get('session_id'))
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

async function testLargeBodyCompatibility(): Promise<void> {
  const requestBody = {
    model: 'gpt-5.3-codex',
    input: 'x'.repeat(gatewayJsonBodyLargeWarningBytes),
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
  assert.ok(Buffer.byteLength(rawBodyText) > gatewayJsonBodyLargeWarningBytes)

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
  assert.equal(input[0]?.content?.[0]?.text, requestBody.input)
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
    cookie: 'secret=value'
  })
  const apiKeyAccount = {
    apiKey: 'sk-upstream',
    passthroughEnabled: true,
    type: 'api_key',
    credentials: {}
  }
  const body = buildUpstreamRequestBody(req, apiKeyAccount.passthroughEnabled)
  const headers = buildUpstreamHeaders(req.headers, apiKeyAccount)

  assert.ok(Buffer.isBuffer(body))
  assert.equal(headers.get('authorization'), 'Bearer sk-upstream')
  assert.equal(headers.get('content-type'), 'application/json; charset=utf-8')
  assert.equal(headers.get('x-custom-header'), 'kept-for-api-key')
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
