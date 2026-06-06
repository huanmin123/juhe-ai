import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'

import { requestModel } from '../../modules/gateway/openai-gateway-usage.js'
import {
  buildOpenAICodexUpstreamUrls,
  buildUpstreamUrl,
  isOpenAIModelsRequest
} from '../../modules/gateway/openai-gateway-route-helpers.js'
import { buildUpstreamHeaders, buildUpstreamRequestBody, buildUpstreamRequestParts, isEffectiveOpenAIStreamRequest } from '../../modules/gateway/openai-gateway-upstream.js'
import {
  createGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  gatewayJsonBodyLargeWarningBytes,
  gatewayRawBodyHardLimitBytes,
  gatewayTextRawBodyHardLimitBytes,
  type GatewayRawBodyRequest
} from '../../modules/gateway/openai-gateway-request-body.js'
import { captureGatewayRawBody } from '../../modules/gateway/openai-gateway-request-body-middleware.js'
import { stopGatewayJsonParseWorker } from '../../modules/gateway/openai-gateway-json-parser.js'

type TestRequest = GatewayRawBodyRequest
type MockResponse = EventEmitter & {
  headersSent: boolean
  destroyed: boolean
  statusCode: number
  body?: unknown
  status: (statusCode: number) => MockResponse
  json: (body: unknown) => MockResponse
}

const apiKeyAccount = {
  apiKey: 'sk-upstream',
  type: 'api_key',
  credentials: {}
}

async function main(): Promise<void> {
  testRawBodyPassthrough()
  await testOpenAIStandardRequestPartsPassthrough()
  await testCodexResponsesCompatibilityRequestParts()
  await testCodexResponsesCompatibilityKeepsExplicitToolSettings()
  await testCodexResponsesCompatibilityDoesNotRewriteChatCompletions()
  testApiKeyHeaderFiltering()
  testOpenAIAccountHeadersAreNotClientOrCredentialControlled()
  testOpenAIBetaPreservedFromClient()
  testOpenAIUpstreamUrlNormalization()
  testOpenAIClientPathNormalization()
  testParsedJsonBodyPassthroughForGatewayMetadata()
  await testMediumJsonBodyDeferredByGatewayMiddleware()
  await testOversizeJsonBodyRejectedByGatewayMiddleware()
  await testLargeImageJsonBodyAllowedByGatewayMiddleware()
  await testDeferredJsonBodyImageToolMetadataScanned()
  await testDeferredInvalidJsonMarkedWithoutWorkerParse()
  await testDeferredInvalidJsonPrimitiveMarkedWithoutWorkerParse()
  console.log('OpenAI API Key passthrough regression passed')
}

function testRawBodyPassthrough(): void {
  const rawBody = Buffer.from('{"model":"gpt-5.4","input":"hello","metadata":{"keep":true}}')
  const req = createRequest({ model: 'gpt-5.4', input: 'hello' }, { 'content-type': 'application/json' }, rawBody)
  const body = buildUpstreamRequestBody(req)

  assert.ok(Buffer.isBuffer(body))
  assert.equal(Buffer.compare(body, rawBody), 0)
}

async function testOpenAIStandardRequestPartsPassthrough(): Promise<void> {
  const rawBody = Buffer.from('{"model":"gpt-5.4","input":"hello","stream":false}')
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/responses')
  const parts = await buildUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'openai_standard'
  }, testIdentity)

  assert.ok(Buffer.isBuffer(parts.body))
  assert.equal(Buffer.compare(parts.body, rawBody), 0)
  assert.equal(parts.headers.get('authorization'), 'Bearer sk-upstream')
}

