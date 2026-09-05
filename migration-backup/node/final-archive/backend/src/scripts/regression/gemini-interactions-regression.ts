import assert from 'node:assert/strict'

import type { Request } from 'express'
import { createGatewayRequestBodyState, type GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'
import {
  geminiEndpointFamilyFromPath,
  geminiEndpointModeForRequestShape
} from '../../domain/gemini-endpoint-modes.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import { geminiProviderDriver } from '../../modules/providers/drivers/gemini/driver.js'
import {
  buildGeminiUpstreamUrl,
  isGeminiNativeRequest
} from '../../modules/gateway/protocols/gemini-v1beta/route-helpers.js'
import {
  extractGeminiJsonSemanticFrames,
  extractGeminiSseSemanticFrames
} from '../../modules/gateway/protocols/gemini-v1beta/response-semantics.js'
import { GeminiStreamInspector } from '../../modules/gateway/protocols/gemini-v1beta/stream-inspection.js'
import { extractGeminiUsage } from '../../modules/gateway/protocols/gemini-v1beta/usage.js'
import { parseOpenAISseEventText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'
import { requestStream } from '../../modules/gateway/request/metadata.js'
import { resolveGeminiGatewayClientStrategy } from '../../modules/gateway/client-profiles/strategy.js'
import { setGatewayRequestJsonMaterializationObserverForTest } from '../../modules/gateway/request/json-parser.js'
import { isSuccessfulEmptyUpstreamResponseAllowed } from '../../modules/gateway/response/finalization.js'
import * as interactionAffinityModule from '../../modules/gateway/protocols/gemini-v1beta/interaction-affinity.service.js'

function fakeRequest(
  method: string,
  originalUrl: string,
  body: Record<string, unknown> | undefined = { model: 'gemini-3.5-flash', input: 'hello' },
  accept = 'application/json'
): Request {
  const rawBody = body ? Buffer.from(JSON.stringify(body)) : undefined
  return {
    method,
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {
      'content-type': 'application/json',
      accept
    },
    body,
    rawBody,
    header(name: string) {
      if (name.toLowerCase() === 'content-type') return 'application/json'
      if (name.toLowerCase() === 'accept') return accept
      return undefined
    }
  } as unknown as Request
}

function assertInteractionsRouting(): void {
  assert.equal(geminiEndpointFamilyFromPath('/v1beta/interactions'), 'interactions')
  assert.equal(geminiEndpointFamilyFromPath('/v1beta/interactions/abc123'), 'interactions')
  assert.equal(geminiEndpointFamilyFromPath('/v1beta/interactions/abc123/cancel'), 'interactions')
  assert.equal(geminiEndpointModeForRequestShape({ endpoint: '/v1beta/interactions', stream: false }), 'interactions_json')
  assert.equal(geminiEndpointModeForRequestShape({ endpoint: '/v1beta/interactions', stream: true }), 'interactions_sse')
  assert.equal(buildGeminiUpstreamUrl('https://generativelanguage.googleapis.com/v1beta', '/v1beta/interactions', { stream: true }), 'https://generativelanguage.googleapis.com/v1beta/interactions')
  assert.equal(buildGeminiUpstreamUrl('https://generativelanguage.googleapis.com/v1beta', '/v1beta/interactions?alt=sse'), 'https://generativelanguage.googleapis.com/v1beta/interactions')

  const legacyAltOnlyRequest = fakeRequest('POST', '/v1beta/interactions?alt=sse')
  assert.equal(requestStream(legacyAltOnlyRequest), false)
  assert.equal(resolveGeminiGatewayClientStrategy(legacyAltOnlyRequest).downstreamProtocol, 'json')

  assert.equal(isGeminiNativeRequest(fakeRequest('POST', '/v1beta/interactions')), true)
  assert.equal(isGeminiNativeRequest(fakeRequest('GET', '/v1beta/interactions/abc123')), true)
  assert.equal(isGeminiNativeRequest(fakeRequest('DELETE', '/v1beta/interactions/abc123')), true)
  assert.equal(isGeminiNativeRequest(fakeRequest('POST', '/v1beta/interactions/abc123/cancel')), true)
}

function assertInteractionsJsonSemantics(): void {
  const frames = extractGeminiJsonSemanticFrames({
    id: 'interaction-1',
    object: 'interaction',
    status: 'completed',
    service_tier: 'standard',
    steps: [
      { type: 'model_output', content: [{ type: 'text', text: 'hello interactions' }] }
    ],
    metadata: {
      total_usage: {
        input_tokens: 7,
        output_tokens: 3,
        thought_tokens: 2,
        cached_tokens: 1,
        total_tokens: 12
      }
    }
  }, 'interactions')
  assert(frames.some((frame) => frame.frameType === 'output_text_done' && frame.text === 'hello interactions'))
  assert(frames.some((frame) => frame.frameType === 'usage' && frame.usage?.inputTokens === 7 && frame.usage.outputTokens === 5 && frame.usage.thinkingTokens === 2 && frame.usage.serviceTier === 'standard'))
  assert(frames.some((frame) => frame.frameType === 'usage' && frame.rawJsonPaths?.includes('metadata.total_usage')))
  assert.deepEqual(extractGeminiUsage({ service_tier: 'standard', metadata: { total_usage: { input_tokens: 7, output_tokens: 3, thought_tokens: 2 } } }), {
    serviceTier: 'standard',
    inputTokens: 7,
    outputTokens: 5,
    cacheReadTokens: undefined,
    thinkingTokens: 2
  })
}

function assertInteractionsSseSemantics(): void {
  const delta = parseOpenAISseEventText('data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":"hello "},"metadata":{"total_usage":{"input_tokens":2,"output_tokens":1}}}\n\n')
  const completed = parseOpenAISseEventText('data: {"event_type":"interaction.completed","interaction":{"id":"interaction-1","status":"completed","service_tier":"standard","usage":{"total_input_tokens":7,"total_output_tokens":3,"total_thought_tokens":2}}}\n\n')
  assert.equal(delta.eventType, 'step.delta')
  assert.equal((delta.data?.delta as Record<string, unknown> | undefined)?.type, 'text')
  const openAIType = parseOpenAISseEventText('data: {"type":"response.completed","event_type":"metadata-only"}\n\n')
  assert.equal(openAIType.eventType, 'response.completed')
  assert.equal(openAIType.data?.event_type, 'metadata-only')
  const deltaFrames = extractGeminiSseSemanticFrames(delta, 'interactions')
  const completedFrames = extractGeminiSseSemanticFrames(completed, 'interactions')
  assert(deltaFrames.some((frame) => frame.frameType === 'output_text_delta' && frame.text === 'hello '))
  assert(completedFrames.some((frame) => frame.frameType === 'usage' && frame.usage?.outputTokens === 5 && frame.usage.serviceTier === 'standard'))
  assert(completedFrames.some((frame) => frame.frameType === 'usage' && frame.rawJsonPaths?.includes('metadata.total_usage')))
  assert(completedFrames.some((frame) => frame.frameType === 'completed' && frame.status === 'completed'))

  const inspector = new GeminiStreamInspector()
  inspector.pushText('data: {"event_type":"step.delta","delta":{"type":"text","text":"hello "},"metadata":{"total_usage":{"input_tokens":2,"output_tokens":1}}}\n\n')
  inspector.pushText('data: {"event_type":"interaction.completed","interaction":{"id":"interaction-1","status":"completed","service_tier":"standard","usage":{"total_input_tokens":7,"total_output_tokens":3,"total_thought_tokens":2}}}\n\n')
  const inspection = inspector.finish()
  assert.equal(inspection.outputReceived, true)
  assert.equal(inspection.terminalReceived, true)
  assert.equal(inspection.usage.inputTokens, 7)
  assert.equal(inspection.usage.outputTokens, 5)
  assert.equal(inspection.usage.serviceTier, 'standard', 'Interactions SSE 必须保留上游报告的实际服务等级')
  assert.equal(inspection.responseResourceId, 'interaction-1', 'Interaction ID 必须作为协议元数据提取，不能依赖审计正文捕获')

  for (const [label, event] of [
    ['finish', 'data: {"event_type":"finish","interaction":{"status":"completed"}}\n\n'],
    ['done', 'event: done\ndata: {}\n\n'],
    ['[DONE]', 'data: [DONE]\n\n']
  ] as const) {
    const terminalInspector = new GeminiStreamInspector()
    terminalInspector.pushText(event)
    const terminalInspection = terminalInspector.finish()
    assert.equal(terminalInspection.terminalReceived, true, `Interactions ${label} 终止信号必须被识别`)
    assert.equal(terminalInspection.failedReceived, false, `Interactions ${label} 终止信号不得误判为失败`)
  }
}

async function assertDriverPassThrough(): Promise<void> {
  const request = fakeRequest('POST', '/v1beta/interactions', {
    model: 'gemini-3.5-flash',
    input: 'hello',
    stream: true
  }, 'text/event-stream')
  const account = {
    id: 'gemini-interactions-account',
    name: 'Gemini Interactions',
    providerCode: 'gemini',
    type: 'api_key',
    apiKey: 'sk-gemini-interactions',
    baseUrl: 'https://generativelanguage.googleapis.com/v1beta',
    protocolCode: 'gemini',
    protocolVersion: 'v1beta',
    providerProtocolProfileId: 'profile_gemini_native_v1beta',
    supportedEndpointModes: ['interactions_json', 'interactions_sse'] as AccountSupportedEndpointMode[],
    credentials: { supported_endpoint_modes: ['interactions_json', 'interactions_sse'] }
  } as never
  assert.equal(geminiProviderDriver.accountSupportsRequest(request, account), true)
  assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(account, request), [
    'https://generativelanguage.googleapis.com/v1beta/interactions'
  ])
  const parts = await geminiProviderDriver.buildUpstreamRequestParts(request, account, {
    systemAccountId: 'system-account',
    groupId: 'group'
  })
  assert.equal(parts.headers.get('x-goog-api-key'), 'sk-gemini-interactions')
  assert.equal(parts.headers.get('accept'), 'text/event-stream')
  assert.equal(parts.headers.get('api-revision'), '2026-05-20', 'Interactions 请求必须补充缺省 Api-Revision')
  assert.deepEqual(JSON.parse(Buffer.from(parts.body ?? '').toString('utf8')), request.body)

  const scannedRawBody = Buffer.from(JSON.stringify({
    model: 'gemini-3.5-flash',
    input: 'hello',
    stream: true
  }))
  const scannedRequest = fakeRequest('POST', '/v1beta/interactions', undefined, 'text/event-stream') as GatewayRawBodyRequest
  scannedRequest.body = undefined
  scannedRequest.rawBody = scannedRawBody
  scannedRequest.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody: scannedRawBody,
    contentType: 'application/json',
    jsonParseStatus: 'scanned_json',
    model: 'gemini-3.5-flash',
    stream: true
  })
  let materializationCount = 0
  setGatewayRequestJsonMaterializationObserverForTest(() => {
    materializationCount += 1
  })
  try {
    const scannedParts = await geminiProviderDriver.buildUpstreamRequestParts(scannedRequest, account, {
      systemAccountId: 'system-account',
      groupId: 'group'
    })
    assert.equal(scannedParts.body, scannedRawBody)
    assert.equal(materializationCount, 0, '原生 Gemini Interactions stream=true 透传不得完整解析 JSON')
  } finally {
    setGatewayRequestJsonMaterializationObserverForTest(undefined)
  }

  const googleOAuthAccount = {
    ...(account as unknown as Record<string, unknown>),
    type: 'google_oauth',
    credentials: {
      supported_endpoint_modes: ['interactions_json', 'interactions_sse'],
      service_tier_override: 'priority',
      reasoning_effort_override: 'high'
    }
  } as never
  const googleOAuthParts = await geminiProviderDriver.buildUpstreamRequestParts(request, googleOAuthAccount, {
    systemAccountId: 'system-account',
    groupId: 'group'
  })
  assert.equal(googleOAuthParts.headers.get('authorization'), 'Bearer sk-gemini-interactions')
  assert.equal(googleOAuthParts.headers.get('x-goog-api-key'), null)
  assert.deepEqual(
    JSON.parse(Buffer.from(googleOAuthParts.body ?? '').toString('utf8')),
    request.body,
    'Interactions 目前保持原生透明转发，账户级 GenerateContent 覆盖不得误写入未知字段'
  )

  const customRevisionRequest = fakeRequest('POST', '/v1beta/interactions', {
    model: 'gemini-3.5-flash',
    input: 'hello'
  })
  customRevisionRequest.headers['api-revision'] = '2026-06-01'
  const customRevisionParts = await geminiProviderDriver.buildUpstreamRequestParts(customRevisionRequest, account, {
    systemAccountId: 'system-account',
    groupId: 'group'
  })
  assert.equal(customRevisionParts.headers.get('api-revision'), '2026-06-01', '客户端提供的 Api-Revision 必须保留')

  const bodyStreamRequest = fakeRequest('POST', '/v1beta/interactions', {
    model: 'gemini-3.5-flash',
    input: 'hello',
    stream: true
  }, 'application/json')
  const bodyStreamParts = await geminiProviderDriver.buildUpstreamRequestParts(bodyStreamRequest, account, {
    systemAccountId: 'system-account',
    groupId: 'group'
  })
  assert.equal(bodyStreamParts.headers.get('accept'), 'text/event-stream', 'Interactions body.stream=true 必须覆盖为 SSE Accept')

  const acceptStreamRequest = fakeRequest('POST', '/v1beta/interactions', {
    model: 'gemini-3.5-flash',
    input: 'hello'
  }, 'text/event-stream')
  const acceptStreamParts = await geminiProviderDriver.buildUpstreamRequestParts(acceptStreamRequest, account, {
    systemAccountId: 'system-account',
    groupId: 'group'
  })
  assert.equal(
    JSON.parse(Buffer.from(acceptStreamParts.body ?? '').toString('utf8')).stream,
    true,
    'Interactions SSE Accept 必须在上游 JSON body 中补充 stream=true'
  )

  const largePayload = {
    model: 'gemini-3.5-flash',
    input: 'x'.repeat(300 * 1024)
  }
  const largeRawBody = Buffer.from(JSON.stringify(largePayload))
  const largeAcceptStreamRequest = fakeRequest('POST', '/v1beta/interactions', largePayload, 'text/event-stream') as GatewayRawBodyRequest
  largeAcceptStreamRequest.body = undefined
  largeAcceptStreamRequest.rawBody = largeRawBody
  largeAcceptStreamRequest.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody: largeRawBody,
    contentType: 'application/json',
    jsonParseStatus: 'deferred_large_json',
    model: largePayload.model
  })
  const largeAcceptStreamParts = await geminiProviderDriver.buildUpstreamRequestParts(largeAcceptStreamRequest, account, {
    systemAccountId: 'system-account',
    groupId: 'group'
  })
  assert.equal(
    JSON.parse(Buffer.from(largeAcceptStreamParts.body ?? '').toString('utf8')).stream,
    true,
    '超过内联解析上限的 Interactions 请求也必须把 SSE Accept 规范化为 body.stream=true'
  )

  const getStreamRequest = fakeRequest('GET', '/v1beta/interactions/abc123?stream=true', undefined, 'text/event-stream')
  assert.equal(requestStream(getStreamRequest), true)
  assert.equal(resolveGeminiGatewayClientStrategy(getStreamRequest).downstreamProtocol, 'gemini_interactions_sse')
  assert.equal(geminiProviderDriver.endpointModeForRequest(getStreamRequest, account), 'interactions_sse')
  assert.equal(geminiProviderDriver.accountSupportsRequest(getStreamRequest, account), true)
  assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(account, getStreamRequest), [
    'https://generativelanguage.googleapis.com/v1beta/interactions/abc123?stream=true'
  ])

  const cancelRequest = fakeRequest('POST', '/v1beta/interactions/abc123/cancel', undefined)
  assert.equal(geminiProviderDriver.accountSupportsRequest(cancelRequest, account), true)
  assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(account, cancelRequest), [
    'https://generativelanguage.googleapis.com/v1beta/interactions/abc123/cancel'
  ])
}

