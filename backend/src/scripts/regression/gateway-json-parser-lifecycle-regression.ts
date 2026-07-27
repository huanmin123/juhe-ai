import assert from 'node:assert/strict'
import type { Request } from 'express'

import type { ResolvedOpenAIModelMapping } from '../../modules/gateway/protocols/openai-v1/model-mapping.js'
import {
  extractGatewayJsonBodyMetadataInWorker,
  parseGatewayJsonBodyInWorker,
  parseGatewayRequestJsonBody,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'
import { buildAnthropicMessagesChatBridgeBody } from '../../modules/providers/drivers/_shared/anthropic-openai-chat-bridge.js'
import { buildCodexResponsesChatBridgeBody } from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import { buildGeminiGenerateContentAnthropicMessagesBridgeBody } from '../../modules/providers/drivers/_shared/gemini-anthropic-messages-bridge.js'
import { buildGeminiGenerateContentChatBridgeBody } from '../../modules/providers/drivers/_shared/gemini-openai-chat-bridge.js'
import { buildOpenAIToAnthropicBridgeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'
import { buildOpenAIOrAnthropicToGeminiNativeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-gemini-native-bridge.js'

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

  await assertBridgeRequestBodyCacheReuse()
} finally {
  await stopGatewayJsonParseWorker()
}

console.log('网关 JSON parser 生命周期回归通过：连续解析、并发、关闭重建和请求级结果复用均正常')

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
    await item.build(item.req)
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
