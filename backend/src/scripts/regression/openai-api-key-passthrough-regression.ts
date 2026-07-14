import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'

import { requestModel } from '../../modules/gateway/request/metadata.js'
import {
  buildOpenAIModelsResponse,
  buildOpenAICodexUpstreamUrls,
  buildUpstreamUrl,
  isCodexModelsRequest,
  isOpenAIModelsRequest
} from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { buildGatewayUpstreamRequestParts, buildGatewayUpstreamUrlsForAccount } from '../../modules/providers/drivers/registry.js'
import { applyGptAccountRequestOverrides } from '../../modules/providers/drivers/gpt/request-overrides.js'
import { buildUpstreamHeaders, buildUpstreamRequestBody, isEffectiveOpenAIStreamRequest } from '../../modules/gateway/upstream/request.js'
import {
  createGatewayRequestBodyState,
  clearGatewayRequestBodyInFlightForTest,
  gatewayJsonBodyInlineParseMaxBytes,
  gatewayJsonBodyLargeWarningBytes,
  gatewayRawBodyHardLimitBytes,
  gatewayTextRawBodyHardLimitBytes,
  getGatewayRequestBodyInFlightState,
  setGatewayRequestBodyInFlightMaxBytesForTest,
  type GatewayRawBodyRequest
} from '../../modules/gateway/request/body.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { stopGatewayJsonParseWorker } from '../../modules/gateway/request/json-parser.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import { sanitizeRequestHeaders } from '../../modules/gateway/usage/snapshots.js'

type TestRequest = GatewayRawBodyRequest
type MockResponse = EventEmitter & {
  headersSent: boolean
  destroyed: boolean
  statusCode: number
  headers: Record<string, string>
  body?: unknown
  setHeader: (name: string, value: string) => MockResponse
  status: (statusCode: number) => MockResponse
  json: (body: unknown) => MockResponse
}

const apiKeyAccount: OpenAIAccountSecret = {
  id: 'acc_test',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  systemAccountId: 'sys_test',
  accountOwnerSystemAccountId: 'sys_test',
  groupOwnerSystemAccountId: 'sys_test',
  accountAccessType: 'owner',
  groupAccessType: 'owner',
  name: 'OpenAI API Key passthrough regression',
  apiKey: 'sk-upstream',
  type: 'api_key',
  status: 'active',
  concurrencyLimit: 20,
  priority: 0,
  superPriorityEnabled: false,
  fallbackEnabled: false,
  clientCompatibility: 'openai_standard',
  healthCheckModel: 'gpt-5.4',
  healthCheckEndpointMode: 'responses_sse',
  baseUrl: 'https://api.openai.com/v1',
  streamFailureCount: 0,
  credentials: {}
}

async function main(): Promise<void> {
  testRawBodyPassthrough()
  testGptAccountRequestOverridePureFunction()
  await testOpenAIStandardRequestPartsPassthrough()
  await testGptResponsesRequestOverrides()
  await testGptChatRequestOverrides()
  await testGptCompactRequestOverrides()
  await testGptRequestOverridesAfterResponsesToChatBridge()
  await testGptLargeRequestOverrides()
  await testCodexCompatibleAccountKeepsStandardResponsesPassthrough()
  await testCodexResponsesCompatibilityRequestParts()
  await testCodexResponsesCompatibilityKeepsExplicitToolSettings()
  await testCodexResponsesCompatibilityDoesNotRewriteChatCompletions()
  testApiKeyHeaderFiltering()
  testOpenAIAccountHeadersAreNotClientOrCredentialControlled()
  testOpenAIBetaPreservedFromClient()
  testOpenAIUpstreamUrlNormalization()
  testOpenAIClientPathNormalization()
  testResponsesCompactRouteCapability()
  testParsedJsonBodyPassthroughForGatewayMetadata()
  await testMediumJsonBodyDeferredByGatewayMiddleware()
  await testOversizeJsonBodyRejectedByGatewayMiddleware()
  await testLargeImageJsonBodyAllowedByGatewayMiddleware()
  await testGatewayRawBodyInFlightLimit()
  await testDeferredJsonBodyImageToolMetadataScanned()
  await testDeferredInvalidJsonMarkedWithoutWorkerParse()
  await testDeferredInvalidJsonPrimitiveMarkedWithoutWorkerParse()
  console.log('OpenAI API Key passthrough regression passed')
}