async function testCodexResponsesCompatibilityRequestParts(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.5',
    input: '只输出 OK',
    include: ['file_search_call.results'],
    tools: [
      { type: 'web_search_preview' },
      { type: 'web_search_preview_2025_03_11' }
    ],
    tool_choice: {
      type: 'allowed_tools',
      tools: [{ type: 'web_search_preview' }]
    },
    stream: false,
    store: true,
    max_output_tokens: 16,
    max_completion_tokens: 16,
    temperature: 0.2,
    top_p: 0.9,
    context_management: [{ type: 'compact' }],
    truncation: 'disabled',
    user: 'local-user'
  }))
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/responses')
  const parts = await buildUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'codex_responses'
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.model, 'gpt-5.5')
  assert.equal(body.stream, true)
  assert.equal(body.store, false)
  assert.equal(body.instructions, '')
  assert.deepEqual(body.include, ['file_search_call.results', 'reasoning.encrypted_content'])
  assert.equal(body.tools[0]?.type, 'web_search')
  assert.equal(body.tools[1]?.type, 'web_search')
  assert.equal(body.tool_choice?.type, 'allowed_tools')
  assert.equal(body.tool_choice?.tools?.[0]?.type, 'web_search')
  assert.equal(body.parallel_tool_calls, true)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'max_output_tokens'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'max_completion_tokens'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'temperature'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'top_p'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'context_management'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'truncation'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'user'), false)

  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('content-type'), 'application/json')
  assert.equal(parts.headers.get('originator'), 'codex_cli_rs')
  assert.equal(parts.headers.get('user-agent'), 'codex_cli_rs/0.125.0')
  assert.equal(parts.headers.get('version'), '0.125.0')
  assert.equal(parts.headers.get('openai-beta'), 'responses=experimental')

  const input = body.input
  assert.ok(Array.isArray(input))
  assert.equal(input[0]?.role, 'user')
  assert.equal(input[0]?.content?.[0]?.type, 'input_text')
  assert.equal(input[0]?.content?.[0]?.text, '只输出 OK')
}

async function testCodexResponsesCompatibilityKeepsExplicitToolSettings(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.5',
    input: [
      {
        type: 'message',
        role: 'system',
        content: [
          {
            type: 'input_text',
            text: '作为开发者指令处理'
          }
        ]
      },
      {
        type: 'message',
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: '只输出 OK'
          }
        ]
      }
    ],
    tools: [{ type: 'function', name: 'noop' }],
    tool_choice: { type: 'function', name: 'noop' },
    parallel_tool_calls: false
  }))
  const req = createRequest(undefined, {
    'accept': 'application/json',
    'content-type': 'text/plain',
    'openai-beta': 'responses=v1',
    'originator': 'codex_vscode',
    'user-agent': 'codex_vscode/1.2.3',
    'version': '1.2.3'
  }, rawBody, '/responses')
  const parts = await buildUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'codex_responses'
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.input[0]?.role, 'developer')
  assert.equal(body.input[1]?.role, 'user')
  assert.deepEqual(body.tools, [{ type: 'function', name: 'noop' }])
  assert.deepEqual(body.tool_choice, { type: 'function', name: 'noop' })
  assert.equal(body.parallel_tool_calls, false)
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('content-type'), 'application/json')
  assert.equal(parts.headers.get('openai-beta'), 'responses=v1')
  assert.equal(parts.headers.get('originator'), 'codex_vscode')
  assert.equal(parts.headers.get('user-agent'), 'codex_vscode/1.2.3')
  assert.equal(parts.headers.get('version'), '1.2.3')
}

async function testCodexResponsesCompatibilityDoesNotRewriteChatCompletions(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: 'hello' }],
    stream: false
  }))
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/chat/completions')
  const parts = await buildUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'codex_responses'
  }, testIdentity)

  assert.ok(Buffer.isBuffer(parts.body))
  assert.equal(Buffer.compare(parts.body, rawBody), 0)
  assert.equal(parts.headers.get('accept'), null)
  assert.equal(parts.headers.get('content-type'), 'application/json')
  assert.equal(parts.headers.get('originator'), null)
  assert.equal(parts.headers.get('openai-beta'), null)
}

