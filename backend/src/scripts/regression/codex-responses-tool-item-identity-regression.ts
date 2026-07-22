import assert from 'node:assert/strict'
import type { Request } from 'express'

import {
  buildCodexResponsesChatBridgeBody,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

interface BridgeToolCaseResult {
  added: JsonRecord
  done: JsonRecord
  completedItem: JsonRecord
}

for (const toolKind of ['function', 'custom'] as const) {
  const expectedType = toolKind === 'custom' ? 'custom_tool_call' : 'function_call'
  const expectedPrefix = toolKind === 'custom' ? /^ctc_/ : /^fc_/
  const sseResult = await runBridgeToolCase(toolKind, true)
  const jsonResult = await runBridgeToolCase(toolKind, false)

  for (const [transport, result] of [['SSE', sseResult], ['JSON', jsonResult]] as const) {
    const addedItem = objectValue(result.added.item)
    const doneItem = objectValue(result.done.item)

    assert.equal(addedItem?.type, expectedType, `${transport} added 必须保留 ${toolKind} 工具类型`)
    assert.match(stringValue(addedItem?.id) ?? '', expectedPrefix, `${transport} added 必须按 ${toolKind} 类型生成 ID`)
    assert.equal(doneItem?.type, expectedType, `${transport} done 必须保留 ${toolKind} 工具类型`)
    assert.equal(doneItem?.id, addedItem?.id, `${transport} added/done 必须使用同一个 item ID`)
    assert.equal(doneItem?.call_id, addedItem?.call_id, `${transport} added/done 必须使用同一个 call_id`)
    assert.equal(result.done.output_index, result.added.output_index, `${transport} added/done 必须使用同一个 output_index`)
    assert.equal(result.completedItem.id, addedItem?.id, `${transport} completed response 必须复用流式 item ID`)
    assert.equal(result.completedItem.call_id, addedItem?.call_id, `${transport} completed response 必须复用 call_id`)
  }
}

console.log('Codex Responses 工具 item 身份回归通过：function/custom 在 JSON 与 SSE 中从源头生成正确前缀，并保持 added/done/completed 身份一致')

async function runBridgeToolCase(
  toolKind: 'function' | 'custom',
  stream: boolean
): Promise<BridgeToolCaseResult> {
  const responsesName = toolKind === 'custom' ? 'apply_patch' : 'run_task'
  const body: JsonRecord = {
    model: 'gpt-5.6-sol',
    input: 'call the tool',
    stream,
    tools: toolKind === 'custom'
      ? [{ type: 'custom', name: responsesName, description: 'Apply a patch.' }]
      : [{
          type: 'function',
          name: responsesName,
          description: 'Run a task.',
          parameters: { type: 'object', properties: { value: { type: 'string' } } }
        }]
  }
  const req = {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body
  } as Request
  const chatBody = JSON.parse((await buildCodexResponsesChatBridgeBody(req, {
    defaultModel: 'gpt-5.6-sol'
  })).toString('utf8')) as JsonRecord
  const chatTools = Array.isArray(chatBody.tools) ? chatBody.tools : []
  const chatFunction = objectValue(objectValue(chatTools[0])?.function)
  const chatName = stringValue(chatFunction?.name)
  assert.ok(chatName, 'bridge 必须生成 Chat function tool name')

  const argumentsText = toolKind === 'custom'
    ? JSON.stringify({ input: '*** Begin Patch\n*** End Patch\n' })
    : JSON.stringify({ value: 'ok' })
  const transformed = transformCodexResponsesChatBridgeUpstreamResponse(req, chatSseResponse(chatName, argumentsText), {
    enabled: true,
    explicitMappingBridge: true,
    defaultModel: 'gpt-5.6-sol',
    idPrefix: 'identity_test'
  })
  assert.ok(transformed.body, 'bridge 响应必须包含 body')
  const text = await collectBody(transformed.body)

  if (!stream) {
    const response = JSON.parse(text) as JsonRecord
    const output = Array.isArray(response.output) ? response.output : []
    assert.equal(output.length, 1, 'JSON 响应必须包含一个工具 item')
    const completedItem = objectValue(output[0]) ?? {}
    return {
      added: { output_index: 0, item: completedItem },
      done: { output_index: 0, item: completedItem },
      completedItem
    }
  }

  const events = parseSseJsonEvents(text)
  const added = events.find((event) => event.type === 'response.output_item.added')
  const done = events.find((event) => event.type === 'response.output_item.done')
  const completed = events.find((event) => event.type === 'response.completed')
  assert.ok(added, 'SSE 必须包含 response.output_item.added')
  assert.ok(done, 'SSE 必须包含 response.output_item.done')
  assert.ok(completed, 'SSE 必须包含 response.completed')
  const completedResponse = objectValue(completed.response)
  const completedOutput = Array.isArray(completedResponse?.output) ? completedResponse.output : []
  assert.equal(completedOutput.length, 1, 'completed response 必须包含一个工具 item')
  return {
    added,
    done,
    completedItem: objectValue(completedOutput[0]) ?? {}
  }
}

function chatSseResponse(chatName: string, argumentsText: string): GatewayUpstreamResponse {
  const chunks = [
    sseData({
      id: 'chatcmpl_identity',
      object: 'chat.completion.chunk',
      model: 'gpt-5.6-sol',
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            id: 'call_identity',
            type: 'function',
            function: { name: chatName, arguments: argumentsText }
          }]
        },
        finish_reason: null
      }]
    }),
    sseData({
      id: 'chatcmpl_identity',
      object: 'chat.completion.chunk',
      model: 'gpt-5.6-sol',
      choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }]
    }),
    'data: [DONE]\n\n'
  ]
  return {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'text/event-stream' }),
    body: (async function * () {
      for (const chunk of chunks) yield Buffer.from(chunk, 'utf8')
    })()
  }
}

function sseData(value: unknown): string {
  return `data: ${JSON.stringify(value)}\n\n`
}

async function collectBody(body: AsyncIterable<Uint8Array>): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

function parseSseJsonEvents(text: string): JsonRecord[] {
  const output: JsonRecord[] = []
  for (const block of text.split(/\r?\n\r?\n/)) {
    const data = block
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trimStart())
      .join('\n')
    if (!data || data === '[DONE]') continue
    const parsed = JSON.parse(data) as unknown
    if (isPlainObject(parsed)) output.push(parsed)
  }
  return output
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