function testGptAccountRequestOverridePureFunction(): void {
  const responsesInput = {
    service_tier: 'flex',
    reasoning_effort: 'low',
    reasoning: {
      effort: 'medium',
      summary: 'detailed'
    }
  }
  const responsesOutput = applyGptAccountRequestOverrides(responsesInput, {
    credentials: {
      service_tier_override: 'priority',
      reasoning_effort_override: 'max'
    },
    endpointFamily: 'responses',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['low', 'medium', 'high']
    }
  })
  assert.equal(responsesOutput.service_tier, 'priority')
  assert.deepEqual(responsesOutput.reasoning, {
    effort: 'high',
    summary: 'detailed'
  })
  assert.equal(responsesOutput.reasoning_effort, undefined)
  assert.equal(responsesInput.service_tier, 'flex', '纯函数不能修改输入对象')
  assert.deepEqual(responsesInput.reasoning, {
    effort: 'medium',
    summary: 'detailed'
  }, '纯函数不能修改输入 reasoning')

  const chatOutput = applyGptAccountRequestOverrides({
    service_tier: 'priority',
    reasoning: { effort: 'low' },
    reasoning_effort: 'high'
  }, {
    credentials: {
      service_tier_override: 'default',
      reasoning_effort_override: 'none'
    },
    endpointFamily: 'chat_completions',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['none']
    }
  })
  assert.equal(chatOutput.service_tier, undefined)
  assert.equal(chatOutput.reasoning_effort, 'none')
  assert.equal(chatOutput.reasoning, undefined)

  const flexOutput = applyGptAccountRequestOverrides({
    service_tier: 'priority'
  }, {
    credentials: {
      service_tier_override: 'flex'
    },
    endpointFamily: 'responses',
    modelCapabilities: {
      supportedServiceTiers: ['flex'],
      supportedReasoningEfforts: []
    }
  })
  assert.equal(flexOutput.service_tier, 'flex')

  const unsupportedServiceTierOutput = applyGptAccountRequestOverrides({
    service_tier: 'flex'
  }, {
    credentials: {
      service_tier_override: 'priority'
    },
    endpointFamily: 'responses',
    modelCapabilities: {
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  })
  assert.equal(unsupportedServiceTierOutput.service_tier, 'flex', 'Priority 与 Flex 平级，不支持配置档位时必须保留客户端原值')

  const compactOutput = applyGptAccountRequestOverrides({
    service_tier: 'flex',
    reasoning: { effort: 'low', summary: 'auto' }
  }, {
    credentials: {
      service_tier_override: 'priority',
      reasoning_effort_override: 'max'
    },
    endpointFamily: 'responses',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['max']
    },
    compact: true
  })
  assert.equal(compactOutput.service_tier, 'priority')
  assert.deepEqual(compactOutput.reasoning, { effort: 'low', summary: 'auto' }, 'compact 不应用 reasoning 覆盖')

  assert.throws(() => applyGptAccountRequestOverrides({}, {
    credentials: {
      reasoning_effort_override: 'ultra'
    },
    endpointFamily: 'responses'
  }), /reasoning_effort_override/, 'Ultra 不能作为账户 wire reasoning effort')
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
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'openai_standard'
  }, testIdentity)

  assert.ok(Buffer.isBuffer(parts.body))
  assert.equal(Buffer.compare(parts.body, rawBody), 0)
  assert.equal(parts.headers.get('authorization'), 'Bearer sk-upstream')
}

async function testGptResponsesRequestOverrides(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.6-sol',
    input: 'hello',
    service_tier: 'flex',
    reasoning_effort: 'low',
    reasoning: {
      effort: 'medium',
      summary: 'detailed'
    }
  }))
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/responses')
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    credentials: {
      service_tier_override: 'priority',
      reasoning_effort_override: 'max'
    }
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.service_tier, 'priority')
  assert.deepEqual(body.reasoning, {
    effort: 'max',
    summary: 'detailed'
  })
  assert.equal(body.reasoning_effort, undefined)
}

