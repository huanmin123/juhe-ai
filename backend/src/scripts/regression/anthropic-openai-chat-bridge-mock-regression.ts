import assert from 'node:assert/strict'
import type { Request } from 'express'

import {
  buildAnthropicMessagesChatBridgeBody,
  prepareAnthropicMessagesChatBridgeHeaders,
  transformAnthropicMessagesChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/anthropic-openai-chat-bridge.js'
import { GatewayAgentGuidanceResponse } from '../../modules/gateway/request/validation-error.js'

type JsonRecord = Record<string, unknown>

async function main(): Promise<void> {
  await testAnthropicMessagesRequestBodyToChat()
  await testChatJsonResponseToAnthropicMessage()
  await testChatJsonEmptyContentGuidance()
  await testChatJsonRefusalToAnthropicText()
  await testInvalidChatJsonResponseToAnthropicError()
  await testChatSseResponseToAnthropicMessagesSse()
  await testUnsupportedAnthropicThinkingGuidance()
  await testUnsupportedAnthropicCacheControlGuidance()
  await testChatSseErrorToAnthropicErrorEvent()
  console.log('anthropic-openai-chat-bridge mock regression passed')
}

async function testAnthropicMessagesRequestBodyToChat(): Promise<void> {
  const req = fakeRequest('/v1/messages', {
    model: 'claude-sonnet-4-6',
    system: 'You are terse.',
    max_tokens: 64,
    temperature: 0.2,
    stop_sequences: ['END'],
    metadata: { user_id: 'user-1' },
    messages: [
      {
        role: 'user',
        content: [
          { type: 'text', text: 'Look' },
          { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'abc123' } }
        ]
      },
      {
        role: 'assistant',
        content: [
          { type: 'tool_use', id: 'toolu_1', name: 'lookup', input: { q: 'glm' } }
        ]
      },
      {
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: 'toolu_1', content: 'result text' },
          { type: 'text', text: 'Continue.' }
        ]
      }
    ],
    tools: [
      {
        name: 'lookup',
        description: 'Search local data',
        input_schema: { type: 'object', properties: { q: { type: 'string' } }, required: ['q'] }
      }
    ],
    tool_choice: { type: 'tool', name: 'lookup', disable_parallel_tool_use: true },
    stream: false
  })
  const headers = new Headers({ 'anthropic-version': '2023-06-01', 'x-api-key': 'client-key' })
  prepareAnthropicMessagesChatBridgeHeaders(headers, req)
  assert.equal(headers.get('anthropic-version'), null)
  assert.equal(headers.get('x-api-key'), null)
  assert.equal(headers.get('content-type'), 'application/json')
  assert.equal(headers.get('accept'), 'application/json')

  const bodyBuffer = await buildAnthropicMessagesChatBridgeBody(req, {
    defaultModel: 'deepseek-v4-flash',
    modelOverride: 'deepseek-v4-flash'
  })
  const body = JSON.parse(bodyBuffer.toString('utf8')) as JsonRecord
  assert.equal(body.model, 'deepseek-v4-flash')
  assert.equal(body.stream, false)
  assert.equal(body.max_tokens, 64)
  assert.equal(body.stop, 'END')
  assert.equal(body.user, 'user-1')
  const messages = body.messages as JsonRecord[]
  assert.equal(messages[0]?.role, 'system')
  assert.equal(messages[1]?.role, 'user')
  assert.equal((messages[1]?.content as JsonRecord[])[1]?.type, 'image_url')
  assert.equal(((messages[1]?.content as JsonRecord[])[1]?.image_url as JsonRecord).url, 'data:image/png;base64,abc123')
  assert.equal(messages[2]?.role, 'assistant')
  assert.equal(((messages[2]?.tool_calls as JsonRecord[])[0]?.function as JsonRecord).name, 'lookup')
  assert.equal(messages[3]?.role, 'tool')
  assert.equal(messages[3]?.tool_call_id, 'toolu_1')
  assert.equal(messages[4]?.role, 'user')
  assert.deepEqual(body.tool_choice, { type: 'function', function: { name: 'lookup' } })
  assert.equal(body.parallel_tool_calls, false)
}