function assertDeleteEmptyResponsePolicy(): void {
  const account = {
    providerCode: 'gemini',
    protocolCode: 'gemini',
    protocolVersion: 'v1beta',
    providerProtocolProfileId: 'profile_gemini_native_v1beta'
  } as never
  assert.equal(isSuccessfulEmptyUpstreamResponseAllowed({
    req: fakeRequest('DELETE', '/v1beta/interactions/abc123', undefined),
    account,
    statusCode: 204
  }), true)
  assert.equal(isSuccessfulEmptyUpstreamResponseAllowed({
    req: fakeRequest('DELETE', '/v1beta/interactions/abc123', undefined),
    account,
    statusCode: 200
  }), true)
  assert.equal(isSuccessfulEmptyUpstreamResponseAllowed({
    req: fakeRequest('DELETE', '/v1beta/interactions/abc123', undefined),
    account,
    statusCode: 404
  }), false)
  assert.equal(isSuccessfulEmptyUpstreamResponseAllowed({
    req: fakeRequest('GET', '/v1beta/interactions/abc123', undefined),
    account,
    statusCode: 204
  }), false)
  assert.equal(isSuccessfulEmptyUpstreamResponseAllowed({
    req: fakeRequest('DELETE', '/v1beta/interactions', undefined),
    account,
    statusCode: 204
  }), false)
  assert.equal(isSuccessfulEmptyUpstreamResponseAllowed({
    req: fakeRequest('DELETE', '/v1beta/interactions/abc123/cancel', undefined),
    account,
    statusCode: 204
  }), false)
}

async function main(): Promise<void> {
  assert.equal(
    typeof (interactionAffinityModule as { setGeminiInteractionAffinityStateStoreForTest?: unknown }).setGeminiInteractionAffinityStateStoreForTest,
    'function',
    'Interaction affinity 必须提供可验证的状态存储故障注入边界'
  )
  assertInteractionsRouting()
  assertInteractionsJsonSemantics()
  assertInteractionsSseSemantics()
  await assertDriverPassThrough()
  assertDeleteEmptyResponsePolicy()
  console.log('Gemini Interactions regression passed')
}

await main()