async function testGptChatRequestOverrides(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.6-luna',
    messages: [{ role: 'user', content: 'hello' }],
    service_tier: 'priority',
    reasoning: { effort: 'high' },
    reasoning_effort: 'high'
  }))
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/chat/completions')
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    credentials: {
      service_tier_override: 'default',
      reasoning_effort_override: 'none'
    }
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.service_tier, undefined)
  assert.equal(body.reasoning_effort, 'none')
  assert.equal(body.reasoning, undefined)
}

async function testGptCompactRequestOverrides(): Promise<void> {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.6-terra',
    input: [],
    service_tier: 'flex',
    reasoning: { effort: 'low', summary: 'auto' }
  }))
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/responses/compact')
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    credentials: {
      service_tier_override: 'priority',
      reasoning_effort_override: 'max'
    }
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.service_tier, 'priority')
  assert.deepEqual(body.reasoning, { effort: 'low', summary: 'auto' }, 'API Key compact 只能应用 service tier')
}

async function testGptRequestOverridesAfterResponsesToChatBridge(): Promise<void> {
  const requestBody = {
    model: 'gpt-client-alias',
    input: 'hello',
    service_tier: 'flex',
    reasoning: { effort: 'low' }
  }
  const rawBody = Buffer.from(JSON.stringify(requestBody))
  const req = createRequest(requestBody, { 'content-type': 'application/json' }, rawBody, '/v1/responses')
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    credentials: {
      service_tier_override: 'priority',
      reasoning_effort_override: 'high'
    },
    modelMappings: [{
      sourceModel: 'gpt-client-alias',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'gpt-5.6-terra',
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }]
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.model, 'gpt-5.6-terra')
  assert.equal(body.service_tier, 'priority')
  assert.equal(body.reasoning_effort, 'high')
  assert.equal(body.reasoning, undefined)
}

async function testGptLargeRequestOverrides(): Promise<void> {
  const clientInput = 'x'.repeat(gatewayJsonBodyInlineParseMaxBytes + 1024)
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.6-sol',
    input: clientInput,
    service_tier: 'flex'
  }))
  assert.ok(rawBody.length > gatewayJsonBodyInlineParseMaxBytes)
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/responses')
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    credentials: {
      service_tier_override: 'priority'
    }
  }, testIdentity)
  const body = parseJsonBuffer(parts.body)

  assert.equal(body.input, clientInput)
  assert.equal(body.service_tier, 'priority')
}

async function testCodexCompatibleAccountKeepsStandardResponsesPassthrough(): Promise<void> {
  const rawBody = Buffer.from('{"model":"gpt-5.5","input":"hello","stream":false}')
  const req = createRequest(undefined, { 'content-type': 'application/json' }, rawBody, '/v1/responses')
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'codex_responses'
  }, testIdentity, undefined, {
    requestClientCompatibility: 'openai_standard'
  })

  assert.ok(Buffer.isBuffer(parts.body))
  assert.equal(Buffer.compare(parts.body, rawBody), 0)
  assert.equal(parts.headers.get('accept'), null)
  assert.equal(parts.headers.get('originator'), null)
  assert.equal(parts.headers.get('openai-beta'), null)
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
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_openai_v1',
    clientCompatibility: 'openai_standard'
  }, testIdentity, undefined, {
    requestClientCompatibility: 'codex_responses'
  })
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
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'codex_responses'
  }, testIdentity, undefined, {
    requestClientCompatibility: 'codex_responses'
  })
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
  const parts = await buildGatewayUpstreamRequestParts(req, {
    ...apiKeyAccount,
    clientCompatibility: 'codex_responses'
  }, testIdentity, undefined, {
    requestClientCompatibility: 'codex_responses'
  })

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
    'x-oai-attestation': 'device-proof',
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
    'x-vercel-id',
    'x-oai-attestation'
  ]) {
    assert.equal(headers.get(name), null, `${name} should be stripped`)
  }
  assert.equal(
    Object.prototype.hasOwnProperty.call(sanitizeRequestHeaders({ 'x-oai-attestation': 'device-proof' }), 'x-oai-attestation'),
    false,
    'usage 请求快照必须从捕获范围排除 attestation 正文'
  )
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
  assert.equal(isCodexModelsRequest(createRequest(undefined, {}, undefined, '/v1/models?client_version=0.99.0', 'GET')), true)
  assert.equal(isCodexModelsRequest(createRequest(undefined, { originator: 'codex_cli_rs' }, undefined, '/models', 'GET')), true)
  assert.equal(isCodexModelsRequest(createRequest(undefined, {}, undefined, '/v1/models', 'GET')), false)
  const openAIModels = buildOpenAIModelsResponse([modelCatalogItem('gpt-standard')])
  assert.equal('data' in openAIModels, true, '普通 /models 响应应保持 OpenAI 标准 data 字段')
  const codexModels = buildOpenAIModelsResponse([modelCatalogItem('gpt-codex')], createRequest(undefined, {}, undefined, '/v1/models?client_version=0.99.0', 'GET'))
  assert.equal('models' in codexModels, true, 'Codex /models 响应应使用 models 字段')
  assert.equal('data' in codexModels, false, 'Codex /models 响应不应返回 OpenAI 标准 data 字段')
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

