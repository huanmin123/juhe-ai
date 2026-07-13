import assert from 'node:assert/strict'
import type { Request } from 'express'

import type { AccountModelMapping } from '../../domain/types.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import {
  applyCodexResponsesContextStatePreflight,
  codexResponsesContextAllowsAccount,
  codexResponsesChatBridgeCompletionHandlerForRequest,
  getCodexResponsesContextState,
  hasExplicitCodexResponsesChatBridgeRuntimeAccount,
  prepareCodexResponsesCompactDispatchForAccounts,
  setCodexResponsesContextStateForRequest
} from '../../modules/gateway/codex-responses/chat-bridge-state.js'
import { buildPreparedUpstreamRequestParts } from '../../modules/gateway/dispatch/account-preparation.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'
import { replaceGatewayJsonBody, type GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'

const model = 'gpt-5.5'
const summary = 'Earlier turns established the deployment constraints.'
const inlineSummary = `juhecmp.v1.${Buffer.from(JSON.stringify({ summary }), 'utf8').toString('base64url')}`
const canonicalBody = {
  model,
  stream: true,
  input: [
    {
      type: 'compaction_summary',
      encrypted_content: inlineSummary
    },
    {
      type: 'reasoning',
      encrypted_content: 'native-upstream-encrypted-content',
      summary: []
    },
    {
      type: 'message',
      role: 'user',
      content: [{ type: 'input_text', text: 'Continue.' }]
    }
  ],
  tools: [{
    type: 'function',
    name: 'permission_filtered_tool',
    description: 'This tool must not be restored after permission preflight.',
    parameters: { type: 'object', properties: {} }
  }],
  previous_response_id: 'resp_deepseek_bridge_previous'
}
const historicalInput = {
  type: 'message',
  role: 'assistant',
  content: [{ type: 'output_text', text: 'Historical response that must not be saved as current input.' }]
}

const req = request(canonicalBody)
setCodexResponsesContextStateForRequest(req, {
  requestKind: 'responses',
  boundary: {
    systemAccountId: 'sys_test',
    apiKeyId: 'key_test',
    groupId: 'group_test',
    providerCode: 'openai-compatible'
  },
  canonicalBody,
  currentBody: canonicalBody,
  currentInput: canonicalBody.input,
  materializedInput: [historicalInput, ...canonicalBody.input],
  materializedCurrentInputStartIndex: 1,
  previousResponseId: canonicalBody.previous_response_id,
  previousResponseKind: 'internal',
  sessionId: 'session_test',
  restored: true
})
replaceGatewayJsonBody(req, {
  ...canonicalBody,
  instructions: 'Only use capabilities allowed after permission preflight.',
  tools: []
})

const bridgeAccount = account('bridge', [{
  sourceModel: model,
  sourceEndpointFamily: 'responses',
  upstreamModel: 'gpt-5.5-chat',
  upstreamEndpointFamily: 'chat_completions',
  enabled: true
}])
const nativeAccount = account('native')
const usageContext = {
  systemAccountId: 'sys_test',
  groupId: 'group_test',
  trafficSource: 'gateway'
} as GatewayUsageContext

assert.equal(codexResponsesContextAllowsAccount(req, bridgeAccount), true)
assert.equal(hasExplicitCodexResponsesChatBridgeRuntimeAccount(req, [nativeAccount]), false)
assert.equal(hasExplicitCodexResponsesChatBridgeRuntimeAccount(req, [bridgeAccount]), true)
const bridgeParts = await buildPreparedUpstreamRequestParts(req, bridgeAccount, usageContext, undefined, {
  requestClientCompatibility: 'codex_responses'
})
const bridgeBody = jsonBody(bridgeParts.body)
assert.equal(bridgeBody.model, 'gpt-5.5-chat')
assert.equal(bridgeBody.stream, true)
assert.equal(Array.isArray(bridgeBody.messages), true)
assert.match(JSON.stringify(bridgeBody.messages), /Only use capabilities allowed after permission preflight/)
assert.equal(bridgeBody.tools, undefined)
assert.match(JSON.stringify(bridgeBody.messages), /Earlier turns established the deployment constraints/)
assert.doesNotMatch(JSON.stringify(bridgeBody), /juhecmp\.v[12]/)
assert.equal(typeof codexResponsesChatBridgeCompletionHandlerForRequest(req, bridgeAccount), 'function')

const nativeParts = await buildPreparedUpstreamRequestParts(req, nativeAccount, usageContext, undefined, {
  requestClientCompatibility: 'codex_responses'
})
const nativeBody = jsonBody(nativeParts.body)
assert.equal(nativeBody.model, model)
assert.equal(nativeBody.previous_response_id, undefined)
assert.equal(Array.isArray(nativeBody.input), true)
assert.deepEqual((nativeBody.input as unknown[]).find((item) => (
  typeof item === 'object' && item !== null && (item as Record<string, unknown>).role === 'developer'
)), {
  type: 'message',
  role: 'developer',
  content: [{ type: 'input_text', text: summary }]
})
assert.match(JSON.stringify(nativeBody), /native-upstream-encrypted-content/)
assert.doesNotMatch(JSON.stringify(nativeBody), /juhecmp\.v[12]/)
assert.equal(codexResponsesChatBridgeCompletionHandlerForRequest(req, nativeAccount), undefined)

replaceGatewayJsonBody(req, {
  ...(req.body as Record<string, unknown>),
  input: [
    ...((req.body as Record<string, unknown>).input as unknown[]),
    {
      type: 'message',
      role: 'user',
      content: [{ type: 'input_text', text: 'Apply the hybrid quality repair instruction.' }]
    }
  ]
})

const bridgeAgain = jsonBody((await buildPreparedUpstreamRequestParts(req, bridgeAccount, usageContext)).body)
assert.match(JSON.stringify(bridgeAgain.messages), /Earlier turns established the deployment constraints/)
assert.match(JSON.stringify(bridgeAgain.messages), /Apply the hybrid quality repair instruction/)
assert.doesNotMatch(JSON.stringify(bridgeAgain), /native-upstream-encrypted-content/)
const updatedState = getCodexResponsesContextState(req)
assert.match(JSON.stringify(updatedState?.currentInput), /Apply the hybrid quality repair instruction/)
assert.doesNotMatch(JSON.stringify(updatedState?.currentInput), /Historical response that must not be saved as current input/)
assert.match(JSON.stringify(updatedState?.materializedInput), /Historical response that must not be saved as current input/)

const externalReq = request({
  model,
  stream: true,
  input: 'Continue native session.',
  previous_response_id: 'resp_external_native'
})
const externalRawBody = Buffer.from((externalReq as unknown as GatewayRawBodyRequest).rawBody ?? Buffer.alloc(0))
const externalPreflightResult = await applyCodexResponsesContextStatePreflight({
  req: externalReq,
  res: {} as never,
  auditCapture: { addGatewayMetadata() {} } as never,
  usageContext,
  startedAt: Date.now(),
  systemAccountId: 'sys_test',
  apiKeyId: 'key_test',
  groupId: 'group_test',
  groupAccess: {
    groupOwnerSystemAccountId: 'sys_test',
    providerCode: 'openai',
    groupAccessType: 'owner'
  }
})
assert.equal(externalPreflightResult, 'continued')
assert.deepEqual((externalReq as unknown as GatewayRawBodyRequest).rawBody, externalRawBody)
assert.equal(codexResponsesContextAllowsAccount(externalReq, bridgeAccount), false)
assert.equal(codexResponsesContextAllowsAccount(externalReq, nativeAccount), true)
const externalNativeBody = jsonBody((await buildPreparedUpstreamRequestParts(externalReq, nativeAccount, usageContext)).body)
assert.equal(externalNativeBody.previous_response_id, 'resp_external_native')

const compatibilityOnlyAccount = account('compatibility-only')
assert.equal(hasExplicitCodexResponsesChatBridgeRuntimeAccount(externalReq, [compatibilityOnlyAccount]), false)
assert.equal(codexResponsesContextAllowsAccount(externalReq, compatibilityOnlyAccount), true)

const externalCompactReq = request({
  model,
  input: 'Compact the native session.',
  previous_response_id: 'resp_external_native'
}, '/v1/responses/compact')
const externalCompactPreflight = await applyCodexResponsesContextStatePreflight({
  req: externalCompactReq,
  res: {} as never,
  auditCapture: { addGatewayMetadata() {} } as never,
  usageContext,
  startedAt: Date.now(),
  systemAccountId: 'sys_test',
  apiKeyId: 'key_test',
  groupId: 'group_test',
  groupAccess: {
    groupOwnerSystemAccountId: 'sys_test',
    providerCode: 'openai',
    groupAccessType: 'owner'
  }
})
assert.equal(externalCompactPreflight, 'continued')
assert.equal(codexResponsesContextAllowsAccount(externalCompactReq, bridgeAccount), false)
assert.equal(codexResponsesContextAllowsAccount(externalCompactReq, nativeAccount), true)
assert.equal(prepareCodexResponsesCompactDispatchForAccounts(externalCompactReq, [nativeAccount]), false)

const mixedCompactReq = request({ model, input: 'Compact this full input.' }, '/v1/responses/compact')
await applyCodexResponsesContextStatePreflight({
  req: mixedCompactReq,
  res: {} as never,
  auditCapture: { addGatewayMetadata() {} } as never,
  usageContext,
  startedAt: Date.now(),
  systemAccountId: 'sys_test',
  apiKeyId: 'key_test',
  groupId: 'group_test',
  groupAccess: {
    groupOwnerSystemAccountId: 'sys_test',
    providerCode: 'openai',
    groupAccessType: 'owner'
  }
})
assert.equal(prepareCodexResponsesCompactDispatchForAccounts(mixedCompactReq, [bridgeAccount, nativeAccount]), false)
assert.equal(codexResponsesContextAllowsAccount(mixedCompactReq, bridgeAccount), false)
assert.equal(codexResponsesContextAllowsAccount(mixedCompactReq, nativeAccount), true)

const untouchedReq = request({ model, stream: true, input: 'Canonical request body.' })
const untouchedBody = untouchedReq.body
const untouchedRawBody = Buffer.from((untouchedReq as unknown as GatewayRawBodyRequest).rawBody ?? Buffer.alloc(0))
const preflightResult = await applyCodexResponsesContextStatePreflight({
  req: untouchedReq,
  res: {} as never,
  auditCapture: { addGatewayMetadata() {} } as never,
  usageContext,
  startedAt: Date.now(),
  systemAccountId: 'sys_test',
  apiKeyId: 'key_test',
  groupId: 'group_test',
  groupAccess: {
    groupOwnerSystemAccountId: 'sys_test',
    providerCode: 'openai',
    groupAccessType: 'owner'
  }
})
assert.equal(preflightResult, 'continued')
assert.deepEqual((untouchedReq as unknown as GatewayRawBodyRequest).rawBody, untouchedRawBody)
const untouchedParts = await buildPreparedUpstreamRequestParts(untouchedReq, nativeAccount, usageContext)
assert.equal(untouchedReq.body, untouchedBody, '原生 Responses 无转换派发不得替换请求 body 引用')
assert.deepEqual((untouchedReq as unknown as GatewayRawBodyRequest).rawBody, untouchedRawBody, '原生 Responses 无转换派发不得重写 rawBody')
assert.deepEqual(jsonBody(untouchedParts.body), untouchedBody, '原生 Responses 无转换派发的上游 body 语义应保持不变')

console.log('跨协议 Codex 上下文回归通过：内部摘要按实际账号渲染，原生加密内容和外部 previous_response_id 保持边界')

function request(body: Record<string, unknown>, originalUrl = '/v1/responses'): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  return {
    method: 'POST',
    path: originalUrl.replace(/^\/v1/, ''),
    originalUrl,
    headers: {
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body,
    rawBody,
    gatewayParsedJsonBodyAvailable: true,
    gatewayParsedJsonBody: body
  } as unknown as Request & GatewayRawBodyRequest
}

function account(id: string, modelMappings: AccountModelMapping[] = []): UpstreamAccount {
  const modes = ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'] as const
  return {
    id,
    name: id,
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'sys_test',
    accountOwnerSystemAccountId: 'sys_test',
    groupOwnerSystemAccountId: 'sys_test',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'codex_responses',
    healthCheckEndpointFamily: 'chat_completions',
    supportedEndpointModes: [...modes],
    supportedModels: [model, 'gpt-5.5-chat'],
    modelMappings,
    baseUrl: 'https://example.test/v1',
    apiKey: 'sk-test',
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-test',
      base_url: 'https://example.test/v1',
      supported_endpoint_modes: [...modes]
    }
  } as UpstreamAccount
}

function jsonBody(body: Buffer | string | undefined): Record<string, unknown> {
  assert.ok(body, '上游请求必须包含 JSON body')
  const text = Buffer.isBuffer(body) ? body.toString('utf8') : body
  return JSON.parse(text) as Record<string, unknown>
}
