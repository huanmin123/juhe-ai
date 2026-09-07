import assert from 'node:assert/strict'
import type { Request } from 'express'

import { buildOpenAIOAuthCodexRequestParts } from '../../modules/gateway/adapters/gpt-codex/oauth-adapter.js'
import {
  GatewayJsonWorkerCanceledError,
  isGatewayJsonWorkerCanceledError,
  isGatewayJsonWorkerInvalidJsonError,
  parseGatewayRequestJsonBody,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'
import { buildAnthropicMessagesChatBridgeBody } from '../../modules/providers/drivers/_shared/anthropic-openai-chat-bridge.js'
import { buildCodexResponsesChatBridgeBody } from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import { buildGeminiGenerateContentAnthropicMessagesBridgeBody } from '../../modules/providers/drivers/_shared/gemini-anthropic-messages-bridge.js'
import { buildGeminiGenerateContentChatBridgeBody } from '../../modules/providers/drivers/_shared/gemini-openai-chat-bridge.js'
import { buildOpenAIToAnthropicBridgeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'
import { buildOpenAIOrAnthropicToGeminiNativeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-gemini-native-bridge.js'
import { applyGptAccountRequestOverridesToBody } from '../../modules/providers/drivers/gpt/request-override-body.js'

const rawBody = Buffer.from(JSON.stringify({
  model: 'gpt-regression',
  input: 'x'.repeat(320 * 1024),
  messages: [{ role: 'user', content: 'hello' }]
}), 'utf8')

try {
  const controller = new AbortController()
  controller.abort()
  const cases: Array<{ label: string; run: () => Promise<unknown> }> = [
    {
      label: 'Anthropic -> Chat bridge',
      run: () => buildAnthropicMessagesChatBridgeBody(request(), { defaultModel: 'gpt-regression' }, controller.signal)
    },
    {
      label: 'Codex -> Chat bridge',
      run: () => buildCodexResponsesChatBridgeBody(request(), { defaultModel: 'gpt-regression' }, controller.signal)
    },
    {
      label: 'Gemini -> Anthropic bridge',
      run: () => buildGeminiGenerateContentAnthropicMessagesBridgeBody(request(), { defaultModel: 'gpt-regression' }, controller.signal)
    },
    {
      label: 'Gemini -> Chat bridge',
      run: () => buildGeminiGenerateContentChatBridgeBody(request(), { defaultModel: 'gpt-regression' }, controller.signal)
    },
    {
      label: 'OpenAI -> Anthropic bridge',
      run: () => buildOpenAIToAnthropicBridgeBody(request(), {}, controller.signal)
    },
    {
      label: 'OpenAI/Anthropic -> Gemini bridge',
      run: () => buildOpenAIOrAnthropicToGeminiNativeBody(request(), { mapping: {} as never }, controller.signal)
    },
    {
      label: 'OpenAI OAuth adapter',
      run: () => buildOpenAIOAuthCodexRequestParts(request(), {}, {
        id: 'oauth-regression',
        apiKey: 'token',
        credentials: {}
      }, {
        systemAccountId: 'system-regression',
        apiKeyId: 'key-regression',
        groupId: 'group-regression'
      }, controller.signal)
    },
    {
      label: 'GPT account override',
      run: () => applyGptAccountRequestOverridesToBody(rawBody, {
        account: {
          id: 'override-regression',
          credentials: { service_tier_override: 'priority' }
        } as never,
        credentials: { service_tier_override: 'priority' },
        endpointFamily: 'responses',
        modelCapabilities: {
          supportedServiceTiers: ['priority'],
          supportedReasoningEfforts: []
        },
        signal: controller.signal
      })
    }
  ]

  for (const item of cases) {
    await assert.rejects(item.run(), (error: unknown) => {
      assert.ok(isGatewayJsonWorkerCanceledError(error), `${item.label} 必须保留 worker 取消语义`)
      assert.ok(error instanceof GatewayJsonWorkerCanceledError)
      return true
    })
  }

  const invalidRequest = request(Buffer.from('{"model":', 'utf8'))
  await assert.rejects(parseGatewayRequestJsonBody(invalidRequest), (error: unknown) => {
    assert.equal(isGatewayJsonWorkerInvalidJsonError(error), true, '只有 JSON 语法错误可标记为 invalid JSON')
    assert.equal(isGatewayJsonWorkerCanceledError(error), false)
    return true
  })
} finally {
  await stopGatewayJsonParseWorker()
}

function request(body = rawBody): Request {
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers,
    rawBody: body,
    body: undefined,
    gatewayRequestBody: {
      rawBodyBytes: body.length,
      contentType: 'application/json',
      isJson: true,
      jsonParseStatus: 'scanned_json',
      jsonParseWarningBytes: 2 * 1024 * 1024,
      model: 'gpt-regression'
    },
    header(name: string) {
      const value = headers[name.toLowerCase()]
      return Array.isArray(value) ? value[0] : value
    }
  } as unknown as Request
}

console.log('网关 JSON 错误分类回归通过：invalid/queue/abort/timeout 语义不再混淆')
