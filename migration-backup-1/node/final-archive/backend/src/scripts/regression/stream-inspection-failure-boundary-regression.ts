import assert from 'node:assert/strict'
import type { Request } from 'express'

import { AnthropicStreamInspector } from '../../modules/gateway/protocols/anthropic-v1/stream-inspection.js'
import { extractAnthropicSseSemanticFrames } from '../../modules/gateway/protocols/anthropic-v1/response-semantics.js'
import { GeminiStreamInspector } from '../../modules/gateway/protocols/gemini-v1beta/stream-inspection.js'
import { extractGeminiSseSemanticFrames } from '../../modules/gateway/protocols/gemini-v1beta/response-semantics.js'
import { parseOpenAISseEventText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'
import {
  transformAnthropicMessagesChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/anthropic-openai-chat-bridge.js'
import {
  transformGeminiGenerateContentChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/gemini-openai-chat-bridge.js'
import {
  transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/gemini-anthropic-messages-bridge.js'
import {
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

function fakeRequest(path: string, body: JsonRecord = { model: 'test-model', messages: [{ role: 'user', content: 'hi' }], stream: true }): Request {
  const headers: Record<string, string> = { accept: 'text/event-stream', 'content-type': 'application/json' }
  return {
    method: 'POST',
    originalUrl: path,
    path: path.split('?', 1)[0],
    body,
    headers,
    header(name: string) {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

async function collect(body: AsyncIterable<Uint8Array>): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

async function * asyncChunks(value: string): AsyncIterable<Uint8Array> {
  yield Buffer.from(value, 'utf8')
}

function response(body: string): GatewayUpstreamResponse {
  return {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'text/event-stream' }),
    body: asyncChunks(body)
  }
}

function testInspectorsIgnoreNestedErrorFields(): void {
  const anthropic = new AnthropicStreamInspector()
  anthropic.pushText('data: {"type":"message_delta","delta":{"stop_reason":null},"error":{"message":"field only"},"metadata":{"error":"diagnostic"}}\n\n')
  anthropic.pushText('event: message_stop\ndata: {"type":"message_stop"}\n\n')
  assert.equal(anthropic.finish().failedReceived, false, 'Anthropic 普通事件中的 error 字段不得判失败')

  const gemini = new GeminiStreamInspector()
  gemini.pushText('data: {"event_type":"step.delta","delta":{"type":"text","text":"ok"},"error":{"message":"field only"},"metadata":{"error":"diagnostic"}}\n\n')
  gemini.pushText('data: {"event_type":"interaction.completed","interaction":{"status":"completed"}}\n\n')
  assert.equal(gemini.finish().failedReceived, false, 'Gemini 普通事件中的 error/metadata.error 不得判失败')

  const anthropicFrame = extractAnthropicSseSemanticFrames(
    parseOpenAISseEventText('event: message_delta\ndata: {"type":"message_delta","error":{"message":"field only"}}\n\n')
  )
  assert.equal(anthropicFrame.some((frame) => frame.frameType === 'error'), false)

  const geminiFrame = extractGeminiSseSemanticFrames(
    parseOpenAISseEventText('data: {"event_type":"step.delta","delta":{"type":"text","text":"ok"},"error":{"message":"field only"}}\n\n')
  )
  assert.equal(geminiFrame.some((frame) => frame.frameType === 'error'), false)
}

function testExplicitFailureEventsRemainFailures(): void {
  const anthropic = new AnthropicStreamInspector()
  anthropic.pushText('event: error\ndata: {"type":"message_delta","error":{"type":"api_error","message":"boom"}}\n\n')
  assert.equal(anthropic.finish().failedReceived, true)

  const gemini = new GeminiStreamInspector()
  gemini.pushText('data: {"event_type":"interaction.failed","interaction":{"status":"failed","error":{"message":"boom"}}}\n\n')
  assert.equal(gemini.finish().failedReceived, true)

  const geminiTypedError = new GeminiStreamInspector()
  geminiTypedError.pushText('data: {"type":"error","error":{"message":"boom"}}\n\n')
  assert.equal(geminiTypedError.finish().failedReceived, true)
}

async function testChatBridgesIgnoreNestedErrorFields(): Promise<void> {
  const chatChunk = [
    'data: {"id":"chat-1","model":"test-model","choices":[{"delta":{"content":"ok"}}],"error":{"message":"field only"},"metadata":{"error":"diagnostic"}}\n\n',
    'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n',
    'data: [DONE]\n\n'
  ].join('')
  const anthropicText = await collect(transformAnthropicMessagesChatBridgeUpstreamResponse(
    fakeRequest('/v1/messages'),
    response(chatChunk),
    { enabled: true, model: 'test-model' }
  ).body!)
  assert.match(anthropicText, /"text":"ok"/)
  assert.doesNotMatch(anthropicText, /event: error/)

  const geminiText = await collect(transformGeminiGenerateContentChatBridgeUpstreamResponse(
    fakeRequest('/v1beta/models/test-model:streamGenerateContent?alt=sse'),
    response(chatChunk),
    { enabled: true, model: 'test-model' }
  ).body!)
  assert.match(geminiText, /"text":"ok"/)
  assert.doesNotMatch(geminiText, /"error"/)

  const anthropicChunk = [
    'event: message_start\ndata: {"type":"message_start","message":{"model":"test-model","usage":{"input_tokens":1,"output_tokens":0}}}\n\n',
    'event: content_block_start\ndata: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}\n\n',
    'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"},"error":{"message":"field only"},"metadata":{"error":"diagnostic"}}\n\n',
    'event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n',
    'event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}\n\n',
    'event: message_stop\ndata: {"type":"message_stop"}\n\n'
  ].join('')
  const geminiFromAnthropicText = await collect(transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse(
    fakeRequest('/v1beta/models/test-model:streamGenerateContent?alt=sse'),
    response(anthropicChunk),
    { enabled: true, model: 'test-model' }
  ).body!)
  assert.match(geminiFromAnthropicText, /"text":"ok"/)
  assert.doesNotMatch(geminiFromAnthropicText, /"error"/)

  const codexText = await collect(transformCodexResponsesChatBridgeUpstreamResponse(
    fakeRequest('/v1/responses', { model: 'test-model', input: 'hi', stream: true }),
    response(chatChunk),
    { enabled: true, explicitMappingBridge: true, defaultModel: 'test-model' }
  ).body!)
  assert.match(codexText, /"type":"response\.output_text\.delta"/)
  assert.match(codexText, /"delta":"ok"/)
  assert.match(codexText, /"type":"response\.completed"/)
  assert.doesNotMatch(codexText, /"type":"response\.failed"/)
}

async function main(): Promise<void> {
  testInspectorsIgnoreNestedErrorFields()
  testExplicitFailureEventsRemainFailures()
  await testChatBridgesIgnoreNestedErrorFields()
  console.log('stream inspection failure boundary regression passed')
}

await main()