function testApiKeyHeaderFiltering(): void {
  const headers = buildUpstreamHeaders({
    authorization: 'Bearer local-key',
    'content-type': 'application/json; charset=utf-8',
    accept: 'text/event-stream',
    cookie: 'secret=value',
    'x-api-key': 'local-x-api-key',
    'x-goog-api-key': 'local-google-key',
    'api-key': 'local-api-key',
    'chatgpt-account-id': 'client-chatgpt-account',
    'accept-encoding': 'gzip, br',
    'content-encoding': 'gzip',
    'content-length': '123',
    host: '127.0.0.1:3000',
    connection: 'keep-alive',
    'x-forwarded-for': '127.0.0.1',
    'x-forwarded-proto': 'https',
    'x-real-ip': '127.0.0.1',
    forwarded: 'for=127.0.0.1',
    via: 'proxy',
    'cf-connecting-ip': '127.0.0.1',
    'x-request-id': 'client-request',
    traceparent: '00-abc-abc-01',
    tracestate: 'state',
    baggage: 'local=value',
    'x-amzn-trace-id': 'Root=1',
    'x-cloud-trace-context': 'trace/span',
    'x-stainless-lang': 'js',
    'x-stainless-package-version': '4.0.0',
    'x-openai-client-user-agent': 'sdk-noise',
    'x-vercel-id': 'iad1::abc',
    'idempotency-key': 'idem-123',
    'x-custom-header': 'kept'
  }, apiKeyAccount)

  assert.equal(headers.get('authorization'), 'Bearer sk-upstream')
  assert.equal(headers.get('content-type'), 'application/json; charset=utf-8')
  assert.equal(headers.get('accept'), 'text/event-stream')
  assert.equal(headers.get('idempotency-key'), 'idem-123')
  assert.equal(headers.get('x-custom-header'), 'kept')

  for (const name of [
    'cookie',
    'x-api-key',
    'x-goog-api-key',
    'api-key',
    'chatgpt-account-id',
    'accept-encoding',
    'content-encoding',
    'content-length',
    'host',
    'connection',
    'x-forwarded-for',
    'x-forwarded-proto',
    'x-real-ip',
    'forwarded',
    'via',
    'cf-connecting-ip',
    'x-request-id',
    'traceparent',
    'tracestate',
    'baggage',
    'x-amzn-trace-id',
    'x-cloud-trace-context',
    'x-stainless-lang',
    'x-stainless-package-version',
    'x-openai-client-user-agent',
    'x-vercel-id'
  ]) {
    assert.equal(headers.get(name), null, `${name} should be stripped`)
  }
}

function testOpenAIAccountHeadersAreNotClientOrCredentialControlled(): void {
  const headers = buildUpstreamHeaders({
    'openai-organization': 'client-org',
    'openai-project': 'client-project',
    'openai-beta': 'responses=experimental'
  }, {
    ...apiKeyAccount,
    credentials: {
      openai_organization: 'acct-org',
      openai_project: 'acct-project',
      openai_beta: 'acct-beta'
    }
  })

  assert.equal(headers.get('openai-organization'), null)
  assert.equal(headers.get('openai-project'), null)
  assert.equal(headers.get('openai-beta'), 'responses=experimental')
}

function testOpenAIBetaPreservedFromClient(): void {
  const clientHeaders = buildUpstreamHeaders({
    'openai-beta': 'assistants=v2'
  }, apiKeyAccount)
  assert.equal(clientHeaders.get('openai-beta'), 'assistants=v2')
}

function testOpenAIUpstreamUrlNormalization(): void {
  assert.equal(buildUpstreamUrl('https://api.openai.com', '/responses'), 'https://api.openai.com/v1/responses')
  assert.equal(buildUpstreamUrl('https://api.openai.com', '/v1/responses'), 'https://api.openai.com/v1/responses')
  assert.equal(buildUpstreamUrl('https://api.openai.com/v1', '/responses'), 'https://api.openai.com/v1/responses')
  assert.equal(buildUpstreamUrl('https://api.openai.com/v1', '/v1/responses'), 'https://api.openai.com/v1/responses')
  assert.equal(buildUpstreamUrl('https://example.com/openai/v1', '/responses?stream=true'), 'https://example.com/openai/v1/responses?stream=true')
  assert.equal(buildUpstreamUrl('https://example.com/openai/v1/', 'v1/chat/completions'), 'https://example.com/openai/v1/chat/completions')
}

function testOpenAIClientPathNormalization(): void {
  assert.equal(isOpenAIModelsRequest(createRequest(undefined, {}, undefined, '/models', 'GET')), true)
  assert.equal(isOpenAIModelsRequest(createRequest(undefined, {}, undefined, '/v1/models', 'GET')), true)
  assert.equal(buildUpstreamUrl('https://api.openai.com', '/models'), 'https://api.openai.com/v1/models')
  assert.equal(buildUpstreamUrl('https://api.openai.com/v1', '/models?limit=20'), 'https://api.openai.com/v1/models?limit=20')
  assert.equal(buildUpstreamUrl('https://api.openai.com', '/chat/completions'), 'https://api.openai.com/v1/chat/completions')
  assert.equal(buildUpstreamUrl('https://api.openai.com/v1', '/v1/images/generations'), 'https://api.openai.com/v1/images/generations')
  assert.deepEqual(
    buildOpenAICodexUpstreamUrls(createRequest({ input: 'hello' }, {}, undefined, '/responses')),
    ['https://chatgpt.com/backend-api/codex/responses']
  )
  assert.deepEqual(
    buildOpenAICodexUpstreamUrls(createRequest({ input: 'hello' }, {}, undefined, '/v1/responses')),
    ['https://chatgpt.com/backend-api/codex/responses']
  )
  assert.deepEqual(
    buildOpenAICodexUpstreamUrls(createRequest({ input: 'hello' }, {}, undefined, '/chat/completions')),
    []
  )
}