async function testChatJsonResponseToAnthropicMessage(): Promise<void> {
  const req = fakeRequest('/v1/messages', { model: 'claude-sonnet-4-6', messages: [], stream: false })
  const response = transformAnthropicMessagesChatBridgeUpstreamResponse(req, {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: asyncChunks([JSON.stringify({
      id: 'chatcmpl_1',
      object: 'chat.completion',
      created: 1,
      model: 'glm-5.2',
      choices: [{
        index: 0,
        message: {
          role: 'assistant',
          content: 'Need tool.',
          tool_calls: [{
            id: 'call_1',
            type: 'function',
            function: { name: 'lookup', arguments: '{"q":"x"}' }
          }]
        },
        finish_reason: 'tool_calls'
      }],
      usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 }
    })])
  }, { enabled: true, model: 'glm-5.2' })
  const parsed = JSON.parse(Buffer.concat(await collect(response.body!)).toString('utf8')) as JsonRecord
  assert.equal(parsed.type, 'message')
  assert.equal(parsed.id, 'msg_chatcmpl_1')
  assert.equal(parsed.model, 'glm-5.2')
  assert.equal(parsed.stop_reason, 'tool_use')
  assert.equal((parsed.usage as JsonRecord).input_tokens, 10)
  assert.equal((parsed.usage as JsonRecord).output_tokens, 3)
  const content = parsed.content as JsonRecord[]
  assert.equal(content[0]?.type, 'text')
  assert.equal(content[1]?.type, 'tool_use')
  assert.deepEqual(content[1]?.input, { q: 'x' })
}

async function testChatJsonEmptyContentGuidance(): Promise<void> {
  const req = fakeRequest('/v1/messages', { model: 'claude-sonnet-4-6', messages: [], stream: false })
  const response = transformAnthropicMessagesChatBridgeUpstreamResponse(req, {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: asyncChunks([JSON.stringify({
      id: 'chatcmpl_empty',
      object: 'chat.completion',
      created: 1,
      model: 'glm-5.2',
      choices: [{
        index: 0,
        message: {
          role: 'assistant',
          content: ''
        },
        finish_reason: 'stop'
      }],
      usage: { prompt_tokens: 7, completion_tokens: 0 }
    })])
  }, { enabled: true, model: 'glm-5.2' })
  const parsed = JSON.parse(Buffer.concat(await collect(response.body!)).toString('utf8')) as JsonRecord
  const content = parsed.content as JsonRecord[]
  assert.equal(content[0]?.type, 'text')
  assert.match(String(content[0]?.text ?? ''), /空 assistant 内容/)
  assert.equal(parsed.stop_reason, 'end_turn')
}

async function testChatJsonRefusalToAnthropicText(): Promise<void> {
  const req = fakeRequest('/v1/messages', { model: 'claude-sonnet-4-6', messages: [], stream: false })
  const response = transformAnthropicMessagesChatBridgeUpstreamResponse(req, {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: asyncChunks([JSON.stringify({
      id: 'chatcmpl_refusal',
      object: 'chat.completion',
      created: 1,
      model: 'glm-5.2',
      choices: [{
        index: 0,
        message: {
          role: 'assistant',
          content: null,
          refusal: '不能协助该请求。'
        },
        finish_reason: 'content_filter'
      }],
      usage: { prompt_tokens: 7, completion_tokens: 2 }
    })])
  }, { enabled: true, model: 'glm-5.2' })
  const parsed = JSON.parse(Buffer.concat(await collect(response.body!)).toString('utf8')) as JsonRecord
  assert.equal(parsed.stop_reason, 'refusal')
  const content = parsed.content as JsonRecord[]
  assert.deepEqual(content[0], { type: 'text', text: '不能协助该请求。' })
}

async function testInvalidChatJsonResponseToAnthropicError(): Promise<void> {
  const req = fakeRequest('/v1/messages', { model: 'claude-sonnet-4-6', messages: [], stream: false })
  const response = transformAnthropicMessagesChatBridgeUpstreamResponse(req, {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: asyncChunks(['{not-json'])
  }, { enabled: true, model: 'glm-5.2' })
  const parsed = JSON.parse(Buffer.concat(await collect(response.body!)).toString('utf8')) as JsonRecord
  assert.equal(parsed.type, 'error')
  assert.equal((parsed.error as JsonRecord).code, 'upstream_chat_completions_invalid_json')
}

