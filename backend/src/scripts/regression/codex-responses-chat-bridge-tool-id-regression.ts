import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  buildCodexResponsesChatBridgeBody,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

async function main(): Promise<void> {
  await assertCustomToolIdsUseCtcPrefix()
  await assertFunctionToolIdsUseFcPrefix()
  await assertLateNameStillUsesCorrectPrefix()
  console.log('codex-responses-chat-bridge-tool-id-regression: ok')
}

async function assertCustomToolIdsUseCtcPrefix(): Promise<void> {
  const items = await collectBridgeOutputItems({
    tools: [
      {
        type: 'custom',
        name: 'apply_patch',
        description: 'Apply a patch in the workspace.'
      }
    ],
    chatToolName: 'custom__apply_patch',
    argumentsText: JSON.stringify({ input: '*** Begin Patch\n*** End Patch\n' })
  })
  const toolItems = items.filter((item) => item.type === 'custom_tool_call' || item.type === 'function_call')
  assert.equal(toolItems.length, 1, '应产出一个工具 item')
  const tool = toolItems[0]!
  assert.equal(tool.type, 'custom_tool_call', 'apply_patch 必须是 custom_tool_call')
  assert.match(String(tool.id ?? ''), /^ctc_/, `custom tool item.id 必须是 ctc_*，实际=${String(tool.id)}`)
  assert.equal(tool.name, 'apply_patch')
  assert.equal(tool.call_id, 'call_apply_patch_1')
}

async function assertFunctionToolIdsUseFcPrefix(): Promise<void> {
  const items = await collectBridgeOutputItems({
    tools: [
      {
        type: 'function',
        name: 'lookup',
        description: 'Lookup something.',
        parameters: { type: 'object', properties: { q: { type: 'string' } } }
      }
    ],
    chatToolName: 'lookup',
    argumentsText: JSON.stringify({ q: 'ping' })
  })
  const toolItems = items.filter((item) => item.type === 'custom_tool_call' || item.type === 'function_call')
  assert.equal(toolItems.length, 1, '应产出一个 function tool item')
  const tool = toolItems[0]!
  assert.equal(tool.type, 'function_call')
  assert.match(String(tool.id ?? ''), /^fc_/, `function tool item.id 必须是 fc_*，实际=${String(tool.id)}`)
  assert.equal(tool.name, 'lookup')
  assert.equal(tool.call_id, 'call_lookup_1')
}

async function assertLateNameStillUsesCorrectPrefix(): Promise<void> {
  const req = await prepareBridgeRequest({
    tools: [
      {
        type: 'custom',
        name: 'apply_patch',
        description: 'Apply a patch in the workspace.'
      }
    ]
  })
  const events = [
    chatSseEvent({
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            id: 'call_late_1',
            type: 'function',
            function: { arguments: '' }
          }]
        }
      }]
    }),
    chatSseEvent({
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            function: {
              name: 'custom__apply_patch',
              arguments: JSON.stringify({ input: 'patch' })
            }
          }]
        }
      }]
    }),
    chatSseEvent({
      choices: [{
        index: 0,
        delta: {},
        finish_reason: 'tool_calls'
      }]
    }),
    'data: [DONE]\n\n'
  ]
  const payload = await transformToResponsesJson(req, events)
  const tool = (payload.output as JsonRecord[]).find((item) => item.type === 'custom_tool_call' || item.type === 'function_call')
  assert.ok(tool, '迟到 name 场景也应产出 tool item')
  assert.equal(tool.type, 'custom_tool_call')
  assert.match(String(tool.id ?? ''), /^ctc_/, `迟到 name 的 custom tool 也必须是 ctc_*，实际=${String(tool.id)}`)
  assert.equal(tool.call_id, 'call_late_1')
}

async function collectBridgeOutputItems(input: {
  tools: JsonRecord[]
  chatToolName: string
  argumentsText: string
}): Promise<JsonRecord[]> {
  const req = await prepareBridgeRequest({ tools: input.tools })
  const events = [
    chatSseEvent({
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            id: input.chatToolName === 'lookup' ? 'call_lookup_1' : 'call_apply_patch_1',
            type: 'function',
            function: {
              name: input.chatToolName,
              arguments: input.argumentsText
            }
          }]
        }
      }]
    }),
    chatSseEvent({
      choices: [{
        index: 0,
        delta: {},
        finish_reason: 'tool_calls'
      }]
    }),
    'data: [DONE]\n\n'
  ]
  const payload = await transformToResponsesJson(req, events)
  assert.equal(payload.object, 'response')
  assert.ok(Array.isArray(payload.output), 'response.output 必须是数组')
  return payload.output as JsonRecord[]
}

async function prepareBridgeRequest(input: { tools: JsonRecord[] }): Promise<Request> {
  const body = {
    model: 'gpt-test',
    stream: false,
    input: 'please call the tool',
    tools: input.tools
  }
  const req = {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    url: '/v1/responses',
    headers: {},
    body
  } as unknown as Request
  await buildCodexResponsesChatBridgeBody(req, { defaultModel: 'gpt-test' })
  return req
}

async function transformToResponsesJson(req: Request, events: string[]): Promise<JsonRecord> {
  const upstream: GatewayUpstreamResponse = {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'text/event-stream' }),
    body: (async function * () {
      for (const event of events) {
        yield Buffer.from(event, 'utf8')
      }
    })()
  }
  const transformed = transformCodexResponsesChatBridgeUpstreamResponse(req, upstream, {
    enabled: true,
    explicitMappingBridge: true,
    defaultModel: 'gpt-test',
    idPrefix: 'chat_bridge'
  })
  assert.ok(transformed.body, '转换后必须有 body')
  const chunks: Buffer[] = []
  for await (const chunk of transformed.body) {
    chunks.push(Buffer.from(chunk))
  }
  const text = Buffer.concat(chunks).toString('utf8')
  return JSON.parse(text) as JsonRecord
}

function chatSseEvent(payload: JsonRecord): string {
  return `data: ${JSON.stringify(payload)}\n\n`
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