function testParsedJsonBodyPassthroughForGatewayMetadata(): void {
  const body = {
    model: 'gpt-5.4',
    stream: true,
    input: 'hello'
  }
  const rawBody = Buffer.from(JSON.stringify(body))
  const req = createRequest(body, { 'content-type': 'application/json' }, rawBody)
  req.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType: 'application/json',
    jsonParseStatus: 'parsed',
    parsedBody: body
  })
  req.body = undefined

  const upstreamBody = buildUpstreamRequestBody(req)

  assert.ok(Buffer.isBuffer(upstreamBody))
  assert.equal(Buffer.compare(upstreamBody, rawBody), 0)
  assert.equal(requestModel(req), 'gpt-5.4')
  assert.equal(isEffectiveOpenAIStreamRequest(req, { type: 'api_key' }), true)
}

async function testMediumJsonBodyDeferredByGatewayMiddleware(): Promise<void> {
  const body = {
    model: 'gpt-5.4',
    stream: false,
    input: 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes)
  }
  const rawBody = Buffer.from(JSON.stringify(body))
  assert.ok(rawBody.length > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(rawBody.length < gatewayJsonBodyLargeWarningBytes)
  const req = createRequest(rawBody, { 'content-type': 'application/json' }, rawBody)
  let nextCalled = false

  await captureGatewayRawBody(req, new EventEmitter() as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.equal(req.gatewayRequestBody?.jsonParseStatus, 'deferred_large_json')
  assert.equal(req.gatewayRequestBody?.model, 'gpt-5.4')
  assert.equal(req.gatewayRequestBody?.stream, false)
  assert.notEqual(req.gatewayParsedJsonBodyAvailable, true, '超过主进程内联解析阈值的 JSON 不应在 server 事件循环完整解析')
  assert.equal(requestModel(req), 'gpt-5.4')
  assert.equal(isEffectiveOpenAIStreamRequest(req, { type: 'api_key' }), false)
}

async function testOversizeJsonBodyRejectedByGatewayMiddleware(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.4',
    stream: true,
    input: 'x'.repeat(gatewayTextRawBodyHardLimitBytes)
  }))
  assert.ok(rawBody.length > gatewayTextRawBodyHardLimitBytes)
  assert.ok(rawBody.length < gatewayRawBodyHardLimitBytes)
  const req = createRequest(rawBody, { 'content-type': 'application/json' }, rawBody)
  const res = createMockResponse()
  let nextCalled = false

  await captureGatewayRawBody(req, res as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, false)
  assert.equal(req.body, undefined)
  assert.equal(req.rawBody, undefined)
  assert.equal(req.gatewayRequestBody?.rawBodyBytes, rawBody.length)
  assert.equal(res.statusCode, 413)
  assert.deepEqual(res.body, {
    error: {
      message: '请求体过大',
      type: 'request_too_large'
    }
  })
}

async function testLargeImageJsonBodyAllowedByGatewayMiddleware(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.4',
    input: 'x'.repeat(gatewayTextRawBodyHardLimitBytes),
    tools: [{ type: 'image_generation', output_format: 'png' }],
    tool_choice: 'auto'
  }))
  assert.ok(rawBody.length > gatewayTextRawBodyHardLimitBytes)
  assert.ok(rawBody.length < gatewayRawBodyHardLimitBytes)
  const req = createRequest(rawBody, { 'content-type': 'application/json' }, rawBody)
  const res = createMockResponse()
  let nextCalled = false

  await captureGatewayRawBody(req, res as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.ok(req.rawBody)
  assert.equal(Buffer.compare(req.rawBody, rawBody), 0)
  assert.equal(req.gatewayRequestBody?.jsonParseStatus, 'deferred_large_json')
  assert.equal(req.gatewayRequestBody?.imageGeneration, true)
  assert.equal(req.gatewayRequestBody?.imageGenerationForced, false)
  assert.equal(res.statusCode, 200)
}