async function testChatSseResponseToAnthropicMessagesSse(): Promise<void> {
  const req = fakeRequest('/v1/messages', { model: 'claude-sonnet-4-6', messages: [], stream: true })
  const upstream = [
    chatSse({ id: 'chatcmpl_sse', object: 'chat.completion.chunk', created: 1, model: 'glm-5.2', choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] }),
    chatSse({ id: 'chatcmpl_sse', object: 'chat.completion.chunk', created: 1, model: 'glm-5.2', choices: [{ index: 0, delta: { content: 'Hi' }, finish_reason: null }] }),
    chatSse({ id: 'chatcmpl_sse', object: 'chat.completion.chunk', created: 1, model: 'glm-5.2', choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }),
    chatSse({ id: 'chatcmpl_sse', object: 'chat.completion.chunk', created: 1, model: 'glm-5.2', choices: [], usage: { prompt_tokens: 4, completion_tokens: 1 } }),
    'data: [DONE]\n\n'
  ].join('')
  const response = transformAnthropicMessagesChatBridgeUpstreamResponse(req, {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'text/event-stream' }),
    body: asyncChunks([upstream])
  }, { enabled: true, model: 'glm-5.2' })
  const text = Buffer.concat(await collect(response.body!)).toString('utf8')
  assert.match(text, /event: message_start/)
  assert.match(text, /event: content_block_start/)
  assert.match(text, /"type":"text_delta","text":"Hi"/)
  assert.match(text, /event: content_block_stop/)
  assert.match(text, /event: message_delta/)
  assert.match(text, /"stop_reason":"end_turn"/)
  assert.match(text, /"usage":\{"output_tokens":1\}/)
  assert.match(text, /event: message_stop/)
  assert.doesNotMatch(text, /\[DONE\]/)
}

async function testUnsupportedAnthropicThinkingGuidance(): Promise<void> {
  const req = fakeRequest('/v1/messages', {
    model: 'claude-sonnet-4-6',
    thinking: { type: 'enabled', budget_tokens: 1024 },
    messages: [{ role: 'user', content: 'hi' }],
    stream: false
  })
  await assert.rejects(
    () => buildAnthropicMessagesChatBridgeBody(req, { defaultModel: 'glm-5.2', guidanceProviderName: 'GLM' }),
    (error) => {
      assert.ok(error instanceof GatewayAgentGuidanceResponse)
      assert.equal(error.protocol, 'messages')
      assert.equal(error.stream, false)
      assert.equal(error.code, 'unsupported_anthropic_messages_chat_bridge_fields')
      assert.match(error.message, /本地 agent \/ MCP|真实支持/)
      return true
    }
  )
}

async function testUnsupportedAnthropicCacheControlGuidance(): Promise<void> {
  const req = fakeRequest('/v1/messages', {
    model: 'claude-sonnet-4-6',
    messages: [{
      role: 'user',
      content: [{ type: 'text', text: 'cache me', cache_control: { type: 'ephemeral' } }]
    }],
    stream: false
  })
  await assert.rejects(
    () => buildAnthropicMessagesChatBridgeBody(req, { defaultModel: 'glm-5.2', guidanceProviderName: 'GLM' }),
    (error) => {
      assert.ok(error instanceof GatewayAgentGuidanceResponse)
      assert.equal(error.protocol, 'messages')
      assert.equal(error.code, 'unsupported_anthropic_messages_cache_control')
      assert.match(error.message, /cache_control|prompt caching/)
      return true
    }
  )
}

async function testChatSseErrorToAnthropicErrorEvent(): Promise<void> {
  const req = fakeRequest('/v1/messages', { model: 'claude-sonnet-4-6', messages: [], stream: true })
  const response = transformAnthropicMessagesChatBridgeUpstreamResponse(req, {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'text/event-stream' }),
    body: asyncChunks([`event: error\ndata: ${JSON.stringify({ error: { type: 'api_error', code: 'upstream_failed', message: 'boom' } })}\n\n`])
  }, { enabled: true, model: 'glm-5.2' })
  const text = Buffer.concat(await collect(response.body!)).toString('utf8')
  assert.match(text, /event: error/)
  assert.match(text, /"message":"boom"/)
  assert.match(text, /"code":"upstream_failed"/)
}

function fakeRequest(path: string, body: JsonRecord): Request {
  return {
    method: 'POST',
    originalUrl: path,
    path,
    body,
    headers: {}
  } as Request
}

function chatSse(payload: JsonRecord): string {
  return `data: ${JSON.stringify(payload)}\n\n`
}

async function * asyncChunks(chunks: string[]): AsyncIterable<Uint8Array> {
  for (const chunk of chunks) {
    yield Buffer.from(chunk, 'utf8')
  }
}

async function collect(body: AsyncIterable<Uint8Array>): Promise<Buffer[]> {
  const chunks: Buffer[] = []
  for await (const chunk of body) {
    chunks.push(Buffer.from(chunk))
  }
  return chunks
}

void main().catch((error) => {
  console.error(error)
  process.exit(1)
})