function modelCatalogItem(model: string): Parameters<typeof buildOpenAIModelsResponse>[0][number] {
  return {
    providerCode: 'gpt',
    model,
    mode: 'text',
    supportedApiProtocols: ['responses'],
    supportsPromptCaching: false,
    supportedServiceTiers: [],
    supportedReasoningEfforts: [],
    codexSupportedReasoningLevels: [],
    supportsServiceTier: false,
    source: 'regression',
    scope: 'built_in',
    status: 'active'
  }
}

function testResponsesCompactRouteCapability(): void {
  const passthroughAccount: OpenAIAccountSecret = {
    ...apiKeyAccount,
    baseUrl: 'https://api.openai.com'
  }

  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(passthroughAccount, createRequest({ model: 'gpt-5.4', input: [] }, {}, undefined, '/v1/responses/compact')),
    ['https://api.openai.com/v1/responses/compact']
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

async function testGatewayRawBodyInFlightLimit(): Promise<void> {
  clearGatewayRequestBodyInFlightForTest()
  const rawBodyA = Buffer.alloc(512 * 1024, 'a')
  const maxBytes = rawBodyA.length
  setGatewayRequestBodyInFlightMaxBytesForTest(maxBytes)

  try {
    const rawBodyB = Buffer.alloc(rawBodyA.length + 1, 'b')
    const reqB = createRequest(rawBodyB, { 'content-type': 'application/octet-stream' }, rawBodyB)
    const resB = createMockResponse()
    let nextB = false

    await captureGatewayRawBody(reqB, resB as never, () => {
      nextB = true
    })

    assert.equal(nextB, false)
    assert.equal(reqB.rawBody, undefined)
    assert.equal(resB.statusCode, 503)
    assert.equal(resB.headers['retry-after'], '1')
    assert.deepEqual(resB.body, {
      error: {
        message: '网关请求体在途总量过高，请稍后重试',
        type: 'server_overloaded',
        code: 'gateway_body_in_flight_limit_exceeded'
      }
    })
    assert.deepEqual(getGatewayRequestBodyInFlightState(), {
      currentBytes: 0,
      requestCount: 0,
      maxBytes,
      rejectedCount: 1
    })

    const reqA = createRequest(rawBodyA, { 'content-type': 'application/octet-stream' }, rawBodyA)
    const resA = createMockResponse()
    let nextA = false

    await captureGatewayRawBody(reqA, resA as never, () => {
      nextA = true
    })

    assert.equal(nextA, true)
    assert.equal(reqA.rawBody?.length, rawBodyA.length)
    assert.deepEqual(getGatewayRequestBodyInFlightState(), {
      currentBytes: rawBodyA.length,
      requestCount: 1,
      maxBytes,
      rejectedCount: 1
    })

    resA.emit('finish')
    resA.emit('close')
    assert.deepEqual(getGatewayRequestBodyInFlightState(), {
      currentBytes: 0,
      requestCount: 0,
      maxBytes,
      rejectedCount: 1
    })
  } finally {
    clearGatewayRequestBodyInFlightForTest()
  }
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
    headers: {} as Record<string, string>,
    body: undefined as unknown,
    setHeader(name: string, value: string) {
      response.headers[name.toLowerCase()] = value
      return response
    },
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