async function testDeferredJsonBodyImageToolMetadataScanned(): Promise<void> {
  const body = {
    model: 'gpt-5.4',
    input: 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 32 * 1024),
    tools: [{ type: 'image_generation', output_format: 'png' }],
    tool_choice: 'auto'
  }
  const rawBody = Buffer.from(JSON.stringify(body))
  assert.ok(rawBody.length > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(rawBody.length < gatewayRawBodyHardLimitBytes)
  const req = createRequest(rawBody, { 'content-type': 'application/json' }, rawBody)
  let nextCalled = false

  await captureGatewayRawBody(req, new EventEmitter() as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.equal(req.gatewayRequestBody?.jsonParseStatus, 'deferred_large_json')
  assert.equal(req.gatewayRequestBody?.model, 'gpt-5.4')
  assert.equal(req.gatewayRequestBody?.imageGeneration, true)
  assert.equal(req.gatewayRequestBody?.imageGenerationForced, false)
  assert.notEqual(req.gatewayParsedJsonBodyAvailable, true, '大 JSON optional 图像工具只应扫描顶层元数据')
}

async function testDeferredInvalidJsonMarkedWithoutWorkerParse(): Promise<void> {
  const rawBody = Buffer.from(`{"model":"gpt-5.4","input":${JSON.stringify('x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 32 * 1024))},`)
  assert.ok(rawBody.length > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(rawBody.length < gatewayRawBodyHardLimitBytes)
  const req = createRequest(rawBody, { 'content-type': 'application/json' }, rawBody)
  let nextCalled = false

  await captureGatewayRawBody(req, new EventEmitter() as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.equal(req.gatewayRequestBody?.jsonParseStatus, 'invalid_json')
  assert.equal(req.gatewayRequestBody?.model, 'gpt-5.4')
  assert.notEqual(req.gatewayParsedJsonBodyAvailable, true, '大 JSON 非法结构不应通过 worker 完整解析后才判定')
}

async function testDeferredInvalidJsonPrimitiveMarkedWithoutWorkerParse(): Promise<void> {
  const rawBody = Buffer.from(`{"model":${'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 32 * 1024)}}`)
  assert.ok(rawBody.length > gatewayJsonBodyInlineParseMaxBytes)
  assert.ok(rawBody.length < gatewayRawBodyHardLimitBytes)
  const req = createRequest(rawBody, { 'content-type': 'application/json' }, rawBody)
  let nextCalled = false

  await captureGatewayRawBody(req, new EventEmitter() as never, () => {
    nextCalled = true
  })

  assert.equal(nextCalled, true)
  assert.equal(req.body, undefined)
  assert.equal(req.gatewayRequestBody?.jsonParseStatus, 'invalid_json')
  assert.notEqual(req.gatewayParsedJsonBodyAvailable, true, '大 JSON 非法 primitive token 不应被延迟到上游才失败')
}

function createRequest(
  body: unknown,
  headers: Record<string, string | string[] | undefined> = {},
  rawBody = Buffer.from(JSON.stringify(body ?? {})),
  pathAndQuery = '/v1/responses',
  method = 'POST'
): TestRequest {
  const queryIndex = pathAndQuery.indexOf('?')
  const path = queryIndex >= 0 ? pathAndQuery.slice(0, queryIndex) : pathAndQuery
  return Object.assign(new EventEmitter(), {
    method,
    originalUrl: pathAndQuery,
    path: path || '/',
    headers,
    body,
    rawBody
  }) as TestRequest
}

function createMockResponse(): MockResponse {
  const response = Object.assign(new EventEmitter(), {
    headersSent: false,
    destroyed: false,
    statusCode: 200,
    body: undefined as unknown,
    status(statusCode: number) {
      response.statusCode = statusCode
      return response
    },
    json(body: unknown) {
      response.body = body
      response.headersSent = true
      return response
    }
  })
  return response
}

function parseJsonBuffer(value: Buffer | string | undefined): Record<string, any> {
  assert.ok(value, 'upstream request body should exist')
  return JSON.parse(Buffer.isBuffer(value) ? value.toString('utf8') : value) as Record<string, any>
}

const testIdentity = {
  systemAccountId: 'sys_test',
  groupId: 'grp_test'
}

try {
  await main()
} finally {
  await stopGatewayJsonParseWorker()
}
