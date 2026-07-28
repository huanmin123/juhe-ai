import assert from 'node:assert/strict'
import type { Request } from 'express'

import type { ResolvedOpenAIModelMapping } from '../../modules/gateway/protocols/openai-v1/model-mapping.js'
import { replaceGatewayJsonBody, type GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'
import {
  extractGatewayJsonBodyMetadata,
  setGatewayJsonMetadataScannerStackObserverForTest
} from '../../modules/gateway/request/json-metadata-scanner.js'
import {
  extractGatewayJsonBodyMetadataInWorker,
  parseGatewayJsonBodyInWorker,
  parseGatewayRequestJsonBody,
  setGatewayRequestJsonMaterializationObserverForTest,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'
import { buildAnthropicMessagesChatBridgeBody } from '../../modules/providers/drivers/_shared/anthropic-openai-chat-bridge.js'
import { buildCodexResponsesChatBridgeBody } from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import { buildGeminiGenerateContentAnthropicMessagesBridgeBody } from '../../modules/providers/drivers/_shared/gemini-anthropic-messages-bridge.js'
import { buildGeminiGenerateContentChatBridgeBody } from '../../modules/providers/drivers/_shared/gemini-openai-chat-bridge.js'
import { buildOpenAIToAnthropicBridgeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'
import { buildOpenAIOrAnthropicToGeminiNativeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-gemini-native-bridge.js'
import { gatewaySerializedJsonObject } from '../../modules/gateway/request/serialized-json-body.js'

const padding = 'x'.repeat(320 * 1024)
const body = Buffer.from(JSON.stringify({ model: 'gpt-regression', stream: true, input: padding }), 'utf8')

try {
  for (let index = 0; index < 64; index += 1) {
    const metadata = await extractGatewayJsonBodyMetadataInWorker(body)
    assert.equal(metadata.model, 'gpt-regression')
    const parsed = await parseGatewayJsonBodyInWorker(body) as { input?: unknown }
    assert.equal(typeof parsed.input, 'string')
  }

  const concurrent = await Promise.all(Array.from({ length: 16 }, async () => {
    const parsed = await parseGatewayJsonBodyInWorker(body) as { model?: unknown }
    return parsed.model
  }))
  assert(concurrent.every((model) => model === 'gpt-regression'))

  await stopGatewayJsonParseWorker()
  const restarted = await parseGatewayJsonBodyInWorker(body) as { stream?: unknown }
  assert.equal(restarted.stream, true, '显式关闭后 worker pool 必须能够干净重建')

  const request = {
    rawBody: Buffer.from(JSON.stringify({ model: 'gpt-5.4', input: 'cache-me' }), 'utf8'),
    body: undefined,
    gatewayRequestBody: {
      rawBodyBytes: 0,
      contentType: 'application/json',
      isJson: true,
      jsonParseStatus: 'deferred_large_json',
      jsonParseWarningBytes: 0
    }
  } as unknown as Request
  const [first, second] = await Promise.all([
    parseGatewayRequestJsonBody(request),
    parseGatewayRequestJsonBody(request)
  ])
  assert.equal(first, second, '同一请求的并发完整 JSON 解析应共享对象结果')
  assert.equal(request.body, first, '请求级解析结果应写回 req.body 供后续模块和重试复用')

  await assertRequestMaterializationLifecycleIsolation()
  assertCompactMetadataScannerStack()
  await assertBridgeRequestBodyCacheReuse()
} finally {
  setGatewayRequestJsonMaterializationObserverForTest(undefined)
  setGatewayJsonMetadataScannerStackObserverForTest(undefined)
  await stopGatewayJsonParseWorker()
}

console.log('网关 JSON parser 生命周期回归通过：连续解析、并发、关闭重建和请求级结果复用均正常')

async function assertRequestMaterializationLifecycleIsolation(): Promise<void> {
  let materializationStarts = 0
  setGatewayRequestJsonMaterializationObserverForTest(() => {
    materializationStarts += 1
  })

  const request = rawJsonRequest('/v1/responses', { model: 'gpt-local-signal', input: 'hello' })
  const localAbortController = new AbortController()
  localAbortController.abort()
  const [localWaiter, concurrentWaiter] = await Promise.allSettled([
    parseGatewayRequestJsonBody(request, 1, localAbortController.signal),
    parseGatewayRequestJsonBody(request)
  ])
  assert.equal(localWaiter.status, 'rejected', '局部取消仍应只取消当前 waiter')
  assert.equal(concurrentWaiter.status, 'fulfilled', '局部取消不得污染健康 waiter 的共享物化任务')
  assert.deepEqual(
    concurrentWaiter.status === 'fulfilled' ? concurrentWaiter.value : undefined,
    { model: 'gpt-local-signal', input: 'hello' }
  )
  assert.equal(materializationStarts, 1, '局部取消与健康 waiter 并发时仍只能启动一次完整解析')

  const invalidRequest = rawJsonRequest('/v1/responses', { model: 'placeholder' }) as GatewayRawBodyRequest
  invalidRequest.rawBody = Buffer.from('{"model":', 'utf8')
  invalidRequest.body = undefined
  invalidRequest.gatewayParsedJsonBodyAvailable = false
  invalidRequest.gatewayParsedJsonBody = undefined
  let firstFailure: unknown
  let secondFailure: unknown
  try {
    await parseGatewayRequestJsonBody(invalidRequest)
  } catch (error) {
    firstFailure = error
  }
  try {
    await parseGatewayRequestJsonBody(invalidRequest)
  } catch (error) {
    secondFailure = error
  }
  assert(firstFailure instanceof Error)
  assert.equal(secondFailure, firstFailure, '同一 rawBody 的 terminal failure 必须复用同一失败结果')
  assert.equal(materializationStarts, 2, '非法 rawBody 连续消费不得重复启动完整解析')
  assert(invalidRequest.gatewayParsedJsonBodyPromise, 'terminal failure 必须保留到 Body 换代')

  replaceGatewayJsonBody(invalidRequest, { model: 'recovered' })
  assert.equal(invalidRequest.gatewayParsedJsonBodyPromise, undefined, 'Body 换代必须清除旧 terminal failure')
  assert.deepEqual(await parseGatewayRequestJsonBody(invalidRequest), { model: 'recovered' })
  assert.equal(materializationStarts, 2, '结构化替换 Body 应直接复用，不再启动 parser')
  setGatewayRequestJsonMaterializationObserverForTest(undefined)
}

function assertCompactMetadataScannerStack(): void {
  const depth = 200_000
  const rawBody = Buffer.from(
    `{"reasoning":{"ignored":${'['.repeat(depth)}{"type":"compaction_trigger"}${']'.repeat(depth)},"effort":"high"}}`,
    'utf8'
  )
  let maxStackCapacityBytes = 0
  setGatewayJsonMetadataScannerStackObserverForTest((capacityBytes) => {
    maxStackCapacityBytes = Math.max(maxStackCapacityBytes, capacityBytes)
  })
  const metadata = extractGatewayJsonBodyMetadata(rawBody)
  setGatewayJsonMetadataScannerStackObserverForTest(undefined)
  assert.equal(metadata.invalidJson, undefined)
  assert.equal(metadata.codexCompactionTrigger, true, '深层合法 JSON 的 compaction metadata 语义必须保持')
  assert.equal(metadata.reasoningEffort, 'high', '深层未知 metadata 字段不得影响后续受支持字段提取')
  assert(
    maxStackCapacityBytes < rawBody.byteLength,
    `scanner 栈必须按每层 1 byte 紧凑增长：stack=${maxStackCapacityBytes}, body=${rawBody.byteLength}`
  )
}

async function assertBridgeRequestBodyCacheReuse(): Promise<void> {
  const largeText = 'bridge-cache'.repeat(32 * 1024)
  const chatToGeminiMapping: ResolvedOpenAIModelMapping = {
    sourceModel: 'client-chat-model',
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: 'gemini-upstream',
    upstreamEndpointFamily: 'generate_content'
  }
  const cases: Array<{
    label: string
    req: Request
    build: (req: Request) => Promise<Buffer>
  }> = [
    {
      label: 'Anthropic Messages -> OpenAI Chat',
      req: rawJsonRequest('/v1/messages', {
        model: 'claude-test',
        max_tokens: 1024,
        messages: [{ role: 'user', content: largeText }]
      }),
      build: async (req) => await buildAnthropicMessagesChatBridgeBody(req, { defaultModel: 'gpt-test' })
    },
    {
      label: 'Gemini GenerateContent -> OpenAI Chat',
      req: rawJsonRequest('/v1beta/models/gemini-test:generateContent', {
        contents: [{ role: 'user', parts: [{ text: largeText }] }]
      }),
      build: async (req) => await buildGeminiGenerateContentChatBridgeBody(req, { defaultModel: 'gpt-test' })
    },
    {
      label: 'Gemini GenerateContent -> Anthropic Messages',
      req: rawJsonRequest('/v1beta/models/gemini-test:generateContent', {
        contents: [{ role: 'user', parts: [{ text: largeText }] }]
      }),
      build: async (req) => await buildGeminiGenerateContentAnthropicMessagesBridgeBody(req, { defaultModel: 'claude-test' })
    },
    {
      label: 'OpenAI Chat -> Gemini GenerateContent',
      req: rawJsonRequest('/v1/chat/completions', {
        model: 'client-chat-model',
        messages: [{ role: 'user', content: largeText }]
      }),
      build: async (req) => await buildOpenAIOrAnthropicToGeminiNativeBody(req, { mapping: chatToGeminiMapping })
    },
    {
      label: 'OpenAI Chat -> Anthropic Messages',
      req: rawJsonRequest('/v1/chat/completions', {
        model: 'client-chat-model',
        messages: [{ role: 'user', content: largeText }]
      }),
      build: async (req) => await buildOpenAIToAnthropicBridgeBody(req, { modelOverride: 'claude-test' })
    },
    {
      label: 'OpenAI Responses -> Chat',
      req: rawJsonRequest('/v1/responses', {
        model: 'client-responses-model',
        input: largeText
      }),
      build: async (req) => await buildCodexResponsesChatBridgeBody(req, { defaultModel: 'gpt-test' })
    }
  ]

  for (const item of cases) {
    const serializedBody = await item.build(item.req)
    assert.ok(gatewaySerializedJsonObject(serializedBody), `${item.label} 必须把已转换对象绑定到精确 Buffer，供账户覆盖免解析复用`)
    const cachedBody = item.req.body
    assert(cachedBody && typeof cachedBody === 'object', `${item.label} 首次解析应写回 req.body`)
    await item.build(item.req)
    assert.equal(item.req.body, cachedBody, `${item.label} 重复构建应复用同一请求解析对象`)
  }
}

function rawJsonRequest(path: string, body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  return {
    body: undefined,
    headers: { 'content-type': 'application/json' },
    method: 'POST',
    originalUrl: path,
    path,
    rawBody,
    gatewayRequestBody: {
      rawBodyBytes: rawBody.byteLength,
      contentType: 'application/json',
      isJson: true,
      jsonParseStatus: 'deferred_large_json',
      jsonParseWarningBytes: 0
    }
  } as unknown as Request
}
